package protocol

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

type ErrorKind string

const (
	ErrInvalidRequest ErrorKind = "invalid_request"
	ErrAuthentication ErrorKind = "authentication"
	ErrPermission     ErrorKind = "permission"
	ErrRateLimit      ErrorKind = "rate_limit"
	ErrNotFound       ErrorKind = "not_found"
	ErrServer         ErrorKind = "server"
	ErrNetwork        ErrorKind = "network"
	ErrTimeout        ErrorKind = "timeout"
	ErrCancelled      ErrorKind = "cancelled"
	ErrProtocol       ErrorKind = "protocol"
)

func (k ErrorKind) Valid() bool {
	switch k {
	case ErrInvalidRequest, ErrAuthentication, ErrPermission, ErrRateLimit,
		ErrNotFound, ErrServer, ErrNetwork, ErrTimeout, ErrCancelled, ErrProtocol:
		return true
	default:
		return false
	}
}

type ProviderError struct {
	Kind       ErrorKind     `json:"kind"`
	Provider   string        `json:"provider,omitempty"`
	StatusCode int           `json:"status_code,omitempty"`
	Message    string        `json:"message"`
	Retryable  bool          `json:"retryable"`
	RetryAfter time.Duration `json:"-"`
	Cause      error         `json:"-"`
}

func (e *ProviderError) Error() string {
	if e == nil {
		return "<nil>"
	}
	var b strings.Builder
	b.WriteString("provider error")
	if e.Kind != "" {
		fmt.Fprintf(&b, " (%s)", e.Kind)
	}
	if e.Provider != "" {
		fmt.Fprintf(&b, " provider=%s", e.Provider)
	}
	if e.StatusCode != 0 {
		fmt.Fprintf(&b, " status=%d", e.StatusCode)
	}
	if e.Message != "" {
		fmt.Fprintf(&b, ": %s", e.Message)
	}
	if e.Cause != nil {
		fmt.Fprintf(&b, ": caused by %s", e.Cause.Error())
	}
	return b.String()
}

func (e *ProviderError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func NewProviderError(kind ErrorKind, provider, message string) *ProviderError {
	return &ProviderError{
		Kind:     kind,
		Provider: provider,
		Message:  message,
	}
}

func ErrorFromStatus(provider string, status int, message string, retryAfter time.Duration) *ProviderError {
	e := &ProviderError{
		Provider:   provider,
		StatusCode: status,
		Message:    message,
		RetryAfter: retryAfter,
	}
	switch {
	case status == 400 || status == 422:
		e.Kind = ErrInvalidRequest
	case status == 401:
		e.Kind = ErrAuthentication
	case status == 403:
		e.Kind = ErrPermission
	case status == 404:
		e.Kind = ErrNotFound
	case status == 408:
		e.Kind = ErrTimeout
		e.Retryable = true
	case status == 429:
		e.Kind = ErrRateLimit
		e.Retryable = true
	case status >= 500 && status <= 599:
		e.Kind = ErrServer
		e.Retryable = true
	default:
		e.Kind = ErrProtocol
	}
	return e
}

func ErrorFromContext(provider string, err error) *ProviderError {
	e := &ProviderError{
		Provider: provider,
		Cause:    err,
	}
	if err != nil {
		e.Message = err.Error()
	}
	switch {
	case errors.Is(err, context.Canceled):
		e.Kind = ErrCancelled
	case errors.Is(err, context.DeadlineExceeded):
		e.Kind = ErrTimeout
		e.Retryable = true
	default:
		e.Kind = ErrNetwork
		e.Retryable = true
	}
	return e
}

var (
	// ErrStreamIncomplete is returned when a stream ends before an explicit
	// terminal done event. A partial body is not a successful completion.
	ErrStreamIncomplete = errors.New("protocol: stream ended before a terminal done event")
	// ErrContentFiltered is a model refusal. It is never a completed answer.
	ErrContentFiltered = errors.New("protocol: response refused by content filter")
	// ErrFinishError is the model's own error finish reason.
	ErrFinishError = errors.New("protocol: model reported an error finish")
	// ErrFinishUnknown is any finish reason the runtime does not recognize.
	ErrFinishUnknown = errors.New("protocol: unknown finish reason")
	// ErrTruncated is returned when a length finish produced no usable text
	// or stopped in the middle of a tool call. A length finish that still
	// produced text completes with Result.Truncated set instead.
	ErrTruncated = errors.New("protocol: response truncated by output length")
)

func IsRetryable(err error) bool {
	var pe *ProviderError
	if errors.As(err, &pe) {
		return pe.Retryable
	}
	return false
}
