package openai

import (
	"encoding/json"
	"fmt"
	"strings"

	"velo/internal/protocol"
)

type chatMessage struct {
	Role       string         `json:"role"`
	Content    *string        `json:"content,omitempty"`
	ToolCalls  []chatToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
	Name       string         `json:"name,omitempty"`
}

type chatToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function chatCallFunction `json:"function"`
}

type chatCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type chatToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

type chatTool struct {
	Type     string           `json:"type"`
	Function chatToolFunction `json:"function"`
}

type chatResponse struct {
	Choices []chatChoice `json:"choices"`
	Usage   *chatUsage   `json:"usage"`
	Error   *chatError   `json:"error"`
}

type chatChoice struct {
	Message      chatResponseMessage `json:"message"`
	Delta        chatResponseDelta   `json:"delta"`
	FinishReason string              `json:"finish_reason"`
}

type chatResponseMessage struct {
	Role      string             `json:"role"`
	Content   json.RawMessage    `json:"content"`
	ToolCalls []chatResponseCall `json:"tool_calls"`
}

type chatResponseDelta struct {
	Role      string             `json:"role"`
	Content   json.RawMessage    `json:"content"`
	ToolCalls []chatResponseCall `json:"tool_calls"`
}

type chatResponseCall struct {
	Index    int              `json:"index"`
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function chatResponseFunc `json:"function"`
}

type chatResponseFunc struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type chatUsage struct {
	PromptTokens        int `json:"prompt_tokens"`
	CompletionTokens    int `json:"completion_tokens"`
	TotalTokens         int `json:"total_tokens"`
	PromptTokensDetails *struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
}

type chatError struct {
	Message string          `json:"message"`
	Type    string          `json:"type"`
	Code    json.RawMessage `json:"code"`
	Status  int             `json:"status"`
}

var reservedKeys = map[string]struct{}{
	"model":          {},
	"messages":       {},
	"tools":          {},
	"stream":         {},
	"stream_options": {},
	"max_tokens":     {},
	"temperature":    {},
}

func (p *Provider) buildBody(req protocol.Request, stream bool) ([]byte, error) {
	if len(req.Messages) == 0 {
		return nil, protocol.NewProviderError(protocol.ErrInvalidRequest, providerName, "at least one message is required")
	}

	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = p.model
	}

	messages, err := convertMessages(req.Messages)
	if err != nil {
		return nil, err
	}
	tools, err := convertTools(req.Tools)
	if err != nil {
		return nil, err
	}

	body := map[string]any{
		"model":    model,
		"messages": messages,
		"stream":   stream,
	}
	if len(tools) > 0 {
		body["tools"] = tools
	}
	if req.MaxOutputTokens > 0 {
		body["max_tokens"] = req.MaxOutputTokens
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if stream {
		body["stream_options"] = map[string]any{"include_usage": true}
	}

	if len(req.ProviderOptions) > 0 {
		var extra map[string]json.RawMessage
		if err := json.Unmarshal(req.ProviderOptions, &extra); err != nil {
			return nil, protocol.NewProviderError(protocol.ErrInvalidRequest, providerName, fmt.Sprintf("invalid provider options: %v", err))
		}
		for key, value := range extra {
			if _, reserved := reservedKeys[key]; reserved {
				continue
			}
			body[key] = value
		}
	}

	data, err := json.Marshal(body)
	if err != nil {
		return nil, protocol.NewProviderError(protocol.ErrInvalidRequest, providerName, fmt.Sprintf("encode request: %v", err))
	}
	return data, nil
}

func convertMessages(messages []protocol.Message) ([]chatMessage, error) {
	out := make([]chatMessage, 0, len(messages))
	for i, message := range messages {
		converted, err := convertMessage(message)
		if err != nil {
			return nil, protocol.NewProviderError(protocol.ErrInvalidRequest, providerName, fmt.Sprintf("message %d: %v", i, err))
		}
		out = append(out, converted)
	}
	return out, nil
}

func convertMessage(message protocol.Message) (chatMessage, error) {
	switch message.Role {
	case protocol.RoleSystem, protocol.RoleUser:
		text, err := messageText(message)
		if err != nil {
			return chatMessage{}, err
		}
		return chatMessage{Role: string(message.Role), Content: stringPointer(text)}, nil

	case protocol.RoleAssistant:
		var parts []string
		var calls []chatToolCall
		for _, content := range message.Content {
			switch content.Type {
			case protocol.ContentText:
				parts = append(parts, content.Text)
			case protocol.ContentToolCall:
				if content.ToolCall == nil {
					return chatMessage{}, fmt.Errorf("tool call content is missing tool call")
				}
				call := *content.ToolCall
				if strings.TrimSpace(call.ID) == "" {
					return chatMessage{}, fmt.Errorf("tool call id is required")
				}
				if strings.TrimSpace(call.Name) == "" {
					return chatMessage{}, fmt.Errorf("tool call %q name is required", call.ID)
				}
				arguments := strings.TrimSpace(string(call.Arguments))
				if arguments == "" {
					arguments = "{}"
				}
				if !json.Valid([]byte(arguments)) {
					return chatMessage{}, fmt.Errorf("tool call %q arguments are not valid json", call.ID)
				}
				calls = append(calls, chatToolCall{
					ID:   call.ID,
					Type: "function",
					Function: chatCallFunction{
						Name:      call.Name,
						Arguments: arguments,
					},
				})
			default:
				return chatMessage{}, fmt.Errorf("unsupported %q content for assistant message", content.Type)
			}
		}
		msg := chatMessage{Role: string(protocol.RoleAssistant), ToolCalls: calls}
		if text := strings.Join(parts, ""); text != "" {
			msg.Content = stringPointer(text)
		}
		return msg, nil

	case protocol.RoleTool:
		if strings.TrimSpace(message.ToolCallID) == "" {
			return chatMessage{}, fmt.Errorf("tool message requires tool_call_id")
		}
		text, err := messageText(message)
		if err != nil {
			return chatMessage{}, err
		}
		return chatMessage{
			Role:       string(protocol.RoleTool),
			Content:    stringPointer(text),
			ToolCallID: message.ToolCallID,
			Name:       message.Name,
		}, nil

	default:
		return chatMessage{}, fmt.Errorf("unsupported role %q", message.Role)
	}
}

func messageText(message protocol.Message) (string, error) {
	var b strings.Builder
	for _, content := range message.Content {
		switch content.Type {
		case protocol.ContentText:
			b.WriteString(content.Text)
		default:
			return "", fmt.Errorf("unsupported %q content for %s message", content.Type, message.Role)
		}
	}
	return b.String(), nil
}

func convertTools(definitions []protocol.ToolDefinition) ([]chatTool, error) {
	if len(definitions) == 0 {
		return nil, nil
	}
	out := make([]chatTool, 0, len(definitions))
	for i, definition := range definitions {
		name := strings.TrimSpace(definition.Name)
		if name == "" {
			return nil, protocol.NewProviderError(protocol.ErrInvalidRequest, providerName, fmt.Sprintf("tool %d: name is required", i))
		}
		parameters := json.RawMessage(`{"type":"object","properties":{}}`)
		if len(definition.InputSchema) > 0 {
			if !json.Valid(definition.InputSchema) {
				return nil, protocol.NewProviderError(protocol.ErrInvalidRequest, providerName, fmt.Sprintf("tool %q: input schema is not valid json", name))
			}
			parameters = definition.InputSchema
		}
		out = append(out, chatTool{
			Type: "function",
			Function: chatToolFunction{
				Name:       name,
				Parameters: parameters,
			},
		})
	}
	return out, nil
}

func convertResponseToolCall(call chatResponseCall) (protocol.ToolCall, error) {
	if strings.TrimSpace(call.ID) == "" {
		return protocol.ToolCall{}, protocol.NewProviderError(protocol.ErrProtocol, providerName, "tool call id is required")
	}
	if strings.TrimSpace(call.Function.Name) == "" {
		return protocol.ToolCall{}, protocol.NewProviderError(protocol.ErrProtocol, providerName, fmt.Sprintf("tool call %q name is required", call.ID))
	}
	arguments := strings.TrimSpace(call.Function.Arguments)
	if arguments == "" {
		arguments = "{}"
	}
	if !json.Valid([]byte(arguments)) {
		return protocol.ToolCall{}, protocol.NewProviderError(protocol.ErrProtocol, providerName, fmt.Sprintf("tool call %q arguments are not valid json", call.ID))
	}
	return protocol.ToolCall{
		ID:        call.ID,
		Name:      call.Function.Name,
		Arguments: json.RawMessage(arguments),
	}, nil
}

func contentText(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return "", nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text, nil
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts); err == nil {
		var b strings.Builder
		for _, part := range parts {
			if part.Type == "" || part.Type == "text" || part.Type == "output_text" {
				b.WriteString(part.Text)
			}
		}
		return b.String(), nil
	}
	return "", fmt.Errorf("unsupported message content shape")
}

func convertUsage(usage *chatUsage) *protocol.Usage {
	if usage == nil {
		return nil
	}
	out := &protocol.Usage{
		InputTokens:  usage.PromptTokens,
		OutputTokens: usage.CompletionTokens,
		TotalTokens:  usage.TotalTokens,
	}
	if usage.PromptTokensDetails != nil {
		out.CachedInputTokens = usage.PromptTokensDetails.CachedTokens
	}
	out.Normalize()
	return out
}

func mapFinishReason(reason string) protocol.FinishReason {
	switch strings.TrimSpace(reason) {
	case "stop":
		return protocol.FinishStop
	case "length":
		return protocol.FinishLength
	case "tool_calls", "function_call":
		return protocol.FinishToolCalls
	case "content_filter":
		return protocol.FinishContentFilter
	default:
		return protocol.FinishUnknown
	}
}

func errorFromChatError(err *chatError) *protocol.ProviderError {
	if err == nil {
		return protocol.NewProviderError(protocol.ErrProtocol, providerName, "unknown provider error")
	}
	kind := protocol.ErrProtocol
	switch strings.ToLower(strings.TrimSpace(err.Type)) {
	case "invalid_request_error":
		kind = protocol.ErrInvalidRequest
	case "authentication_error":
		kind = protocol.ErrAuthentication
	case "permission_error":
		kind = protocol.ErrPermission
	case "rate_limit_error":
		kind = protocol.ErrRateLimit
	case "not_found_error":
		kind = protocol.ErrNotFound
	case "server_error", "api_error":
		kind = protocol.ErrServer
	case "timeout_error":
		kind = protocol.ErrTimeout
	}
	message := err.Message
	if message == "" {
		message = err.Type
	}
	if message == "" {
		message = "provider error"
	}
	if kind == protocol.ErrProtocol && err.Status != 0 {
		kind = protocol.ErrorFromStatus(providerName, err.Status, message, 0).Kind
	}
	return &protocol.ProviderError{
		Kind:       kind,
		Provider:   providerName,
		StatusCode: err.Status,
		Message:    message,
		Retryable:  kind == protocol.ErrRateLimit || kind == protocol.ErrServer || kind == protocol.ErrTimeout,
	}
}

func stringPointer(s string) *string {
	return &s
}
