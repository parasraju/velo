package ui

import (
	"strings"

	"github.com/charmbracelet/glamour"
)

// standardStyle maps the UI theme to a glamour built-in style name.
func standardStyle(theme string) string {
	switch theme {
	case "light":
		return "light"
	case "notty":
		return "ascii"
	default:
		return "dark"
	}
}

// renderMarkdown converts finalized assistant text to styled terminal output.
// It degrades to the raw string (with light manual emphasis handling) if the
// renderer cannot be constructed, so a markdown glitch can never blank the
// transcript.
func renderMarkdown(theme, text string, width int) string {
	if strings.TrimSpace(text) == "" {
		return text
	}
	wrap := width - 2
	if wrap < 32 {
		wrap = 32
	}
	r, err := glamour.NewTermRenderer(
		glamour.WithStandardStyle(standardStyle(theme)),
		glamour.WithWordWrap(wrap),
	)
	if err != nil {
		return text
	}
	out, err := r.Render(text)
	if err != nil {
		return text
	}
	return strings.TrimRight(out, "\n")
}
