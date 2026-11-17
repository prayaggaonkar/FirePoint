package proxy

import (
	"html/template"
	"net/http"

	"github.com/prayaggaonkar/firerouter/internal/observability"
)

// StatusInfo is the public-page description of the limits this process is using.
type StatusInfo struct {
	IPPerMinute int
	MaxBody     int64
}

// StatusHandler renders a read-only page. Each GET samples the in-process
// metrics, so the numbers change when the page is loaded again. The page
// does not accept input and does not include an API key.
func StatusHandler(m *observability.Metrics, info StatusInfo) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		scheme := "http"
		if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
			scheme = "https"
		}
		host := r.Host
		if host == "" {
			host = "localhost"
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		data := statusPage{
			Base:        scheme + "://" + host,
			IPPerMinute: info.IPPerMinute,
			MaxBody:     info.MaxBody,
			Summary:     m.Summary(),
		}
		if err := statusTmpl.Execute(w, data); err != nil {
			// The header is already committed. There is nothing safe to add.
			return
		}
	})
}

type statusPage struct {
	Base        string
	IPPerMinute int
	MaxBody     int64
	Summary     observability.Summary
}

var statusTmpl = template.Must(template.New("status").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta http-equiv="refresh" content="5">
<title>FireRouter sandbox</title>
</head>
<body>
<h1>FireRouter sandbox</h1>
<p>This process talks only to the mock model running on the same machine. No Fireworks API key is configured. The mock is not reachable from the public internet.</p>
{{if .IPPerMinute}}<p>Each client address may send {{.IPPerMinute}} requests per minute, including loads of this page. Request bodies are capped at {{.MaxBody}} bytes. <code>GET /healthz</code> is not counted.</p>{{else}}<p>Per-address limits are off. Request bodies are capped at {{.MaxBody}} bytes.</p>{{end}}
<h2>Live metrics</h2>
<p>These numbers are from this process, sampled when the page was rendered.</p>
<ul>
<li>Active streams: {{printf "%.0f" .Summary.ActiveStreams}}</li>
<li>Time to first byte: {{.Summary.MeanTTFT}}</li>
<li>Per-tenant rate-limit rejections: {{printf "%.0f" .Summary.RateLimited}}</li>
<li>Per-address rate-limit rejections: {{printf "%.0f" .Summary.IPLimited}}</li>
</ul>
<h3>Requests by serving model</h3>
<ul>
{{range .Summary.Requests}}<li>{{.Label}}: {{printf "%.0f" .Value}}</li>{{else}}<li>none yet</li>{{end}}
</ul>
<h3>Upstream status codes</h3>
<ul>
{{range .Summary.Upstream}}<li>{{.Label}}: {{printf "%.0f" .Value}}</li>{{else}}<li>none yet</li>{{end}}
</ul>
<h3>Breaker state</h3>
<p>0 is closed, 1 is open, 2 is half-open.</p>
<ul>
{{range .Summary.Breakers}}<li>{{.Label}}: {{printf "%.0f" .Value}}</li>{{else}}<li>none yet</li>{{end}}
</ul>
<h2>Try a request</h2>
<p>These examples call the mock. They do not use a Fireworks key.</p>
<pre>curl -N {{.Base}}/v1/chat/completions \
  -H 'content-type: application/json' \
  -d '{"model":"primary","stream":true,"messages":[{"role":"user","content":"hi"}]}'</pre>
<pre>curl {{.Base}}/v1/chat/completions \
  -H 'content-type: application/json' \
  -d '{"model":"primary","messages":[{"role":"user","content":"hi"}]}'</pre>
<pre>curl {{.Base}}/v1/models</pre>
</body>
</html>
`))
