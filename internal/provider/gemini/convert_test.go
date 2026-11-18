package gemini

import (
	"encoding/json"
	"strings"
	"testing"

	"velo/internal/protocol"
)

func TestBuildBodyToolSignatureRoundTrip(t *testing.T) {
	req := protocol.Request{
		Model: "gemini-3.5-flash-lite",
		Messages: []protocol.Message{
			{Role: protocol.RoleSystem, Content: []protocol.Content{protocol.TextContent("you are velo")}},
			{Role: protocol.RoleUser, Content: []protocol.Content{protocol.TextContent("inspect")}},
			{
				Role: protocol.RoleAssistant,
				Content: []protocol.Content{
					protocol.ToolCallContent(protocol.ToolCall{
						ID:               "call_1",
						Name:             "list_files",
						Arguments:        json.RawMessage(`{"path":"."}`),
						ThoughtSignature: "sig-abc",
					}),
				},
			},
			{
				Role:             protocol.RoleTool,
				ToolCallID:       "call_1",
				Name:             "list_files",
				ThoughtSignature: "sig-abc",
				Content:          []protocol.Content{protocol.TextContent("a.txt\nb.txt")},
			},
		},
		Tools: []protocol.ToolDefinition{{
			Name:        "list_files",
			Description: "list files",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"additionalProperties":false}`),
		}},
	}

	data, model, err := buildBody(req, "")
	if err != nil {
		t.Fatalf("buildBody: %v", err)
	}
	if model != "gemini-3.5-flash-lite" {
		t.Fatalf("model = %q", model)
	}

	var body struct {
		Contents []struct {
			Role  string `json:"role"`
			Parts []struct {
				Text             string `json:"text"`
				ThoughtSignature string `json:"thoughtSignature"`
				FunctionCall     *struct {
					Name string          `json:"name"`
					Args json.RawMessage `json:"args"`
					ID   string          `json:"id"`
				} `json:"functionCall"`
				FunctionResponse *struct {
					Name     string          `json:"name"`
					Response json.RawMessage `json:"response"`
					ID       string          `json:"id"`
				} `json:"functionResponse"`
			} `json:"parts"`
		} `json:"contents"`
		SystemInstruction struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"systemInstruction"`
		Tools []struct {
			FunctionDeclarations []struct {
				Name       string         `json:"name"`
				Parameters map[string]any `json:"parameters"`
			} `json:"functionDeclarations"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(data, &body); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}

	if got := body.SystemInstruction.Parts[0].Text; got != "you are velo" {
		t.Fatalf("system instruction = %q", got)
	}
	if len(body.Contents) != 3 {
		t.Fatalf("expected 3 contents (system excluded), got %d", len(body.Contents))
	}

	assistant := body.Contents[1]
	if assistant.Role != "model" {
		t.Fatalf("assistant role = %q", assistant.Role)
	}
	fc := assistant.Parts[0]
	if fc.FunctionCall == nil || fc.FunctionCall.Name != "list_files" {
		t.Fatalf("assistant functionCall = %+v", fc.FunctionCall)
	}
	if fc.ThoughtSignature != "sig-abc" {
		t.Fatalf("assistant thoughtSignature = %q", fc.ThoughtSignature)
	}
	if fc.FunctionCall.ID != "call_1" {
		t.Fatalf("assistant functionCall id = %q", fc.FunctionCall.ID)
	}

	tool := body.Contents[2]
	if tool.Role != "user" {
		t.Fatalf("tool role = %q", tool.Role)
	}
	fr := tool.Parts[0]
	if fr.FunctionResponse == nil || fr.FunctionResponse.Name != "list_files" {
		t.Fatalf("tool functionResponse = %+v", fr.FunctionResponse)
	}
	if fr.ThoughtSignature != "sig-abc" {
		t.Fatalf("tool thoughtSignature = %q", fr.ThoughtSignature)
	}
	if fr.FunctionResponse.ID != "call_1" {
		t.Fatalf("tool functionResponse id = %q", fr.FunctionResponse.ID)
	}

	params := body.Tools[0].FunctionDeclarations[0].Parameters
	if _, ok := params["additionalProperties"]; ok {
		t.Fatalf("additionalProperties must be stripped, got %+v", params)
	}
}

func TestParseResponseToolCallWithSignature(t *testing.T) {
	raw := `{
		"candidates": [{
			"content": {
				"parts": [
					{"functionCall": {"name": "list_files", "args": {"path": "."}, "id": "call_9"}, "thoughtSignature": "sig-9"}
				],
				"role": "model"
			},
			"finishReason": "STOP"
		}],
		"usageMetadata": {"promptTokenCount": 10, "candidatesTokenCount": 5, "totalTokenCount": 15}
	}`

	resp, err := parseResponse([]byte(raw))
	if err != nil {
		t.Fatalf("parseResponse: %v", err)
	}
	if resp.FinishReason != protocol.FinishToolCalls {
		t.Fatalf("finish reason = %q", resp.FinishReason)
	}
	calls := resp.Message.ToolCalls()
	if len(calls) != 1 {
		t.Fatalf("tool calls = %d", len(calls))
	}
	if calls[0].ID != "call_9" || calls[0].Name != "list_files" {
		t.Fatalf("call = %+v", calls[0])
	}
	if calls[0].ThoughtSignature != "sig-9" {
		t.Fatalf("thought signature = %q", calls[0].ThoughtSignature)
	}
	if resp.Usage == nil || resp.Usage.TotalTokens != 15 {
		t.Fatalf("usage = %+v", resp.Usage)
	}
}

func TestParseResponseTextWithSignature(t *testing.T) {
	raw := `{
		"candidates": [{
			"content": {
				"parts": [
					{"text": "hello", "thoughtSignature": "sig-7"}
				],
				"role": "model"
			},
			"finishReason": "STOP"
		}]
	}`

	resp, err := parseResponse([]byte(raw))
	if err != nil {
		t.Fatalf("parseResponse: %v", err)
	}
	if resp.FinishReason != protocol.FinishStop {
		t.Fatalf("finish reason = %q", resp.FinishReason)
	}
	if resp.Message.Text() != "hello" {
		t.Fatalf("text = %q", resp.Message.Text())
	}
	if len(resp.Message.Content) != 1 || resp.Message.Content[0].ThoughtSignature != "sig-7" {
		t.Fatalf("content = %+v", resp.Message.Content)
	}
}

func TestParseResponseError(t *testing.T) {
	raw := `{"error": {"code": 400, "message": "bad request", "status": "INVALID_ARGUMENT"}}`
	_, err := parseResponse([]byte(raw))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "bad request") {
		t.Fatalf("error = %v", err)
	}
}

func TestParseResponseGeneratesCallIDWhenMissing(t *testing.T) {
	raw := `{
		"candidates": [{
			"content": {"parts": [{"functionCall": {"name": "list_files", "args": {}}}], "role": "model"},
			"finishReason": "STOP"
		}]
	}`
	resp, err := parseResponse([]byte(raw))
	if err != nil {
		t.Fatalf("parseResponse: %v", err)
	}
	calls := resp.Message.ToolCalls()
	if len(calls) != 1 || calls[0].ID == "" {
		t.Fatalf("expected generated call id, got %+v", calls)
	}
}

func TestSanitizeSchemaNested(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"items": map[string]any{
				"type":                 "array",
				"additionalProperties": false,
				"items":                map[string]any{"type": "string"},
			},
		},
		"additionalProperties": false,
	}
	out := sanitizeSchema(schema).(map[string]any)
	if _, ok := out["additionalProperties"]; ok {
		t.Fatalf("top-level additionalProperties not removed")
	}
	props := out["properties"].(map[string]any)
	items := props["items"].(map[string]any)
	if _, ok := items["additionalProperties"]; ok {
		t.Fatalf("nested additionalProperties not removed")
	}
	if items["type"] != "array" {
		t.Fatalf("unrelated keys dropped: %+v", items)
	}
}
