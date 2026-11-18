package contextmgr

import (
	"encoding/json"
	"strings"
	"testing"

	"velo/internal/protocol"
)

func textMessage(role protocol.Role, text string) protocol.Message {
	return protocol.Message{Role: role, Content: []protocol.Content{protocol.TextContent(text)}}
}

func toolCallMessage(id, name string) protocol.Message {
	return protocol.Message{
		Role: protocol.RoleAssistant,
		Content: []protocol.Content{protocol.ToolCallContent(protocol.ToolCall{
			ID:        id,
			Name:      name,
			Arguments: json.RawMessage(`{"path":"main.go"}`),
		})},
	}
}

func toolResultMessage(id, name, text string) protocol.Message {
	return protocol.Message{
		Role:       protocol.RoleTool,
		ToolCallID: id,
		Name:       name,
		Content:    []protocol.Content{protocol.TextContent(text)},
	}
}

func TestEstimateTokensPositive(t *testing.T) {
	messages := []protocol.Message{textMessage(protocol.RoleUser, "hello world")}
	if got := EstimateTokens(messages); got <= 0 {
		t.Fatalf("expected positive estimate, got %d", got)
	}
	if got := EstimateTokens(nil); got != 0 {
		t.Fatalf("expected zero estimate for empty input, got %d", got)
	}
}

func TestTrimDisabledWithoutBudget(t *testing.T) {
	messages := []protocol.Message{
		textMessage(protocol.RoleUser, "objective"),
		textMessage(protocol.RoleAssistant, strings.Repeat("a", 10000)),
	}
	out, stats := Trim(messages, Budget{})
	if stats.Trimmed {
		t.Fatal("trim must be disabled when MaxInputTokens is zero")
	}
	if len(out) != len(messages) {
		t.Fatalf("expected unchanged messages, got %d", len(out))
	}
}

func TestTrimKeepsObjectiveAndRecentMessages(t *testing.T) {
	messages := []protocol.Message{textMessage(protocol.RoleUser, "original objective")}
	for i := 0; i < 40; i++ {
		messages = append(messages, textMessage(protocol.RoleAssistant, strings.Repeat("x", 200)))
	}
	budget := Budget{MaxInputTokens: 200, ReserveOutputTokens: 20, SafetyMargin: 0.1}
	out, stats := Trim(messages, budget)
	if !stats.Trimmed {
		t.Fatal("expected trimming")
	}
	if len(out) >= len(messages) {
		t.Fatalf("expected fewer messages, got %d", len(out))
	}
	if out[0].Text() != "original objective" {
		t.Fatalf("objective was not preserved: %q", out[0].Text())
	}
	if out[len(out)-1].Text() != messages[len(messages)-1].Text() {
		t.Fatal("most recent message was not preserved")
	}
	if stats.MessagesAfter != len(out) || stats.Dropped != stats.MessagesBefore-stats.MessagesAfter {
		t.Fatalf("inconsistent stats: %+v", stats)
	}
}

func TestTrimNeverStartsWithToolMessage(t *testing.T) {
	messages := []protocol.Message{textMessage(protocol.RoleUser, "objective")}
	for i := 0; i < 20; i++ {
		id := "call_" + strings.Repeat("x", 1) + string(rune('a'+i))
		messages = append(messages, toolCallMessage(id, "read_file"))
		messages = append(messages, toolResultMessage(id, "read_file", strings.Repeat("r", 400)))
	}
	budget := Budget{MaxInputTokens: 500, ReserveOutputTokens: 50, SafetyMargin: 0.1}
	out, stats := Trim(messages, budget)
	if !stats.Trimmed {
		t.Fatal("expected trimming")
	}
	if out[0].Role == protocol.RoleTool {
		t.Fatalf("trimmed context starts with a tool message: %+v", out[0])
	}
	for i := 0; i < len(out); i++ {
		if out[i].Role != protocol.RoleTool {
			continue
		}
		if i == 0 || out[i-1].Role != protocol.RoleAssistant {
			t.Fatalf("tool message at %d is not preceded by its assistant call", i)
		}
	}
}

func TestTrimKeepsToolPairTogether(t *testing.T) {
	messages := []protocol.Message{
		textMessage(protocol.RoleUser, "objective"),
		toolCallMessage("call_1", "read_file"),
		toolResultMessage("call_1", "read_file", strings.Repeat("a", 300)),
		toolCallMessage("call_2", "read_file"),
		toolResultMessage("call_2", "read_file", strings.Repeat("b", 300)),
		textMessage(protocol.RoleAssistant, "done"),
	}
	budget := Budget{MaxInputTokens: 120, ReserveOutputTokens: 0, SafetyMargin: 0}
	out, _ := Trim(messages, budget)
	if out[0].Text() != "objective" {
		t.Fatalf("objective missing: %+v", out)
	}
	// Every retained tool call must be followed by its matching result when
	// the result is present in the original tail.
	for i, m := range out {
		if m.Role != protocol.RoleAssistant {
			continue
		}
		for _, call := range m.ToolCalls() {
			found := false
			for j := i + 1; j < len(out); j++ {
				if out[j].Role == protocol.RoleTool && out[j].ToolCallID == call.ID {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("tool call %s retained without its result", call.ID)
			}
		}
	}
}

func TestTrimFallsBackToLastMessage(t *testing.T) {
	messages := []protocol.Message{
		textMessage(protocol.RoleAssistant, strings.Repeat("a", 1000)),
	}
	out, stats := Trim(messages, Budget{MaxInputTokens: 10, ReserveOutputTokens: 0, SafetyMargin: 0})
	if len(out) != 1 || out[0].Text() != messages[0].Text() {
		t.Fatalf("expected last message fallback, got %+v", out)
	}
	if !stats.OverBudget {
		t.Fatal("expected over-budget stats")
	}
}
