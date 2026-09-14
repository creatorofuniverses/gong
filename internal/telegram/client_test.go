package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSendUsesOneNonReplayablePOSTAndDecodesResult(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		if r.Header.Get("Idempotency-Key") != "" || r.Header.Get("X-Idempotency-Key") != "" {
			t.Errorf("mutation unexpectedly carried an idempotency header")
		}
		if got := r.URL.Path; got != "/botsecret/sendMessage" {
			t.Errorf("path = %q", got)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
			http.Error(w, "invalid test request", http.StatusBadRequest)
			return
		}
		want := map[string]any{
			"chat_id":              "-1001",
			"text":                 "<b>done</b>",
			"parse_mode":           "HTML",
			"message_thread_id":    float64(42),
			"disable_notification": true,
		}
		if fmt.Sprint(payload) != fmt.Sprint(want) {
			t.Errorf("payload = %#v, want %#v", payload, want)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":77}}`)
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)
	got, err := client.Send(context.Background(), SendRequest{
		ChatID: "-1001", Text: "<b>done</b>", TopicID: 42, Silent: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != (SendResult{MessageID: 77}) {
		t.Fatalf("result = %#v", got)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1", calls.Load())
	}
}

func TestMutationRequestCannotBeReplayedByHTTP2(t *testing.T) {
	var protocol atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		protocol.Store(int32(r.ProtoMajor))
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":78}}`)
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()

	recorder := &requestReplayRecorder{base: server.Client().Transport}
	client, err := New(Options{
		BotToken: "secret", BaseURL: server.URL, Timeout: time.Second,
		HTTPClient: &http.Client{Transport: recorder},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := client.Send(context.Background(), SendRequest{ChatID: "1", Text: "x"})
	if err != nil || got.MessageID != 78 {
		t.Fatalf("result = %#v, error = %v", got, err)
	}
	if protocol.Load() != 2 {
		t.Fatalf("protocol major = %d, want HTTP/2", protocol.Load())
	}
	if !recorder.nonemptyBody.Load() {
		t.Fatal("mutation request body was empty")
	}
	if recorder.getBodyPresent.Load() {
		t.Fatal("mutation request exposed GetBody and could be replayed")
	}
}

func TestSendFallsBackOnlyForExplicitHTMLFormattingRejection(t *testing.T) {
	for _, tc := range []struct {
		name        string
		description string
		fallback    bool
		wantCalls   int32
		wantSuccess bool
	}{
		{name: "format rejection", description: "Bad Request: can't parse entities: unsupported start tag", fallback: true, wantCalls: 2, wantSuccess: true},
		{name: "ordinary bad request", description: "Bad Request: chat not found", fallback: true, wantCalls: 1},
		{name: "fallback disabled", description: "Bad Request: can't parse entities", fallback: false, wantCalls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := calls.Add(1)
				var payload map[string]any
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
					http.Error(w, "invalid test request", http.StatusBadRequest)
					return
				}
				if payload["text"] != "<broken>" {
					t.Errorf("fallback changed text: %#v", payload["text"])
				}
				if n == 1 {
					w.WriteHeader(http.StatusBadRequest)
					fmt.Fprintf(w, `{"ok":false,"error_code":400,"description":%q}`, tc.description)
					return
				}
				if _, exists := payload["parse_mode"]; exists {
					t.Errorf("fallback retained parse_mode")
				}
				fmt.Fprint(w, `{"ok":true,"result":{"message_id":8}}`)
			}))
			defer server.Close()

			client := newTestClient(t, server.URL)
			got, err := client.Send(context.Background(), SendRequest{ChatID: "1", Text: "<broken>", FallbackPlainText: tc.fallback})
			if tc.wantSuccess {
				if err != nil || got != (SendResult{MessageID: 8, FallbackUsed: true}) {
					t.Fatalf("result = %#v, error = %v", got, err)
				}
			} else if err == nil {
				t.Fatal("expected rejection")
			}
			if calls.Load() != tc.wantCalls {
				t.Fatalf("calls = %d, want %d", calls.Load(), tc.wantCalls)
			}
		})
	}
}

func TestTelegramResponseErrorsCarrySafeClassification(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     int
		body       string
		wantCode   string
		wantStatus int
		wantRetry  int
		uncertain  bool
	}{
		{name: "clear 4xx", status: 400, body: `{"ok":false,"error_code":400,"description":"secret body"}`, wantCode: "telegram_rejected", wantStatus: 502},
		{name: "rate limit", status: 429, body: `{"ok":false,"error_code":429,"description":"secret body","parameters":{"retry_after":17}}`, wantCode: "telegram_rate_limited", wantStatus: 429, wantRetry: 17},
		{name: "upstream 5xx", status: 500, body: `{"ok":false,"error_code":500,"description":"secret body"}`, wantCode: "telegram_uncertain", wantStatus: 502, uncertain: true},
		{name: "malformed", status: 200, body: `{secret malformed`, wantCode: "telegram_uncertain", wantStatus: 502, uncertain: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			client := newTestClient(t, server.URL)
			_, err := client.Send(context.Background(), SendRequest{ChatID: "1", Text: "message-secret"})
			var got *Error
			if !errors.As(err, &got) {
				t.Fatalf("error type = %T, want *Error", err)
			}
			if got.Code != tc.wantCode || got.HTTPStatus != tc.wantStatus || got.RetryAfter != tc.wantRetry || got.Uncertain != tc.uncertain {
				t.Fatalf("error = %#v", got)
			}
			for _, secret := range []string{"secret", "message-secret", "botsecret"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("safe error exposed %q: %v", secret, err)
				}
			}
		})
	}
}

func TestTransportDistinguishesBeforeWriteFromLostReply(t *testing.T) {
	t.Run("dial failure is clear", func(t *testing.T) {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := listener.Addr().String()
		listener.Close()
		client := newTestClient(t, "http://"+addr)
		_, err = client.CreateTopic(context.Background(), "1", "name", Icon{})
		assertTypedError(t, err, "telegram_transport", 502, false)
	})

	t.Run("connection lost after request is uncertain", func(t *testing.T) {
		var calls atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			hijacker := w.(http.Hijacker)
			conn, _, err := hijacker.Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			conn.Close()
		}))
		defer server.Close()
		client := newTestClient(t, server.URL)
		_, err := client.CreateTopic(context.Background(), "1", "name", Icon{})
		assertTypedError(t, err, "telegram_uncertain", 502, true)
		if calls.Load() != 1 {
			t.Fatalf("calls = %d, mutation was retried", calls.Load())
		}
	})
}

func TestTransportPhaseContractAroundCancellationAndWriteErrors(t *testing.T) {
	t.Run("canceled before connection is clear", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		client := newRoundTripClient(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return nil, r.Context().Err()
		}))
		_, err := client.CreateTopic(ctx, "1", "name", Icon{})
		assertTypedError(t, err, "telegram_transport", 502, false)
	})

	t.Run("canceled after connection is conservatively uncertain", func(t *testing.T) {
		client := newRoundTripClient(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
			trace := httptrace.ContextClientTrace(r.Context())
			trace.GotConn(httptrace.GotConnInfo{})
			return nil, context.Canceled
		}))
		_, err := client.CreateTopic(context.Background(), "1", "name", Icon{})
		assertTypedError(t, err, "telegram_uncertain", 502, true)
	})

	t.Run("write callback error remains uncertain", func(t *testing.T) {
		client := newRoundTripClient(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
			trace := httptrace.ContextClientTrace(r.Context())
			trace.WroteRequest(httptrace.WroteRequestInfo{Err: errors.New("partial write")})
			return nil, errors.New("write failed")
		}))
		_, err := client.CreateTopic(context.Background(), "1", "name", Icon{})
		assertTypedError(t, err, "telegram_uncertain", 502, true)
	})
}

func TestOversizedResponseIsBoundedAndUncertain(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, strings.Repeat("x", maximumResponseSize+1))
	}))
	defer server.Close()
	client := newTestClient(t, server.URL)
	_, err := client.Send(context.Background(), SendRequest{ChatID: "1", Text: "x"})
	assertTypedError(t, err, "telegram_uncertain", 502, true)
}

func TestTimeoutMapsTo504(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	defer func() {
		close(release)
		server.Close()
	}()
	client, err := New(Options{BotToken: "botsecret", BaseURL: server.URL, Timeout: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Send(context.Background(), SendRequest{ChatID: "1", Text: "x"})
	var got *Error
	if !errors.As(err, &got) || got.HTTPStatus != 504 || got.Code != "telegram_timeout" {
		t.Fatalf("error = %#v (%v)", got, err)
	}
}

func TestBodyReadTimeoutMapsTo504(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-release
	}))
	defer func() {
		close(release)
		server.Close()
	}()
	client, err := New(Options{BotToken: "secret", BaseURL: server.URL, Timeout: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Send(context.Background(), SendRequest{ChatID: "1", Text: "x"})
	assertTypedError(t, err, "telegram_timeout", 504, true)
}

func TestSuccessfulResponsesRequirePositiveIdentifiers(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/botsecret/sendMessage":
			fmt.Fprint(w, `{"ok":true,"result":{"message_id":0}}`)
		case "/botsecret/createForumTopic":
			fmt.Fprint(w, `{"ok":true,"result":{"message_thread_id":0,"name":"ops"}}`)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			http.Error(w, "unexpected path", http.StatusNotFound)
		}
	}))
	defer server.Close()
	client := newTestClient(t, server.URL)

	_, err := client.Send(context.Background(), SendRequest{ChatID: "1", Text: "x"})
	assertTypedError(t, err, "telegram_uncertain", 502, true)
	_, err = client.CreateTopic(context.Background(), "1", "ops", Icon{})
	assertTypedError(t, err, "telegram_uncertain", 502, true)
}

func TestTypedTopicMethodsUseTelegramFields(t *testing.T) {
	requests := make(chan struct {
		path    string
		payload map[string]any
	}, 6)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
			http.Error(w, "invalid test request", http.StatusBadRequest)
			return
		}
		requests <- struct {
			path    string
			payload map[string]any
		}{r.URL.Path, payload}
		switch r.URL.Path {
		case "/botsecret/createForumTopic":
			fmt.Fprint(w, `{"ok":true,"result":{"message_thread_id":9,"name":"ops"}}`)
		case "/botsecret/getForumTopicIconStickers":
			fmt.Fprint(w, `{"ok":true,"result":[{"custom_emoji_id":"a"},{"custom_emoji_id":""},{"custom_emoji_id":"b"}]}`)
		default:
			fmt.Fprint(w, `{"ok":true,"result":true}`)
		}
	}))
	defer server.Close()
	client := newTestClient(t, server.URL)

	topic, err := client.CreateTopic(context.Background(), "-1", "ops", Icon{CustomEmojiID: "emoji"})
	if err != nil || topic != (Topic{ID: 9, Name: "ops"}) {
		t.Fatalf("topic = %#v, error = %v", topic, err)
	}
	if _, err := client.CreateTopic(context.Background(), "-1", "alerts", Icon{Color: 7322096}); err != nil {
		t.Fatal(err)
	}
	if err := client.DeleteTopic(context.Background(), "-1", 9); err != nil {
		t.Fatal(err)
	}
	if err := client.Pin(context.Background(), "-1", 77); err != nil {
		t.Fatal(err)
	}
	icons, err := client.GetTopicIcons(context.Background())
	if err != nil || fmt.Sprint(icons) != "[a b]" {
		t.Fatalf("icons = %v, error = %v", icons, err)
	}

	wants := []struct {
		path    string
		payload map[string]any
	}{
		{"/botsecret/createForumTopic", map[string]any{"chat_id": "-1", "name": "ops", "icon_custom_emoji_id": "emoji"}},
		{"/botsecret/createForumTopic", map[string]any{"chat_id": "-1", "name": "alerts", "icon_color": float64(7322096)}},
		{"/botsecret/deleteForumTopic", map[string]any{"chat_id": "-1", "message_thread_id": float64(9)}},
		{"/botsecret/pinChatMessage", map[string]any{"chat_id": "-1", "message_id": float64(77), "disable_notification": true}},
		{"/botsecret/getForumTopicIconStickers", map[string]any{}},
	}
	for _, want := range wants {
		got := <-requests
		if got.path != want.path || fmt.Sprint(got.payload) != fmt.Sprint(want.payload) {
			t.Errorf("request = %s %#v, want %s %#v", got.path, got.payload, want.path, want.payload)
		}
	}
}

func TestRedirectIsNotFollowed(t *testing.T) {
	var destinationCalls atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		destinationCalls.Add(1)
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":1}}`)
	}))
	defer destination.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer origin.Close()
	client := newTestClient(t, origin.URL)
	_, err := client.Send(context.Background(), SendRequest{ChatID: "1", Text: "x"})
	assertTypedError(t, err, "telegram_uncertain", 502, true)
	if destinationCalls.Load() != 0 {
		t.Fatalf("redirect destination calls = %d", destinationCalls.Load())
	}
}

func newTestClient(t *testing.T, baseURL string) *Client {
	t.Helper()
	client, err := New(Options{BotToken: "secret", BaseURL: baseURL, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.CloseIdleConnections)
	return client
}

func assertTypedError(t *testing.T, err error, code string, status int, uncertain bool) {
	t.Helper()
	var got *Error
	if !errors.As(err, &got) {
		t.Fatalf("error type = %T, want *Error", err)
	}
	if got.Code != code || got.HTTPStatus != status || got.Uncertain != uncertain {
		t.Fatalf("error = %#v", got)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

type requestReplayRecorder struct {
	base           http.RoundTripper
	nonemptyBody   atomic.Bool
	getBodyPresent atomic.Bool
}

func (r *requestReplayRecorder) RoundTrip(request *http.Request) (*http.Response, error) {
	r.nonemptyBody.Store(request.Body != nil && request.Body != http.NoBody && request.ContentLength > 0)
	r.getBodyPresent.Store(request.GetBody != nil)
	return r.base.RoundTrip(request)
}

func newRoundTripClient(t *testing.T, transport http.RoundTripper) *Client {
	t.Helper()
	client, err := New(Options{
		BotToken: "secret", BaseURL: "https://telegram.invalid", Timeout: time.Second,
		HTTPClient: &http.Client{Transport: transport},
	})
	if err != nil {
		t.Fatal(err)
	}
	return client
}
