package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type blockingReadCloser struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
	closed  atomic.Int32
}

func (r *blockingReadCloser) Read([]byte) (int, error) {
	select {
	case <-r.started:
	default:
		close(r.started)
	}
	<-r.release
	return 0, io.EOF
}

func (r *blockingReadCloser) Close() error { r.closed.Add(1); return nil }

func (r *blockingReadCloser) unblock() { r.once.Do(func() { close(r.release) }) }

func TestNotifySendsFlagsAuthorizationAndMessage(t *testing.T) {
	t.Setenv("GONG_URL", "")
	t.Setenv("GONG_API_TOKEN", "")
	request := make(chan *http.Request, 1)
	body := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		request <- r.Clone(context.Background())
		body <- data
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"ok":true,"message_id":42,"pin_status":"not_requested"}`+"\n")
	}))
	defer server.Close()

	var stdout, stderr bytes.Buffer
	exit := Run(context.Background(), []string{"notify", "--url", server.URL, "--token", "api-secret", "--target", "alerts", "--topic", "nightly backup", "--level", "success", "--category", "result", "hello", "quoted world"}, strings.NewReader("unused"), &stdout, &stderr)

	if exit != 0 || stderr.Len() != 0 {
		t.Fatalf("exit=%d stderr=%q", exit, stderr.String())
	}
	if stdout.String() != `{"ok":true,"message_id":42,"pin_status":"not_requested"}`+"\n" {
		t.Fatalf("stdout=%q", stdout.String())
	}
	gotRequest := <-request
	if gotRequest.Method != http.MethodPost || gotRequest.URL.Path != "/notify" {
		t.Fatalf("request=%s %s", gotRequest.Method, gotRequest.URL.String())
	}
	if gotRequest.Header.Get("Authorization") != "Bearer api-secret" || gotRequest.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("headers=%v", gotRequest.Header)
	}
	var got map[string]any
	if err := json.Unmarshal(<-body, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"message": "hello quoted world", "target": "alerts", "topic": "nightly backup", "level": "success", "category": "result", "fallback_plain_text": true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("payload=%#v want=%#v", got, want)
	}
}

func TestNotifyPreservesMultilineStdinAndLeadingDashArgument(t *testing.T) {
	t.Setenv("GONG_API_TOKEN", "")
	tests := []struct {
		name  string
		args  []string
		stdin string
		want  string
	}{
		{name: "stdin", stdin: "first line\nsecond 'quoted' line\n", want: "first line\nsecond 'quoted' line\n"},
		{name: "leading dash", args: []string{"--", "--deployment complete"}, want: "--deployment complete"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var gotMessage string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var payload struct {
					Message string `json:"message"`
				}
				_ = json.NewDecoder(r.Body).Decode(&payload)
				gotMessage = payload.Message
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, `{"ok":true,"message_id":1,"pin_status":"not_requested"}`+"\n")
			}))
			defer server.Close()
			args := append([]string{"notify", "--url", server.URL}, test.args...)
			var stdout, stderr bytes.Buffer
			if exit := Run(context.Background(), args, strings.NewReader(test.stdin), &stdout, &stderr); exit != 0 {
				t.Fatalf("exit=%d stderr=%q", exit, stderr.String())
			}
			if gotMessage != test.want {
				t.Fatalf("message=%q want=%q", gotMessage, test.want)
			}
		})
	}
}

func TestInvalidUTF8PayloadInputExitsTwoWithoutRequest(t *testing.T) {
	t.Setenv("GONG_API_TOKEN", "")
	invalid := string([]byte{0xff})
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"ok":true}`+"\n")
	}))
	defer server.Close()
	tests := []struct {
		name  string
		args  []string
		stdin string
	}{
		{"message argv", []string{"notify", "--url", server.URL, invalid}, ""},
		{"message stdin", []string{"notify", "--url", server.URL}, invalid},
		{"topic", []string{"notify", "--url", server.URL, "--topic", invalid, "message"}, ""},
		{"category", []string{"notify", "--url", server.URL, "--category", invalid, "message"}, ""},
		{"notify target", []string{"notify", "--url", server.URL, "--target", invalid, "message"}, ""},
		{"create name", []string{"topics", "create", "--url", server.URL, "--name", invalid}, ""},
		{"create target", []string{"topics", "create", "--url", server.URL, "--name", "topic", "--target", invalid}, ""},
		{"delete target", []string{"topics", "delete", "--url", server.URL, "--id", "12", "--target", invalid}, ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if exit := Run(context.Background(), test.args, strings.NewReader(test.stdin), &stdout, &stderr); exit != 2 {
				t.Fatalf("exit=%d stdout=%q stderr=%q", exit, stdout.String(), stderr.String())
			}
			if stdout.Len() != 0 || !strings.Contains(stderr.String(), "valid UTF-8") {
				t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
			}
		})
	}
	if requests.Load() != 0 {
		t.Fatalf("gateway requests=%d", requests.Load())
	}
}

func TestOversizedNotificationInputExitsTwoWithoutRequest(t *testing.T) {
	t.Setenv("GONG_API_TOKEN", "")
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"ok":true}`+"\n")
	}))
	defer server.Close()
	tests := []struct {
		name  string
		stdin string
	}{
		{"stdin exceeds read bound", strings.Repeat("x", 64*1024+1)},
		{"JSON body exceeds gateway bound", strings.Repeat("x", 65500)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			exit := Run(context.Background(), []string{"notify", "--url", server.URL}, strings.NewReader(test.stdin), &stdout, &stderr)
			if exit != 2 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "64 KiB") {
				t.Fatalf("exit=%d stdout=%q stderr=%q", exit, stdout.String(), stderr.String())
			}
		})
	}
	if requests.Load() != 0 {
		t.Fatalf("gateway requests=%d", requests.Load())
	}
}

func TestCanceledRunDoesNotAssumeInjectedReaderOwnership(t *testing.T) {
	t.Setenv("GONG_API_TOKEN", "")
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"ok":true,"message_id":1,"pin_status":"not_requested"}`+"\n")
	}))
	defer server.Close()
	stdin := &blockingReadCloser{started: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(stdin.unblock)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	type result struct {
		exit   int
		stdout string
		stderr string
	}
	done := make(chan result, 1)
	go func() {
		var stdout, stderr bytes.Buffer
		exit := Run(ctx, []string{"notify", "--url", server.URL}, stdin, &stdout, &stderr)
		done <- result{exit: exit, stdout: stdout.String(), stderr: stderr.String()}
	}()
	select {
	case <-stdin.started:
	case <-time.After(time.Second):
		t.Fatal("Run did not start reading stdin")
	}
	cancel()
	select {
	case got := <-done:
		t.Fatalf("Run returned before the synchronous reader completed: %+v", got)
	case <-time.After(50 * time.Millisecond):
	}
	if stdin.closed.Load() != 0 {
		t.Fatalf("Run closed caller-owned stdin %d times", stdin.closed.Load())
	}
	stdin.unblock()
	select {
	case got := <-done:
		if got.exit != 1 || !strings.Contains(got.stdout, `"code":"stdin_cancelled"`) {
			t.Fatalf("exit=%d stdout=%q stderr=%q", got.exit, got.stdout, got.stderr)
		}
		if !strings.Contains(got.stderr, "cancelled") || !strings.Contains(got.stderr, "no request was sent") {
			t.Fatalf("stderr=%q", got.stderr)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not finish after the synchronous reader completed")
	}
	if requests.Load() != 0 {
		t.Fatalf("gateway requests=%d", requests.Load())
	}
}

func TestNotifyReportsDeliveredPinFailureWithoutSuggestingRetry(t *testing.T) {
	t.Setenv("GONG_API_TOKEN", "")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"ok":true,"message_id":42,"pin_status":"failed","pin_error":"message was already delivered"}`+"\n")
	}))
	defer server.Close()
	var stdout, stderr bytes.Buffer

	exit := Run(context.Background(), []string{"notify", "--url", server.URL, "done"}, strings.NewReader(""), &stdout, &stderr)

	if exit != 1 {
		t.Fatalf("exit=%d", exit)
	}
	if !strings.Contains(stderr.String(), "message was delivered") || !strings.Contains(stderr.String(), "do not retry") {
		t.Fatalf("stderr=%q", stderr.String())
	}
	if !strings.Contains(stdout.String(), `"pin_status":"failed"`) {
		t.Fatalf("stdout=%q", stdout.String())
	}
}

func TestTopicsCreateAndDeleteUseExactAPI(t *testing.T) {
	t.Setenv("GONG_API_TOKEN", "env-token")
	type observed struct{ method, path, query, auth, body string }
	requests := make(chan observed, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		requests <- observed{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization"), string(data)}
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusCreated)
			io.WriteString(w, `{"ok":true,"topic_id":55,"name":"Ночной backup","target":"ops team"}`+"\n")
			return
		}
		io.WriteString(w, `{"ok":true,"topic_id":9223372036854775807,"target":"ops team"}`+"\n")
	}))
	defer server.Close()

	for _, args := range [][]string{
		{"topics", "create", "--url", server.URL, "--name", "Ночной backup", "--target", "ops team"},
		{"topics", "delete", "--url", server.URL, "--id", "9223372036854775807", "--target", "ops team"},
	} {
		var stdout, stderr bytes.Buffer
		if exit := Run(context.Background(), args, strings.NewReader(""), &stdout, &stderr); exit != 0 {
			t.Fatalf("args=%v exit=%d stderr=%q", args, exit, stderr.String())
		}
	}
	create := <-requests
	if create.method != http.MethodPost || create.path != "/topics" || create.query != "" || create.auth != "Bearer env-token" {
		t.Fatalf("create=%+v", create)
	}
	var payload map[string]string
	if err := json.Unmarshal([]byte(create.body), &payload); err != nil || !reflect.DeepEqual(payload, map[string]string{"name": "Ночной backup", "target": "ops team"}) {
		t.Fatalf("create body=%q payload=%v err=%v", create.body, payload, err)
	}
	deleteRequest := <-requests
	if deleteRequest.method != http.MethodDelete || deleteRequest.path != "/topics/9223372036854775807" || deleteRequest.query != "target=ops+team" || deleteRequest.body != "" {
		t.Fatalf("delete=%+v", deleteRequest)
	}
}

func TestMalformedSuccessEnvelopeIsUncertainFailure(t *testing.T) {
	t.Setenv("GONG_API_TOKEN", "")
	tests := []struct {
		name     string
		response string
		status   int
		args     func(string) []string
	}{
		{"notify missing message id", `{"ok":true,"pin_status":"not_requested"}`, http.StatusOK, func(base string) []string { return []string{"notify", "--url", base, "done"} }},
		{"notify unknown pin status", `{"ok":true,"message_id":7,"pin_status":"unknown"}`, http.StatusOK, func(base string) []string { return []string{"notify", "--url", base, "done"} }},
		{"create missing fields", `{"ok":true}`, http.StatusCreated, func(base string) []string {
			return []string{"topics", "create", "--url", base, "--name", "nightly", "--target", "ops"}
		}},
		{"create mismatched name", `{"ok":true,"topic_id":8,"name":"other","target":"ops"}`, http.StatusCreated, func(base string) []string {
			return []string{"topics", "create", "--url", base, "--name", "nightly", "--target", "ops"}
		}},
		{"create mismatched target", `{"ok":true,"topic_id":8,"name":"nightly","target":"other"}`, http.StatusCreated, func(base string) []string {
			return []string{"topics", "create", "--url", base, "--name", "nightly", "--target", "ops"}
		}},
		{"delete missing fields", `{"ok":true}`, http.StatusOK, func(base string) []string {
			return []string{"topics", "delete", "--url", base, "--id", "8", "--target", "ops"}
		}},
		{"delete mismatched id", `{"ok":true,"topic_id":9,"target":"ops"}`, http.StatusOK, func(base string) []string {
			return []string{"topics", "delete", "--url", base, "--id", "8", "--target", "ops"}
		}},
		{"delete mismatched target", `{"ok":true,"topic_id":8,"target":"other"}`, http.StatusOK, func(base string) []string {
			return []string{"topics", "delete", "--url", base, "--id", "8", "--target", "ops"}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(test.status)
				io.WriteString(w, test.response+"\n")
			}))
			defer server.Close()
			var stdout, stderr bytes.Buffer
			exit := Run(context.Background(), test.args(server.URL), strings.NewReader(""), &stdout, &stderr)
			if exit != 1 || !strings.Contains(stdout.String(), `"code":"invalid_response"`) {
				t.Fatalf("exit=%d stdout=%q stderr=%q", exit, stdout.String(), stderr.String())
			}
			if strings.Contains(stdout.String(), test.response) || !strings.Contains(stderr.String(), "outcome is uncertain") || !strings.Contains(stderr.String(), "do not retry") {
				t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
			}
		})
	}
}

func TestClientDoesNotFollowRedirectOrReplayMutationBody(t *testing.T) {
	t.Setenv("GONG_API_TOKEN", "")
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Redirect(w, r, "/notify-again", http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	var stdout, stderr bytes.Buffer

	exit := Run(context.Background(), []string{"notify", "--url", server.URL, "done"}, strings.NewReader(""), &stdout, &stderr)

	if exit != 1 || requests.Load() != 1 {
		t.Fatalf("exit=%d requests=%d stderr=%q", exit, requests.Load(), stderr.String())
	}
	if !json.Valid(stdout.Bytes()) || !strings.Contains(stdout.String(), `"code":"redirect_refused"`) {
		t.Fatalf("stdout=%q", stdout.String())
	}
	req, err := newMutationRequest(context.Background(), http.MethodPost, server.URL+"/notify", []byte(`{"message":"done"}`), "")
	if err != nil {
		t.Fatal(err)
	}
	if req.GetBody != nil {
		t.Fatal("mutation request body is replayable")
	}
}

func TestAPIErrorsStayOnStdoutAndUncertainOutcomeIsExplicit(t *testing.T) {
	t.Setenv("GONG_API_TOKEN", "")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusGatewayTimeout)
		io.WriteString(w, `{"ok":false,"code":"request_timeout","error":"remote operation timed out","uncertain":true}`+"\n")
	}))
	defer server.Close()
	var stdout, stderr bytes.Buffer
	exit := Run(context.Background(), []string{"notify", "--url", server.URL, "done"}, strings.NewReader(""), &stdout, &stderr)
	if exit != 1 || !strings.Contains(stdout.String(), `"code":"request_timeout"`) {
		t.Fatalf("exit=%d stdout=%q", exit, stdout.String())
	}
	if !strings.Contains(stderr.String(), "outcome is uncertain") || !strings.Contains(stderr.String(), "do not retry") {
		t.Fatalf("stderr=%q", stderr.String())
	}
}

func TestArgumentErrorsExitTwoWithoutContactingGateway(t *testing.T) {
	t.Setenv("GONG_URL", "")
	t.Setenv("GONG_API_TOKEN", "")
	var stdout, stderr bytes.Buffer
	tests := [][]string{
		{},
		{"unknown-secret-command"},
		{"help", "unexpected"},
		{"--help", "unexpected"},
		{"version", "unexpected"},
		{"--version", "unexpected"},
		{"topics", "help", "unexpected"},
		{"topics", "--help", "unexpected"},
		{"notify", "--topic", "name", "--topic-id", "12", "message"},
		{"notify", "--topic", "", "--topic-id", "", "message"},
		{"notify", "--topic", "", "message"},
		{"notify", "--topic-id", "", "message"},
		{"notify", "--category", "", "message"},
		{"notify", "--target", "", "message"},
		{"notify", "--level", "urgent", "message"},
		{"notify", "--topic-id", "0", "message"},
		{"topics", "create"},
		{"topics", "create", "--name", "topic", "--target", ""},
		{"topics", "delete", "--id", "not-a-number"},
		{"topics", "delete", "--id", "12", "--target", ""},
		{"notify", "--url", "ftp://example.test", "message"},
	}
	for _, args := range tests {
		stdout.Reset()
		stderr.Reset()
		if exit := Run(context.Background(), args, strings.NewReader(""), &stdout, &stderr); exit != 2 {
			t.Fatalf("args=%v exit=%d stdout=%q stderr=%q", args, exit, stdout.String(), stderr.String())
		}
		if stdout.Len() != 0 || stderr.Len() == 0 {
			t.Fatalf("args=%v stdout=%q stderr=%q", args, stdout.String(), stderr.String())
		}
		if strings.Contains(stderr.String(), "unknown-secret-command") {
			t.Fatalf("unknown command was echoed: %q", stderr.String())
		}
	}
}

func TestHelpVersionAndServeConfigFailure(t *testing.T) {
	oldVersion := Version
	Version = "v1.2.3-test"
	t.Cleanup(func() { Version = oldVersion })
	for _, args := range [][]string{{"--help"}, {"notify", "--help"}, {"topics", "delete", "--help"}} {
		var stdout, stderr bytes.Buffer
		if exit := Run(context.Background(), args, strings.NewReader(""), &stdout, &stderr); exit != 0 || stdout.Len() == 0 || stderr.Len() != 0 {
			t.Fatalf("args=%v exit=%d stdout=%q stderr=%q", args, exit, stdout.String(), stderr.String())
		}
	}
	var stdout, stderr bytes.Buffer
	if exit := Run(context.Background(), []string{"version"}, strings.NewReader(""), &stdout, &stderr); exit != 0 || stdout.String() != "v1.2.3-test\n" {
		t.Fatalf("version exit=%d stdout=%q stderr=%q", exit, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if exit := Run(context.Background(), []string{"serve", "--config", filepath.Join(t.TempDir(), "missing.yaml")}, strings.NewReader(""), &stdout, &stderr); exit != 1 || !strings.Contains(stderr.String(), "load config") {
		t.Fatalf("serve exit=%d stdout=%q stderr=%q", exit, stdout.String(), stderr.String())
	}
}

func TestRequestTimeoutProducesSafeMachineError(t *testing.T) {
	t.Setenv("GONG_API_TOKEN", "secret-that-must-not-appear")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
	}))
	defer server.Close()
	var stdout, stderr bytes.Buffer
	exit := Run(context.Background(), []string{"notify", "--url", server.URL, "--timeout", "10ms", "done"}, strings.NewReader(""), &stdout, &stderr)
	combined := stdout.String() + stderr.String()
	if exit != 1 || !strings.Contains(stdout.String(), `"code":"request_failed"`) || strings.Contains(combined, server.URL) || strings.Contains(combined, "secret-that-must-not-appear") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", exit, stdout.String(), stderr.String())
	}
}

func TestExecutableSubprocessUsesEnvironmentAndExitContract(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the command")
	}
	request := make(chan *http.Request, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request <- r.Clone(context.Background())
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"ok":true,"message_id":9,"pin_status":"not_requested"}`+"\n")
	}))
	defer server.Close()
	binary := filepath.Join(t.TempDir(), "gong")
	build := exec.Command("go", "build", "-o", binary, "../../cmd/gong")
	build.Env = os.Environ()
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build command: %v\n%s", err, output)
	}
	command := exec.Command(binary, "notify")
	command.Env = append(os.Environ(), "GONG_URL="+server.URL, "GONG_API_TOKEN=subprocess-token")
	command.Stdin = strings.NewReader("subprocess\nmessage\n")
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("run command: %v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
	got := <-request
	if got.Header.Get("Authorization") != "Bearer subprocess-token" || stdout.String() != `{"ok":true,"message_id":9,"pin_status":"not_requested"}`+"\n" || stderr.Len() != 0 {
		t.Fatalf("auth=%q stdout=%q stderr=%q", got.Header.Get("Authorization"), stdout.String(), stderr.String())
	}
}

func TestExecutableSIGINTInterruptsOpenStdinPipe(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and signals the command")
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"ok":true,"message_id":9,"pin_status":"not_requested"}`+"\n")
	}))
	defer server.Close()
	binary := filepath.Join(t.TempDir(), "gong")
	build := exec.Command("go", "build", "-o", binary, "../../cmd/gong")
	build.Env = os.Environ()
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build command: %v\n%s", err, output)
	}
	command := exec.Command(binary, "notify")
	command.Env = append(os.Environ(), "GONG_URL="+server.URL, "GONG_API_TOKEN=")
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	time.Sleep(150 * time.Millisecond)
	if err := command.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
			t.Fatalf("wait error=%v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
		}
	case <-time.After(350 * time.Millisecond):
		_ = command.Process.Kill()
		<-done
		t.Fatal("gong remained alive after SIGINT with stdin open")
	}
	if !strings.Contains(stdout.String(), `"code":"stdin_cancelled"`) || !strings.Contains(stderr.String(), "no request was sent") {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	if requests.Load() != 0 {
		t.Fatalf("gateway requests=%d", requests.Load())
	}
}
