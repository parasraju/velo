package ui

import (
	"strings"

	"velo/internal/events"
)

type blockKind int

const (
	blockUser blockKind = iota
	blockAssistant
	blockTool
	blockSystem
)

// block is a single rendered entry in the transcript scrollback.
type block struct {
	kind blockKind

	// text holds user input, assistant markdown source, or system notices.
	text string
	// rendered caches the styled/marked-up form of text for finalized blocks.
	rendered string

	// tool carries tool-block state (name, status, args, output).
	tool *events.Tool
	// streaming marks an assistant block still receiving deltas; it is
	// rendered as raw text until finalized into markdown.
	streaming bool
	// expanded toggles the verbose tool view.
	expanded bool
}

func newUserBlock(text string) block {
	return block{kind: blockUser, text: text}
}

func newAssistantBlock() block {
	return block{kind: blockAssistant, streaming: true}
}

func newToolBlock(t *events.Tool) block {
	return block{kind: blockTool, tool: t}
}

func newSystemBlock(text string) block {
	return block{kind: blockSystem, text: text}
}

// toolLine renders the compact one-line summary of a tool call.
func (b block) toolLine(th Theme) string {
	if b.tool == nil {
		return ""
	}
	status := b.tool.Status
	icon := "·"
	switch status {
	case "running", "requested":
		icon = "…"
	case "succeeded", "approved", "trusted", "done":
		icon = "✓"
	case "failed", "denied":
		icon = "✗"
	}
	statusStyle := th.Dim
	switch icon {
	case "✓":
		statusStyle = th.Success
	case "✗":
		statusStyle = th.Error
	case "…":
		statusStyle = th.Warning
	}
	label := th.ToolLabel.Render("⚙ " + b.tool.Name)
	meta := statusStyle.Render(icon + " " + status)
	detail := toolDetail(b.tool)
	parts := []string{label}
	if detail != "" {
		parts = append(parts, th.Dim.Render(detail))
	}
	parts = append(parts, meta)
	if b.tool.DurationMS > 0 {
		parts = append(parts, th.Dim.Render(formatDuration(b.tool.DurationMS)))
	}
	return strings.Join(parts, " ")
}

func toolDetail(t *events.Tool) string {
	switch t.Name {
	case "write_file", "read_file", "list_files", "search_files":
		if p := jsonField(t.Arguments, "path"); p != "" {
			return p
		}
	case "run_command":
		if e := jsonField(t.Arguments, "executable"); e != "" {
			return e
		}
	case "apply_patch":
		return "patch"
	}
	return t.Category
}

func formatDuration(ms int64) string {
	switch {
	case ms < 1000:
		return itoa64(ms) + "ms"
	case ms < 60_000:
		return trimFloat(float64(ms)/1000.0) + "s"
	default:
		mins := ms / 60_000
		secs := (ms % 60_000) / 1000
		return itoa64(mins) + "m" + itoa64(secs) + "s"
	}
}

func jsonField(raw, field string) string {
	// Minimal scanner for a top-level string field keeps the UI dependency-free.
	key := `"` + field + `"`
	i := strings.Index(raw, key)
	if i < 0 {
		return ""
	}
	rest := raw[i+len(key):]
	colon := strings.IndexByte(rest, ':')
	if colon < 0 {
		return ""
	}
	rest = strings.TrimLeft(rest[colon+1:], " ")
	if len(rest) == 0 || rest[0] != '"' {
		return ""
	}
	rest = rest[1:]
	var sb strings.Builder
	for j := 0; j < len(rest); j++ {
		c := rest[j]
		if c == '\\' && j+1 < len(rest) {
			j++
			switch rest[j] {
			case 'n':
				sb.WriteByte('\n')
			case '"':
				sb.WriteByte('"')
			case '\\':
				sb.WriteByte('\\')
			default:
				sb.WriteByte(rest[j])
			}
			continue
		}
		if c == '"' {
			break
		}
		sb.WriteByte(c)
	}
	out := sb.String()
	if idx := strings.IndexByte(out, '\n'); idx >= 0 {
		out = out[:idx]
	}
	return truncate(out, 48)
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func itoa64(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func trimFloat(f float64) string {
	whole := int64(f)
	frac := int64((f - float64(whole)) * 10)
	if frac == 0 {
		return itoa64(whole)
	}
	return itoa64(whole) + "." + itoa64(frac)
}
