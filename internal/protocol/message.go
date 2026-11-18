package protocol

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

func (r Role) Valid() bool {
	switch r {
	case RoleSystem, RoleUser, RoleAssistant, RoleTool:
		return true
	default:
		return false
	}
}

type ContentType string

const (
	ContentText       ContentType = "text"
	ContentToolCall   ContentType = "tool_call"
	ContentToolResult ContentType = "tool_result"
)

func (t ContentType) Valid() bool {
	switch t {
	case ContentText, ContentToolCall, ContentToolResult:
		return true
	default:
		return false
	}
}

type ToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
	// ThoughtSignature carries a provider-specific opaque signature that must
	// be echoed back on the matching tool result. Gemini thinking models emit
	// one on every function call; omitting it on the function response is
	// rejected. Other providers leave it empty.
	ThoughtSignature string `json:"thought_signature,omitempty"`
}

func (c ToolCall) Validate() error {
	if strings.TrimSpace(c.ID) == "" {
		return errors.New("tool call: id is required")
	}
	if strings.TrimSpace(c.Name) == "" {
		return errors.New("tool call: name is required")
	}
	if len(c.Arguments) > 0 && !json.Valid(c.Arguments) {
		return fmt.Errorf("tool call %q: arguments are not valid json", c.ID)
	}
	return nil
}

type ToolResult struct {
	CallID  string `json:"call_id"`
	Content string `json:"content"`
	IsError bool   `json:"is_error,omitempty"`
}

type Content struct {
	Type       ContentType `json:"type"`
	Text       string      `json:"text,omitempty"`
	ToolCall   *ToolCall   `json:"tool_call,omitempty"`
	ToolResult *ToolResult `json:"tool_result,omitempty"`
	// ThoughtSignature carries a provider-specific signature for a text part.
	// Gemini thinking models attach one to text output as well as tool calls;
	// preserving it keeps reasoning continuity across resumed sessions.
	ThoughtSignature string `json:"thought_signature,omitempty"`
}

func TextContent(s string) Content {
	return Content{Type: ContentText, Text: s}
}

func ToolCallContent(c ToolCall) Content {
	return Content{Type: ContentToolCall, ToolCall: &c}
}

func ToolResultContent(r ToolResult) Content {
	return Content{Type: ContentToolResult, ToolResult: &r}
}

func (c Content) Validate() error {
	if !c.Type.Valid() {
		return fmt.Errorf("content: invalid type %q", c.Type)
	}
	switch c.Type {
	case ContentText:
		return nil
	case ContentToolCall:
		if c.ToolCall == nil {
			return errors.New("content: tool_call is required")
		}
		return c.ToolCall.Validate()
	case ContentToolResult:
		if c.ToolResult == nil {
			return errors.New("content: tool_result is required")
		}
		if strings.TrimSpace(c.ToolResult.CallID) == "" {
			return errors.New("content: tool_result call_id is required")
		}
		return nil
	default:
		return fmt.Errorf("content: invalid type %q", c.Type)
	}
}

type Message struct {
	Role             Role            `json:"role"`
	Content          []Content       `json:"content,omitempty"`
	ToolCallID       string          `json:"tool_call_id,omitempty"`
	Name             string          `json:"name,omitempty"`
	ThoughtSignature string          `json:"thought_signature,omitempty"`
	ProviderMetadata json.RawMessage `json:"provider_metadata,omitempty"`
}

func (m Message) Text() string {
	var b strings.Builder
	for _, c := range m.Content {
		if c.Type == ContentText {
			b.WriteString(c.Text)
		}
	}
	return b.String()
}

func (m Message) ToolCalls() []ToolCall {
	var calls []ToolCall
	for _, c := range m.Content {
		if c.Type == ContentToolCall && c.ToolCall != nil {
			calls = append(calls, *c.ToolCall)
		}
	}
	return calls
}

func (m Message) Validate() error {
	if !m.Role.Valid() {
		return fmt.Errorf("message: invalid role %q", m.Role)
	}
	if len(m.ProviderMetadata) > 0 && !json.Valid(m.ProviderMetadata) {
		return errors.New("message: provider_metadata is not valid json")
	}
	for i, c := range m.Content {
		if err := c.Validate(); err != nil {
			return fmt.Errorf("message content %d: %w", i, err)
		}
		if c.Type == ContentToolCall && m.Role != RoleAssistant {
			return fmt.Errorf("message content %d: tool_call requires assistant role", i)
		}
	}
	if m.Role == RoleTool && strings.TrimSpace(m.ToolCallID) == "" {
		return errors.New("message: tool role requires tool_call_id")
	}
	return nil
}
