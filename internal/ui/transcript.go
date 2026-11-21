package ui

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"velo/internal/events"
	"velo/internal/protocol"
)

// reloadTranscript rebuilds the visible blocks from the active session's
// persisted messages. It runs after :sessions resume so history is legible.
func (m *Model) reloadTranscript() {
	msgs, err := m.ctrl.Transcript()
	if err != nil {
		m.notice = "transcript: " + err.Error()
		return
	}
	m.blocks = nil
	m.usage = nil
	m.turn = 0
	for _, msg := range msgs {
		m.appendHistory(msg)
	}
	if extra := len(m.blocks) - maxTranscriptBlocks; extra > 0 {
		m.blocks = append([]block(nil), m.blocks[extra:]...)
	}
	m.relayout()
	m.vp.GotoBottom()
}

func (m *Model) appendHistory(msg protocol.Message) {
	switch msg.Role {
	case protocol.RoleUser:
		if text := strings.TrimSpace(msg.Text()); text != "" {
			m.pushBlock(newUserBlock(text))
		}
	case protocol.RoleAssistant:
		if text := strings.TrimSpace(msg.Text()); text != "" {
			b := block{kind: blockAssistant, text: text}
			b.rendered = renderMarkdown(m.theme.Name, text, m.contentWidth())
			m.pushBlock(b)
		}
		for _, call := range msg.ToolCalls() {
			tool := &events.Tool{
				ID:        call.ID,
				Name:      call.Name,
				Arguments: events.Cap(string(call.Arguments), events.MaxArgumentPreview),
				Status:    "done",
			}
			if def, ok := m.toolCategory(call.Name); ok {
				tool.Category = def
			}
			m.pushBlock(newToolBlock(tool))
		}
	case protocol.RoleTool:
		content := msg.Text()
		for i := len(m.blocks) - 1; i >= 0; i-- {
			b := &m.blocks[i]
			if b.kind == blockTool && b.tool != nil && b.tool.ID == msg.ToolCallID {
				b.tool.Output = events.Cap(content, events.MaxOutputPreview)
				break
			}
		}
	}
}

// toolCategory is best-effort: history does not carry tool definitions, so
// unknown names simply render without a category badge.
func (m *Model) toolCategory(name string) (string, bool) {
	switch name {
	case "read_file", "list_files", "search_files", "project_structure", "git_status", "git_diff":
		return "read", true
	case "write_file", "apply_patch":
		return "write", true
	case "run_command":
		return "exec", true
	}
	return "", false
}

// argsPreview renders the human-facing detail of a pending tool call for the
// approval modal: a diff-ish view for writes, an exact command line for exec.
func argsPreview(t *events.Tool) string {
	raw := t.Arguments
	var args map[string]any
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return oneLine(raw, 400)
	}
	switch t.Name {
	case "write_file":
		path, _ := args["path"].(string)
		content, _ := args["content"].(string)
		var sb strings.Builder
		fmt.Fprintf(&sb, "write %s (%d bytes)\n", path, len(content))
		sb.WriteString(previewLines(content, 24, "+ "))
		return sb.String()
	case "apply_patch":
		patch, _ := args["patch"].(string)
		return previewLines(patch, 32, "")
	case "run_command":
		exe, _ := args["executable"].(string)
		var argv []string
		if list, ok := args["args"].([]any); ok {
			for _, item := range list {
				if s, ok := item.(string); ok {
					argv = append(argv, s)
				}
			}
		}
		line := strings.TrimSpace(exe + " " + strings.Join(argv, " "))
		if line == "" {
			line = "(empty command)"
		}
		return "$ " + line
	default:
		var sb strings.Builder
		keys := make([]string, 0, len(args))
		for k := range args {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			fmt.Fprintf(&sb, "%s: %s\n", k, oneLine(fmt.Sprint(args[k]), 160))
		}
		return strings.TrimRight(sb.String(), "\n")
	}
}

func previewLines(text string, max int, prefix string) string {
	lines := strings.Split(text, "\n")
	var sb strings.Builder
	for i, line := range lines {
		if i >= max {
			fmt.Fprintf(&sb, "%s… (%d more lines)", prefix, len(lines)-max)
			break
		}
		sb.WriteString(prefix + truncate(line, 120) + "\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}
