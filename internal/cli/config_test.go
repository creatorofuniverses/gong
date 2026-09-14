package cli

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// Keep automatic discovery away from the developer's personal configuration.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "gong-cli-config-test-")
	if err != nil {
		panic(err)
	}
	if err := os.Setenv("XDG_CONFIG_HOME", dir); err != nil {
		panic(err)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func clientConfig(t *testing.T, listen, token string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gong.yaml")
	data := fmt.Sprintf("listen: %q\napi_token: %q\ntelegram:\n  bot_token: dummy-bot-token\ntargets:\n  default:\n    chat_id: '123'\n", listen, token)
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestClientConfigUsesPortAndTokenForAllCommands(t *testing.T) {
	t.Setenv("GONG_URL", "")
	t.Setenv("GONG_API_TOKEN", "")
	t.Setenv("GONG_BOT_TOKEN", "")
	t.Setenv("GONG_PROXY_URL", "")
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if got := r.Header.Get("Authorization"); got != "Bearer config-token" {
			t.Errorf("authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/notify":
			io.WriteString(w, `{"ok":true,"message_id":1,"pin_status":"not_requested"}`)
		case "/topics":
			io.WriteString(w, `{"ok":true,"topic_id":2,"name":"backup","target":"default"}`)
		case "/topics/2":
			io.WriteString(w, `{"ok":true,"topic_id":2,"target":"default"}`)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	}))
	defer server.Close()
	path := clientConfig(t, strings.TrimPrefix(server.URL, "http://"), "config-token")
	for _, args := range [][]string{
		{"notify", "--config", path, "hello"},
		{"topics", "create", "--config", path, "--name", "backup"},
		{"topics", "delete", "--config", path, "--id", "2"},
	} {
		var out, stderr bytes.Buffer
		if code := Run(context.Background(), args, nil, &out, &stderr); code != 0 {
			t.Fatalf("%v: code=%d stderr=%s stdout=%s", args[:1], code, &stderr, &out)
		}
	}
	if requests != 3 {
		t.Fatalf("requests=%d", requests)
	}
}

func TestClientConfigDoesNotRequireServerSettings(t *testing.T) {
	t.Setenv("GONG_URL", "")
	t.Setenv("GONG_API_TOKEN", "")
	t.Setenv("GONG_BOT_TOKEN", "")
	// Server environment settings are irrelevant in a client shell too.
	t.Setenv("GONG_PROXY_URL", "invalid-proxy")
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get("Authorization") != "Bearer client-token" {
			t.Error("client did not use the configured API token")
		}
		io.WriteString(w, `{"ok":true,"message_id":1,"pin_status":"not_requested"}`)
	}))
	defer server.Close()
	for name, serverSettings := range map[string]string{
		"minimal client":                     "",
		"server token only in another shell": "targets: {default: {chat_id: '123'}}\n",
		"invalid server values":              "telegram: {bot_token: '', proxy_url: invalid, timeout: soon}\ntargets: {'bad alias': {chat_id: '0', mode: invalid}}\nnotify_min_level: invalid\npin_categories: ['']\nmax_topics: -1\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "gong.yaml")
			data := fmt.Sprintf("listen: %q\napi_token: client-token\n%s", strings.TrimPrefix(server.URL, "http://"), serverSettings)
			if err := os.WriteFile(path, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			var stderr bytes.Buffer
			if code := Run(context.Background(), []string{"notify", "--config", path, "hello"}, nil, io.Discard, &stderr); code != 0 {
				t.Fatalf("code=%d stderr=%s", code, &stderr)
			}
		})
	}
	if requests != 3 {
		t.Fatalf("requests=%d, want 3", requests)
	}
}

func TestClientConfigRetainsStrictDecodingWithExplicitOverrides(t *testing.T) {
	t.Setenv("GONG_API_TOKEN", "")
	t.Setenv("GONG_URL", "")
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++ }))
	defer server.Close()
	for name, data := range map[string]string{
		"unknown root field":       "unknown: secret-value\n",
		"unknown telegram field":   "telegram: {unknown: secret-value}\n",
		"unknown target field":     "targets: {default: {unknown: secret-value}}\n",
		"wrong server type":        "telegram: [secret-value]\n",
		"wrong server scalar type": "max_topics: secret-value\n",
		"wrong target type":        "targets: [secret-value]\n",
		"multiple documents":       "{}\n---\n{}\n",
		"malformed YAML":           "telegram: [secret-value\n",
		"invalid listen":           "listen: secret-value\n",
		"invalid token":            "api_token: 'secret-value with spaces'\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "private-file.yaml")
			if err := os.WriteFile(path, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			var out, stderr bytes.Buffer
			code := Run(context.Background(), []string{"notify", "--config", path, "--url", server.URL, "--token", "explicit-token", "hello"}, nil, &out, &stderr)
			if code != 1 || !strings.Contains(out.String(), "config_failed") || requests != 0 {
				t.Fatalf("code=%d requests=%d stdout=%s stderr=%s", code, requests, &out, &stderr)
			}
			if strings.Contains(stderr.String()+out.String(), "secret-value") || strings.Contains(stderr.String()+out.String(), "private-file") {
				t.Fatal("configuration details leaked")
			}
		})
	}
}

func TestClientConfigOverridesAreExplicit(t *testing.T) {
	t.Setenv("GONG_BOT_TOKEN", "")
	t.Setenv("GONG_PROXY_URL", "")
	for _, tc := range []struct {
		name, envToken, flagToken, want string
		urlFlag                         bool
	}{
		{"environment", "env-token", "", "Bearer env-token", false},
		{"flags", "env-token", "flag-token", "Bearer flag-token", true},
		{"empty-token-flag", "env-token", "", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				if got := r.Header.Get("Authorization"); got != tc.want {
					t.Errorf("authorization=%q want=%q", got, tc.want)
				}
				io.WriteString(w, `{"ok":true,"message_id":1,"pin_status":"not_requested"}`)
			}))
			defer server.Close()
			t.Setenv("GONG_URL", server.URL)
			t.Setenv("GONG_API_TOKEN", tc.envToken)
			path := clientConfig(t, "127.0.0.1:1", "config-token")
			args := []string{"notify", "--config", path}
			if tc.urlFlag {
				t.Setenv("GONG_URL", "http://127.0.0.1:1")
				args = append(args, "--url", server.URL, "--token", tc.flagToken)
			}
			args = append(args, "hello")
			var out, stderr bytes.Buffer
			if code := Run(context.Background(), args, nil, &out, &stderr); code != 0 || !called {
				t.Fatalf("code=%d called=%v stderr=%s", code, called, &stderr)
			}
		})
	}
}

func TestInvalidClientConfigFailsBeforeRequestWithoutSecrets(t *testing.T) {
	t.Setenv("GONG_API_TOKEN", "")
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++ }))
	defer server.Close()
	t.Setenv("GONG_URL", server.URL)
	invalid := filepath.Join(t.TempDir(), "private-file-name.yaml")
	if err := os.WriteFile(invalid, []byte("token: super-secret-value\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"", invalid, invalid + ".missing"} {
		var out, stderr bytes.Buffer
		code := Run(context.Background(), []string{"notify", "--config", path, "hello"}, nil, &out, &stderr)
		if code == 0 || requests != 0 || !strings.Contains(stderr.String(), "config") {
			t.Fatalf("code=%d requests=%d stderr=%s", code, requests, &stderr)
		}
		if strings.Contains(stderr.String(), "private-file-name") || strings.Contains(stderr.String(), "super-secret-value") {
			t.Fatal("configuration details leaked")
		}
	}
}

func TestRootHelpShowsClientAddressAndConfig(t *testing.T) {
	var out bytes.Buffer
	if code := Run(context.Background(), []string{"--help"}, nil, &out, io.Discard); code != 0 {
		t.Fatalf("code=%d", code)
	}
	for _, text := range []string{"--url", "--config", "GONG_URL", "8081", "listen"} {
		if !strings.Contains(out.String(), text) {
			t.Errorf("root help missing %q", text)
		}
	}
}

func TestClientAutomaticallyDiscoversConfigAndWorksWithoutOne(t *testing.T) {
	t.Setenv("GONG_URL", "")
	t.Setenv("GONG_API_TOKEN", "")
	t.Setenv("GONG_BOT_TOKEN", "")
	t.Setenv("GONG_PROXY_URL", "")
	t.Chdir(t.TempDir())
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	var tokens []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokens = append(tokens, r.Header.Get("Authorization"))
		io.WriteString(w, `{"ok":true,"message_id":1,"pin_status":"not_requested"}`)
	}))
	defer server.Close()
	write := func(path, token string) {
		t.Helper()
		source := clientConfig(t, strings.TrimPrefix(server.URL, "http://"), token)
		data, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	run := func(args ...string) {
		t.Helper()
		var out, stderr bytes.Buffer
		if code := Run(context.Background(), append([]string{"notify"}, args...), nil, &out, &stderr); code != 0 {
			t.Fatalf("code=%d stderr=%s", code, &stderr)
		}
	}
	// With no configuration file, explicit connection settings still work.
	run("--url", server.URL, "without config")
	write(filepath.Join(xdg, "gong", "config.yaml"), "user-config")
	run("user configuration")
	write("gong.yaml", "local-config")
	run("local configuration wins")
	explicit := clientConfig(t, strings.TrimPrefix(server.URL, "http://"), "explicit-config")
	run("--config", explicit, "explicit configuration wins")
	if got := strings.Join(tokens, ","); got != ",Bearer user-config,Bearer local-config,Bearer explicit-config" {
		t.Fatalf("tokens=%q", got)
	}
	if err := os.WriteFile("gong.yaml", []byte("invalid: configuration\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	if code := Run(context.Background(), []string{"notify", "hello"}, nil, io.Discard, &stderr); code != 1 || len(tokens) != 4 {
		t.Fatalf("invalid discovered config: code=%d requests=%d stderr=%s", code, len(tokens), &stderr)
	}
}

func TestConfigBindAddressesBecomeClientURLs(t *testing.T) {
	for _, tc := range []struct{ listen, want string }{
		{"127.0.0.1:8081", "http://127.0.0.1:8081"},
		{":8081", "http://127.0.0.1:8081"},
		{"0.0.0.0:8081", "http://127.0.0.1:8081"},
		{"[::ffff:0.0.0.0]:8081", "http://127.0.0.1:8081"},
		{"[::]:8081", "http://[::1]:8081"},
		{"[0:0:0:0:0:0:0:0]:8081", "http://[::1]:8081"},
		{"[::1]:8081", "http://[::1]:8081"},
		{"127.10.20.30:8081", "http://127.10.20.30:8081"},
		{"[::ffff:127.0.0.1]:8081", "http://127.0.0.1:8081"},
		{"localhost:8081", "http://127.0.0.1:8081"},
		{"LOCALHOST.:8081", "http://127.0.0.1:8081"},
	} {
		if got, err := localGatewayURL(tc.listen); err != nil || got != tc.want {
			t.Errorf("%s: got %s, error %v, want %s", tc.listen, got, err, tc.want)
		}
	}
}

func TestClientWithoutConfigKeepsDefaultAddress(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("GONG_URL", "")
	t.Setenv("GONG_API_TOKEN", "")
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	values := addClientFlags(fs)
	if err := resolveClientConfig(fs, values); err != nil {
		t.Fatal(err)
	}
	if values.baseURL != defaultGatewayURL || values.token != "" {
		t.Fatalf("unexpected defaults: %#v", values)
	}
}

func TestWildcardConfigConnectsToLocalGateway(t *testing.T) {
	t.Setenv("GONG_URL", "")
	t.Setenv("GONG_API_TOKEN", "")
	t.Setenv("GONG_PROXY_URL", "")
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		io.WriteString(w, `{"ok":true,"message_id":1,"pin_status":"not_requested"}`)
	}))
	defer server.Close()
	_, port, err := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	path := clientConfig(t, "0.0.0.0:"+port, "")
	var stderr bytes.Buffer
	if code := Run(context.Background(), []string{"notify", "--config", path, "hello"}, nil, io.Discard, &stderr); code != 0 || !called {
		t.Fatalf("code=%d called=%v stderr=%s", code, called, &stderr)
	}
}

func TestNonLocalConfigRequiresExplicitURL(t *testing.T) {
	t.Setenv("GONG_URL", "")
	t.Setenv("GONG_API_TOKEN", "environment-secret")
	for _, listen := range []string{
		"collector.invalid:80", "192.0.2.1:8080", "10.0.0.1:8080",
		"[2001:db8::1]:8080", "[fe80::1%eth0]:8080", "[::ffff:192.0.2.1]:8080",
	} {
		t.Run(listen, func(t *testing.T) {
			path := clientConfig(t, listen, "file-token")
			fs := flag.NewFlagSet("test", flag.ContinueOnError)
			values := addClientFlags(fs)
			if err := fs.Parse([]string{"--config", path}); err != nil {
				t.Fatal(err)
			}
			// Test resolution directly so a broken guard cannot contact this host.
			err := resolveClientConfig(fs, values)
			if err == nil || !strings.Contains(err.Error(), "--url") || !strings.Contains(err.Error(), "GONG_URL") {
				t.Fatalf("error=%v, want actionable explicit URL requirement", err)
			}
		})
	}
}

func TestDiscoveredHostnameFailsBeforeNetworkAndAllowsExplicitURL(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("GONG_URL", "")
	t.Setenv("GONG_API_TOKEN", "environment-secret")
	// A regression must never contact the configured host, even during RED.
	var dnsCalls atomic.Int32
	resolver := net.DefaultResolver
	net.DefaultResolver = &net.Resolver{PreferGo: true, Dial: func(context.Context, string, string) (net.Conn, error) {
		dnsCalls.Add(1)
		return nil, fmt.Errorf("external DNS is disabled in this test")
	}}
	t.Cleanup(func() { net.DefaultResolver = resolver })
	data := "listen: collector.invalid:80\ntelegram: {bot_token: dummy}\ntargets: {default: {chat_id: '1'}}\n"
	if err := os.WriteFile("gong.yaml", []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	code := Run(context.Background(), []string{"notify", "--timeout", "100ms", "private message"}, nil, &out, &stderr)
	if code != 1 || !strings.Contains(out.String(), "config_failed") || dnsCalls.Load() != 0 {
		t.Errorf("code=%d dns_calls=%d stdout=%s stderr=%s", code, dnsCalls.Load(), &out, &stderr)
	}
	if !strings.Contains(stderr.String(), "--url") || !strings.Contains(stderr.String(), "GONG_URL") {
		t.Errorf("missing explicit URL guidance: %s", &stderr)
	}
	if strings.Contains(stderr.String()+out.String(), "environment-secret") || strings.Contains(stderr.String()+out.String(), "collector.invalid") {
		t.Error("configuration details leaked")
	}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get("Authorization") != "Bearer environment-secret" {
			t.Error("environment token precedence changed")
		}
		io.WriteString(w, `{"ok":true,"message_id":1,"pin_status":"not_requested"}`)
	}))
	defer server.Close()
	for _, from := range []string{"flag", "environment"} {
		args := []string{"notify"}
		if from == "flag" {
			args = append(args, "--url", server.URL)
		} else {
			t.Setenv("GONG_URL", server.URL)
		}
		stderr.Reset()
		if code := Run(context.Background(), append(args, "hello"), nil, io.Discard, &stderr); code != 0 {
			t.Fatalf("%s: code=%d stderr=%s", from, code, &stderr)
		}
	}
	if requests != 2 {
		t.Fatalf("explicit URL requests=%d, want 2", requests)
	}
}

func TestExplicitURLCanSelectRemoteHost(t *testing.T) {
	for _, from := range []string{"flag", "environment"} {
		t.Run(from, func(t *testing.T) {
			t.Setenv("GONG_URL", "")
			t.Setenv("GONG_API_TOKEN", "environment-token")
			args := []string{"--config", clientConfig(t, "collector.invalid:80", "file-token")}
			const endpoint = "https://chosen.example:8443"
			if from == "flag" {
				args = append(args, "--url", endpoint)
			} else {
				t.Setenv("GONG_URL", endpoint)
			}
			fs := flag.NewFlagSet("test", flag.ContinueOnError)
			values := addClientFlags(fs)
			if err := fs.Parse(args); err != nil {
				t.Fatal(err)
			}
			if err := resolveClientConfig(fs, values); err != nil {
				t.Fatal(err)
			}
			if values.baseURL != endpoint || values.token != "environment-token" {
				t.Fatal("explicit remote URL or token precedence was lost")
			}
		})
	}
}
