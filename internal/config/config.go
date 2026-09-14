// Package config loads and validates Gong's process configuration.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

const (
	defaultListen         = "127.0.0.1:8080"
	defaultTimeout        = "10s"
	defaultNotifyMinLevel = "warning"
	defaultMaxTopics      = 10000
)

var (
	aliasPattern  = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	chatIDPattern = regexp.MustCompile(`^-?[1-9][0-9]*$`)
	hostPattern   = regexp.MustCompile(`^[A-Za-z0-9.-]+$`)
	zonePattern   = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
)

// Config is the validated, effective process configuration.
type Config struct {
	Listen         string
	Telegram       TelegramConfig
	Targets        map[string]Target
	NotifyMinLevel string
	PinCategories  []string
	APIToken       string
	MaxTopics      int
}

// ClientConfig contains only the settings used by local HTTP clients.
type ClientConfig struct {
	Listen   string
	APIToken string
}

// TelegramConfig configures calls to the Telegram Bot API.
type TelegramConfig struct {
	BotToken string
	ProxyURL string
	Timeout  time.Duration
}

// Target maps a public alias to a Telegram chat and routing mode.
type Target struct {
	ChatID string `yaml:"chat_id"`
	Mode   string `yaml:"mode"`
}

type rawConfig struct {
	Listen   string            `yaml:"listen"`
	Telegram rawTelegramConfig `yaml:"telegram"`
	Targets  map[string]Target `yaml:"targets"`

	NotifyMinLevel string   `yaml:"notify_min_level"`
	PinCategories  []string `yaml:"pin_categories"`
	APIToken       string   `yaml:"api_token"`
	MaxTopics      int      `yaml:"max_topics"`
}

type rawTelegramConfig struct {
	BotToken string `yaml:"bot_token"`
	ProxyURL string `yaml:"proxy_url"`
	Timeout  string `yaml:"timeout"`
}

// Load reads path, applies process environment overrides, and validates the
// resulting configuration.
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	return Parse(data, os.Getenv)
}

// Parse decodes YAML, applies nonempty environment overrides, and validates
// the effective configuration. getenv may be nil to disable overrides.
func Parse(data []byte, getenv func(string) string) (Config, error) {
	raw, err := decode(data)
	if err != nil {
		return Config{}, err
	}

	if getenv != nil {
		if value := getenv("GONG_BOT_TOKEN"); value != "" {
			raw.Telegram.BotToken = value
		}
		if value := getenv("GONG_PROXY_URL"); value != "" {
			raw.Telegram.ProxyURL = value
		}
		if value := getenv("GONG_API_TOKEN"); value != "" {
			raw.APIToken = value
		}
	}

	timeout, err := time.ParseDuration(raw.Telegram.Timeout)
	if err != nil {
		return Config{}, errors.New("telegram.timeout must be a valid duration")
	}
	cfg := Config{
		Listen: raw.Listen,
		Telegram: TelegramConfig{
			BotToken: raw.Telegram.BotToken,
			ProxyURL: raw.Telegram.ProxyURL,
			Timeout:  timeout,
		},
		Targets:        raw.Targets,
		NotifyMinLevel: raw.NotifyMinLevel,
		PinCategories:  raw.PinCategories,
		APIToken:       raw.APIToken,
		MaxTopics:      raw.MaxTopics,
	}
	for alias, target := range cfg.Targets {
		if target.Mode == "" {
			target.Mode = "chat"
			cfg.Targets[alias] = target
		}
	}
	if err := validate(cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// LoadClient reads path and validates the HTTP client settings, applying a
// nonempty GONG_API_TOKEN override. Server-only values need not be valid.
func LoadClient(path string) (ClientConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return ClientConfig{}, fmt.Errorf("read config: %w", err)
	}
	return ParseClient(data, os.Getenv)
}

// ParseClient uses the full strict YAML schema, but validates only listen and
// the effective api_token. getenv may be nil to disable the API token override.
func ParseClient(data []byte, getenv func(string) string) (ClientConfig, error) {
	raw, err := decode(data)
	if err != nil {
		return ClientConfig{}, err
	}
	if getenv != nil {
		if value := getenv("GONG_API_TOKEN"); value != "" {
			raw.APIToken = value
		}
	}
	if err := validateListen(raw.Listen); err != nil {
		return ClientConfig{}, err
	}
	if err := validateAPIToken(raw.APIToken); err != nil {
		return ClientConfig{}, err
	}
	return ClientConfig{Listen: raw.Listen, APIToken: raw.APIToken}, nil
}

func decode(data []byte) (rawConfig, error) {
	raw := rawConfig{
		Listen:         defaultListen,
		NotifyMinLevel: defaultNotifyMinLevel,
		PinCategories:  make([]string, 0),
		MaxTopics:      defaultMaxTopics,
		Telegram:       rawTelegramConfig{Timeout: defaultTimeout},
	}

	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&raw); err != nil {
		return rawConfig{}, errors.New("invalid YAML configuration")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return rawConfig{}, errors.New("invalid YAML configuration: exactly one document is required")
	}
	return raw, nil
}

func validate(cfg Config) error {
	if strings.TrimSpace(cfg.Telegram.BotToken) == "" {
		return errors.New("telegram.bot_token must be nonempty")
	}
	if cfg.Telegram.Timeout <= 0 {
		return errors.New("telegram.timeout must be positive")
	}
	if err := validateListen(cfg.Listen); err != nil {
		return err
	}
	if err := validateProxyURL(cfg.Telegram.ProxyURL); err != nil {
		return err
	}
	if len(cfg.Targets) == 0 {
		return errors.New("targets.default is required")
	}
	if _, ok := cfg.Targets["default"]; !ok {
		return errors.New("targets.default is required")
	}
	for alias, target := range cfg.Targets {
		if !aliasPattern.MatchString(alias) {
			return errors.New("targets contains an invalid alias")
		}
		if !chatIDPattern.MatchString(target.ChatID) {
			return errors.New("target chat_id must be a canonical nonzero signed integer")
		}
		if _, err := strconv.ParseInt(target.ChatID, 10, 64); err != nil {
			return errors.New("target chat_id must fit in a signed 64-bit integer")
		}
		if target.Mode != "chat" && target.Mode != "forum" {
			return errors.New("target mode must be chat or forum")
		}
	}
	if !validLevel(cfg.NotifyMinLevel) {
		return errors.New("notify_min_level must be debug, info, success, warning, or error")
	}
	if cfg.MaxTopics <= 0 {
		return errors.New("max_topics must be positive")
	}
	for _, category := range cfg.PinCategories {
		if category == "" {
			return errors.New("pin_categories entries must be nonempty")
		}
	}
	return validateAPIToken(cfg.APIToken)
}

func validateAPIToken(token string) error {
	if strings.ContainsAny(token, " \t\r\n") {
		return errors.New("api_token must not contain whitespace")
	}
	return nil
}

func validateListen(address string) error {
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return errors.New("listen must be a valid TCP host:port address")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return errors.New("listen port must be between 1 and 65535")
	}
	if host != "" && !validHost(host) {
		return errors.New("listen host is invalid")
	}
	return nil
}

func validHost(host string) bool {
	if net.ParseIP(host) != nil {
		return true
	}
	if percent := strings.LastIndexByte(host, '%'); percent > 0 {
		return net.ParseIP(host[:percent]) != nil && zonePattern.MatchString(host[percent+1:])
	}
	if len(host) > 253 || !hostPattern.MatchString(host) {
		return false
	}
	for _, label := range strings.Split(strings.TrimSuffix(host, "."), ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
	}
	return true
}

func validateProxyURL(value string) error {
	if value == "" {
		return nil
	}
	u, err := url.Parse(value)
	if err != nil || u.Opaque != "" {
		return errors.New("telegram.proxy_url is invalid")
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "socks5", "socks5h":
	default:
		return errors.New("telegram.proxy_url has an unsupported scheme")
	}
	if u.Hostname() == "" || !validHost(u.Hostname()) {
		return errors.New("telegram.proxy_url must include a valid host")
	}
	if portText := u.Port(); portText != "" {
		port, err := strconv.Atoi(portText)
		if err != nil || port < 1 || port > 65535 {
			return errors.New("telegram.proxy_url port must be between 1 and 65535")
		}
	}
	if u.Path != "" && u.Path != "/" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return errors.New("telegram.proxy_url must not contain a path, query, or fragment")
	}
	if u.User != nil {
		password, hasPassword := u.User.Password()
		if u.User.Username() == "" || !hasPassword || password == "" {
			return errors.New("telegram.proxy_url credentials require a username and password")
		}
	}
	return nil
}

func validLevel(level string) bool {
	switch level {
	case "debug", "info", "success", "warning", "error":
		return true
	default:
		return false
	}
}
