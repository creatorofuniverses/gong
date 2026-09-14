package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
)

const (
	defaultBaseURL      = "https://api.telegram.org"
	defaultTimeout      = 10 * time.Second
	maximumResponseSize = 1 << 20
)

type Options struct {
	BotToken   string
	ProxyURL   string
	Timeout    time.Duration
	BaseURL    string
	HTTPClient *http.Client
}

type Client struct {
	baseURL  string
	botToken string
	http     *http.Client
	timeout  time.Duration
}

// Transport is the Telegram surface consumed by the gateway and topic resolver.
type Transport interface {
	Send(context.Context, SendRequest) (SendResult, error)
	CreateTopic(context.Context, string, string, Icon) (Topic, error)
	DeleteTopic(context.Context, string, int64) error
	Pin(context.Context, string, int64) error
	GetTopicIcons(context.Context) ([]string, error)
}

var _ Transport = (*Client)(nil)

type SendRequest struct {
	ChatID            string
	Text              string
	TopicID           int64
	Silent            bool
	FallbackPlainText bool
}

type SendResult struct {
	MessageID    int64
	FallbackUsed bool
}

type Topic struct {
	ID   int64
	Name string
}

type Icon struct {
	CustomEmojiID string
	Color         int
}

func New(options Options) (*Client, error) {
	if strings.TrimSpace(options.BotToken) == "" {
		return nil, errors.New("telegram bot token must be nonempty")
	}
	if options.Timeout < 0 {
		return nil, errors.New("telegram timeout must not be negative")
	}
	if options.Timeout == 0 {
		options.Timeout = defaultTimeout
	}
	if options.BaseURL == "" {
		options.BaseURL = defaultBaseURL
	}
	base, err := url.Parse(options.BaseURL)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return nil, errors.New("telegram base URL is invalid")
	}
	httpClient, err := buildHTTPClient(options)
	if err != nil {
		return nil, err
	}
	return &Client{
		baseURL:  strings.TrimRight(options.BaseURL, "/"),
		botToken: options.BotToken,
		http:     httpClient,
		timeout:  options.Timeout,
	}, nil
}

func (c *Client) CloseIdleConnections() {
	if c != nil && c.http != nil {
		c.http.CloseIdleConnections()
	}
}

func (c *Client) Send(ctx context.Context, request SendRequest) (SendResult, error) {
	type sendPayload struct {
		ChatID              string `json:"chat_id"`
		Text                string `json:"text"`
		ParseMode           string `json:"parse_mode,omitempty"`
		MessageThreadID     int64  `json:"message_thread_id,omitempty"`
		DisableNotification bool   `json:"disable_notification"`
	}
	type sendResponse struct {
		MessageID int64 `json:"message_id"`
	}
	payload := sendPayload{
		ChatID: request.ChatID, Text: request.Text, ParseMode: "HTML",
		MessageThreadID: request.TopicID, DisableNotification: request.Silent,
	}
	var response sendResponse
	err := c.call(ctx, "sendMessage", payload, &response)
	if err == nil {
		if response.MessageID <= 0 {
			return SendResult{}, uncertainError("Telegram returned an invalid response after the request was sent")
		}
		return SendResult{MessageID: response.MessageID}, nil
	}
	var telegramError *Error
	if !request.FallbackPlainText || !errors.As(err, &telegramError) || !telegramError.formattingRejection {
		return SendResult{}, err
	}

	payload.ParseMode = ""
	response = sendResponse{}
	if err := c.call(ctx, "sendMessage", payload, &response); err != nil {
		return SendResult{}, err
	}
	if response.MessageID <= 0 {
		return SendResult{}, uncertainError("Telegram returned an invalid response after the request was sent")
	}
	return SendResult{MessageID: response.MessageID, FallbackUsed: true}, nil
}

func (c *Client) CreateTopic(ctx context.Context, chatID, name string, icon Icon) (Topic, error) {
	payload := struct {
		ChatID            string `json:"chat_id"`
		Name              string `json:"name"`
		IconCustomEmojiID string `json:"icon_custom_emoji_id,omitempty"`
		IconColor         int    `json:"icon_color,omitempty"`
	}{ChatID: chatID, Name: name, IconCustomEmojiID: icon.CustomEmojiID}
	if icon.CustomEmojiID == "" {
		payload.IconColor = icon.Color
	}
	var response struct {
		ID   int64  `json:"message_thread_id"`
		Name string `json:"name"`
	}
	if err := c.call(ctx, "createForumTopic", payload, &response); err != nil {
		return Topic{}, err
	}
	if response.ID <= 0 {
		return Topic{}, uncertainError("Telegram returned an invalid response after the request was sent")
	}
	return Topic{ID: response.ID, Name: response.Name}, nil
}

func (c *Client) DeleteTopic(ctx context.Context, chatID string, topicID int64) error {
	payload := struct {
		ChatID          string `json:"chat_id"`
		MessageThreadID int64  `json:"message_thread_id"`
	}{chatID, topicID}
	return c.callBool(ctx, "deleteForumTopic", payload)
}

func (c *Client) Pin(ctx context.Context, chatID string, messageID int64) error {
	payload := struct {
		ChatID              string `json:"chat_id"`
		MessageID           int64  `json:"message_id"`
		DisableNotification bool   `json:"disable_notification"`
	}{chatID, messageID, true}
	return c.callBool(ctx, "pinChatMessage", payload)
}

func (c *Client) GetTopicIcons(ctx context.Context) ([]string, error) {
	var response []struct {
		CustomEmojiID string `json:"custom_emoji_id"`
	}
	if err := c.call(ctx, "getForumTopicIconStickers", struct{}{}, &response); err != nil {
		return nil, err
	}
	icons := make([]string, 0, len(response))
	for _, sticker := range response {
		if sticker.CustomEmojiID != "" {
			icons = append(icons, sticker.CustomEmojiID)
		}
	}
	return icons, nil
}

func (c *Client) callBool(ctx context.Context, method string, payload any) error {
	var result bool
	if err := c.call(ctx, method, payload, &result); err != nil {
		return err
	}
	if !result {
		return uncertainError("Telegram returned an invalid response after the request was sent")
	}
	return nil
}

type apiEnvelope struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	ErrorCode   int             `json:"error_code"`
	Description string          `json:"description"`
	Parameters  struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

type requestPhase struct {
	gotConnection  atomic.Bool
	writeAttempted atomic.Bool
}

func (c *Client) call(ctx context.Context, method string, payload, result any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return &Error{Code: "telegram_request_invalid", Message: "could not encode Telegram request", HTTPStatus: 502}
	}
	callCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	phase := &requestPhase{}
	trace := &httptrace.ClientTrace{
		// DNS, dial, CONNECT/SOCKS, and TLS failures precede GotConn and are
		// clear. Once a connection is handed to net/http, httptrace exposes no
		// byte count, so an otherwise unproven failure is conservatively uncertain.
		GotConn: func(httptrace.GotConnInfo) { phase.gotConnection.Store(true) },
		// A non-nil WroteRequestInfo.Err does not prove zero bytes were written.
		// Treat every callback as a possible partial write.
		WroteRequest: func(httptrace.WroteRequestInfo) { phase.writeAttempted.Store(true) },
	}
	callCtx = httptrace.WithClientTrace(callCtx, trace)
	endpoint := c.baseURL + "/bot" + url.PathEscape(c.botToken) + "/" + method
	request, err := http.NewRequestWithContext(callCtx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return &Error{Code: "telegram_request_invalid", Message: "could not create Telegram request", HTTPStatus: 502}
	}
	// NewRequest gives bytes.Reader a GetBody function. Go's HTTP/2 transport
	// uses it to replay POST bodies after some peer stream errors, including
	// errors that can arrive after a mutation was written. Removing GetBody
	// keeps the nonempty body while making those operations non-replayable.
	request.GetBody = nil
	request.Header.Set("Content-Type", "application/json")

	response, err := c.http.Do(request)
	if err != nil {
		return classifyTransportError(callCtx, err, phase)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, maximumResponseSize+1))
	if err != nil {
		if errors.Is(callCtx.Err(), context.DeadlineExceeded) || isTimeout(err) {
			return &Error{
				Code: "telegram_timeout", Message: "Telegram request timed out", HTTPStatus: 504,
				Uncertain: true,
			}
		}
		return uncertainError("Telegram returned an unreadable response after the request was sent")
	}
	if len(responseBody) > maximumResponseSize {
		return uncertainError("Telegram returned an unreadable response after the request was sent")
	}
	var envelope apiEnvelope
	if err := json.Unmarshal(responseBody, &envelope); err != nil {
		return uncertainError("Telegram returned an invalid response after the request was sent")
	}

	if response.StatusCode >= 500 || response.StatusCode < 200 || response.StatusCode >= 300 && response.StatusCode < 400 {
		return uncertainError("Telegram returned a server error after the request was sent")
	}
	if !envelope.OK {
		if envelope.ErrorCode < 400 || envelope.ErrorCode > 499 {
			return uncertainError("Telegram returned an invalid response after the request was sent")
		}
		if envelope.ErrorCode == http.StatusTooManyRequests {
			return &Error{
				Code: "telegram_rate_limited", Message: "Telegram rate limit exceeded",
				HTTPStatus: 429, RetryAfter: envelope.Parameters.RetryAfter,
			}
		}
		telegramError := &Error{
			Code: "telegram_rejected", Message: "Telegram rejected the request", HTTPStatus: 502,
		}
		telegramError.formattingRejection = envelope.ErrorCode == http.StatusBadRequest && isFormattingRejection(envelope.Description)
		return telegramError
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || len(envelope.Result) == 0 || string(envelope.Result) == "null" {
		return uncertainError("Telegram returned an invalid response after the request was sent")
	}
	if err := json.Unmarshal(envelope.Result, result); err != nil {
		return uncertainError("Telegram returned an invalid response after the request was sent")
	}
	return nil
}

func classifyTransportError(ctx context.Context, err error, phase *requestPhase) *Error {
	uncertain := phase.writeAttempted.Load() || phase.gotConnection.Load()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || isTimeout(err) {
		return &Error{
			Code: "telegram_timeout", Message: "Telegram request timed out", HTTPStatus: 504,
			Uncertain: uncertain,
		}
	}
	if uncertain {
		return uncertainError("connection to Telegram was lost after the request may have been sent")
	}
	return clearTransportError()
}

func isTimeout(err error) bool {
	var netError net.Error
	return errors.As(err, &netError) && netError.Timeout()
}

func isFormattingRejection(description string) bool {
	description = strings.ToLower(description)
	for _, marker := range []string{
		"can't parse entities",
		"unsupported start tag",
		"can't find end tag",
		"unclosed start tag",
		"wrong nested tag",
	} {
		if strings.Contains(description, marker) {
			return true
		}
	}
	return false
}
