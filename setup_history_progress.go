package main

import (
	"fmt"
	"io"
	"os"
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
	eta     setupProgressETACache
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
	now := time.Now()
	u.phase, u.done, u.total, u.frame, u.active = "discovering", 0, 0, 0, true
	u.last = now
	u.eta.reset()
	if u.p.interactive() {
		fmt.Fprintln(u.p.out)
	}
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
	now := time.Now()
	if u.phase != "preparing" {
		u.eta.reset()
	}
	u.phase, u.done, u.total = "preparing", done, total
	u.eta.update(now, done, total)
	if u.p.interactive() || done == 0 || done == total || done == 1 || time.Since(u.last) >= time.Second {
		u.renderLocked(true)
	}
}

func (u *setupHistoryProgress) finalizing() {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.active {
		u.phase, u.frame = "finalizing", 0
		u.eta.reset()
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
	u.eta.reset()
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
	eta := u.eta.value
	width := 32
	limit := u.width()
	if limit > 0 {
		width = min(width, limit-len("Preparing history "+spinner+" [] ")-len(count)-len(etaPrefix(eta)))
		if width < 3 && eta != "" {
			eta = ""
			width = min(32, limit-len("Preparing history "+spinner+" [] ")-len(count))
		}
	}
	if width < 3 {
		return u.fit("Preparing history "+spinner+" "+count, "Preparing "+spinner+" "+fmt.Sprintf("%d/%d checked", u.done, u.total), spinner)
	}
	return fmt.Sprintf("Preparing history %s %s%s", spinner, setupProgressBar(u.done, u.total, width)+" sessions checked", etaPrefix(eta))
}

// setupProgressETACache keeps a recent rate sample for a single progress phase.
// Its value is updated by completed-work callbacks and read by render ticks.
type setupProgressETACache struct {
	at    time.Time
	done  int
	total int
	value string
}

func (e *setupProgressETACache) reset() {
	*e = setupProgressETACache{}
}

func (e *setupProgressETACache) update(now time.Time, done, total int) string {
	if total <= 0 || done >= total {
		e.reset()
		return ""
	}
	if e.total != total {
		e.reset()
	}
	if e.at.IsZero() {
		e.at, e.done, e.total = now, done, total
		return e.value
	}
	if done <= e.done || now.Sub(e.at) < 5*time.Second {
		return e.value
	}

	elapsed := now.Sub(e.at)
	delta := done - e.done
	e.at, e.done = now, done
	// Keep collecting baselines during warmup, but hide the estimate until
	// enough work has completed to make the first sample representative.
	warmup := (total + 9) / 10
	if warmup < 2 {
		warmup = 2
	}
	if done < warmup {
		e.value = ""
		return e.value
	}
	seconds := int(float64(total-done) * elapsed.Seconds() / float64(delta))
	if seconds < 1 {
		e.value = ""
		return e.value
	}
	if seconds < 60 {
		e.value = fmt.Sprintf("~%ds left", seconds)
	} else {
		e.value = fmt.Sprintf("~%dm %02ds left", seconds/60, seconds%60)
	}
	return e.value
}

func etaPrefix(eta string) string {
	if eta == "" {
		return ""
	}
	return "  " + eta
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

// uploadProgress shows the wizard's progress bars on a terminal. Otherwise it prints a short
// line at most every few seconds, so logs stay small.
type uploadProgress struct {
	out     io.Writer
	history *setupHistoryProgress
	upload  *setupProgress
	last    time.Time
}

func newUploadProgress(out io.Writer) *uploadProgress {
	u := &uploadProgress{out: out, last: time.Now()}
	if p := newSetupInput(os.Stdin, out); p.interactive() {
		u.history, u.upload = newSetupHistoryProgress(p), newSetupProgress(p)
	}
	return u
}

func (u *uploadProgress) prepared(done, total int) {
	if u.history == nil {
		u.line("Prepared %d/%d sessions", done, total)
		return
	}
	u.history.discovering() // Restarts the bar after other output stopped it.
	u.history.processed(done, total)
}

func (u *uploadProgress) sent(done, total int) {
	if u.upload == nil {
		u.line("Sent %d/%d batches", done, total)
		return
	}
	u.history.stop()
	u.upload.upload(done, total)
}

func (u *uploadProgress) line(format string, done, total int) {
	if time.Since(u.last) < 3*time.Second {
		return
	}
	u.last = time.Now()
	fmt.Fprintf(u.out, format+"\n", done, total)
}

func (u *uploadProgress) stop() {
	if u.history != nil {
		u.history.stop()
		u.upload.stop()
	}
}

// progressWriter ends a progress line before other output reaches the terminal.
type progressWriter struct {
	io.Writer
	stop func()
}

func (w progressWriter) Write(p []byte) (int, error) {
	w.stop()
	return w.Writer.Write(p)
}
