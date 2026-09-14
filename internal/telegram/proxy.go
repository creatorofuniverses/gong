package telegram

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
)

func buildHTTPClient(options Options) (*http.Client, error) {
	var client http.Client
	if options.HTTPClient != nil {
		client = *options.HTTPClient
	}

	if options.Timeout > 0 {
		client.Timeout = options.Timeout
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}

	proxyURL, err := parseProxyURL(options.ProxyURL)
	if err != nil {
		return nil, err
	}

	switch transport := client.Transport.(type) {
	case nil:
		clone := http.DefaultTransport.(*http.Transport).Clone()
		clone.Proxy = nil
		if proxyURL != nil {
			clone.Proxy = http.ProxyURL(proxyURL)
		}
		client.Transport = clone
	case *http.Transport:
		clone := transport.Clone()
		clone.Proxy = nil
		if proxyURL != nil {
			clone.Proxy = http.ProxyURL(proxyURL)
		}
		client.Transport = clone
	default:
		if proxyURL != nil {
			return nil, errors.New("telegram proxy cannot be combined with a custom HTTP transport")
		}
	}

	return &client, nil
}

func parseProxyURL(raw string) (*url.URL, error) {
	if raw == "" {
		return nil, nil
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, errors.New("telegram proxy URL is invalid")
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	switch parsed.Scheme {
	case "http", "https", "socks5", "socks5h":
	default:
		return nil, errors.New("telegram proxy URL uses an unsupported scheme")
	}
	if parsed.Hostname() == "" {
		return nil, errors.New("telegram proxy URL must include a host")
	}
	if parsed.Path != "" && parsed.Path != "/" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return nil, errors.New("telegram proxy URL must not contain a path, query, or fragment")
	}
	parsed.Path = ""
	if parsed.User != nil {
		username := parsed.User.Username()
		password, hasPassword := parsed.User.Password()
		if username == "" || !hasPassword || password == "" {
			return nil, errors.New("telegram proxy credentials require a username and password")
		}
	}
	return parsed, nil
}
