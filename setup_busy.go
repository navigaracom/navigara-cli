package main

import (
	"fmt"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"
)

// busy gives the terminal a small, display-only indication while a setup
// operation runs synchronously. It does not own cancellation or input.
func (p *setupInput) busy(title string, action func() error) error {
	if !p.interactive() {
		fmt.Fprintln(p.out, title)
		return action()
	}

	u := setupBusy{p: p, title: title, stopCh: make(chan struct{}), stopped: make(chan struct{})}
	u.render(0, false)
	go u.animate()
	defer u.stop()
	return action()
}

type setupBusy struct {
	p               *setupInput
	title           string
	stopCh, stopped chan struct{}
}

func (u *setupBusy) animate() {
	tick := time.NewTicker(120 * time.Millisecond)
	defer tick.Stop()
	defer close(u.stopped)
	frame := 0
	for {
		select {
		case <-u.stopCh:
			return
		case <-tick.C:
			frame++
			u.render(frame, true)
		}
	}
}

func (u *setupBusy) stop() {
	close(u.stopCh)
	<-u.stopped
	fmt.Fprint(u.p.out, "\r\033[2K\n")
}

func (u *setupBusy) render(frame int, replace bool) {
	line := lipgloss.NewStyle().Foreground(lipgloss.Color("#7571F9")).Render(
		[]string{"|", "/", "-", "\\"}[frame%4] + " " + u.title,
	)
	if columns, _, err := term.GetSize(u.p.terminal.Fd()); err == nil && columns > 1 {
		line = ansi.Truncate(line, columns-1, "")
	}
	if replace {
		fmt.Fprint(u.p.out, "\r\033[2K")
	}
	fmt.Fprint(u.p.out, line)
}
