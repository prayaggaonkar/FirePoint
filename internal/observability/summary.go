package observability

import (
	"fmt"
	"sort"

	dto "github.com/prometheus/client_model/go"
)

// Row is one labeled value on the status page.
// Labels are model names and status codes, never prompts or keys.
type Row struct {
	Label string
	Value float64
}

// Summary is a point-in-time reading of this process's series.
// Tenant names are summed into RateLimited and are not listed, so a hash
// of an Authorization header never appears on the public page.
type Summary struct {
	ActiveStreams float64
	Requests      []Row
	Upstream      []Row
	Breakers      []Row
	RateLimited   float64
	IPLimited     float64
	TTFTCount     uint64
	TTFTSum       float64
}

// MeanTTFT is the average time to first byte, or a note when nothing has been served.
func (s Summary) MeanTTFT() string {
	if s.TTFTCount == 0 {
		return "no samples yet"
	}
	ms := s.TTFTSum / float64(s.TTFTCount) * 1000
	return fmt.Sprintf("%.1f ms over %d samples", ms, s.TTFTCount)
}

// Summary reads the private registry. It does not include tenant labels.
func (m *Metrics) Summary() Summary {
	var s Summary
	if m == nil {
		return s
	}
	families, err := m.reg.Gather()
	if err != nil {
		return s
	}
	for _, f := range families {
		switch f.GetName() {
		case "firepoint_active_streams":
			s.ActiveStreams = gaugeValue(f)
		case "firepoint_requests_total":
			s.Requests = labeled(f, "model")
		case "firepoint_upstream_status_total":
			s.Upstream = labeled(f, "status")
		case "firepoint_breaker_state":
			s.Breakers = labeled(f, "model")
		case "firepoint_ratelimit_rejections_total":
			s.RateLimited = sumCounters(f)
		case "firepoint_ip_rate_limit_rejections_total":
			s.IPLimited = sumCounters(f)
		case "firepoint_ttft_seconds":
			if len(f.Metric) > 0 && f.Metric[0].Histogram != nil {
				h := f.Metric[0].Histogram
				s.TTFTCount = h.GetSampleCount()
				s.TTFTSum = h.GetSampleSum()
			}
		}
	}
	return s
}

func gaugeValue(f *dto.MetricFamily) float64 {
	if len(f.Metric) == 0 || f.Metric[0].Gauge == nil {
		return 0
	}
	return f.Metric[0].Gauge.GetValue()
}

func sumCounters(f *dto.MetricFamily) float64 {
	var n float64
	for _, metric := range f.Metric {
		if metric.Counter != nil {
			n += metric.Counter.GetValue()
		}
	}
	return n
}

func labeled(f *dto.MetricFamily, label string) []Row {
	rows := make([]Row, 0, len(f.Metric))
	for _, metric := range f.Metric {
		name := labelValue(metric, label)
		if name == "" {
			continue
		}
		var v float64
		switch {
		case metric.Counter != nil:
			v = metric.Counter.GetValue()
		case metric.Gauge != nil:
			v = metric.Gauge.GetValue()
		default:
			continue
		}
		rows = append(rows, Row{Label: name, Value: v})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Label < rows[j].Label })
	return rows
}

func labelValue(metric *dto.Metric, name string) string {
	for _, label := range metric.Label {
		if label.GetName() == name {
			return label.GetValue()
		}
	}
	return ""
}
