package provider

import (
	"context"
	"fmt"
	"net/http"

	"velo/internal/protocol"
)

// MaxRetriesLimit is the inclusive upper bound for provider retry counts.
// Config validation and every provider constructor use this one limit.
const MaxRetriesLimit = 5

// CheckMaxRetries reports whether n is an allowed retry count. Zero selects
// the adapter's built-in default.
func CheckMaxRetries(n int) error {
	if n < 0 || n > MaxRetriesLimit {
		return fmt.Errorf("max retries must be between 0 and %d", MaxRetriesLimit)
	}
	return nil
}

type Capabilities struct {
	Streaming         bool `json:"streaming"`
	Tools             bool `json:"tools"`
	ParallelToolCalls bool `json:"parallel_tool_calls"`
	Usage             bool `json:"usage"`
	MaxInputTokens    int  `json:"max_input_tokens,omitempty"`
	MaxOutputTokens   int  `json:"max_output_tokens,omitempty"`
}

type Options struct {
	Model      string
	BaseURL    string
	APIKey     string
	Headers    map[string]string
	HTTPClient *http.Client
	MaxRetries int
}

type Provider interface {
	Name() string
	Model() string
	Capabilities() Capabilities
	Complete(ctx context.Context, req protocol.Request) (*protocol.Response, error)
	Stream(ctx context.Context, req protocol.Request) (Stream, error)
	Close() error
}

type Stream interface {
	Recv() (protocol.StreamEvent, error)
	Close() error
}

type Factory func(Options) (Provider, error)
