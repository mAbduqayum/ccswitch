package tui

import (
	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/lipgloss"
)

var (
	titleStyle = lipgloss.NewStyle().Bold(true).
			Foreground(lipgloss.AdaptiveColor{Light: "#5A56E0", Dark: "#8B87F7"})
	statusStyle = lipgloss.NewStyle().
			Foreground(lipgloss.AdaptiveColor{Light: "#1A7F37", Dark: "#57D364"})
	dialogStyle = lipgloss.NewStyle().Bold(true).
			Foreground(lipgloss.AdaptiveColor{Light: "#9A6700", Dark: "#E3B341"})
	dimStyle = lipgloss.NewStyle().
			Foreground(lipgloss.AdaptiveColor{Light: "#6E7781", Dark: "#8B949E"})
)

// tableHeaderHeight is what the styled header occupies: the title line plus
// its bottom border.
const tableHeaderHeight = 2

func tableStyles() table.Styles {
	s := table.DefaultStyles()
	s.Header = s.Header.Bold(true).
		Foreground(lipgloss.AdaptiveColor{Light: "#24292F", Dark: "#C9D1D9"}).
		BorderStyle(lipgloss.NormalBorder()).
		BorderForeground(lipgloss.AdaptiveColor{Light: "#D0D7DE", Dark: "#30363D"}).
		BorderBottom(true)
	s.Selected = s.Selected.Bold(true).
		Foreground(lipgloss.AdaptiveColor{Light: "#0969DA", Dark: "#79C0FF"}).
		Background(lipgloss.AdaptiveColor{Light: "#DDF4FF", Dark: "#0D419D"})
	return s
}

// columns sizes the table for the given terminal width; the account column
// absorbs the slack.
func columns(width int) []table.Column {
	account := 28
	if width > 66 {
		account = width - 38
	}
	if account > 44 {
		account = 44
	}
	return []table.Column{
		{Title: "", Width: 2},
		{Title: "#", Width: 3},
		{Title: "ACCOUNT", Width: account},
		{Title: "ALIAS", Width: 10},
		{Title: "PLAN", Width: 7},
		{Title: "TOKEN", Width: 10},
	}
}
