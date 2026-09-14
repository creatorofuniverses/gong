package gateway

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/creatorofuniverses/gong/internal/telegram"
)

type apiError struct {
	status     int
	code       string
	message    string
	uncertain  bool
	retryAfter int
}

func (e apiError) Error() string { return e.code + ": " + e.message }

func invalid(message string) apiError {
	return apiError{status: http.StatusUnprocessableEntity, code: "invalid_request", message: message}
}

func (s *Server) authorize(r *http.Request) error {
	if _, present := r.Header["Origin"]; present {
		return apiError{status: http.StatusForbidden, code: "browser_request_forbidden", message: "browser-origin requests are not allowed"}
	}
	if values, present := r.Header["Sec-Fetch-Site"]; present {
		if len(values) != 1 || values[0] != "none" {
			return apiError{status: http.StatusForbidden, code: "browser_request_forbidden", message: "cross-site browser requests are not allowed"}
		}
	}
	if s.cfg.APIToken != "" {
		if !validBearer(r.Header.Values("Authorization"), s.cfg.APIToken) {
			return apiError{status: http.StatusUnauthorized, code: "unauthorized", message: "a valid Bearer token is required"}
		}
		return nil
	}
	if !s.allowedHost(r.Host) {
		return apiError{status: http.StatusForbidden, code: "host_forbidden", message: "request Host is not allowed"}
	}
	return nil
}

func validBearer(values []string, token string) bool {
	if len(values) != 1 {
		return false
	}
	want := []byte("Bearer " + token)
	got := []byte(values[0])
	lengthEqual := subtle.ConstantTimeEq(int32(len(got)), int32(len(want)))
	comparisonLength := len(want)
	if len(got) > comparisonLength {
		comparisonLength = len(got)
	}
	gotPadded := make([]byte, comparisonLength)
	wantPadded := make([]byte, comparisonLength)
	copy(gotPadded, got)
	copy(wantPadded, want)
	return lengthEqual&subtle.ConstantTimeCompare(gotPadded, wantPadded) == 1
}

func (s *Server) allowedHost(hostport string) bool {
	host, ok := parseRequestHost(hostport)
	if !ok {
		return false
	}
	if strings.EqualFold(host, "localhost") || host == "127.0.0.1" || host == "::1" {
		return true
	}
	listenHost, _, err := net.SplitHostPort(s.cfg.Listen)
	if err != nil {
		return false
	}
	listenHost = stripIPv6Zone(listenHost)
	listenIP := net.ParseIP(listenHost)
	requestIP := net.ParseIP(host)
	return listenIP != nil && !listenIP.IsUnspecified() && requestIP != nil && listenIP.Equal(requestIP)
}

func stripIPv6Zone(host string) string {
	percent := strings.LastIndexByte(host, '%')
	if percent <= 0 || percent == len(host)-1 {
		return host
	}
	address := host[:percent]
	if net.ParseIP(address) == nil {
		return host
	}
	return address
}

func parseRequestHost(value string) (string, bool) {
	if value == "" || strings.ContainsAny(value, "/\\@ \t\r\n") {
		return "", false
	}
	if strings.HasPrefix(value, "[") {
		end := strings.IndexByte(value, ']')
		if end < 0 {
			return "", false
		}
		host := value[1:end]
		if net.ParseIP(host) == nil {
			return "", false
		}
		rest := value[end+1:]
		if rest == "" {
			return host, true
		}
		if len(rest) < 2 || rest[0] != ':' || !validPort(rest[1:]) {
			return "", false
		}
		return host, true
	}
	switch strings.Count(value, ":") {
	case 0:
		return value, validHostnameOrIP(value)
	case 1:
		host, port, _ := strings.Cut(value, ":")
		return host, validHostnameOrIP(host) && validPort(port)
	default:
		return "", false
	}
}

func validHostnameOrIP(value string) bool {
	if net.ParseIP(value) != nil {
		return true
	}
	if len(value) == 0 || len(value) > 253 {
		return false
	}
	for _, label := range strings.Split(strings.TrimSuffix(value, "."), ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-') {
				return false
			}
		}
	}
	return true
}

func validPort(value string) bool {
	port, err := strconv.Atoi(value)
	return err == nil && port >= 1 && port <= 65535
}

func errorFromTelegram(err error) apiError {
	var typed *telegram.Error
	if errors.As(err, &typed) {
		status := typed.HTTPStatus
		if status < 400 || status > 599 {
			status = http.StatusBadGateway
		}
		code := typed.Code
		if code == "" {
			code = "telegram_error"
		}
		message := typed.Message
		if message == "" {
			message = "Telegram request failed"
		}
		return apiError{status: status, code: code, message: message, uncertain: typed.Uncertain, retryAfter: typed.RetryAfter}
	}
	return apiError{status: http.StatusBadGateway, code: "telegram_error", message: "Telegram request failed"}
}

func writeMethodNotAllowed(w http.ResponseWriter, allowed string) {
	w.Header().Set("Allow", allowed)
	writeAPIError(w, apiError{status: http.StatusMethodNotAllowed, code: "method_not_allowed", message: "method not allowed"})
}

func writeAPIError(w http.ResponseWriter, err error) {
	var safe apiError
	if !errors.As(err, &safe) {
		safe = apiError{status: http.StatusInternalServerError, code: "internal_error", message: "internal server error"}
	}
	response := struct {
		OK         bool   `json:"ok"`
		Code       string `json:"code"`
		Error      string `json:"error"`
		Uncertain  bool   `json:"uncertain,omitempty"`
		RetryAfter int    `json:"retry_after,omitempty"`
	}{false, safe.code, safe.message, safe.uncertain, safe.retryAfter}
	if safe.status == http.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", "Bearer")
	}
	if safe.retryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(safe.retryAfter))
	}
	writeJSON(w, safe.status, response)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
