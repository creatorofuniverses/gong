package gateway

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creatorofuniverses/gong/internal/config"
	"github.com/creatorofuniverses/gong/internal/telegram"
)

type fakeTransport struct {
	mu sync.Mutex

	sends   []telegram.SendRequest
	creates []createCall
	deletes []deleteCall
	pins    []pinCall
	icons   int

	sendResult telegram.SendResult
	createIDs  []int64
	iconList   []string
	sendErr    error
	createErr  error
	deleteErr  error
	pinErr     error
	sendFn     func(context.Context, telegram.SendRequest) (telegram.SendResult, error)
}

type createCall struct {
	chatID string
	name   string
	icon   telegram.Icon
}

type deleteCall struct {
	chatID string
	id     int64
}

type pinCall struct {
	chatID    string
	messageID int64
}

func (f *fakeTransport) Send(ctx context.Context, request telegram.SendRequest) (telegram.SendResult, error) {
	f.mu.Lock()
	f.sends = append(f.sends, request)
	fn, result, err := f.sendFn, f.sendResult, f.sendErr
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, request)
	}
	if result.MessageID == 0 {
		result.MessageID = 7
	}
	return result, err
}

func (f *fakeTransport) CreateTopic(_ context.Context, chatID, name string, icon telegram.Icon) (telegram.Topic, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.creates = append(f.creates, createCall{chatID: chatID, name: name, icon: icon})
	if f.createErr != nil {
		return telegram.Topic{}, f.createErr
	}
	id := int64(100 + len(f.creates))
	if len(f.createIDs) >= len(f.creates) {
		id = f.createIDs[len(f.creates)-1]
	}
	return telegram.Topic{ID: id, Name: name}, nil
}

func (f *fakeTransport) DeleteTopic(_ context.Context, chatID string, topicID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletes = append(f.deletes, deleteCall{chatID: chatID, id: topicID})
	return f.deleteErr
}

func (f *fakeTransport) Pin(_ context.Context, chatID string, messageID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pins = append(f.pins, pinCall{chatID: chatID, messageID: messageID})
	return f.pinErr
}

func (f *fakeTransport) GetTopicIcons(context.Context) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.icons++
	return append([]string(nil), f.iconList...), nil
}

func testConfig() config.Config {
	return config.Config{
		Listen:   "127.0.0.1:8080",
		Telegram: config.TelegramConfig{BotToken: "bot-secret", Timeout: time.Second},
		Targets: map[string]config.Target{
			"default": {ChatID: "-1001", Mode: "forum"},
			"alerts":  {ChatID: "42", Mode: "chat"},
		},
		NotifyMinLevel: "warning",
		PinCategories:  []string{"result"},
		MaxTopics:      10,
	}
}

func performRequest(t *testing.T, server http.Handler, method, path, contentType, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	req.Host = "localhost:9123"
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, req)
	return recorder
}

func decodeObject(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &value); err != nil {
		t.Fatalf("decode response %q: %v", recorder.Body.String(), err)
	}
	return value
}

func TestHealthIsPublicAndDoesNotContactTelegram(t *testing.T) {
	cfg := testConfig()
	cfg.APIToken = "api-secret"
	sender := &fakeTransport{}
	server := New(cfg, sender, context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)))

	recorder := performRequest(t, server, http.MethodGet, "/health", "", "", map[string]string{
		"Origin": "https://attacker.example",
	})

	if recorder.Code != http.StatusOK || recorder.Body.String() != "{\"status\":\"ok\"}\n" {
		t.Fatalf("health response = %d %q", recorder.Code, recorder.Body.String())
	}
	if len(sender.sends)+len(sender.creates)+len(sender.deletes)+len(sender.pins)+sender.icons != 0 {
		t.Fatal("health contacted Telegram")
	}
}

func TestNotifyJSONRoutesForumTopicAndPinsCategory(t *testing.T) {
	sender := &fakeTransport{sendResult: telegram.SendResult{MessageID: 81, FallbackUsed: true}, iconList: []string{"emoji-2", "emoji-1"}}
	server := New(testConfig(), sender, context.Background(), nil)

	recorder := performRequest(t, server, http.MethodPost, "/notify", "application/json; charset=utf-8",
		`{"message":"<b>done</b>","topic":"backup","level":"success","category":"result","fallback_plain_text":false}`, nil)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	wantBody := map[string]any{
		"ok": true, "message_id": float64(81), "level": "success", "silent": true,
		"fallback_used": true, "target": "default", "topic_id": float64(101), "pin_status": "pinned",
	}
	if got := decodeObject(t, recorder); !mapsEqual(got, wantBody) {
		t.Fatalf("response = %#v, want %#v", got, wantBody)
	}
	if sender.icons != 1 || len(sender.creates) != 1 || sender.creates[0].chatID != "-1001" || sender.creates[0].name != "backup" || sender.creates[0].icon.CustomEmojiID == "" {
		t.Fatalf("create sequence = icons:%d creates:%#v", sender.icons, sender.creates)
	}
	if len(sender.sends) != 1 || sender.sends[0] != (telegram.SendRequest{ChatID: "-1001", Text: "<b>done</b>", TopicID: 101, Silent: true, FallbackPlainText: false}) {
		t.Fatalf("send = %#v", sender.sends)
	}
	if len(sender.pins) != 1 || sender.pins[0] != (pinCall{chatID: "-1001", messageID: 81}) {
		t.Fatalf("pins = %#v", sender.pins)
	}
}

func TestNotifyTextRoutesChatTopicAsUnicodeHashtag(t *testing.T) {
	sender := &fakeTransport{}
	server := New(testConfig(), sender, context.Background(), nil)

	recorder := performRequest(t, server, http.MethodPost, "/notify", "text/plain; charset=UTF-8", "готово", map[string]string{
		"X-Target": "alerts", "X-Topic": "  Ночной backup!!!  ", "X-Level": "error", "X-Fallback-Plain-Text": "FALSE",
	})

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if len(sender.sends) != 1 {
		t.Fatalf("sends = %#v", sender.sends)
	}
	want := telegram.SendRequest{ChatID: "42", Text: "готово\n#Ночной_backup", Silent: false, FallbackPlainText: false}
	if sender.sends[0] != want {
		t.Fatalf("send = %#v, want %#v", sender.sends[0], want)
	}
	got := decodeObject(t, recorder)
	if _, exists := got["topic_id"]; exists || got["target"] != "alerts" || got["pin_status"] != "not_requested" {
		t.Fatalf("response = %#v", got)
	}
}

func TestNotifyDefaultsAndForumGeneral(t *testing.T) {
	sender := &fakeTransport{}
	server := New(testConfig(), sender, context.Background(), nil)
	recorder := performRequest(t, server, http.MethodPost, "/notify", "application/json", `{"message":"progress"}`, map[string]string{
		"X-Target": "alerts", "X-Level": "error",
	})

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	want := telegram.SendRequest{ChatID: "-1001", Text: "progress", Silent: true, FallbackPlainText: true}
	if len(sender.sends) != 1 || sender.sends[0] != want {
		t.Fatalf("send = %#v, want %#v", sender.sends, want)
	}
	got := decodeObject(t, recorder)
	if _, exists := got["topic_id"]; exists || got["level"] != "info" || got["target"] != "default" {
		t.Fatalf("response = %#v", got)
	}
}

func TestExplicitTopicIDBypassesResolverAndIconPicker(t *testing.T) {
	sender := &fakeTransport{}
	server := New(testConfig(), sender, context.Background(), nil)
	recorder := performRequest(t, server, http.MethodPost, "/notify", "application/json", `{"message":"done","topic_id":9223372036854775807}`, nil)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if sender.icons != 0 || len(sender.creates) != 0 || len(sender.sends) != 1 || sender.sends[0].TopicID != int64(9223372036854775807) {
		t.Fatalf("calls: icons=%d creates=%#v sends=%#v", sender.icons, sender.creates, sender.sends)
	}
}

func TestNotifyValidationRejectsBeforeTelegram(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		body        string
		headers     map[string]string
		wantStatus  int
	}{
		{"unsupported media", "application/x-www-form-urlencoded", "message=x", nil, 415},
		{"malformed media", "not a media type", "x", nil, 415},
		{"non utf8", "text/plain", string([]byte{0xff}), nil, 422},
		{"blank text", "text/plain", " \n\t", nil, 422},
		{"bad level header", "text/plain", "x", map[string]string{"X-Level": "urgent"}, 422},
		{"bad bool header", "text/plain", "x", map[string]string{"X-Fallback-Plain-Text": "yes"}, 422},
		{"present empty topic header", "text/plain", "x", map[string]string{"X-Topic": ""}, 422},
		{"invalid utf8 topic header", "text/plain", "x", map[string]string{"X-Topic": string([]byte{0xff})}, 422},
		{"invalid utf8 category header", "text/plain", "x", map[string]string{"X-Category": string([]byte{0xff})}, 422},
		{"bad topic id header", "text/plain", "x", map[string]string{"X-Topic-ID": "0"}, 422},
		{"malformed json", "application/json", `{"message":`, nil, 422},
		{"trailing json", "application/json", `{"message":"x"}{}`, nil, 422},
		{"unknown json", "application/json", `{"message":"x","mesage":"typo"}`, nil, 422},
		{"raw invalid utf8 json", "application/json", string([]byte{'{', '"', 'm', 'e', 's', 's', 'a', 'g', 'e', '"', ':', '"', 0xff, '"', '}'}), nil, 422},
		{"null message", "application/json", `{"message":null}`, nil, 422},
		{"blank message", "application/json", `{"message":"   "}`, nil, 422},
		{"empty target", "application/json", `{"message":"x","target":""}`, nil, 422},
		{"null target", "application/json", `{"message":"x","target":null}`, nil, 422},
		{"unknown target", "application/json", `{"message":"x","target":"missing"}`, nil, 422},
		{"empty topic", "application/json", `{"message":"x","topic":""}`, nil, 422},
		{"long topic", "application/json", `{"message":"x","topic":"` + strings.Repeat("ж", 129) + `"}`, nil, 422},
		{"zero topic id", "application/json", `{"message":"x","topic_id":0}`, nil, 422},
		{"fraction topic id", "application/json", `{"message":"x","topic_id":1.5}`, nil, 422},
		{"overflow topic id", "application/json", `{"message":"x","topic_id":9223372036854775808}`, nil, 422},
		{"both topic forms", "application/json", `{"message":"x","topic":"name","topic_id":1}`, nil, 422},
		{"bad level", "application/json", `{"message":"x","level":"urgent"}`, nil, 422},
		{"null fallback", "application/json", `{"message":"x","fallback_plain_text":null}`, nil, 422},
		{"empty category", "application/json", `{"message":"x","category":""}`, nil, 422},
		{"chat topic id", "application/json", `{"message":"x","target":"alerts","topic_id":1}`, nil, 422},
		{"empty hashtag", "application/json", `{"message":"x","target":"alerts","topic":"!!!"}`, nil, 422},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sender := &fakeTransport{}
			server := New(testConfig(), sender, context.Background(), nil)
			recorder := performRequest(t, server, http.MethodPost, "/notify", tc.contentType, tc.body, tc.headers)
			if recorder.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d, body = %s", recorder.Code, tc.wantStatus, recorder.Body.String())
			}
			if len(sender.sends)+len(sender.creates)+len(sender.deletes)+len(sender.pins)+sender.icons != 0 {
				t.Fatalf("invalid request contacted Telegram: %#v", sender)
			}
			assertSafeError(t, recorder)
		})
	}
}

func TestNotifyRejectsBodiesLargerThan64KiB(t *testing.T) {
	sender := &fakeTransport{}
	server := New(testConfig(), sender, context.Background(), nil)
	recorder := performRequest(t, server, http.MethodPost, "/notify", "text/plain", strings.Repeat("x", 64*1024+1), nil)
	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if len(sender.sends) != 0 {
		t.Fatal("oversize request was delivered")
	}
}

func TestHashtagNormalizationUsesRunesAndPrefixesNumericTags(t *testing.T) {
	tests := map[string]string{
		"nightly backup": "nightly_backup",
		"42":             "topic_42",
		"42___":          "topic_42",
		"é/東京":           "é_東京",
		"a---b":          "a_b",
	}
	for input, want := range tests {
		got, err := hashtag(input)
		if err != nil || got != want {
			t.Errorf("hashtag(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
}

func TestSecurityChecksRunBeforeMutations(t *testing.T) {
	tests := []struct {
		name    string
		token   string
		host    string
		headers http.Header
		status  int
	}{
		{"origin even empty", "", "localhost", http.Header{"Origin": []string{""}}, 403},
		{"cross site", "", "localhost", http.Header{"Sec-Fetch-Site": []string{"cross-site"}}, 403},
		{"same origin fetch rejected", "", "localhost", http.Header{"Sec-Fetch-Site": []string{"same-origin"}}, 403},
		{"foreign host", "", "attacker.example", nil, 403},
		{"invalid host port", "", "localhost:70000", nil, 403},
		{"missing bearer", "api-secret", "attacker.example", nil, 401},
		{"wrong bearer", "api-secret", "attacker.example", http.Header{"Authorization": []string{"Bearer wrong"}}, 401},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testConfig()
			cfg.APIToken = tc.token
			sender := &fakeTransport{}
			server := New(cfg, sender, context.Background(), nil)
			req := httptest.NewRequest(http.MethodDelete, "/topics/9", nil)
			req.Host = tc.host
			req.Header = tc.headers.Clone()
			recorder := httptest.NewRecorder()
			server.ServeHTTP(recorder, req)
			if recorder.Code != tc.status {
				t.Fatalf("status = %d, want %d, body = %s", recorder.Code, tc.status, recorder.Body.String())
			}
			if len(sender.deletes) != 0 {
				t.Fatal("rejected request mutated Telegram")
			}
		})
	}
}

func TestCrossSitePlainTextNotifyIsRejectedBeforeTelegram(t *testing.T) {
	sender := &fakeTransport{}
	server := New(testConfig(), sender, context.Background(), nil)
	recorder := performRequest(t, server, http.MethodPost, "/notify", "text/plain", "browser payload", map[string]string{
		"Origin":         "https://attacker.example",
		"Sec-Fetch-Site": "cross-site",
	})
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if len(sender.sends)+len(sender.creates)+len(sender.deletes)+len(sender.pins)+sender.icons != 0 {
		t.Fatal("cross-site text/plain notify contacted Telegram")
	}
}

func TestSecurityAllowsBearerWithoutHostRestrictionAndAllowsValidLocalPorts(t *testing.T) {
	tests := []struct {
		name    string
		token   string
		host    string
		headers map[string]string
	}{
		{"bearer external host", "api-secret", "gateway.example", map[string]string{"Authorization": "Bearer api-secret"}},
		{"localhost remapped port", "", "localhost:49152", nil},
		{"ipv4 listen address", "", "127.0.0.1:1", nil},
		{"ipv6 loopback", "", "[::1]:65535", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testConfig()
			cfg.APIToken = tc.token
			sender := &fakeTransport{}
			server := New(cfg, sender, context.Background(), nil)
			req := httptest.NewRequest(http.MethodPost, "/notify", strings.NewReader(`{"message":"ok"}`))
			req.Header.Set("Content-Type", "application/json")
			for key, value := range tc.headers {
				req.Header.Set(key, value)
			}
			req.Host = tc.host
			recorder := httptest.NewRecorder()
			server.ServeHTTP(recorder, req)
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestSecurityAllowsConcreteIPv6ListenAddressWithZone(t *testing.T) {
	cfg := testConfig()
	cfg.Listen = "[fe80::1%eth0]:8080"
	sender := &fakeTransport{}
	server := New(cfg, sender, context.Background(), nil)
	req := httptest.NewRequest(http.MethodPost, "/notify", strings.NewReader(`{"message":"ok"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Host = "[fe80::1]:49152"
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if len(sender.sends) != 1 {
		t.Fatalf("sends = %#v", sender.sends)
	}
}

func TestTelegramErrorsKeepSafeTypedFields(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
		uncertain  bool
		retryAfter float64
	}{
		{"rejected", &telegram.Error{Code: "telegram_rejected", Message: "Telegram rejected the request", HTTPStatus: 502}, 502, "telegram_rejected", false, 0},
		{"timeout uncertain", &telegram.Error{Code: "telegram_timeout", Message: "Telegram request timed out", HTTPStatus: 504, Uncertain: true}, 504, "telegram_timeout", true, 0},
		{"rate limit", &telegram.Error{Code: "telegram_rate_limited", Message: "Telegram rate limit exceeded", HTTPStatus: 429, RetryAfter: 17}, 429, "telegram_rate_limited", false, 17},
		{"unknown error", errors.New("contains bot-secret and payload-secret"), 502, "telegram_error", false, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sender := &fakeTransport{sendErr: tc.err}
			server := New(testConfig(), sender, context.Background(), nil)
			recorder := performRequest(t, server, http.MethodPost, "/notify", "application/json", `{"message":"payload-secret"}`, nil)
			if recorder.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d, body = %s", recorder.Code, tc.wantStatus, recorder.Body.String())
			}
			got := decodeObject(t, recorder)
			if got["code"] != tc.wantCode || (got["uncertain"] == true) != tc.uncertain || got["retry_after"] != tc.retryAfter && tc.retryAfter != 0 {
				t.Fatalf("error response = %#v", got)
			}
			if strings.Contains(recorder.Body.String(), "bot-secret") || strings.Contains(recorder.Body.String(), "payload-secret") {
				t.Fatalf("unsafe response = %s", recorder.Body.String())
			}
		})
	}
}

func TestSendMayUseMultipleIndividuallyBoundedTelegramCalls(t *testing.T) {
	var mu sync.Mutex
	attempts := 0
	telegramServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(30 * time.Millisecond)
		mu.Lock()
		attempts++
		attempt := attempts
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if attempt == 1 {
			io.WriteString(w, `{"ok":false,"error_code":400,"description":"Bad Request: can't parse entities"}`)
			return
		}
		io.WriteString(w, `{"ok":true,"result":{"message_id":33}}`)
	}))
	defer telegramServer.Close()
	cfg := testConfig()
	cfg.Telegram.Timeout = 50 * time.Millisecond
	client, err := telegram.New(telegram.Options{BotToken: "token", BaseURL: telegramServer.URL, Timeout: cfg.Telegram.Timeout})
	if err != nil {
		t.Fatal(err)
	}
	server := New(cfg, client, context.Background(), nil)
	server.requestTimeout = 200 * time.Millisecond
	recorder := performRequest(t, server, http.MethodPost, "/notify", "application/json", `{"message":"<broken>","fallback_plain_text":true}`, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if got := decodeObject(t, recorder); got["message_id"] != float64(33) || got["fallback_used"] != true {
		t.Fatalf("response = %#v", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if attempts != 2 {
		t.Fatalf("send attempts = %d, want 2", attempts)
	}
}

func TestResolverStatesMapToCapacityUncertainAndWaitTimeout(t *testing.T) {
	t.Run("capacity", func(t *testing.T) {
		cfg := testConfig()
		cfg.MaxTopics = 1
		release := make(chan struct{})
		sender := &fakeTransport{}
		sender.createErr = &telegram.Error{Code: "telegram_uncertain", Message: "unknown", HTTPStatus: 502, Uncertain: true}
		server := New(cfg, sender, context.Background(), nil)
		first := performRequest(t, server, http.MethodPost, "/notify", "application/json", `{"message":"x","topic":"one"}`, nil)
		close(release)
		if first.Code != 502 {
			t.Fatalf("first status = %d, body = %s", first.Code, first.Body.String())
		}
		second := performRequest(t, server, http.MethodPost, "/notify", "application/json", `{"message":"x","topic":"two"}`, nil)
		if second.Code != 503 || decodeObject(t, second)["code"] != "topic_capacity_exhausted" {
			t.Fatalf("capacity response = %d %s", second.Code, second.Body.String())
		}
		repeated := performRequest(t, server, http.MethodPost, "/notify", "application/json", `{"message":"x","topic":"one"}`, nil)
		if repeated.Code != 409 || decodeObject(t, repeated)["code"] != "topic_creation_uncertain" {
			t.Fatalf("stored uncertain response = %d %s", repeated.Code, repeated.Body.String())
		}
	})

	t.Run("waiter deadline", func(t *testing.T) {
		sender := &fakeTransport{}
		sender.createErr = nil
		block := make(chan struct{})
		sender.iconList = []string{"x"}
		sender.sendFn = nil
		server := New(testConfig(), &blockingCreateTransport{fakeTransport: sender, block: block}, context.Background(), nil)
		server.requestTimeout = 15 * time.Millisecond
		recorder := performRequest(t, server, http.MethodPost, "/notify", "application/json", `{"message":"x","topic":"slow"}`, nil)
		close(block)
		got := decodeObject(t, recorder)
		if recorder.Code != 504 || got["code"] != "topic_resolution_timeout" || got["uncertain"] != true || !strings.Contains(got["error"].(string), "may finish") {
			t.Fatalf("timeout response = %d %#v", recorder.Code, got)
		}
		if len(sender.sends) != 0 {
			t.Fatal("message sent after resolver waiter timed out")
		}
	})
}

type blockingCreateTransport struct {
	*fakeTransport
	block <-chan struct{}
}

func (b *blockingCreateTransport) CreateTopic(ctx context.Context, chatID, name string, icon telegram.Icon) (telegram.Topic, error) {
	select {
	case <-b.block:
		return b.fakeTransport.CreateTopic(ctx, chatID, name, icon)
	case <-ctx.Done():
		return telegram.Topic{}, ctx.Err()
	}
}

func TestPinTimeoutIsPartialSuccess(t *testing.T) {
	sender := &fakeTransport{sendResult: telegram.SendResult{MessageID: 25}, pinErr: context.DeadlineExceeded}
	server := New(testConfig(), sender, context.Background(), nil)
	recorder := performRequest(t, server, http.MethodPost, "/notify", "application/json", `{"message":"done","category":"result"}`, nil)
	got := decodeObject(t, recorder)
	if recorder.Code != http.StatusOK || got["ok"] != true || got["message_id"] != float64(25) || got["pin_status"] != "failed" {
		t.Fatalf("partial response = %d %#v", recorder.Code, got)
	}
	if !strings.Contains(got["pin_error"].(string), "already delivered") {
		t.Fatalf("unsafe/inactionable pin response = %s", recorder.Body.String())
	}
}

func TestTopicsCreateBypassesRegistryAndDeleteLeavesMappingUntouched(t *testing.T) {
	sender := &fakeTransport{createIDs: []int64{40, 41}}
	server := New(testConfig(), sender, context.Background(), nil)

	created := performRequest(t, server, http.MethodPost, "/topics", "application/json", `{"target":"default","name":"backup"}`, nil)
	if created.Code != http.StatusCreated || decodeObject(t, created)["topic_id"] != float64(40) {
		t.Fatalf("create response = %d %s", created.Code, created.Body.String())
	}
	firstNotify := performRequest(t, server, http.MethodPost, "/notify", "application/json", `{"message":"one","topic":"backup"}`, nil)
	if firstNotify.Code != 200 || len(sender.creates) != 2 || sender.sends[0].TopicID != 41 {
		t.Fatalf("explicit create registered mapping: creates=%#v sends=%#v response=%s", sender.creates, sender.sends, firstNotify.Body.String())
	}
	deleted := performRequest(t, server, http.MethodDelete, "/topics/41?target=default", "", "", nil)
	if deleted.Code != http.StatusOK || len(sender.deletes) != 1 || sender.deletes[0] != (deleteCall{chatID: "-1001", id: 41}) {
		t.Fatalf("delete = %d %s calls=%#v", deleted.Code, deleted.Body.String(), sender.deletes)
	}
	secondNotify := performRequest(t, server, http.MethodPost, "/notify", "application/json", `{"message":"two","topic":"backup"}`, nil)
	if secondNotify.Code != 200 || len(sender.creates) != 2 || sender.sends[1].TopicID != 41 {
		t.Fatalf("mapping changed after delete: creates=%#v sends=%#v", sender.creates, sender.sends)
	}
}

func TestTopicOperationsValidateRouteAndForumTarget(t *testing.T) {
	tests := []struct {
		method, path, contentType, body string
		status                          int
	}{
		{http.MethodPost, "/topics", "application/json", `{"name":""}`, 422},
		{http.MethodPost, "/topics", "application/json", `{"name":"x","extra":true}`, 422},
		{http.MethodPost, "/topics", "application/json", `{"target":"alerts","name":"x"}`, 422},
		{http.MethodDelete, "/topics/0", "", "", 422},
		{http.MethodDelete, "/topics/not-an-id", "", "", 422},
		{http.MethodDelete, "/topics/1?target=alerts", "", "", 422},
		{http.MethodDelete, "/topics/1?other=default", "", "", 422},
		{http.MethodDelete, "/topics/1?target=%zz", "", "", 422},
		{http.MethodDelete, "/topics/1?target=default;other=x", "", "", 422},
		{http.MethodDelete, "/topics/1/extra", "", "", 404},
		{http.MethodPut, "/notify", "application/json", `{}`, 405},
		{http.MethodGet, "/missing", "", "", 404},
	}
	for _, tc := range tests {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			sender := &fakeTransport{}
			server := New(testConfig(), sender, context.Background(), nil)
			recorder := performRequest(t, server, tc.method, tc.path, tc.contentType, tc.body, nil)
			if recorder.Code != tc.status {
				t.Fatalf("status = %d, want %d, body = %s", recorder.Code, tc.status, recorder.Body.String())
			}
			if len(sender.creates)+len(sender.deletes)+len(sender.sends) != 0 {
				t.Fatal("invalid route contacted Telegram")
			}
		})
	}
}

type timeoutBody struct{}

func (timeoutBody) Read([]byte) (int, error) { return 0, timeoutReadError{} }
func (timeoutBody) Close() error             { return nil }

type timeoutReadError struct{}

func (timeoutReadError) Error() string   { return "read deadline reached" }
func (timeoutReadError) Timeout() bool   { return true }
func (timeoutReadError) Temporary() bool { return true }

func TestBodyReadNetTimeoutReturns504WithoutContextRace(t *testing.T) {
	sender := &fakeTransport{}
	server := New(testConfig(), sender, context.Background(), nil)
	req := httptest.NewRequest(http.MethodPost, "/notify", nil)
	req.Body = timeoutBody{}
	req.Header.Set("Content-Type", "text/plain")
	req.Host = "localhost"
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusGatewayTimeout || decodeObject(t, recorder)["code"] != "request_timeout" {
		t.Fatalf("response = %d %s", recorder.Code, recorder.Body.String())
	}
	if len(sender.sends) != 0 {
		t.Fatal("timed-out body was sent")
	}
}

func TestLocalMockEndToEndCreateSendPin(t *testing.T) {
	var mu sync.Mutex
	var methods []string
	telegramServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		mu.Lock()
		methods = append(methods, method)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch method {
		case "getForumTopicIconStickers":
			io.WriteString(w, `{"ok":true,"result":[{"custom_emoji_id":"emoji"}]}`)
		case "createForumTopic":
			io.WriteString(w, `{"ok":true,"result":{"message_thread_id":55,"name":"backup"}}`)
		case "sendMessage":
			io.WriteString(w, `{"ok":true,"result":{"message_id":77}}`)
		case "pinChatMessage":
			io.WriteString(w, `{"ok":true,"result":true}`)
		default:
			http.Error(w, "unexpected", 500)
		}
	}))
	defer telegramServer.Close()
	client, err := telegram.New(telegram.Options{BotToken: "token", BaseURL: telegramServer.URL, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(New(testConfig(), client, context.Background(), nil))
	defer server.Close()
	response, err := http.Post(server.URL+"/notify", "application/json", strings.NewReader(`{"message":"done","topic":"backup","category":"result"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != 200 || !bytes.Contains(body, []byte(`"message_id":77`)) || !bytes.Contains(body, []byte(`"topic_id":55`)) || !bytes.Contains(body, []byte(`"pin_status":"pinned"`)) {
		t.Fatalf("gateway response = %d %s", response.StatusCode, body)
	}
	mu.Lock()
	defer mu.Unlock()
	want := []string{"getForumTopicIconStickers", "createForumTopic", "sendMessage", "pinChatMessage"}
	if strings.Join(methods, ",") != strings.Join(want, ",") {
		t.Fatalf("Telegram sequence = %v, want %v", methods, want)
	}
}

func TestServeForcesCancellationAfterGracePeriod(t *testing.T) {
	started := make(chan struct{})
	cancelled := make(chan struct{})
	sender := &fakeTransport{sendFn: func(ctx context.Context, _ telegram.SendRequest) (telegram.SendResult, error) {
		close(started)
		<-ctx.Done()
		close(cancelled)
		return telegram.SendResult{}, ctx.Err()
	}}
	server := New(testConfig(), sender, context.Background(), nil)
	server.shutdownGrace = 20 * time.Millisecond
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serveCtx, stop := context.WithCancel(context.Background())
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(serveCtx, listener) }()
	requestDone := make(chan struct{})
	go func() {
		defer close(requestDone)
		req, _ := http.NewRequest(http.MethodPost, "http://"+listener.Addr().String()+"/notify", strings.NewReader(`{"message":"x"}`))
		req.Header.Set("Content-Type", "application/json")
		response, err := http.DefaultClient.Do(req)
		if err == nil {
			response.Body.Close()
		}
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("request did not reach sender")
	}
	stop()
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("active request was not force-cancelled")
	}
	select {
	case err := <-serveDone:
		if err != nil {
			t.Fatalf("Serve returned %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Serve did not return")
	}
	select {
	case <-requestDone:
	case <-time.After(time.Second):
		t.Fatal("client request did not finish")
	}
}

func TestServeLetsActiveRequestFinishDuringGracePeriod(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	sender := &fakeTransport{sendFn: func(ctx context.Context, _ telegram.SendRequest) (telegram.SendResult, error) {
		close(started)
		select {
		case <-release:
			return telegram.SendResult{MessageID: 90}, nil
		case <-ctx.Done():
			return telegram.SendResult{}, ctx.Err()
		}
	}}
	server := New(testConfig(), sender, context.Background(), nil)
	server.shutdownGrace = time.Second
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serveCtx, stop := context.WithCancel(context.Background())
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(serveCtx, listener) }()
	responseDone := make(chan int, 1)
	go func() {
		req, _ := http.NewRequest(http.MethodPost, "http://"+listener.Addr().String()+"/notify", strings.NewReader(`{"message":"x"}`))
		req.Header.Set("Content-Type", "application/json")
		response, requestErr := http.DefaultClient.Do(req)
		if requestErr != nil {
			responseDone <- 0
			return
		}
		defer response.Body.Close()
		responseDone <- response.StatusCode
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("request did not start")
	}
	stop()
	close(release)
	select {
	case status := <-responseDone:
		if status != 200 {
			t.Fatalf("response status = %d", status)
		}
	case <-time.After(time.Second):
		t.Fatal("request did not drain")
	}
	select {
	case err := <-serveDone:
		if err != nil {
			t.Fatalf("Serve returned %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Serve did not return after drain")
	}
}

func TestServeClassifiesSlowBodyReadAsTimeout(t *testing.T) {
	server := New(testConfig(), &fakeTransport{}, context.Background(), nil)
	server.requestTimeout = 25 * time.Millisecond
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serveCtx, stop := context.WithCancel(context.Background())
	defer stop()
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(serveCtx, listener) }()
	connection, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	_, err = io.WriteString(connection, "POST /notify HTTP/1.1\r\nHost: localhost\r\nContent-Type: text/plain\r\nContent-Length: 10\r\n\r\nx")
	if err != nil {
		t.Fatal(err)
	}
	_ = connection.SetReadDeadline(time.Now().Add(time.Second))
	response, err := http.ReadResponse(bufio.NewReader(connection), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != 504 || !bytes.Contains(body, []byte(`"code":"request_timeout"`)) {
		t.Fatalf("slow body response = %d %s", response.StatusCode, body)
	}
	stop()
	select {
	case err := <-serveDone:
		if err != nil {
			t.Fatalf("Serve returned %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Serve did not stop")
	}
}

func assertSafeError(t *testing.T, recorder *httptest.ResponseRecorder) {
	t.Helper()
	got := decodeObject(t, recorder)
	if got["ok"] != false || got["code"] == "" || got["error"] == "" {
		t.Fatalf("error body = %#v", got)
	}
}

func mapsEqual(left, right map[string]any) bool {
	leftJSON, _ := json.Marshal(left)
	rightJSON, _ := json.Marshal(right)
	return bytes.Equal(leftJSON, rightJSON)
}
