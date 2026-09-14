package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/creatorofuniverses/gong/internal/telegram"
	"github.com/creatorofuniverses/gong/internal/topics"
)

const maximumBodySize = 64 * 1024

type notification struct {
	message           string
	target            string
	topic             string
	topicPresent      bool
	topicID           int64
	topicIDPresent    bool
	level             string
	category          string
	categoryPresent   bool
	fallbackPlainText bool
}

type notifyResponse struct {
	OK           bool   `json:"ok"`
	MessageID    int64  `json:"message_id"`
	Level        string `json:"level"`
	Silent       bool   `json:"silent"`
	FallbackUsed bool   `json:"fallback_used"`
	Target       string `json:"target"`
	TopicID      int64  `json:"topic_id,omitempty"`
	PinStatus    string `json:"pin_status"`
	PinError     string `json:"pin_error,omitempty"`
}

func (s *Server) handleNotify(w http.ResponseWriter, r *http.Request) {
	note, err := parseNotification(w, r)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	target, ok := s.cfg.Targets[note.target]
	if !ok {
		writeAPIError(w, invalid("target is unknown"))
		return
	}

	text := note.message
	topicID := note.topicID
	if target.Mode == "chat" {
		if note.topicIDPresent {
			writeAPIError(w, invalid("topic_id is not valid for a chat target"))
			return
		}
		if note.topicPresent {
			tag, tagErr := hashtag(note.topic)
			if tagErr != nil {
				writeAPIError(w, tagErr)
				return
			}
			text += "\n#" + tag
		}
	} else if note.topicPresent {
		topicID, err = s.resolver.Resolve(r.Context(), target.ChatID, note.topic)
		if err != nil {
			s.writeResolverError(w, r.Context(), err)
			return
		}
	}

	result, err := s.sender.Send(r.Context(), telegram.SendRequest{
		ChatID: target.ChatID, Text: text, TopicID: topicID,
		Silent: isSilent(note.level, s.cfg.NotifyMinLevel), FallbackPlainText: note.fallbackPlainText,
	})
	if err != nil {
		s.logger.Warn("Telegram send failed", "error", safeLogFields(err))
		writeAPIError(w, operationError(err))
		return
	}

	response := notifyResponse{
		OK: true, MessageID: result.MessageID, Level: note.level,
		Silent: isSilent(note.level, s.cfg.NotifyMinLevel), FallbackUsed: result.FallbackUsed,
		Target: note.target, TopicID: topicID, PinStatus: "not_requested",
	}
	if note.categoryPresent && s.shouldPin(note.category) {
		pinCtx, pinCancel := s.callContext(r.Context())
		pinErr := s.sender.Pin(pinCtx, target.ChatID, result.MessageID)
		pinCancel()
		if pinErr != nil {
			response.PinStatus = "failed"
			response.PinError = "message was already delivered; pinning failed and retrying the request may duplicate it"
			s.logger.Warn("Telegram pin failed after delivery", "error", safeLogFields(pinErr), "message_id", result.MessageID)
		} else {
			response.PinStatus = "pinned"
		}
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) writeResolverError(w http.ResponseWriter, ctx context.Context, err error) {
	switch {
	case errors.Is(err, topics.ErrCapacity):
		writeAPIError(w, apiError{status: http.StatusServiceUnavailable, code: "topic_capacity_exhausted", message: "new topic names are unavailable until restart; use topic_id"})
	case errors.Is(err, topics.ErrUncertain):
		writeAPIError(w, apiError{status: http.StatusConflict, code: "topic_creation_uncertain", message: "topic creation outcome is unknown until restart; check Telegram and use topic_id or a new name", uncertain: true})
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded):
		writeAPIError(w, apiError{status: http.StatusGatewayTimeout, code: "topic_resolution_timeout", message: "topic creation may finish in the background; the message was not sent", uncertain: true})
	default:
		writeAPIError(w, operationError(err))
	}
}

func (s *Server) handleCreateTopic(w http.ResponseWriter, r *http.Request) {
	if err := requireJSONContentType(r); err != nil {
		writeAPIError(w, err)
		return
	}
	body, err := readBody(w, r)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	raw, err := decodeJSONObject(body, map[string]bool{"target": true, "name": true})
	if err != nil {
		writeAPIError(w, err)
		return
	}
	targetName := "default"
	if value, present := raw["target"]; present {
		if err := decodeRequiredString(value, &targetName, "target"); err != nil {
			writeAPIError(w, err)
			return
		}
	}
	var name string
	value, present := raw["name"]
	if !present {
		writeAPIError(w, invalid("name is required"))
		return
	}
	if err := decodeRequiredString(value, &name, "name"); err != nil {
		writeAPIError(w, err)
		return
	}
	if utf8.RuneCountInString(name) > 128 {
		writeAPIError(w, invalid("name must be at most 128 Unicode code points"))
		return
	}
	target, ok := s.cfg.Targets[targetName]
	if !ok {
		writeAPIError(w, invalid("target is unknown"))
		return
	}
	if target.Mode != "forum" {
		writeAPIError(w, invalid("topic operations require a forum target"))
		return
	}
	icon, err := s.picker.Pick(r.Context(), name)
	if err != nil {
		writeAPIError(w, preMutationError(err))
		return
	}
	callCtx, cancel := s.callContext(r.Context())
	topic, err := s.sender.CreateTopic(callCtx, target.ChatID, name, icon)
	cancel()
	if err != nil {
		writeAPIError(w, operationError(err))
		return
	}
	writeJSON(w, http.StatusCreated, struct {
		OK      bool   `json:"ok"`
		TopicID int64  `json:"topic_id"`
		Name    string `json:"name"`
		Target  string `json:"target"`
	}{true, topic.ID, name, targetName})
}

func (s *Server) handleDeleteTopic(w http.ResponseWriter, r *http.Request) {
	textID := strings.TrimPrefix(r.URL.Path, "/topics/")
	topicID, err := strconv.ParseInt(textID, 10, 64)
	if err != nil || topicID <= 0 {
		writeAPIError(w, invalid("topic id must be a positive 64-bit integer"))
		return
	}
	query, parseErr := url.ParseQuery(r.URL.RawQuery)
	if parseErr != nil {
		writeAPIError(w, invalid("query parameters are malformed"))
		return
	}
	if len(query) > 1 {
		writeAPIError(w, invalid("only target query parameter is supported"))
		return
	}
	values, hasTarget := query["target"]
	if len(query) == 1 && !hasTarget {
		writeAPIError(w, invalid("only target query parameter is supported"))
		return
	}
	if hasTarget && len(values) != 1 {
		writeAPIError(w, invalid("target must be provided once"))
		return
	}
	targetName := "default"
	if hasTarget {
		targetName = values[0]
		if strings.TrimSpace(targetName) == "" {
			writeAPIError(w, invalid("target must not be empty"))
			return
		}
	}
	target, ok := s.cfg.Targets[targetName]
	if !ok {
		writeAPIError(w, invalid("target is unknown"))
		return
	}
	if target.Mode != "forum" {
		writeAPIError(w, invalid("topic operations require a forum target"))
		return
	}
	callCtx, cancel := s.callContext(r.Context())
	err = s.sender.DeleteTopic(callCtx, target.ChatID, topicID)
	cancel()
	if err != nil {
		writeAPIError(w, operationError(err))
		return
	}
	writeJSON(w, http.StatusOK, struct {
		OK      bool   `json:"ok"`
		TopicID int64  `json:"topic_id"`
		Target  string `json:"target"`
	}{true, topicID, targetName})
}

func parseNotification(w http.ResponseWriter, r *http.Request) (notification, error) {
	mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" && mediaType != "text/plain" {
		return notification{}, apiError{status: http.StatusUnsupportedMediaType, code: "unsupported_media_type", message: "use application/json or text/plain"}
	}
	if charset, present := params["charset"]; present && !strings.EqualFold(charset, "utf-8") {
		return notification{}, apiError{status: http.StatusUnsupportedMediaType, code: "unsupported_media_type", message: "request body charset must be UTF-8"}
	}
	body, err := readBody(w, r)
	if err != nil {
		return notification{}, err
	}
	if !utf8.Valid(body) {
		return notification{}, invalid("request body must be UTF-8")
	}
	if mediaType == "application/json" {
		return parseJSONNotification(body)
	}
	return parseTextNotification(body, r.Header)
}

func parseJSONNotification(body []byte) (notification, error) {
	allowed := map[string]bool{"message": true, "target": true, "topic": true, "topic_id": true, "level": true, "category": true, "fallback_plain_text": true}
	raw, err := decodeJSONObject(body, allowed)
	if err != nil {
		return notification{}, err
	}
	note := notification{target: "default", level: "info", fallbackPlainText: true}
	message, present := raw["message"]
	if !present {
		return notification{}, invalid("message is required")
	}
	if err := decodeRequiredString(message, &note.message, "message"); err != nil {
		return notification{}, err
	}
	if value, present := raw["target"]; present {
		if err := decodeRequiredString(value, &note.target, "target"); err != nil {
			return notification{}, err
		}
	}
	if value, present := raw["topic"]; present {
		note.topicPresent = true
		if err := decodeRequiredString(value, &note.topic, "topic"); err != nil {
			return notification{}, err
		}
		if utf8.RuneCountInString(note.topic) > 128 {
			return notification{}, invalid("topic must be at most 128 Unicode code points")
		}
	}
	if value, present := raw["topic_id"]; present {
		note.topicIDPresent = true
		if err := json.Unmarshal(value, &note.topicID); err != nil || note.topicID <= 0 {
			return notification{}, invalid("topic_id must be a positive 64-bit JSON integer")
		}
	}
	if note.topicPresent && note.topicIDPresent {
		return notification{}, invalid("topic and topic_id are mutually exclusive")
	}
	if value, present := raw["level"]; present {
		if err := decodeRequiredString(value, &note.level, "level"); err != nil {
			return notification{}, err
		}
	}
	if !validLevel(note.level) {
		return notification{}, invalid("level must be debug, info, success, warning, or error")
	}
	if value, present := raw["category"]; present {
		note.categoryPresent = true
		if err := decodeRequiredString(value, &note.category, "category"); err != nil {
			return notification{}, err
		}
	}
	if value, present := raw["fallback_plain_text"]; present {
		var decoded *bool
		if err := json.Unmarshal(value, &decoded); err != nil || decoded == nil {
			return notification{}, invalid("fallback_plain_text must be a boolean")
		}
		note.fallbackPlainText = *decoded
	}
	return note, nil
}

func parseTextNotification(body []byte, header http.Header) (notification, error) {
	note := notification{message: string(body), target: "default", level: "info", fallbackPlainText: true}
	if strings.TrimSpace(note.message) == "" {
		return notification{}, invalid("message must not be empty")
	}
	stringHeaders := []struct {
		key         string
		destination *string
		present     *bool
	}{
		{"X-Target", &note.target, nil}, {"X-Topic", &note.topic, &note.topicPresent}, {"X-Level", &note.level, nil}, {"X-Category", &note.category, &note.categoryPresent},
	}
	for _, field := range stringHeaders {
		values, present := header[http.CanonicalHeaderKey(field.key)]
		if !present {
			continue
		}
		if len(values) != 1 || !utf8.ValidString(values[0]) || strings.TrimSpace(values[0]) == "" {
			return notification{}, invalid(field.key + " must be one nonempty UTF-8 value")
		}
		*field.destination = values[0]
		if field.present != nil {
			*field.present = true
		}
	}
	if values, present := header["X-Topic-Id"]; present {
		if len(values) != 1 || !utf8.ValidString(values[0]) {
			return notification{}, invalid("X-Topic-ID must be one positive 64-bit integer")
		}
		id, err := strconv.ParseInt(values[0], 10, 64)
		if err != nil || id <= 0 {
			return notification{}, invalid("X-Topic-ID must be one positive 64-bit integer")
		}
		note.topicID, note.topicIDPresent = id, true
	}
	if note.topicPresent && utf8.RuneCountInString(note.topic) > 128 {
		return notification{}, invalid("X-Topic must be at most 128 Unicode code points")
	}
	if note.topicPresent && note.topicIDPresent {
		return notification{}, invalid("X-Topic and X-Topic-ID are mutually exclusive")
	}
	if !validLevel(note.level) {
		return notification{}, invalid("level must be debug, info, success, warning, or error")
	}
	if values, present := header["X-Fallback-Plain-Text"]; present {
		if len(values) != 1 {
			return notification{}, invalid("X-Fallback-Plain-Text must be true or false")
		}
		switch strings.ToLower(values[0]) {
		case "true":
			note.fallbackPlainText = true
		case "false":
			note.fallbackPlainText = false
		default:
			return notification{}, invalid("X-Fallback-Plain-Text must be true or false")
		}
	}
	return note, nil
}

func readBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	reader := http.MaxBytesReader(w, r.Body, maximumBodySize)
	body, err := io.ReadAll(reader)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return nil, apiError{status: http.StatusRequestEntityTooLarge, code: "request_too_large", message: "request body exceeds 64 KiB"}
		}
		var netError net.Error
		if errors.Is(r.Context().Err(), context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) || errors.As(err, &netError) && netError.Timeout() {
			return nil, apiError{status: http.StatusGatewayTimeout, code: "request_timeout", message: "request timed out"}
		}
		return nil, invalid("could not read request body")
	}
	return body, nil
}

func requireJSONContentType(r *http.Request) error {
	mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return apiError{status: http.StatusUnsupportedMediaType, code: "unsupported_media_type", message: "use application/json"}
	}
	if charset, present := params["charset"]; present && !strings.EqualFold(charset, "utf-8") {
		return apiError{status: http.StatusUnsupportedMediaType, code: "unsupported_media_type", message: "request body charset must be UTF-8"}
	}
	return nil
}

func decodeJSONObject(body []byte, allowed map[string]bool) (map[string]json.RawMessage, error) {
	if !utf8.Valid(body) {
		return nil, invalid("request body must be UTF-8")
	}
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	var object map[string]json.RawMessage
	if err := decoder.Decode(&object); err != nil || object == nil {
		return nil, invalid("request body must be one JSON object")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, invalid("request body must contain exactly one JSON object")
	}
	for field := range object {
		if !allowed[field] {
			return nil, invalid("request contains an unknown field")
		}
	}
	return object, nil
}

func decodeRequiredString(raw json.RawMessage, destination *string, field string) error {
	var decoded *string
	if err := json.Unmarshal(raw, &decoded); err != nil || decoded == nil || strings.TrimSpace(*decoded) == "" {
		return invalid(field + " must be a nonempty string")
	}
	*destination = *decoded
	return nil
}

func hashtag(name string) (string, error) {
	var result strings.Builder
	separator := false
	for _, r := range name {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			if separator && result.Len() > 0 {
				result.WriteByte('_')
			}
			separator = false
			result.WriteRune(r)
		} else if result.Len() > 0 {
			separator = true
		}
	}
	tag := strings.Trim(result.String(), "_")
	if tag == "" {
		return "", invalid("topic does not produce a valid hashtag")
	}
	allDigits := true
	for _, r := range tag {
		if !unicode.IsDigit(r) {
			allDigits = false
			break
		}
	}
	if allDigits {
		tag = "topic_" + tag
	}
	return tag, nil
}

func validLevel(level string) bool {
	switch level {
	case "debug", "info", "success", "warning", "error":
		return true
	default:
		return false
	}
}

func isSilent(level, threshold string) bool {
	rank := map[string]int{"debug": 0, "info": 1, "success": 2, "warning": 3, "error": 4}
	return rank[level] < rank[threshold]
}

func (s *Server) shouldPin(category string) bool {
	for _, configured := range s.cfg.PinCategories {
		if category == configured {
			return true
		}
	}
	return false
}

func operationError(err error) apiError {
	if errors.Is(err, context.DeadlineExceeded) {
		return apiError{status: http.StatusGatewayTimeout, code: "request_timeout", message: "remote operation timed out", uncertain: true}
	}
	if errors.Is(err, context.Canceled) {
		return apiError{status: http.StatusGatewayTimeout, code: "request_cancelled", message: "remote operation was cancelled", uncertain: true}
	}
	return errorFromTelegram(err)
}

func preMutationError(err error) apiError {
	if errors.Is(err, context.DeadlineExceeded) {
		return apiError{status: http.StatusGatewayTimeout, code: "request_timeout", message: "request timed out before the topic was created"}
	}
	if errors.Is(err, context.Canceled) {
		return apiError{status: http.StatusGatewayTimeout, code: "request_cancelled", message: "request was cancelled before the topic was created"}
	}
	return errorFromTelegram(err)
}

func safeLogFields(err error) string {
	var typed *telegram.Error
	if errors.As(err, &typed) {
		return typed.Code
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "deadline_exceeded"
	}
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	return "unclassified"
}
