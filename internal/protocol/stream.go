package protocol

import (
	"errors"
	"fmt"
)

type StreamEventType string

const (
	StreamTextDelta     StreamEventType = "text_delta"
	StreamToolCallDelta StreamEventType = "tool_call_delta"
	StreamUsage         StreamEventType = "usage"
	StreamDone          StreamEventType = "done"
	StreamError         StreamEventType = "error"
)

type ToolCallDelta struct {
	Index          int    `json:"index"`
	ID             string `json:"id,omitempty"`
	Name           string `json:"name,omitempty"`
	ArgumentsDelta string `json:"arguments_delta,omitempty"`
}

type StreamEvent struct {
	Type         StreamEventType `json:"type"`
	Text         string          `json:"text,omitempty"`
	ToolCall     *ToolCallDelta  `json:"tool_call,omitempty"`
	FinishReason FinishReason    `json:"finish_reason,omitempty"`
	Usage        *Usage          `json:"usage,omitempty"`
	Error        *ProviderError  `json:"error,omitempty"`
}

func (e StreamEvent) Validate() error {
	switch e.Type {
	case StreamTextDelta:
		if e.Text == "" {
			return errors.New("stream event: text_delta requires non-empty text")
		}
	case StreamToolCallDelta:
		if e.ToolCall == nil {
			return errors.New("stream event: tool_call_delta requires tool_call")
		}
		if e.ToolCall.Index < 0 {
			return errors.New("stream event: tool_call_delta index must not be negative")
		}
	case StreamUsage:
		if e.Usage == nil {
			return errors.New("stream event: usage requires usage")
		}
	case StreamDone:
		if e.FinishReason != "" && !e.FinishReason.Valid() {
			return fmt.Errorf("stream event: invalid finish reason %q", e.FinishReason)
		}
	case StreamError:
		if e.Error == nil {
			return errors.New("stream event: error requires error")
		}
	default:
		return fmt.Errorf("stream event: invalid type %q", e.Type)
	}
	if e.Usage != nil {
		if err := e.Usage.Validate(); err != nil {
			return fmt.Errorf("stream event: %w", err)
		}
	}
	return nil
}
