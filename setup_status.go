package main

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/term"
)

const wizardStatusInset = 2

// status keeps piped output stable while giving terminal users a compact,
// readable checkpoint between wizard steps.
func (p *setupInput) status(title, body string) {
	if !p.interactive() {
		fmt.Fprint(p.out, body)
		return
	}
	width := wizardStatusWidth(p)
	rail := lipgloss.NewStyle().Foreground(lipgloss.Color("#7571F9")).Render("│")
	heading := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#7571F9")).Render(title)
	fmt.Fprintln(p.out)
	for _, line := range strings.Split(lipgloss.Wrap(heading, width-wizardStatusInset, " "), "\n") {
		fmt.Fprintf(p.out, "%s %s\n", rail, line)
	}
	for _, line := range strings.Split(lipgloss.Wrap(strings.TrimSpace(body), width-wizardStatusInset, " /\\?&=._"), "\n") {
		fmt.Fprintf(p.out, "%s %s\n", rail, line)
	}
}

func wizardStatusWidth(p *setupInput) int {
	width := 72
	if columns, _, err := term.GetSize(p.terminal.Fd()); err == nil && columns > 0 {
		width = min(width, max(wizardStatusInset+1, columns-1))
	}
	return width
}

func (p *setupInput) cancelled() {
	p.status("Setup cancelled", "Cancelled. Your tool settings have not changed.\n")
}
