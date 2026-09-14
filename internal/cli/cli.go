// Package cli implements the Gong server and HTTP client command line.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/creatorofuniverses/gong/internal/config"
	"github.com/creatorofuniverses/gong/internal/gateway"
)

const (
	defaultGatewayURL = "http://localhost:8080"
	defaultTimeout    = 30 * time.Second
)

// Version is replaced by release builds with:
// -ldflags "-X github.com/creatorofuniverses/gong/internal/cli.Version=vX.Y.Z".
var Version = "dev"

// Run executes one CLI invocation and returns its process exit code.
func Run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if ctx == nil {
		ctx = context.Background()
	}
	if stdin == nil {
		stdin = strings.NewReader("")
	}
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	if len(args) == 0 {
		rootUsage(stderr)
		return 2
	}
	switch args[0] {
	case "help", "--help", "-h":
		if len(args) != 1 {
			return argumentError(stderr, "gong: unexpected arguments", rootUsage)
		}
		rootUsage(stdout)
		return 0
	case "version", "--version":
		if len(args) != 1 {
			return argumentError(stderr, "gong version: unexpected arguments", rootUsage)
		}
		fmt.Fprintln(stdout, Version)
		return 0
	case "serve":
		return runServe(ctx, args[1:], stdout, stderr)
	case "notify":
		return runNotify(ctx, args[1:], stdin, stdout, stderr)
	case "topics":
		return runTopics(ctx, args[1:], stdout, stderr)
	default:
		fmt.Fprintln(stderr, "gong: unknown command")
		rootUsage(stderr)
		return 2
	}
}

func runServe(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	configPath := fs.String("config", "", "YAML configuration path (otherwise discovered automatically)")
	parsed, code := parseFlags(fs, args, stdout, stderr, serveUsage)
	if !parsed {
		return code
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "gong serve: unexpected arguments")
		serveUsage(stderr)
		return 2
	}
	path, err := discoverConfig(*configPath, visitedFlags(fs)["config"])
	if err != nil {
		return localFailure(stdout, stderr, "config_failed", err.Error(), false)
	}
	if path == "" {
		return localFailure(stdout, stderr, "config_missing", "no config found; create ./gong.yaml or ~/.config/gong/config.yaml, or use --config FILE", false)
	}
	cfg, err := config.Load(path)
	if err != nil {
		return localFailure(stdout, stderr, "config_failed", "could not load config; check the file path and YAML settings", false)
	}
	logger := slog.New(slog.NewJSONHandler(stderr, nil))
	if err := gateway.Run(ctx, cfg, logger); err != nil {
		fmt.Fprintf(stderr, "gong serve: %v\n", err)
		return 1
	}
	return 0
}

type clientFlags struct {
	configPath string
	baseURL    string
	token      string
	timeout    time.Duration
}

func addClientFlags(fs *flag.FlagSet) *clientFlags {
	baseURL := os.Getenv("GONG_URL")
	if baseURL == "" {
		baseURL = defaultGatewayURL
	}
	values := &clientFlags{baseURL: baseURL, token: os.Getenv("GONG_API_TOKEN"), timeout: defaultTimeout}
	fs.StringVar(&values.configPath, "config", "", "server YAML configuration (otherwise discovered automatically)")
	fs.StringVar(&values.baseURL, "url", values.baseURL, "Gong gateway URL (or GONG_URL)")
	fs.StringVar(&values.token, "token", values.token, "API Bearer token (or GONG_API_TOKEN)")
	fs.DurationVar(&values.timeout, "timeout", values.timeout, "request timeout")
	return values
}

func runNotify(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("notify", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	clientValues := addClientFlags(fs)
	target := fs.String("target", "default", "configured target alias")
	topic := fs.String("topic", "", "topic name")
	topicIDText := fs.String("topic-id", "", "positive topic ID")
	level := fs.String("level", "info", "debug, info, success, warning, or error")
	category := fs.String("category", "", "notification category")
	fallback := fs.Bool("fallback-plain-text", true, "retry without HTML parse mode after an HTML parse error")
	parsed, code := parseFlags(fs, args, stdout, stderr, notifyUsage)
	if !parsed {
		return code
	}
	present := visitedFlags(fs)
	if err := resolveClientConfig(fs, clientValues); err != nil {
		return localFailure(stdout, stderr, "config_failed", err.Error(), false)
	}
	if present["topic"] && present["topic-id"] {
		return argumentError(stderr, "gong notify: --topic and --topic-id are mutually exclusive", notifyUsage)
	}
	if !utf8.ValidString(*target) || present["topic"] && !utf8.ValidString(*topic) || present["category"] && !utf8.ValidString(*category) {
		return argumentError(stderr, "gong notify: payload values must be valid UTF-8", notifyUsage)
	}
	if present["topic"] && strings.TrimSpace(*topic) == "" {
		return argumentError(stderr, "gong notify: --topic must not be empty", notifyUsage)
	}
	if present["category"] && strings.TrimSpace(*category) == "" {
		return argumentError(stderr, "gong notify: --category must not be empty", notifyUsage)
	}
	if strings.TrimSpace(*target) == "" {
		return argumentError(stderr, "gong notify: --target must not be empty", notifyUsage)
	}
	if !validNotificationLevel(*level) {
		return argumentError(stderr, "gong notify: --level must be debug, info, success, warning, or error", notifyUsage)
	}
	var topicID int64
	if present["topic-id"] {
		var err error
		topicID, err = strconv.ParseInt(*topicIDText, 10, 64)
		if err != nil || topicID <= 0 {
			return argumentError(stderr, "gong notify: --topic-id must be a positive 64-bit integer", notifyUsage)
		}
	}
	message := strings.Join(fs.Args(), " ")
	if fs.NArg() == 0 {
		data, err := readStdin(ctx, stdin)
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return localFailure(stdout, stderr, "stdin_cancelled", "message input was cancelled; no request was sent", false)
		}
		if err != nil {
			return localFailure(stdout, stderr, "stdin_failed", "could not read the notification message", false)
		}
		if len(data) > maximumRequestSize {
			return argumentError(stderr, "gong notify: stdin exceeds the 64 KiB gateway request limit", notifyUsage)
		}
		message = string(data)
	}
	if !utf8.ValidString(message) {
		return argumentError(stderr, "gong notify: message must be valid UTF-8", notifyUsage)
	}
	if strings.TrimSpace(message) == "" {
		return argumentError(stderr, "gong notify: MESSAGE or nonempty stdin is required", notifyUsage)
	}
	payload := map[string]any{
		"message": message, "target": *target, "level": *level,
		"fallback_plain_text": *fallback,
	}
	if present["topic"] {
		payload["topic"] = *topic
	}
	if present["topic-id"] {
		payload["topic_id"] = topicID
	}
	if *category != "" {
		payload["category"] = *category
	}
	return executeJSON(ctx, http.MethodPost, "/notify", nil, payload, *clientValues, stdout, stderr, successExpectation{kind: successNotify})
}

type stdinResult struct {
	data []byte
	err  error
}

func readStdin(ctx context.Context, stdin io.Reader) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	read := func() stdinResult {
		data, err := io.ReadAll(io.LimitReader(stdin, maximumRequestSize+1))
		return stdinResult{data: data, err: err}
	}
	file, processStdin := stdin.(*os.File)
	if !processStdin || file != os.Stdin || ctx.Done() == nil {
		result := read()
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return result.data, result.err
	}

	done := make(chan stdinResult, 1)
	go func() { done <- read() }()
	select {
	case result := <-done:
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return result.data, result.err
	case <-ctx.Done():
		// This exception is limited to the executable's process-owned stdin.
		// main calls os.Exit immediately after Run returns, terminating the
		// inherited blocking read together with the process.
		return nil, ctx.Err()
	}
}

func runTopics(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		topicsUsage(stderr)
		return 2
	}
	switch args[0] {
	case "help", "--help", "-h":
		if len(args) != 1 {
			return argumentError(stderr, "gong topics: unexpected arguments", topicsUsage)
		}
		topicsUsage(stdout)
		return 0
	case "create":
		return runTopicsCreate(ctx, args[1:], stdout, stderr)
	case "delete":
		return runTopicsDelete(ctx, args[1:], stdout, stderr)
	default:
		fmt.Fprintln(stderr, "gong topics: unknown command")
		topicsUsage(stderr)
		return 2
	}
}

func runTopicsCreate(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("topics create", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	clientValues := addClientFlags(fs)
	name := fs.String("name", "", "new forum topic name (required)")
	target := fs.String("target", "default", "configured forum target alias")
	parsed, code := parseFlags(fs, args, stdout, stderr, topicsCreateUsage)
	if !parsed {
		return code
	}
	if fs.NArg() != 0 {
		return argumentError(stderr, "gong topics create: unexpected arguments", topicsCreateUsage)
	}
	if err := resolveClientConfig(fs, clientValues); err != nil {
		return localFailure(stdout, stderr, "config_failed", err.Error(), false)
	}
	if !utf8.ValidString(*name) || !utf8.ValidString(*target) {
		return argumentError(stderr, "gong topics create: payload values must be valid UTF-8", topicsCreateUsage)
	}
	if strings.TrimSpace(*name) == "" {
		return argumentError(stderr, "gong topics create: --name is required", topicsCreateUsage)
	}
	if strings.TrimSpace(*target) == "" {
		return argumentError(stderr, "gong topics create: --target must not be empty", topicsCreateUsage)
	}
	payload := map[string]any{"name": *name, "target": *target}
	return executeJSON(ctx, http.MethodPost, "/topics", nil, payload, *clientValues, stdout, stderr, successExpectation{
		kind: successCreateTopic, name: *name, target: *target,
	})
}

func runTopicsDelete(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("topics delete", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	clientValues := addClientFlags(fs)
	idText := fs.String("id", "", "positive topic ID (required)")
	target := fs.String("target", "default", "configured forum target alias")
	parsed, code := parseFlags(fs, args, stdout, stderr, topicsDeleteUsage)
	if !parsed {
		return code
	}
	if fs.NArg() != 0 {
		return argumentError(stderr, "gong topics delete: unexpected arguments", topicsDeleteUsage)
	}
	if err := resolveClientConfig(fs, clientValues); err != nil {
		return localFailure(stdout, stderr, "config_failed", err.Error(), false)
	}
	if !utf8.ValidString(*target) {
		return argumentError(stderr, "gong topics delete: target must be valid UTF-8", topicsDeleteUsage)
	}
	id, err := strconv.ParseInt(*idText, 10, 64)
	if err != nil || id <= 0 {
		return argumentError(stderr, "gong topics delete: --id must be a positive 64-bit integer", topicsDeleteUsage)
	}
	if strings.TrimSpace(*target) == "" {
		return argumentError(stderr, "gong topics delete: --target must not be empty", topicsDeleteUsage)
	}
	query := url.Values{"target": []string{*target}}
	return executeJSON(ctx, http.MethodDelete, "/topics/"+strconv.FormatInt(id, 10), query, nil, *clientValues, stdout, stderr, successExpectation{
		kind: successDeleteTopic, topicID: id, target: *target,
	})
}

func parseFlags(fs *flag.FlagSet, args []string, stdout, stderr io.Writer, usage func(io.Writer)) (bool, int) {
	err := fs.Parse(args)
	if errors.Is(err, flag.ErrHelp) {
		usage(stdout)
		return false, 0
	}
	if err != nil {
		fmt.Fprintf(stderr, "gong %s: invalid arguments\n", fs.Name())
		usage(stderr)
		return false, 2
	}
	return true, 0
}

func visitedFlags(fs *flag.FlagSet) map[string]bool {
	visited := make(map[string]bool)
	fs.Visit(func(value *flag.Flag) { visited[value.Name] = true })
	return visited
}

func validNotificationLevel(value string) bool {
	switch value {
	case "debug", "info", "success", "warning", "error":
		return true
	default:
		return false
	}
}

func argumentError(stderr io.Writer, message string, usage func(io.Writer)) int {
	fmt.Fprintln(stderr, message)
	usage(stderr)
	return 2
}

// These wrappers keep command help stable and make the destructive operation
// explicit at the point where users discover it.
func rootUsage(w io.Writer) {
	fmt.Fprintln(w, `Gong sends your events to Telegram.

Usage: gong <serve|notify|topics|version> [options]

  gong serve                       Start the gateway; 'listen' in YAML sets its port
  gong notify 'Done!'               Send through the configured local gateway
  gong notify --url http://localhost:8081 'Done!'
  gong notify --config ./gong.yaml 'Done!'

Config: --config FILE, otherwise ./gong.yaml, then
        $XDG_CONFIG_HOME/gong/config.yaml (default ~/.config/gong/config.yaml).
Clients also work without a config: --url > GONG_URL > config listen > localhost:8080.
API token: --token > GONG_API_TOKEN > config api_token.
Put options after the command and before the message.
Run 'gong <command> --help' for command options.`)
}

func serveUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage: gong serve [--config FILE]")
	fmt.Fprintln(w, "Config: ./gong.yaml, then $XDG_CONFIG_HOME/gong/config.yaml (default ~/.config/gong/config.yaml).")
	fmt.Fprintln(w, `Set listen: "127.0.0.1:8081" in YAML to change the port. Clients discover the same file.`)
}

func notifyUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage: gong notify [--config FILE] [--url URL] [--token TOKEN] [--timeout 30s] [--target ALIAS] [--topic NAME | --topic-id ID] [--level LEVEL] [--category CATEGORY] [--] [MESSAGE ...]")
	fmt.Fprintln(w, "With no MESSAGE, the message is read from stdin. Put flags before MESSAGE.")
	clientUsage(w)
}

func topicsUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage: gong topics <create|delete> [options]")
}

func topicsCreateUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage: gong topics create --name NAME [--target ALIAS] [--config FILE] [--url URL] [--token TOKEN] [--timeout 30s]")
	clientUsage(w)
}

func topicsDeleteUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage: gong topics delete --id ID [--target ALIAS] [--config FILE] [--url URL] [--token TOKEN] [--timeout 30s]")
	clientUsage(w)
	fmt.Fprintln(w, "WARNING: deleting a forum topic also deletes its messages and is not idempotent.")
}

func clientUsage(w io.Writer) {
	fmt.Fprintln(w, "Config discovery: ./gong.yaml, then $XDG_CONFIG_HOME/gong/config.yaml (default ~/.config/gong/config.yaml).")
	fmt.Fprintln(w, "Uses listen/api_token from a valid server config. No config is required for clients.")
	fmt.Fprintln(w, "Address: --url > GONG_URL > config listen > http://localhost:8080.")
	fmt.Fprintln(w, "Token: --token > GONG_API_TOKEN > config api_token. Nonempty environment values override YAML.")
	fmt.Fprintln(w, "Example: gong notify --url http://localhost:8081 -- 'Done!'")
}
