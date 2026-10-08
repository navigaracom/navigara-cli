package main

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/term"
)

// setupProgress reports only batches the receiver has accepted. Plain output is
// deliberately line-oriented so redirected output never contains terminal codes.
type setupProgress struct {
	p       *setupInput
	started bool
}

func newSetupProgress(p *setupInput) *setupProgress {
	return &setupProgress{p: p}
}

func (u *setupProgress) upload(done, total int) {
	if total == 0 {
		if !u.started {
			fmt.Fprintln(u.p.out, "No session history to upload.")
			u.started = true
		}
		return
	}
	if !u.p.interactive() {
		fmt.Fprintf(u.p.out, "Uploaded %d of %d session batches.\n", done, total)
		return
	}
	if u.started {
		fmt.Fprint(u.p.out, "\r\033[2K")
	}
	const label = "Uploading history "
	width := 32
	if columns, _, err := term.GetSize(u.p.terminal.Fd()); err == nil && columns > 0 {
		width = max(1, columns-len(label)-len(fmt.Sprintf("[] %d/%d", done, total)))
	}
	fmt.Fprintf(u.p.out, "%s%s", label, setupProgressBar(done, total, width))
	u.started = true
	if done == total {
		fmt.Fprintln(u.p.out)
		u.started = false
	}
}

func (u *setupProgress) stop() {
	if u.started && u.p.interactive() {
		fmt.Fprintln(u.p.out)
	}
	u.started = false
}

func setupProgressBar(done, total, width int) string {
	filled := min(width, done*width/total)
	complete := lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Render(strings.Repeat("█", filled))
	remaining := lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Render(strings.Repeat("░", width-filled))
	return fmt.Sprintf("[%s%s] %d/%d", complete, remaining, done, total)
}
