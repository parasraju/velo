package ui

import "github.com/charmbracelet/lipgloss"

// Theme holds the palette and shared styles for a rendering pass. The
// notty theme disables color so output stays readable through pipes and
// assistive terminals.
type Theme struct {
	Name string

	Base        lipgloss.Style
	Dim         lipgloss.Style
	Bold        lipgloss.Style
	Error       lipgloss.Style
	Success     lipgloss.Style
	Warning     lipgloss.Style
	Accent      lipgloss.Style
	Prompt      lipgloss.Style
	Border      lipgloss.Style
	StatusBar   lipgloss.Style
	ModalBorder lipgloss.Style

	UserLabel      lipgloss.Style
	AssistantLabel lipgloss.Style
	ToolLabel      lipgloss.Style
	SystemLabel    lipgloss.Style

	CodeBlock lipgloss.Style
	Rule      lipgloss.Style
}

// NewTheme builds a theme from a name: dark, light, or notty.
func NewTheme(name string) Theme {
	switch name {
	case "light":
		return lightTheme()
	case "notty":
		return nottyTheme()
	default:
		return darkTheme()
	}
}

func darkTheme() Theme {
	return Theme{
		Name:           "dark",
		Base:           lipgloss.NewStyle(),
		Dim:            lipgloss.NewStyle().Foreground(lipgloss.Color("243")),
		Bold:           lipgloss.NewStyle().Bold(true),
		Error:          lipgloss.NewStyle().Foreground(lipgloss.Color("197")),
		Success:        lipgloss.NewStyle().Foreground(lipgloss.Color("78")),
		Warning:        lipgloss.NewStyle().Foreground(lipgloss.Color("214")),
		Accent:         lipgloss.NewStyle().Foreground(lipgloss.Color("80")),
		Prompt:         lipgloss.NewStyle().Foreground(lipgloss.Color("80")).Bold(true),
		Border:         lipgloss.NewStyle().Foreground(lipgloss.Color("240")),
		StatusBar:      lipgloss.NewStyle().Foreground(lipgloss.Color("252")).Background(lipgloss.Color("236")).Padding(0, 1),
		ModalBorder:    lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("214")),
		UserLabel:      lipgloss.NewStyle().Foreground(lipgloss.Color("141")).Bold(true),
		AssistantLabel: lipgloss.NewStyle().Foreground(lipgloss.Color("80")).Bold(true),
		ToolLabel:      lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Bold(true),
		SystemLabel:    lipgloss.NewStyle().Foreground(lipgloss.Color("243")).Italic(true),
		CodeBlock:      lipgloss.NewStyle().Foreground(lipgloss.Color("252")).Background(lipgloss.Color("235")).Padding(0, 1),
		Rule:           lipgloss.NewStyle().Foreground(lipgloss.Color("238")),
	}
}

func lightTheme() Theme {
	return Theme{
		Name:           "light",
		Base:           lipgloss.NewStyle(),
		Dim:            lipgloss.NewStyle().Foreground(lipgloss.Color("246")),
		Bold:           lipgloss.NewStyle().Bold(true),
		Error:          lipgloss.NewStyle().Foreground(lipgloss.Color("160")),
		Success:        lipgloss.NewStyle().Foreground(lipgloss.Color("28")),
		Warning:        lipgloss.NewStyle().Foreground(lipgloss.Color("130")),
		Accent:         lipgloss.NewStyle().Foreground(lipgloss.Color("26")),
		Prompt:         lipgloss.NewStyle().Foreground(lipgloss.Color("26")).Bold(true),
		Border:         lipgloss.NewStyle().Foreground(lipgloss.Color("250")),
		StatusBar:      lipgloss.NewStyle().Foreground(lipgloss.Color("234")).Background(lipgloss.Color("252")).Padding(0, 1),
		ModalBorder:    lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("130")),
		UserLabel:      lipgloss.NewStyle().Foreground(lipgloss.Color("91")).Bold(true),
		AssistantLabel: lipgloss.NewStyle().Foreground(lipgloss.Color("26")).Bold(true),
		ToolLabel:      lipgloss.NewStyle().Foreground(lipgloss.Color("130")).Bold(true),
		SystemLabel:    lipgloss.NewStyle().Foreground(lipgloss.Color("246")).Italic(true),
		CodeBlock:      lipgloss.NewStyle().Foreground(lipgloss.Color("234")).Background(lipgloss.Color("254")).Padding(0, 1),
		Rule:           lipgloss.NewStyle().Foreground(lipgloss.Color("252")),
	}
}

func nottyTheme() Theme {
	// No color: labels are conveyed with text and plain emphasis only.
	plain := lipgloss.NewStyle()
	return Theme{
		Name:           "notty",
		Base:           plain,
		Dim:            plain,
		Bold:           plain,
		Error:          plain,
		Success:        plain,
		Warning:        plain,
		Accent:         plain,
		Prompt:         plain,
		Border:         plain,
		StatusBar:      plain.Padding(0, 1),
		ModalBorder:    plain.Border(lipgloss.NormalBorder()),
		UserLabel:      plain,
		AssistantLabel: plain,
		ToolLabel:      plain,
		SystemLabel:    plain,
		CodeBlock:      plain,
		Rule:           plain,
	}
}
