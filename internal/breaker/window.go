package breaker

import (
	"sync"
	"time"
)

// Window counts successes and failures in one-second buckets. Buckets older
// than the span are ignored, so a recovered upstream is not judged by errors
// that have already aged out. There is no timer: callers pass the time.
type Window struct {
	mu    sync.Mutex
	slots []slot
}

type slot struct {
	sec     int64
	success int
	failure int
}

// NewWindow builds a ring of one bucket per second in span. A 30s span keeps
// 30 buckets.
func NewWindow(span time.Duration) *Window {
	n := int(span / time.Second)
	if n < 1 {
		n = 1
	}
	return &Window{slots: make([]slot, n)}
}

// Add records one outcome in the bucket for now's second.
func (w *Window) Add(success bool, now time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	s := w.bucket(now.Unix())
	if success {
		s.success++
		return
	}
	s.failure++
}

// Total is the number of outcomes still inside the window.
func (w *Window) Total(now time.Time) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	total, _ := w.counts(now.Unix())
	return total
}

// ErrorRate is failures divided by Total. An empty window is rate 0, not a
// failure, so a quiet upstream does not trip the breaker.
func (w *Window) ErrorRate(now time.Time) float64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	total, failures := w.counts(now.Unix())
	if total == 0 {
		return 0
	}
	return float64(failures) / float64(total)
}

// Reset drops every bucket. The breaker does this when probes show the
// upstream has recovered, so the old failures cannot reopen it immediately.
func (w *Window) Reset() {
	w.mu.Lock()
	defer w.mu.Unlock()
	for i := range w.slots {
		w.slots[i] = slot{}
	}
}

func (w *Window) bucket(sec int64) *slot {
	s := &w.slots[w.index(sec)]
	if s.sec != sec {
		*s = slot{sec: sec}
	}
	return s
}

func (w *Window) counts(nowSec int64) (total, failures int) {
	oldest := nowSec - int64(len(w.slots)) + 1
	for i := range w.slots {
		s := &w.slots[i]
		if s.sec < oldest || s.sec > nowSec {
			continue
		}
		total += s.success + s.failure
		failures += s.failure
	}
	return total, failures
}

func (w *Window) index(sec int64) int {
	n := int64(len(w.slots))
	i := sec % n
	if i < 0 {
		i += n
	}
	return int(i)
}
