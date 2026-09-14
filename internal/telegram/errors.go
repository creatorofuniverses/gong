package telegram

import "fmt"

// Error is a safe, typed Telegram failure suitable for returning to callers.
// Uncertain means the remote operation may have taken effect.
type Error struct {
	Code       string
	Message    string
	HTTPStatus int
	RetryAfter int
	Uncertain  bool

	formattingRejection bool
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Message == "" {
		return e.Code
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func clearTransportError() *Error {
	return &Error{
		Code:       "telegram_transport",
		Message:    "could not connect to Telegram",
		HTTPStatus: 502,
	}
}

func uncertainError(message string) *Error {
	return &Error{
		Code:       "telegram_uncertain",
		Message:    message,
		HTTPStatus: 502,
		Uncertain:  true,
	}
}
