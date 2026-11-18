package protocol

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type ToolDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
}

type Request struct {
	Model           string           `json:"model"`
	Messages        []Message        `json:"messages"`
	Tools           []ToolDefinition `json:"tools,omitempty"`
	MaxOutputTokens int              `json:"max_output_tokens,omitempty"`
	Temperature     *float64         `json:"temperature,omitempty"`
	Stream          bool             `json:"stream,omitempty"`
	ProviderOptions json.RawMessage  `json:"provider_options,omitempty"`
}

func (r Request) Validate() error {
	if strings.TrimSpace(r.Model) == "" {
		return errors.New("request: model is required")
	}
	if len(r.Messages) == 0 {
		return errors.New("request: at least one message is required")
	}
	for i, m := range r.Messages {
		if err := m.Validate(); err != nil {
			return fmt.Errorf("request message %d: %w", i, err)
		}
	}
	for i, t := range r.Tools {
		if strings.TrimSpace(t.Name) == "" {
			return fmt.Errorf("request tool %d: name is required", i)
		}
		if len(t.InputSchema) > 0 && !json.Valid(t.InputSchema) {
			return fmt.Errorf("request tool %d: input_schema is not valid json", i)
		}
	}
	if r.MaxOutputTokens < 0 {
		return errors.New("request: max_output_tokens must not be negative")
	}
	if r.Temperature != nil {
		t := *r.Temperature
		if t < 0 || t > 2 {
			return errors.New("request: temperature must be between 0 and 2")
		}
	}
	if len(r.ProviderOptions) > 0 && !json.Valid(r.ProviderOptions) {
		return errors.New("request: provider_options is not valid json")
	}
	return nil
}

type FinishReason string

const (
	FinishStop          FinishReason = "stop"
	FinishLength        FinishReason = "length"
	FinishToolCalls     FinishReason = "tool_calls"
	FinishContentFilter FinishReason = "content_filter"
	FinishError         FinishReason = "error"
	FinishUnknown       FinishReason = "unknown"
)

func (f FinishReason) Valid() bool {
	switch f {
	case FinishStop, FinishLength, FinishToolCalls, FinishContentFilter, FinishError, FinishUnknown:
		return true
	default:
		return false
	}
}

type Usage struct {
	InputTokens       int `json:"input_tokens,omitempty"`
	OutputTokens      int `json:"output_tokens,omitempty"`
	TotalTokens       int `json:"total_tokens,omitempty"`
	CachedInputTokens int `json:"cached_input_tokens,omitempty"`
}

func (u *Usage) Normalize() {
	if u == nil {
		return
	}
	if u.TotalTokens == 0 && (u.InputTokens != 0 || u.OutputTokens != 0) {
		u.TotalTokens = u.InputTokens + u.OutputTokens
	}
}

func (u Usage) Validate() error {
	if u.InputTokens < 0 {
		return errors.New("usage: input_tokens must not be negative")
	}
	if u.OutputTokens < 0 {
		return errors.New("usage: output_tokens must not be negative")
	}
	if u.TotalTokens < 0 {
		return errors.New("usage: total_tokens must not be negative")
	}
	if u.CachedInputTokens < 0 {
		return errors.New("usage: cached_input_tokens must not be negative")
	}
	return nil
}

type Response struct {
	Message          Message         `json:"message"`
	FinishReason     FinishReason    `json:"finish_reason"`
	Usage            *Usage          `json:"usage,omitempty"`
	ProviderMetadata json.RawMessage `json:"provider_metadata,omitempty"`
}

func (r Response) Validate() error {
	if r.Message.Role != RoleAssistant {
		return fmt.Errorf("response: message role must be assistant, got %q", r.Message.Role)
	}
	if err := r.Message.Validate(); err != nil {
		return fmt.Errorf("response: %w", err)
	}
	if !r.FinishReason.Valid() {
		return fmt.Errorf("response: invalid finish reason %q", r.FinishReason)
	}
	if r.Usage != nil {
		if err := r.Usage.Validate(); err != nil {
			return fmt.Errorf("response: %w", err)
		}
	}
	if len(r.ProviderMetadata) > 0 && !json.Valid(r.ProviderMetadata) {
		return errors.New("response: provider_metadata is not valid json")
	}
	seen := make(map[string]struct{})
	for _, c := range r.Message.Content {
		if c.Type != ContentToolCall || c.ToolCall == nil {
			continue
		}
		id := c.ToolCall.ID
		if _, ok := seen[id]; ok {
			return fmt.Errorf("response: duplicate tool call id %q", id)
		}
		seen[id] = struct{}{}
	}
	return nil
}
