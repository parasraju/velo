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

func (m *Model) renderTranscript() string {
	if len(m.blocks) == 0 {
		return m.welcome()
	}
	var sb strings.Builder
	width := m.contentWidth()
	for i := range m.blocks {
		if i > 0 {
			sb.WriteString("\n\n")
		}
		b := &m.blocks[i]
		if b.view != "" && b.viewWidth == width && !blockLive(b) {
			sb.WriteString(b.view)
			continue
		}
		body := m.renderBlock(*b, width)
		if !blockLive(b) {
			b.view = body
			b.viewWidth = width
		}
		sb.WriteString(body)
	}
	return strings.TrimRight(sb.String(), "\n")
}

// blockLive reports blocks whose appearance can change on the next event.
// Finalized history is cached instead of restyled on every render.
func blockLive(b *block) bool {
	return b.streaming || b.kind == blockTool
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
		return m.theme.SystemLabel.Render(indent(b.text, "  "))
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
	art := banner
	if th.Name == "notty" {
		art = bannerASCII
	}
	sb.WriteString(th.Accent.Render(art))
	sb.WriteString("\n\n")
	sb.WriteString(th.Bold.Render("  a local coding agent") + "\n")
	sb.WriteString(th.Dim.Render("  workspace  "+m.info.Workspace) + "\n")
	sb.WriteString(th.Dim.Render("  model      "+m.info.Model) + "\n")
	sb.WriteString(th.Dim.Render("  session    "+m.info.SessionID) + "\n\n")
	sb.WriteString(th.Dim.Render("  Type a task to begin. / opens commands. Esc cancels a run, Ctrl+C quits."))
	if !m.info.Ready && m.info.Provider != "" {
		sb.WriteString("\n")
		sb.WriteString(th.Warning.Render("  No provider connected. / then providers. Gemini asks for an API key."))
	}
	return sb.String()
}

const banner = `
╲ ╱   V E L O
 ╲╱   agent runtime
`

const bannerASCII = `
\ /   V E L O
 \/   agent runtime
`

func (m Model) renderStatus() string {
	th := m.theme
	width := m.width
	if width <= 0 {
		width = 80
	}
	ascii := th.Name == "notty"
	var status string
	switch {
	case m.running:
		status = th.Warning.Render(strings.TrimSpace(m.spin.View() + " working"))
	case m.pending != nil:
		status = th.Warning.Render("awaiting approval")
	default:
		if ascii {
			status = th.Success.Render("idle")
		} else {
			status = th.Success.Render("● idle")
		}
	}
	if width < 16 {
		plain := "idle"
		if m.running {
			plain = "working"
		}
		if m.pending != nil {
			plain = "approval"
		}
		return th.StatusBar.MaxWidth(width).Render(fitWidth(plain, width))
	}
	turn := th.Dim.Render(fmt.Sprintf("turn %d", m.turn))
	tokenLabel := "tokens -"
	if !ascii {
		tokenLabel = "tokens —"
	}
	tokens := th.Dim.Render(tokenLabel)
	if m.usage != nil {
		up, down := "^", "v"
		if !ascii {
			up, down = "↑", "↓"
		}
		tokens = th.Dim.Render(fmt.Sprintf("tokens %d%s %d%s", m.usage.InputTokens, up, m.usage.OutputTokens, down))
	}
	sessionMark := "id "
	if !ascii {
		sessionMark = "⌅ "
	}
	session := th.Dim.Render(sessionMark + shortID(m.info.SessionID))
	model := th.Accent.Render(m.info.Model)

	gap := " | "
	if !ascii {
		gap = " · "
	}
	segments := []string{status, model, turn, tokens, session}
	for len(segments) > 1 && statusWidth(segments, gap) > width-2 {
		segments = segments[:len(segments)-1]
	}
	left := segments[0]
	right := ""
	if len(segments) > 1 {
		right = strings.Join(segments[1:], gap)
	}
	space := width - 2 - lipgloss.Width(left) - lipgloss.Width(right)
	if space < 1 {
		space = 1
	}
	line := left + strings.Repeat(" ", space) + right
	return th.StatusBar.MaxWidth(width).Render(line)
}

func statusWidth(segments []string, gap string) int {
	used := 0
	for _, s := range segments {
		used += lipgloss.Width(s)
	}
	if len(segments) > 1 {
		used += lipgloss.Width(gap) * (len(segments) - 1)
	}
	return used
}

func fitWidth(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= width {
		return s
	}
	runes := []rune(s)
	for len(runes) > 0 && lipgloss.Width(string(runes)) > width {
		runes = runes[:len(runes)-1]
	}
	return string(runes)
}

func (m Model) renderNotice() string {
	if m.notice != "" {
		return m.theme.Warning.Render("  " + m.notice)
	}
	if m.running {
		return m.theme.Dim.Render("  esc cancels the run")
	}
	return m.theme.Dim.Render("  enter sends · / chooses a command")
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
	case m.showCommands:
		transcript = m.renderCommands()
	case m.showProviders:
		transcript = m.renderProviders()
	case m.showModels:
		transcript = m.renderModels()
	case m.showPicker:
		transcript = m.renderPicker()
	case m.showHelp:
		transcript = m.renderHelp()
	}

	rule := "─"
	if m.theme.Name == "notty" {
		rule = "-"
	}
	ruleWidth := m.width
	if ruleWidth < 1 {
		ruleWidth = 1
	}
	parts := []string{
		m.renderStatus(),
		transcript,
		m.theme.Border.Render(strings.Repeat(rule, ruleWidth)),
		m.input.View(),
		m.renderNotice(),
	}
	return strings.Join(parts, "\n")
}

func (m Model) renderApprovalModal() string {
	th := m.theme
	t := m.pending
	var sb strings.Builder
	warn := "⚠ approval required"
	if th.Name == "notty" {
		warn = "! approval required"
	}
	sb.WriteString(th.Warning.Render(warn) + "  " + th.ToolLabel.Render(t.Name))
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

func (m Model) renderCommands() string {
	th := m.theme
	var sb strings.Builder
	sb.WriteString(th.Bold.Render("  commands") + th.Dim.Render("  (↑↓ move · enter select · click a row · esc close)") + "\n\n")
	for i, item := range commandPalette {
		cursor := "  "
		style := th.Base
		if i == m.commandIdx {
			cursor = "> "
			style = th.Accent
		}
		sb.WriteString(style.Render(fmt.Sprintf("%s/%-12s %s", cursor, item.label, item.help)) + "\n")
	}
	return sb.String()
}

func (m Model) renderProviders() string {
	th := m.theme
	var sb strings.Builder
	sb.WriteString(th.Bold.Render("  providers") + th.Dim.Render("  (↑↓ move · enter or click · esc close)") + "\n\n")
	if len(m.providers) == 0 {
		sb.WriteString(th.Dim.Render("  no providers registered"))
		return sb.String()
	}
	for i, row := range m.providers {
		cursor := "  "
		style := th.Base
		if i == m.providerIdx {
			cursor = "> "
			style = th.Accent
		}
		state := ""
		switch {
		case row.Ready:
			state = "connected"
		case row.NeedsKey:
			state = "asks for an API key"
		}
		line := cursor + row.Name
		if state != "" {
			line += "  " + state
		}
		sb.WriteString(style.Render(line) + "\n")
	}
	return sb.String()
}

func (m Model) renderModels() string {
	th := m.theme
	var sb strings.Builder
	title := "  models"
	if m.info.Provider != "" {
		title += " for " + m.info.Provider
	}
	sb.WriteString(th.Bold.Render(title) + th.Dim.Render("  (↑↓ move · enter or click · esc close)") + "\n\n")
	if len(m.models) == 0 {
		sb.WriteString(th.Dim.Render("  select a provider first"))
		return sb.String()
	}
	for i, row := range m.models {
		cursor := "  "
		style := th.Base
		if i == m.modelIdx {
			cursor = "> "
			style = th.Accent
		}
		mark := ""
		if row.Current {
			mark = "  current"
		}
		sb.WriteString(style.Render(cursor+row.Name+mark) + "\n")
	}
	return sb.String()
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
	sb.WriteString(th.Dim.Render("  / opens this list. Select one; do not type the name.") + "\n\n")
	for _, item := range commandPalette {
		sb.WriteString(fmt.Sprintf("  %s  %s\n", th.Accent.Render(padded("/"+item.label, 18)), th.Base.Render(item.help)))
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
