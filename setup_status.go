package main

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

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

func wizardPathValue(value string) string {
	var escaped strings.Builder
	escaped.Grow(len(value))
	for len(value) > 0 {
		r, size := utf8.DecodeRuneInString(value)
		if r == utf8.RuneError && size == 1 {
			fmt.Fprintf(&escaped, `\x%02X`, value[0])
		} else if strconv.IsGraphic(r) {
			escaped.WriteString(value[:size])
		} else {
			quoted := strconv.QuoteRune(r)
			escaped.WriteString(quoted[1 : len(quoted)-1])
		}
		value = value[size:]
	}
	return escaped.String()
}

func (p *setupInput) cancelled() {
	p.status("Setup cancelled", "Cancelled. Your tool settings have not changed.\n")
}

func (p *setupInput) completed(sent int) {
	p.completedForMode(sent, p.interactive())
}

func (p *setupInput) completedForMode(sent int, interactive bool) {
	if !interactive {
		fmt.Fprintf(p.out, "\nConnected. Navigara accepted %d upload batches.\nNew activity syncing is configured, not yet verified.\nOpen a new terminal and restart your tools. For Codex desktop/IDE, launch from that terminal so contributor identity is inherited. Check Navigara after your next session.\n", sent)
		return
	}

	blue := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#00A8FF"))
	p.status(
		blue.Render("🎉 You’re connected to Navigara"),
		blue.Render(fmt.Sprintf("History: %d upload batches received", sent))+"\n"+
			"Live sync: Configured; not yet verified\n\n"+
			"Restart your tools in a new terminal. For Codex desktop/IDE, launch from that terminal so contributor identity is inherited. Check Navigara after your next session.\n",
	)
}
