package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/creatorofuniverses/gong/internal/config"
)

const minimalConfig = `
telegram:
  bot_token: yaml-token
targets:
  default:
    chat_id: "-1001234567890"
`

func TestParseAppliesDefaults(t *testing.T) {
	cfg, err := config.Parse([]byte(minimalConfig), noEnvironment)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	if cfg.Listen != "127.0.0.1:8080" {
		t.Errorf("Listen = %q, want default", cfg.Listen)
	}
	if cfg.Telegram.Timeout != 10*time.Second {
		t.Errorf("Telegram.Timeout = %v, want 10s", cfg.Telegram.Timeout)
	}
	if cfg.NotifyMinLevel != "warning" {
		t.Errorf("NotifyMinLevel = %q, want warning", cfg.NotifyMinLevel)
	}
	if cfg.MaxTopics != 10000 {
		t.Errorf("MaxTopics = %d, want 10000", cfg.MaxTopics)
	}
	if cfg.Targets["default"].Mode != "chat" {
		t.Errorf("default target mode = %q, want chat", cfg.Targets["default"].Mode)
	}
	if cfg.PinCategories == nil || len(cfg.PinCategories) != 0 {
		t.Errorf("PinCategories = %#v, want non-nil empty slice", cfg.PinCategories)
	}
}

func TestParseDecodesConfiguredValues(t *testing.T) {
	data := []byte(`
listen: "[::1]:9090"
telegram:
  bot_token: yaml-token
  proxy_url: socks5h://user:pass@proxy.example:1080
  timeout: 3.5s
targets:
  default:
    chat_id: "42"
    mode: forum
  alerts_prod:
    chat_id: "-7"
notify_min_level: success
pin_categories: [result, urgent]
api_token: local-api-token
max_topics: 250
`)
	cfg, err := config.Parse(data, noEnvironment)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	if cfg.Listen != "[::1]:9090" || cfg.Telegram.ProxyURL != "socks5h://user:pass@proxy.example:1080" {
		t.Fatalf("configured network values were not preserved: %#v", cfg)
	}
	if cfg.Telegram.Timeout != 3500*time.Millisecond || cfg.Targets["default"].Mode != "forum" {
		t.Fatalf("configured timeout/mode were not preserved: %#v", cfg)
	}
	if cfg.Targets["alerts_prod"].Mode != "chat" {
		t.Errorf("omitted additional target mode = %q, want chat", cfg.Targets["alerts_prod"].Mode)
	}
	if cfg.NotifyMinLevel != "success" || cfg.APIToken != "local-api-token" || cfg.MaxTopics != 250 {
		t.Errorf("configured policy values were not preserved: %#v", cfg)
	}
}

func TestParseMergesNonEmptyEnvironmentBeforeValidation(t *testing.T) {
	data := []byte(`
telegram:
  proxy_url: ftp://invalid.example
targets:
  default:
    chat_id: "1"
api_token: "invalid token with spaces"
`)
	env := map[string]string{
		"GONG_BOT_TOKEN": "env-token",
		"GONG_PROXY_URL": "https://proxy.example:8443",
		"GONG_API_TOKEN": "env-api-token",
	}

	cfg, err := config.Parse(data, func(key string) string { return env[key] })
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if cfg.Telegram.BotToken != "env-token" || cfg.Telegram.ProxyURL != env["GONG_PROXY_URL"] || cfg.APIToken != "env-api-token" {
		t.Errorf("environment overrides not applied: %#v", cfg)
	}
}

func TestParseIgnoresEmptyEnvironmentOverrides(t *testing.T) {
	data := []byte(`
telegram:
  bot_token: yaml-token
  proxy_url: http://yaml-user:yaml-pass@proxy.example:3128
targets:
  default:
    chat_id: "1"
api_token: yaml-api-token
`)

	cfg, err := config.Parse(data, func(string) string { return "" })
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if cfg.Telegram.BotToken != "yaml-token" || cfg.APIToken != "yaml-api-token" || !strings.Contains(cfg.Telegram.ProxyURL, "yaml-user") {
		t.Errorf("empty environment erased YAML values: %#v", cfg)
	}
}

func TestParseRejectsUnknownFieldsAndExtraDocuments(t *testing.T) {
	tests := map[string]string{
		"top level": minimalConfig + "typo: true\n",
		"telegram":  strings.Replace(minimalConfig, "  bot_token: yaml-token", "  bot_token: yaml-token\n  typo: true", 1),
		"target":    strings.Replace(minimalConfig, "    chat_id: \"-1001234567890\"", "    chat_id: \"-1001234567890\"\n    typo: true", 1),
		"document":  minimalConfig + "---\n{}\n",
	}
	for name, data := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := config.Parse([]byte(data), noEnvironment); err == nil {
				t.Fatal("Parse() error = nil, want invalid configuration")
			}
		})
	}
}

func TestParseValidatesEffectiveConfiguration(t *testing.T) {
	tests := map[string]string{
		"missing bot token":        `targets: {default: {chat_id: "1"}}`,
		"missing default target":   `telegram: {bot_token: token}\ntargets: {other: {chat_id: "1"}}`,
		"invalid target alias":     `telegram: {bot_token: token}\ntargets: {default: {chat_id: "1"}, "bad alias": {chat_id: "2"}}`,
		"empty chat id":            `telegram: {bot_token: token}\ntargets: {default: {chat_id: ""}}`,
		"zero chat id":             `telegram: {bot_token: token}\ntargets: {default: {chat_id: "0"}}`,
		"noncanonical chat id":     `telegram: {bot_token: token}\ntargets: {default: {chat_id: "001"}}`,
		"overflowing chat id":      `telegram: {bot_token: token}\ntargets: {default: {chat_id: "99999999999999999999"}}`,
		"invalid target mode":      `telegram: {bot_token: token}\ntargets: {default: {chat_id: "1", mode: channel}}`,
		"listen missing port":      `listen: localhost\ntelegram: {bot_token: token}\ntargets: {default: {chat_id: "1"}}`,
		"listen port zero":         `listen: localhost:0\ntelegram: {bot_token: token}\ntargets: {default: {chat_id: "1"}}`,
		"listen port overflow":     `listen: localhost:65536\ntelegram: {bot_token: token}\ntargets: {default: {chat_id: "1"}}`,
		"zero timeout":             `telegram: {bot_token: token, timeout: 0s}\ntargets: {default: {chat_id: "1"}}`,
		"negative timeout":         `telegram: {bot_token: token, timeout: -1s}\ntargets: {default: {chat_id: "1"}}`,
		"invalid timeout":          `telegram: {bot_token: token, timeout: soon}\ntargets: {default: {chat_id: "1"}}`,
		"invalid notify level":     `telegram: {bot_token: token}\ntargets: {default: {chat_id: "1"}}\nnotify_min_level: fatal`,
		"zero max topics":          `telegram: {bot_token: token}\ntargets: {default: {chat_id: "1"}}\nmax_topics: 0`,
		"negative max topics":      `telegram: {bot_token: token}\ntargets: {default: {chat_id: "1"}}\nmax_topics: -1`,
		"unsupported proxy scheme": `telegram: {bot_token: token, proxy_url: ftp://proxy.example}\ntargets: {default: {chat_id: "1"}}`,
		"proxy missing host":       `telegram: {bot_token: token, proxy_url: "http:///missing-host"}\ntargets: {default: {chat_id: "1"}}`,
		"proxy with path":          `telegram: {bot_token: token, proxy_url: "https://proxy.example/private"}\ntargets: {default: {chat_id: "1"}}`,
		"proxy with query":         `telegram: {bot_token: token, proxy_url: "socks5://proxy.example?token=secret"}\ntargets: {default: {chat_id: "1"}}`,
		"proxy with empty query":   `telegram: {bot_token: token, proxy_url: "http://proxy.example?"}\ntargets: {default: {chat_id: "1"}}`,
		"proxy empty username":     `telegram: {bot_token: token, proxy_url: "http://:pass@proxy.example"}\ntargets: {default: {chat_id: "1"}}`,
		"empty pin category":       `telegram: {bot_token: token}\ntargets: {default: {chat_id: "1"}}\npin_categories: [""]`,
	}

	for name, data := range tests {
		t.Run(name, func(t *testing.T) {
			data = strings.ReplaceAll(data, `\n`, "\n")
			if _, err := config.Parse([]byte(data), noEnvironment); err == nil {
				t.Fatal("Parse() error = nil, want validation error")
			}
		})
	}
}

func TestParseAcceptsSupportedListenAndProxyForms(t *testing.T) {
	tests := []struct {
		listen string
		proxy  string
	}{
		{"127.0.0.1:8080", "http://proxy.example"},
		{"0.0.0.0:443", "https://user:pass@proxy.example:8443"},
		{"[::1]:9090", "socks5://proxy.example:1080"},
		{":8080", "socks5h://user:pass@[2001:db8::1]:1080"},
	}
	for _, tt := range tests {
		data := "listen: \"" + tt.listen + "\"\ntelegram: {bot_token: token, proxy_url: \"" + tt.proxy + "\"}\ntargets: {default: {chat_id: \"-1\"}}\n"
		if _, err := config.Parse([]byte(data), noEnvironment); err != nil {
			t.Errorf("Parse(listen=%q, proxy=%q) error = %v", tt.listen, tt.proxy, err)
		}
	}
}

func TestParseErrorsDoNotExposeSecrets(t *testing.T) {
	const secret = "super-secret-token-value"
	data := []byte("telegram: [" + secret + "]\ntargets: {default: {chat_id: \"1\"}}\n")
	_, err := config.Parse(data, noEnvironment)
	if err == nil {
		t.Fatal("Parse() error = nil, want malformed YAML error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("Parse() error exposed secret: %v", err)
	}

	_, err = config.Parse([]byte(minimalConfig), func(key string) string {
		if key == "GONG_PROXY_URL" {
			return "http://user:" + secret + "@"
		}
		return ""
	})
	if err == nil {
		t.Fatal("Parse() error = nil, want invalid environment override error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("Parse() environment error exposed secret: %v", err)
	}
}

func TestLoadReadsFileAndUsesProcessEnvironment(t *testing.T) {
	t.Setenv("GONG_BOT_TOKEN", "process-token")
	t.Setenv("GONG_PROXY_URL", "")
	t.Setenv("GONG_API_TOKEN", "")
	path := filepath.Join(t.TempDir(), "gong.yaml")
	if err := os.WriteFile(path, []byte("targets: {default: {chat_id: \"1\"}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Telegram.BotToken != "process-token" {
		t.Errorf("Telegram.BotToken = %q, want process environment override", cfg.Telegram.BotToken)
	}
}

func noEnvironment(string) string { return "" }

func TestParseClientDefaultsAndAPITokenOverride(t *testing.T) {
	for _, tc := range []struct {
		name, data, envToken, listen, token string
	}{
		{"defaults", "{}\n", "", "127.0.0.1:8080", ""},
		{"file settings", "listen: ':8081'\napi_token: file-token\n", "", ":8081", "file-token"},
		{"environment wins before validation", "api_token: 'invalid file token'\n", "env-token", "127.0.0.1:8080", "env-token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := config.ParseClient([]byte(tc.data), func(key string) string {
				if key != "GONG_API_TOKEN" {
					t.Errorf("client requested server-only environment setting %q", key)
				}
				return tc.envToken
			})
			if err != nil || cfg.Listen != tc.listen || cfg.APIToken != tc.token {
				t.Fatalf("ParseClient() = %#v, %v", cfg, err)
			}
		})
	}
}

func TestParseClientValidatesListenAndEffectiveToken(t *testing.T) {
	for name, data := range map[string]string{
		"missing port":     "listen: localhost\n",
		"zero port":        "listen: localhost:0\n",
		"overflow port":    "listen: localhost:65536\n",
		"invalid host":     "listen: 'bad host:8080'\n",
		"token whitespace": "api_token: 'secret with whitespace'\n",
		"token type":       "api_token: [secret]\n",
		"listen type":      "listen: [secret]\n",
		"duplicate key":    "listen: ':8080'\nlisten: ':8081'\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := config.ParseClient([]byte(data), nil)
			if err == nil {
				t.Fatal("ParseClient() accepted malformed client settings")
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatal("ParseClient() error exposed configuration contents")
			}
		})
	}
	_, err := config.ParseClient([]byte("api_token: file-token\n"), func(string) string { return "invalid environment token" })
	if err == nil || strings.Contains(err.Error(), "invalid environment token") {
		t.Fatalf("ParseClient() did not safely reject malformed environment token: %v", err)
	}
}
