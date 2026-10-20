package provider

import (
	"fmt"
	"net/http"
)

// StatusError carries an HTTP status and machine-readable code back to the host envelope.
type StatusError struct {
	Code       string
	Message    string
	HTTPStatus int
	Retryable  bool
}

func (e *StatusError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

func statusError(code, message string, status int) error {
	if status == 0 {
		status = http.StatusInternalServerError
	}
	return &StatusError{Code: code, Message: message, HTTPStatus: status}
}

func upstreamStatusError(status int, detail string) error {
	message := fmt.Sprintf("Ollama Cloud returned HTTP %d", status)
	if detail != "" {
		message += ": " + detail
	}
	code := "upstream_error"
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		code = "upstream_unauthorized"
	case http.StatusTooManyRequests:
		code = "upstream_rate_limited"
	case http.StatusPaymentRequired:
		code = "upstream_payment_required"
	}
	return &StatusError{
		Code:       code,
		Message:    message,
		HTTPStatus: status,
		Retryable:  status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status >= 500,
	}
}
