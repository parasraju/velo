package gemini

import (
	"encoding/json"
	"fmt"
	"strings"

	"velo/internal/protocol"
)

// generateContentResponse is the non-streaming response body for
// POST /v1beta/models/{model}:generateContent.
type generateContentResponse struct {
	Candidates []candidate `json:"candidates"`
	// PromptFeedback is ignored: blocked prompts surface as an empty candidate
	// list plus an error, which we report separately.
	UsageMetadata *usageMetadata `json:"usageMetadata"`
	Error         *apiError      `json:"error"`
}

type candidate struct {
	Content      content `json:"content"`
	FinishReason string  `json:"finishReason"`
}

type content struct {
	Parts []part `json:"parts"`
	Role  string `json:"role"`
}

// part mirrors Gemini's Part. The thoughtSignature sits on the part itself,
// next to the text/functionCall it belongs to.
type part struct {
	Text             string        `json:"text"`
	ThoughtSignature string        `json:"thoughtSignature"`
	FunctionCall     *functionCall `json:"functionCall"`
}

type functionCall struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
	ID   string          `json:"id"`
}

type usageMetadata struct {
	PromptTokenCount     int `json:"promptTokenCount"`
	CandidatesTokenCount int `json:"candidatesTokenCount"`
	TotalTokenCount      int `json:"totalTokenCount"`
}

type apiError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Status  string `json:"status"`
}

// buildBody converts a protocol request into the native generateContent JSON
// body. The effective model name is returned so the caller can route to the
// correct model endpoint.
func buildBody(req protocol.Request, fallbackModel string) ([]byte, string, error) {
	if len(req.Messages) == 0 {
		return nil, "", protocol.NewProviderError(protocol.ErrInvalidRequest, providerName, "at least one message is required")
	}
	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = strings.TrimSpace(fallbackModel)
	}
	if model == "" {
		return nil, "", protocol.NewProviderError(protocol.ErrInvalidRequest, providerName, "model is required")
	}

	contents, err := convertContents(req.Messages)
	if err != nil {
		return nil, "", err
	}

	body := map[string]any{
		"contents": contents,
	}
	if sys := systemInstruction(req.Messages); sys != nil {
		body["systemInstruction"] = sys
	}
	if len(req.Tools) > 0 {
		tools, err := convertTools(req.Tools)
		if err != nil {
			return nil, "", err
		}
		body["tools"] = []any{map[string]any{"functionDeclarations": tools}}
	}

	gen := map[string]any{}
	if req.MaxOutputTokens > 0 {
		gen["maxOutputTokens"] = req.MaxOutputTokens
	}
	if req.Temperature != nil {
		gen["temperature"] = *req.Temperature
	}
	if len(gen) > 0 {
		body["generationConfig"] = gen
	}

	data, err := json.Marshal(body)
	if err != nil {
		return nil, "", protocol.NewProviderError(protocol.ErrInvalidRequest, providerName, fmt.Sprintf("encode request: %v", err))
	}
	return data, model, nil
}

func systemInstruction(messages []protocol.Message) map[string]any {
	for _, m := range messages {
		if m.Role != protocol.RoleSystem {
			continue
		}
		text := m.Text()
		if strings.TrimSpace(text) == "" {
			continue
		}
		return map[string]any{"parts": []any{map[string]any{"text": text}}}
	}
	return nil
}

func convertContents(messages []protocol.Message) ([]map[string]any, error) {
	var out []map[string]any
	for i, m := range messages {
		converted, err := convertMessage(m)
		if err != nil {
			return nil, protocol.NewProviderError(protocol.ErrInvalidRequest, providerName, fmt.Sprintf("message %d: %v", i, err))
		}
		if converted != nil {
			out = append(out, converted)
		}
	}
	return out, nil
}

func convertMessage(m protocol.Message) (map[string]any, error) {
	switch m.Role {
	case protocol.RoleSystem:
		// Handled via systemInstruction.
		return nil, nil
	case protocol.RoleUser:
		text, err := messageText(m)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"role":  "user",
			"parts": []any{map[string]any{"text": text}},
		}, nil
	case protocol.RoleAssistant:
		parts := make([]any, 0, len(m.Content))
		for _, c := range m.Content {
			switch c.Type {
			case protocol.ContentText:
				p := map[string]any{"text": c.Text}
				if c.ThoughtSignature != "" {
					p["thoughtSignature"] = c.ThoughtSignature
				}
				parts = append(parts, p)
			case protocol.ContentToolCall:
				if c.ToolCall == nil {
					return nil, fmt.Errorf("tool call content is missing tool call")
				}
				call := *c.ToolCall
				if strings.TrimSpace(call.Name) == "" {
					return nil, fmt.Errorf("tool call name is required")
				}
				args, err := parseArgs(call.Arguments)
				if err != nil {
					return nil, fmt.Errorf("tool call %q: %w", call.ID, err)
				}
				fc := map[string]any{"name": call.Name, "args": args}
				if strings.TrimSpace(call.ID) != "" {
					fc["id"] = call.ID
				}
				p := map[string]any{"functionCall": fc}
				if call.ThoughtSignature != "" {
					p["thoughtSignature"] = call.ThoughtSignature
				}
				parts = append(parts, p)
			default:
				return nil, fmt.Errorf("unsupported %q content for assistant message", c.Type)
			}
		}
		return map[string]any{"role": "model", "parts": parts}, nil
	case protocol.RoleTool:
		text, err := messageText(m)
		if err != nil {
			return nil, err
		}
		resp := map[string]any{
			"functionResponse": map[string]any{
				"name":     m.Name,
				"response": map[string]any{"result": text},
			},
		}
		if strings.TrimSpace(m.ToolCallID) != "" {
			resp["functionResponse"].(map[string]any)["id"] = m.ToolCallID
		}
		if m.ThoughtSignature != "" {
			resp["thoughtSignature"] = m.ThoughtSignature
		}
		return map[string]any{"role": "user", "parts": []any{resp}}, nil
	default:
		return nil, fmt.Errorf("unsupported role %q", m.Role)
	}
}

func messageText(m protocol.Message) (string, error) {
	var b strings.Builder
	for _, c := range m.Content {
		switch c.Type {
		case protocol.ContentText:
			b.WriteString(c.Text)
		default:
			return "", fmt.Errorf("unsupported %q content for %s message", c.Type, m.Role)
		}
	}
	return b.String(), nil
}

// sanitizeSchema removes JSON Schema keywords that Gemini's function
// declaration Schema dialect does not accept (notably additionalProperties),
// which velo's strict tool schemas include.
func sanitizeSchema(v any) any {
	switch node := v.(type) {
	case map[string]any:
		for _, key := range []string{
			"additionalProperties", "anyOf", "oneOf", "allOf", "not",
			"$schema", "$id", "$ref", "$defs", "definitions",
			"patternProperties", "propertyNames", "dependentSchemas",
			"unevaluatedProperties", "if", "then", "else",
		} {
			delete(node, key)
		}
		for k, child := range node {
			node[k] = sanitizeSchema(child)
		}
		return node
	case []any:
		for i, child := range node {
			node[i] = sanitizeSchema(child)
		}
		return node
	default:
		return v
	}
}

func parseArgs(raw json.RawMessage) (any, error) {
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "" || string(raw) == "null" {
		return map[string]any{}, nil
	}
	if !json.Valid(raw) {
		return nil, fmt.Errorf("arguments are not valid json")
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("arguments are not valid json: %v", err)
	}
	return v, nil
}

func convertTools(definitions []protocol.ToolDefinition) ([]map[string]any, error) {
	out := make([]map[string]any, 0, len(definitions))
	for i, definition := range definitions {
		name := strings.TrimSpace(definition.Name)
		if name == "" {
			return nil, protocol.NewProviderError(protocol.ErrInvalidRequest, providerName, fmt.Sprintf("tool %d: name is required", i))
		}
		params := json.RawMessage(`{"type":"object","properties":{}}`)
		if len(definition.InputSchema) > 0 {
			if !json.Valid(definition.InputSchema) {
				return nil, protocol.NewProviderError(protocol.ErrInvalidRequest, providerName, fmt.Sprintf("tool %q: input schema is not valid json", name))
			}
			params = definition.InputSchema
		}
		var paramsObj any
		if err := json.Unmarshal(params, &paramsObj); err != nil {
			return nil, protocol.NewProviderError(protocol.ErrInvalidRequest, providerName, fmt.Sprintf("tool %q: invalid input schema", name))
		}
		tool := map[string]any{"name": name, "parameters": sanitizeSchema(paramsObj)}
		if definition.Description != "" {
			tool["description"] = definition.Description
		}
		out = append(out, tool)
	}
	return out, nil
}

func parseResponse(data []byte) (*protocol.Response, error) {
	var decoded generateContentResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		return nil, protocol.NewProviderError(protocol.ErrProtocol, providerName, fmt.Sprintf("invalid response json: %v", err))
	}
	if decoded.Error != nil {
		return nil, errorFromAPI(decoded.Error)
	}
	if len(decoded.Candidates) == 0 {
		return nil, protocol.NewProviderError(protocol.ErrProtocol, providerName, "response contained no candidates")
	}

	candidate := decoded.Candidates[0]
	msg := protocol.Message{Role: protocol.RoleAssistant}
	hasToolCalls := false
	for i, pt := range candidate.Content.Parts {
		if pt.FunctionCall != nil {
			call, err := convertFunctionCall(pt.FunctionCall, i)
			if err != nil {
				return nil, err
			}
			call.ThoughtSignature = pt.ThoughtSignature
			msg.Content = append(msg.Content, protocol.ToolCallContent(call))
			hasToolCalls = true
			continue
		}
		if pt.Text != "" {
			c := protocol.TextContent(pt.Text)
			c.ThoughtSignature = pt.ThoughtSignature
			msg.Content = append(msg.Content, c)
		}
	}

	out := &protocol.Response{
		Message:      msg,
		FinishReason: mapFinishReason(candidate.FinishReason, hasToolCalls),
		Usage:        convertUsage(decoded.UsageMetadata),
	}
	if err := out.Validate(); err != nil {
		return nil, protocol.NewProviderError(protocol.ErrProtocol, providerName, fmt.Sprintf("invalid response: %v", err))
	}
	return out, nil
}

func convertFunctionCall(fc *functionCall, index int) (protocol.ToolCall, error) {
	if strings.TrimSpace(fc.Name) == "" {
		return protocol.ToolCall{}, protocol.NewProviderError(protocol.ErrProtocol, providerName, fmt.Sprintf("function call %d: name is required", index))
	}
	args := fc.Args
	if len(args) == 0 || string(args) == "null" {
		args = json.RawMessage("{}")
	}
	if !json.Valid(args) {
		return protocol.ToolCall{}, protocol.NewProviderError(protocol.ErrProtocol, providerName, fmt.Sprintf("function call %d: args are not valid json", index))
	}
	id := strings.TrimSpace(fc.ID)
	if id == "" {
		id = fmt.Sprintf("call_%d", index)
	}
	return protocol.ToolCall{
		ID:        id,
		Name:      fc.Name,
		Arguments: args,
	}, nil
}

func mapFinishReason(reason string, hasToolCalls bool) protocol.FinishReason {
	if hasToolCalls {
		return protocol.FinishToolCalls
	}
	switch strings.TrimSpace(reason) {
	case "MAX_TOKENS":
		return protocol.FinishLength
	case "SAFETY", "RECITATION", "PROHIBITED_CONTENT", "SPII", "IMAGE_SAFETY", "BLOCKLIST":
		return protocol.FinishContentFilter
	case "STOP", "":
		return protocol.FinishStop
	default:
		// Unknown reasons must not look like a normal stop.
		return protocol.FinishUnknown
	}
}

func convertUsage(u *usageMetadata) *protocol.Usage {
	if u == nil {
		return nil
	}
	out := &protocol.Usage{
		InputTokens:  u.PromptTokenCount,
		OutputTokens: u.CandidatesTokenCount,
		TotalTokens:  u.TotalTokenCount,
	}
	out.Normalize()
	return out
}

func errorFromAPI(e *apiError) *protocol.ProviderError {
	message := ""
	if e != nil {
		message = strings.TrimSpace(e.Message)
	}
	if message == "" {
		message = "provider error"
	}
	status := 0
	if e != nil {
		status = e.Code
	}
	if status == 0 {
		status = 500
	}
	return protocol.ErrorFromStatus(providerName, status, message, 0)
}
