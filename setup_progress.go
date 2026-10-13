package main

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"
)

// setupProgress reports only batches the receiver has accepted. Plain output is
// deliberately line-oriented so redirected output never contains terminal codes.
type setupProgress struct {
	p       *setupInput
	started bool
	eta     setupProgressETACache
}

func newSetupProgress(p *setupInput) *setupProgress {
	return &setupProgress{p: p}
}

func (u *setupProgress) upload(done, total int) {
	if total == 0 {
		u.eta.reset()
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
	} else {
		u.started = true
	}
	limit := 0
	if columns, _, err := term.GetSize(u.p.terminal.Fd()); err == nil && columns > 0 {
		limit = columns - 1 // Avoid a terminal's last-column autowrap.
	}
	fmt.Fprint(u.p.out, setupUploadLine(done, total, u.eta.update(time.Now(), done, total), limit))
	if done == total {
		fmt.Fprintln(u.p.out)
		u.started = false
		u.eta.reset()
	}
}

func (u *setupProgress) stop() {
	if u.started && u.p.interactive() {
		fmt.Fprintln(u.p.out)
	}
	u.started = false
	u.eta.reset()
}

func setupUploadLine(done, total int, eta string, limit int) string {
	countWidth := ansi.StringWidth(fmt.Sprintf("[] %d/%d", done, total))
	for _, label := range []string{"Uploading history  ", "Uploading  ", "Upload  ", ""} {
		available := 32
		if limit > 0 {
			available = limit - ansi.StringWidth(label) - countWidth
		}
		if available < 1 {
			continue
		}
		width := min(32, available)
		showETA := eta != ""
		if showETA && limit > 0 {
			width = min(width, available-ansi.StringWidth(etaPrefix(eta)))
			showETA = width >= 3
			if !showETA {
				width = min(32, available)
			}
		}
		line := label + setupProgressBar(done, total, width)
		if showETA {
			line += etaPrefix(eta)
		}
		return line
	}
	if limit <= 0 || ansi.StringWidth(fmt.Sprintf("%d/%d", done, total)) <= limit {
		return fmt.Sprintf("%d/%d", done, total)
	}
	return "…"
}

func setupProgressBar(done, total, width int) string {
	filled := min(width, done*width/total)
	complete := lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Render(strings.Repeat("█", filled))
	remaining := lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Render(strings.Repeat("░", width-filled))
	return fmt.Sprintf("[%s%s] %d/%d", complete, remaining, done, total)
}
