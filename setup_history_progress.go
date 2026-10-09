package main

import (
	"fmt"
	"sync"
	"time"

	"github.com/charmbracelet/x/term"
)

// setupHistoryProgress keeps the wizard responsive while local history is scanned,
// prepared, and frozen for upload. It never controls the export itself.
type setupHistoryProgress struct {
	p       *setupInput
	mu      sync.Mutex
	phase   string
	done    int
	total   int
	frame   int
	active  bool
	last    time.Time
	stopCh  chan struct{}
	stopped chan struct{}
}

func newSetupHistoryProgress(p *setupInput) *setupHistoryProgress {
	return &setupHistoryProgress{p: p}
}

func (u *setupHistoryProgress) discovering() {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.active {
		return
	}
	u.phase, u.done, u.total, u.frame, u.active = "discovering", 0, 0, 0, true
	u.last = time.Now()
	u.renderLocked(false)
	if u.p.interactive() {
		u.stopCh, u.stopped = make(chan struct{}), make(chan struct{})
		go u.animate(u.stopCh, u.stopped)
	}
}

func (u *setupHistoryProgress) processed(done, total int) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if !u.active {
		return
	}
	u.phase, u.done, u.total = "preparing", done, total
	if u.p.interactive() || done == 0 || done == total || done == 1 || time.Since(u.last) >= time.Second {
		u.renderLocked(true)
	}
}

func (u *setupHistoryProgress) finalizing() {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.active {
		u.phase, u.frame = "finalizing", 0
		u.renderLocked(true)
	}
}

func (u *setupHistoryProgress) animate(stop <-chan struct{}, stopped chan<- struct{}) {
	tick := time.NewTicker(120 * time.Millisecond)
	defer tick.Stop()
	defer close(stopped)
	for {
		select {
		case <-stop:
			return
		case <-tick.C:
			u.mu.Lock()
			if u.active {
				u.frame++
				u.renderLocked(true)
			}
			u.mu.Unlock()
		}
	}
}

func (u *setupHistoryProgress) stop() {
	u.mu.Lock()
	if !u.active {
		u.mu.Unlock()
		return
	}
	u.active = false
	stop, stopped := u.stopCh, u.stopped
	u.stopCh, u.stopped = nil, nil
	if u.p.interactive() {
		fmt.Fprint(u.p.out, "\r\033[2K\n")
	}
	u.mu.Unlock()
	if stop != nil {
		close(stop)
		<-stopped
	}
}

func (u *setupHistoryProgress) renderLocked(replace bool) {
	if u.p.interactive() && replace {
		fmt.Fprint(u.p.out, "\r\033[2K")
	}
	fmt.Fprint(u.p.out, u.lineLocked())
	if !u.p.interactive() {
		fmt.Fprintln(u.p.out)
	}
	u.last = time.Now()
}

func (u *setupHistoryProgress) lineLocked() string {
	spinner := []string{"|", "/", "-", "\\"}[u.frame%4]
	switch u.phase {
	case "discovering":
		return u.fit("Finding session history "+spinner, "Finding history "+spinner, "Finding "+spinner, spinner)
	case "finalizing":
		return u.fit("Finalizing history "+spinner, "Finalizing "+spinner, spinner)
	}
	count := fmt.Sprintf("%d/%d sessions checked", u.done, u.total)
	if u.total == 0 {
		return u.fit("Preparing history "+spinner+" "+count, "Preparing "+spinner+" "+count, spinner)
	}
	if !u.p.interactive() {
		return "Preparing history " + spinner + " " + count
	}
	width := 32
	limit := u.width()
	if limit > 0 {
		width = min(width, limit-len("Preparing history "+spinner+" [] ")-len(count))
	}
	if width < 3 {
		return u.fit("Preparing history "+spinner+" "+count, "Preparing "+spinner+" "+fmt.Sprintf("%d/%d checked", u.done, u.total), spinner)
	}
	return fmt.Sprintf("Preparing history %s %s", spinner, setupProgressBar(u.done, u.total, width)+" sessions checked")
}

func (u *setupHistoryProgress) fit(lines ...string) string {
	limit := u.width()
	if limit == 0 {
		return lines[0]
	}
	for _, line := range lines {
		if len(line) <= limit {
			return line
		}
	}
	return lines[len(lines)-1]
}

func (u *setupHistoryProgress) width() int {
	if !u.p.interactive() {
		return 0
	}
	columns, _, err := term.GetSize(u.p.terminal.Fd())
	if err != nil || columns < 2 {
		return 0
	}
	return columns - 1 // Avoid a terminal's last-column autowrap.
}
