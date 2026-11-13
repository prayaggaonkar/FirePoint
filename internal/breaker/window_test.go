package breaker

import (
	"testing"
	"time"
)

func TestWindowCountsAndDropsExpiredSeconds(t *testing.T) {
	start := time.Unix(1_700_000_000, 0)
	w := NewWindow(30 * time.Second)

	w.Add(false, start)
	w.Add(false, start)
	w.Add(true, start.Add(29*time.Second))

	if got := w.Total(start.Add(29 * time.Second)); got != 3 {
		t.Fatalf("total at 29s = %d, want 3", got)
	}
	if got := w.ErrorRate(start.Add(29 * time.Second)); got != 2.0/3.0 {
		t.Fatalf("error rate = %v, want 2/3", got)
	}

	// Second 0 falls out of a 30s window once the clock reaches second 30.
	if got := w.Total(start.Add(30 * time.Second)); got != 1 {
		t.Fatalf("total at 30s = %d, want 1", got)
	}
	if got := w.ErrorRate(start.Add(30 * time.Second)); got != 0 {
		t.Fatalf("error rate at 30s = %v, want 0", got)
	}
	if got := w.ErrorRate(start.Add(time.Minute)); got != 0 {
		t.Fatalf("empty window error rate = %v, want 0", got)
	}
}

func TestWindowReset(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	w := NewWindow(30 * time.Second)
	w.Add(false, now)
	w.Reset()
	if got := w.Total(now); got != 0 {
		t.Fatalf("total after reset = %d", got)
	}
}
