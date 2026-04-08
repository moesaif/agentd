package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

type KeyValue struct {
	Label string
	Value string
}

var (
	cardBorder = lipgloss.RoundedBorder()

	cardStyle = lipgloss.NewStyle().
			Border(cardBorder).
			BorderForeground(lipgloss.Color("63")).
			Padding(1, 2)

	cardTitleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("212"))

	labelStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("247"))

	valueStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("255"))

	mutedStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("241"))

	codeStyle = lipgloss.NewStyle().
			Border(cardBorder).
			BorderForeground(lipgloss.Color("240")).
			Padding(0, 1)
)

func RenderKeyValueCard(title string, rows []KeyValue) string {
	labelWidth := 0
	for _, row := range rows {
		if w := lipgloss.Width(row.Label); w > labelWidth {
			labelWidth = w
		}
	}

	lines := []string{cardTitleStyle.Render(title), ""}
	for _, row := range rows {
		lines = append(lines,
			labelStyle.Width(labelWidth).Render(row.Label)+"  "+valueStyle.Render(row.Value),
		)
	}

	return cardStyle.Render(strings.Join(lines, "\n"))
}

func RenderListCard(title string, lines []string) string {
	if len(lines) == 0 {
		lines = []string{mutedStyle.Render("(none)")}
	}

	body := []string{cardTitleStyle.Render(title), ""}
	body = append(body, lines...)
	return cardStyle.Render(strings.Join(body, "\n"))
}

func RenderCodeCard(title, content string) string {
	body := []string{
		cardTitleStyle.Render(title),
		"",
		codeStyle.Render(content),
	}
	return cardStyle.Render(strings.Join(body, "\n"))
}

func RenderStack(cards ...string) string {
	return strings.Join(cards, "\n\n")
}

func Muted(text string) string {
	return mutedStyle.Render(text)
}
