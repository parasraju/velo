package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// relayout recomputes the viewport geometry, rebuilds transcript content, and
// follows the bottom edge when the user was already at it.
func (m *Model) relayout() {
	if !m.ready {
		return
	}
	m.adjustInputHeight()
	inputH := m.input.Height() + 1
	statusH := 1
	noticeH := 1
	dividerH := 1
	h := m.height - statusH - inputH - noticeH - dividerH
	if h < 1 {
		h = 1
	}
	m.vp.Width = m.width
	m.vp.Height = h
	follow := m.vp.AtBottom()
	m.vp.SetContent(m.renderTranscript())
	if follow {
		m.vp.GotoBottom()
	}
}

// adjustInputHeight keeps the textarea as tall as its content (bounded), so
// transcripts are not squeezed by an oversized input box.
func (m *Model) adjustInputHeight() {
	lines := m.input.LineCount()
	if lines < 1 {
		lines = 1
	}
	max := m.input.MaxHeight
	if max <= 0 {
		max = 6
	}
	if lines > max {
		lines = max
	}
	if lines != m.input.Height() {
		m.input.SetHeight(lines)
	}
}

// contentWidth is the wrap width available to transcript content.
func (m Model) contentWidth() int {
	w := m.width - 2
	if w < 20 {
		w = 20
	}
	return w
}

// scrollToBottom moves the viewport to the newest content. With force=false
// it only follows when the user was already at the bottom.
func (m *Model) scrollToBottom(force bool) {
	if force || m.vp.AtBottom() {
		m.vp.GotoBottom()
	}
}

func (m Model) renderTranscript() string {
	if len(m.blocks) == 0 {
		return m.welcome()
	}
	var sb strings.Builder
	width := m.contentWidth()
	for i, b := range m.blocks {
		if i > 0 {
			sb.WriteString("\n\n")
		}
		sb.WriteString(m.renderBlock(b, width))
	}
	return strings.TrimRight(sb.String(), "\n")
}

func (m Model) renderBlock(b block, width int) string {
	switch b.kind {
	case blockUser:
		label := m.theme.UserLabel.Render("you")
		return label + "\n" + indent(b.text, "  ")
	case blockAssistant:
		label := m.theme.AssistantLabel.Render("velo")
		if b.streaming {
			return label + "\n" + indent(b.text+"▌", "  ")
		}
		body := b.rendered
		if body == "" {
			body = renderMarkdown(m.theme.Name, b.text, width)
		}
		return label + "\n" + body
	case blockTool:
		return m.renderBlockTool(b, width)
	default:
		return m.theme.SystemLabel.Render("  " + b.text)
	}
}

func (m Model) renderBlockTool(b block, width int) string {
	line := m.theme.Border.Render("  ├ ") + b.toolLine(m.theme)
	if !b.expanded {
		return line
	}
	var sb strings.Builder
	sb.WriteString(line)
	if b.tool != nil {
		if b.tool.Arguments != "" {
			sb.WriteString("\n")
			sb.WriteString(indent(m.theme.Dim.Render("args "+oneLine(b.tool.Arguments, 160)), "  │ "))
		}
		if out := strings.TrimSpace(b.tool.Output); out != "" {
			sb.WriteString("\n")
			sb.WriteString(indent(m.theme.CodeBlock.Render(truncate(out, 1200)), "  │ "))
		}
		if b.tool.Error != "" {
			sb.WriteString("\n")
			sb.WriteString(indent(m.theme.Error.Render(b.tool.Error), "  │ "))
		}
	}
	return sb.String()
}

func (m Model) welcome() string {
	th := m.theme
	var sb strings.Builder
	sb.WriteString(th.Accent.Render(banner))
	sb.WriteString("\n\n")
	sb.WriteString(th.Bold.Render("  a local coding agent") + "\n")
	sb.WriteString(th.Dim.Render("  workspace  "+m.info.Workspace) + "\n")
	sb.WriteString(th.Dim.Render("  model      "+m.info.Model) + "\n")
	sb.WriteString(th.Dim.Render("  session    "+m.info.SessionID) + "\n\n")
	sb.WriteString(th.Dim.Render("  Type a task to begin. :help lists commands. Esc cancels a run, Ctrl+C quits."))
	return sb.String()
}

const banner = `
╲ ╱   V E L O
 ╲╱   agent runtime
`

func (m Model) renderStatus() string {
	th := m.theme
	width := m.width
	if width <= 0 {
		width = 80
	}
	var status string
	switch {
	case m.running:
		status = th.Warning.Render(m.spin.View() + " working")
	case m.pending != nil:
		status = th.Warning.Render("awaiting approval")
	default:
		status = th.Success.Render("● idle")

	}
	turn := th.Dim.Render(fmt.Sprintf("turn %d", m.turn))
	tokens := th.Dim.Render("tokens —")
	if m.usage != nil {
		tokens = th.Dim.Render(fmt.Sprintf("tokens %d↑ %d↓", m.usage.InputTokens, m.usage.OutputTokens))
	}
	session := th.Dim.Render("⌅ " + shortID(m.info.SessionID))
	model := th.Accent.Render(m.info.Model)

	segments := []string{status, model, turn, tokens, session}
	inner := width - 2
	used := 0
	for _, s := range segments {
		used += lipgloss.Width(s)
	}
	gap := " · "
	if len(segments) > 1 {
		used += len(gap) * (len(segments) - 1)
	}
	left := segments[0]
	right := ""
	if len(segments) > 1 {
		right = strings.Join(segments[1:], gap)
	}
	space := inner - lipgloss.Width(left) - lipgloss.Width(right)
	if space < 1 {
		space = 1
	}
	return th.StatusBar.Width(width).Render(left + strings.Repeat(" ", space) + right)
}

func (m Model) renderNotice() string {
	if m.notice != "" {
		return m.theme.Warning.Render("  " + m.notice)
	}
	if m.running {
		return m.theme.Dim.Render("  esc cancels the run")
	}
	return m.theme.Dim.Render("  enter sends · :help for commands")
}

// View implements tea.Model.
func (m Model) View() string {
	if !m.ready {
		return "starting velo…"
	}
	transcript := m.vp.View()
	switch {
	case m.pending != nil:
		transcript = m.renderApprovalModal()
	case m.showPicker:
		transcript = m.renderPicker()
	case m.showHelp:
		transcript = m.renderHelp()
	}

	parts := []string{
		m.renderStatus(),
		transcript,
		m.theme.Border.Render(strings.Repeat("─", m.width)),
		m.input.View(),
		m.renderNotice(),
	}
	return strings.Join(parts, "\n")
}

func (m Model) renderApprovalModal() string {
	th := m.theme
	t := m.pending
	var sb strings.Builder
	sb.WriteString(th.Warning.Render("⚠ approval required") + "  " + th.ToolLabel.Render(t.Name))
	if t.Destructive {
		sb.WriteString("  " + th.Error.Render("[DESTRUCTIVE]"))
	}
	sb.WriteString("\n")
	sb.WriteString(th.Dim.Render("  category: "+t.Category) + "\n\n")
	sb.WriteString(indent(argsPreview(t), "  "))
	sb.WriteString("\n\n")
	sb.WriteString("  " + th.Success.Render("[y]") + th.Base.Render(" approve once   ") +
		th.Error.Render("[n]") + th.Base.Render(" deny   ") +
		th.Accent.Render("[t]") + th.Base.Render(" trust for this session"))
	box := th.ModalBorder.Width(min(m.width-4, 96)).Padding(1, 2).Render(sb.String())
	return lipgloss.PlaceHorizontal(m.width, lipgloss.Left, box)
}

func (m Model) renderPicker() string {
	th := m.theme
	var sb strings.Builder
	sb.WriteString(th.Bold.Render("  sessions") + th.Dim.Render("  (↑↓ move · enter resume · esc close)") + "\n\n")
	if len(m.picker) == 0 {
		sb.WriteString(th.Dim.Render("  no saved sessions"))
		return sb.String()
	}
	for i, row := range m.picker {
		cursor := "  "
		style := th.Base
		if i == m.pickerIdx {
			cursor = "▸ "
			style = th.Accent
		}
		name := truncate(row.Objective, 40)
		if name == "" {
			name = "(no objective)"
		}
		marker := ""
		if row.Current {
			marker = th.Success.Render(" ●")
		}
		sb.WriteString(style.Render(fmt.Sprintf("%s%-10s %-10s %2d turns %2d msgs %s", cursor, shortID(row.ID), row.Status, row.Turns, row.Messages, name)) + marker + "\n")
		sb.WriteString(th.Dim.Render(fmt.Sprintf("    updated %s", row.Updated)) + "\n")
	}
	return sb.String()
}

func (m Model) renderHelp() string {
	th := m.theme
	var sb strings.Builder
	sb.WriteString(th.Bold.Render("  keys") + "\n\n")
	for _, k := range [][2]string{
		{"enter", "send message / confirm"},
		{"ctrl+j", "insert newline"},
		{"esc", "cancel running task / close overlay"},
		{"ctrl+c", "quit"},
		{"ctrl+o", "expand/collapse last tool block"},
		{"pgup / pgdown", "scroll transcript"},
		{"home / end", "top / bottom of transcript"},
		{"?", "toggle this help"},
	} {
		sb.WriteString(fmt.Sprintf("  %s  %s\n", th.Accent.Render(padded(k[0], 18)), th.Base.Render(k[1])))
	}
	sb.WriteString("\n" + th.Bold.Render("  commands") + "\n\n")
	for _, k := range [][2]string{
		{":sessions", "browse and resume saved sessions"},
		{":new", "start a fresh session"},
		{":help", "this screen"},
		{":quit", "exit velo"},
	} {
		sb.WriteString(fmt.Sprintf("  %s  %s\n", th.Accent.Render(padded(k[0], 18)), th.Base.Render(k[1])))
	}
	return sb.String()
}

func padded(s string, n int) string {
	if len(s) >= n {
		return s + " "
	}
	return s + strings.Repeat(" ", n-len(s))
}

func indent(text, prefix string) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
}

func oneLine(text string, max int) string {
	text = strings.Join(strings.Fields(text), " ")
	return truncate(text, max)
}
