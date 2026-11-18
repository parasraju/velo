package provider

import (
	"context"
	"net/http"

	"velo/internal/protocol"
)

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
