// Package gateway exposes Gong's HTTP API and server lifecycle.
package gateway

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/creatorofuniverses/gong/internal/config"
	"github.com/creatorofuniverses/gong/internal/telegram"
	"github.com/creatorofuniverses/gong/internal/topics"
)

const (
	defaultRequestTimeout = 25 * time.Second
	defaultShutdownGrace  = 30 * time.Second
	readHeaderTimeout     = 5 * time.Second
	idleTimeout           = 60 * time.Second
)

// Server is Gong's HTTP handler. A Server owns one icon catalogue and one
// in-memory topic-name resolver for its full lifetime.
type Server struct {
	cfg      config.Config
	sender   telegram.Transport
	logger   *slog.Logger
	picker   *topics.IconPicker
	resolver *topics.Resolver

	shutdown       context.Context
	cancelShutdown context.CancelFunc
	requestTimeout time.Duration
	shutdownGrace  time.Duration
}

// New constructs a gateway using sender for all Telegram operations.
func New(cfg config.Config, sender telegram.Transport, shutdown context.Context, logger *slog.Logger) *Server {
	if shutdown == nil {
		shutdown = context.Background()
	}
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(discardWriter{}, nil))
	}
	ownedShutdown, cancel := context.WithCancel(shutdown)
	s := &Server{
		cfg: cfg, sender: sender, logger: logger,
		shutdown: ownedShutdown, cancelShutdown: cancel,
		requestTimeout: defaultRequestTimeout, shutdownGrace: defaultShutdownGrace,
	}
	s.picker = topics.NewIconPicker(ownedShutdown, sender.GetTopicIcons, logger)
	creator := func(ctx context.Context, chatID, name string) (telegram.Topic, error) {
		icon, err := s.picker.Pick(ctx, name)
		if err != nil {
			return telegram.Topic{}, err
		}
		callCtx, callCancel := s.callContext(ctx)
		defer callCancel()
		return s.sender.CreateTopic(callCtx, chatID, name, icon)
	}
	s.resolver = topics.NewResolver(ownedShutdown, cfg.MaxTopics, defaultRequestTimeout, creator)
	return s
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/health" {
		if r.Method != http.MethodGet {
			writeMethodNotAllowed(w, http.MethodGet)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}
	if err := s.authorize(r); err != nil {
		writeAPIError(w, err)
		return
	}

	deadline := time.Now().Add(s.requestTimeout)
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(deadline)
	_ = controller.SetWriteDeadline(deadline.Add(5 * time.Second))
	ctx, cancel := context.WithDeadline(r.Context(), deadline)
	defer cancel()
	r = r.WithContext(ctx)

	switch {
	case r.URL.Path == "/notify":
		if r.Method != http.MethodPost {
			writeMethodNotAllowed(w, http.MethodPost)
			return
		}
		s.handleNotify(w, r)
	case r.URL.Path == "/topics":
		if r.Method != http.MethodPost {
			writeMethodNotAllowed(w, http.MethodPost)
			return
		}
		s.handleCreateTopic(w, r)
	case len(r.URL.Path) > len("/topics/") && r.URL.Path[:len("/topics/")] == "/topics/" && !containsSlash(r.URL.Path[len("/topics/"):]):
		if r.Method != http.MethodDelete {
			writeMethodNotAllowed(w, http.MethodDelete)
			return
		}
		s.handleDeleteTopic(w, r)
	default:
		writeAPIError(w, apiError{status: http.StatusNotFound, code: "not_found", message: "route not found"})
	}
}

// Serve listens until ctx is cancelled. Active requests receive up to the
// configured 30-second grace, after which their contexts are cancelled and
// open connections are closed.
func (s *Server) Serve(ctx context.Context, listener net.Listener) error {
	if ctx == nil {
		ctx = context.Background()
	}
	defer s.cancelShutdown()
	requestRoot, cancelRequests := context.WithCancel(s.shutdown)
	defer cancelRequests()
	httpServer := &http.Server{
		Handler:           s,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       s.requestTimeout,
		WriteTimeout:      s.requestTimeout + 5*time.Second,
		IdleTimeout:       idleTimeout,
		BaseContext: func(net.Listener) context.Context {
			return requestRoot
		},
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- httpServer.Serve(listener) }()

	select {
	case err := <-serveDone:
		return normalizeServeError(err)
	case <-ctx.Done():
	}

	graceCtx, cancelGrace := context.WithTimeout(context.Background(), s.shutdownGrace)
	err := httpServer.Shutdown(graceCtx)
	cancelGrace()
	if err != nil {
		cancelRequests()
		s.cancelShutdown()
		_ = httpServer.Close()
	}
	serveErr := <-serveDone
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return normalizeServeError(serveErr)
}

// Run builds the production Telegram client, listens on cfg.Listen, and
// serves until ctx is cancelled.
func Run(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	client, err := telegram.New(telegram.Options{
		BotToken: cfg.Telegram.BotToken,
		ProxyURL: cfg.Telegram.ProxyURL,
		Timeout:  cfg.Telegram.Timeout,
	})
	if err != nil {
		return err
	}
	defer client.CloseIdleConnections()
	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return err
	}
	server := New(cfg, client, context.Background(), logger)
	return server.Serve(ctx, listener)
}

func (s *Server) callContext(parent context.Context) (context.Context, context.CancelFunc) {
	timeout := s.cfg.Telegram.Timeout
	if timeout <= 0 || timeout > s.requestTimeout {
		timeout = s.requestTimeout
	}
	return context.WithTimeout(parent, timeout)
}

func normalizeServeError(err error) error {
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func containsSlash(value string) bool {
	for i := range len(value) {
		if value[i] == '/' {
			return true
		}
	}
	return false
}

// discardWriter avoids routing library-default logs to stderr when callers do
// not supply a logger.
type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }
