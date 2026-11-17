package observability

import (
	"strings"
	"testing"
	"time"
)

func TestSummaryOmitsTenantLabels(t *testing.T) {
	m := New()
	m.ObserveTTFT(100 * time.Millisecond)
	m.ObserveTTFT(300 * time.Millisecond)
	m.AddStreams(2)
	m.UpstreamStatus("200")
	m.UpstreamStatus("503")
	m.RateLimited("alice")
	m.RateLimited("alice")
	m.Served("fallback")
	m.Served("primary")
	m.SetBreakerState("primary", 1)
	m.IPLimited()

	s := m.Summary()
	if s.ActiveStreams != 2 {
		t.Fatalf("active = %v", s.ActiveStreams)
	}
	if s.RateLimited != 2 {
		t.Fatalf("rate limited = %v", s.RateLimited)
	}
	if s.IPLimited != 1 {
		t.Fatalf("ip limited = %v", s.IPLimited)
	}
	if s.TTFTCount != 2 {
		t.Fatalf("ttft count = %d", s.TTFTCount)
	}
	if got := s.MeanTTFT(); got != "200.0 ms over 2 samples" {
		t.Fatalf("mean = %q", got)
	}
	if len(s.Requests) != 2 || s.Requests[0].Label != "fallback" || s.Requests[1].Label != "primary" {
		t.Fatalf("requests = %+v", s.Requests)
	}
	if strings.Contains(s.MeanTTFT(), "alice") {
		t.Fatal("summary text included a tenant")
	}
	for _, row := range append(append([]Row{}, s.Requests...), append(s.Upstream, s.Breakers...)...) {
		if row.Label == "alice" {
			t.Fatalf("tenant label leaked: %+v", row)
		}
	}
}

func TestNilSummaryIsEmpty(t *testing.T) {
	var m *Metrics
	s := m.Summary()
	if s.MeanTTFT() != "no samples yet" {
		t.Fatalf("mean = %q", s.MeanTTFT())
	}
}
