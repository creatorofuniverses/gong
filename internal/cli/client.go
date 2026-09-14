package cli

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	maximumRequestSize  = 64 * 1024
	maximumResponseSize = 1024 * 1024
)

type responseEnvelope struct {
	OK        bool   `json:"ok"`
	Code      string `json:"code"`
	Uncertain bool   `json:"uncertain"`
	MessageID int64  `json:"message_id"`
	TopicID   int64  `json:"topic_id"`
	Name      string `json:"name"`
	Target    string `json:"target"`
	PinStatus string `json:"pin_status"`
}

type successKind uint8

const (
	successNotify successKind = iota
	successCreateTopic
	successDeleteTopic
)

type successExpectation struct {
	kind    successKind
	topicID int64
	name    string
	target  string
}

func executeJSON(ctx context.Context, method, path string, query url.Values, payload any, values clientFlags, stdout, stderr io.Writer, expectation successExpectation) int {
	base, err := parseBaseURL(values.baseURL)
	if err != nil {
		return argumentError(stderr, "gong: --url must be an absolute HTTP or HTTPS gateway URL without credentials, query, or fragment", rootUsage)
	}
	if values.timeout <= 0 {
		return argumentError(stderr, "gong: --timeout must be positive", rootUsage)
	}
	base.Path = strings.TrimSuffix(base.Path, "/") + path
	base.RawPath = ""
	base.RawQuery = query.Encode()
	var body []byte
	if payload != nil {
		body, err = json.Marshal(payload)
		if err != nil {
			return localFailure(stdout, stderr, "request_encoding_failed", "could not encode request", false)
		}
		if len(body) > maximumRequestSize {
			return argumentError(stderr, "gong: serialized request body exceeds the 64 KiB gateway limit", rootUsage)
		}
	}
	req, err := newMutationRequest(ctx, method, base.String(), body, values.token)
	if err != nil {
		return localFailure(stdout, stderr, "request_failed", "could not create request", false)
	}
	client := newHTTPClient(values.timeout)
	response, err := client.Do(req)
	if err != nil {
		return localFailure(stdout, stderr, "request_failed", "request outcome is uncertain; do not retry automatically", true)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maximumResponseSize+1))
	if err != nil || len(data) > maximumResponseSize {
		return localFailure(stdout, stderr, "invalid_response", "gateway response could not be read safely", true)
	}
	if response.StatusCode >= 300 && response.StatusCode < 400 {
		return localFailure(stdout, stderr, "redirect_refused", "gateway redirects are refused for mutation requests", true)
	}
	var envelope responseEnvelope
	if len(data) == 0 || !json.Valid(data) || json.Unmarshal(data, &envelope) != nil {
		return localFailure(stdout, stderr, "invalid_response", "gateway returned an invalid JSON response", true)
	}
	if response.StatusCode >= 200 && response.StatusCode < 300 && envelope.OK && !expectation.valid(envelope) {
		return localFailure(stdout, stderr, "invalid_response", "gateway success response is invalid; mutation outcome is uncertain; do not retry automatically", true)
	}
	_, _ = stdout.Write(data)
	if response.StatusCode < 200 || response.StatusCode >= 300 || !envelope.OK {
		if envelope.Uncertain {
			fmt.Fprintln(stderr, "gong: request failed and the outcome is uncertain; do not retry automatically")
		} else {
			fmt.Fprintln(stderr, "gong: request failed; see JSON response")
		}
		return 1
	}
	if expectation.kind == successNotify && envelope.PinStatus == "failed" {
		fmt.Fprintln(stderr, "gong: message was delivered, but pinning failed; do not retry the notification request")
		return 1
	}
	return 0
}

func (expectation successExpectation) valid(envelope responseEnvelope) bool {
	switch expectation.kind {
	case successNotify:
		if envelope.MessageID <= 0 {
			return false
		}
		switch envelope.PinStatus {
		case "not_requested", "pinned", "failed":
			return true
		default:
			return false
		}
	case successCreateTopic:
		return envelope.TopicID > 0 && envelope.Name == expectation.name && envelope.Target == expectation.target
	case successDeleteTopic:
		return envelope.TopicID == expectation.topicID && envelope.TopicID > 0 && envelope.Target == expectation.target
	default:
		return false
	}
}

func parseBaseURL(value string) (*url.URL, error) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "http" && parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("invalid gateway URL")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return nil, fmt.Errorf("gateway URL must not have a path")
	}
	return parsed, nil
}

func newMutationRequest(ctx context.Context, method, endpoint string, body []byte, token string) (*http.Request, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return nil, err
	}
	// net/http populates GetBody for byte readers. Clearing it ensures the
	// mutation cannot be transparently replayed after a connection failure.
	req.GetBody = nil
	req.Close = true
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return req, nil
}

func newHTTPClient(timeout time.Duration) *http.Client {
	dialer := &net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		DialContext:         dialer.DialContext,
		ForceAttemptHTTP2:   false,
		TLSHandshakeTimeout: timeout,
		TLSNextProto:        make(map[string]func(string, *tls.Conn) http.RoundTripper),
	}
	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func localFailure(stdout, stderr io.Writer, code, message string, uncertain bool) int {
	response := struct {
		OK        bool   `json:"ok"`
		Code      string `json:"code"`
		Error     string `json:"error"`
		Uncertain bool   `json:"uncertain,omitempty"`
	}{false, code, message, uncertain}
	_ = json.NewEncoder(stdout).Encode(response)
	fmt.Fprintf(stderr, "gong: %s\n", message)
	return 1
}
