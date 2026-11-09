package proxy

import (
	"html/template"
	"net/http"
)

// pageData is the data for the diagnostic pages. html/template escapes every field, so request-controlled
// values (the Host header) and error text can never inject markup.
type pageData struct {
	Host   string
	Target string
	Error  string
}

const pageStyle = `
body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
  background: #0d1117; color: #c9d1d9; padding: 40px; }
h1 { color: {{.}}; }
.card { background: #161b22; border: 1px solid #30363d; border-radius: 8px; padding: 24px;
  max-width: 600px; margin-top: 20px; }
code { background: #21262d; padding: 2px 6px; border-radius: 4px; font-family: monospace; color: #f0883e; }
`

func newPage(accent, title, body string) *template.Template {
	style := template.Must(template.New("style").Parse(pageStyle))
	page := template.Must(template.New("page").Parse(
		`<!DOCTYPE html><html><head><meta charset="utf-8"><title>prot &mdash; ` + title + `</title>` +
			`<style>{{template "style" "` + accent + `"}}</style></head><body>` +
			`<h1>prot &mdash; ` + title + `</h1><div class="card">` + body + `</div></body></html>`,
	))
	template.Must(page.AddParseTree("style", style.Tree))
	return page
}

var (
	notFoundPage = newPage("#58a6ff", "Route Not Found", `
<p>No upstream service is configured for host: <code>{{.Host}}</code></p>
<p>To map an alias to a running server, run:</p>
<pre><code>prot alias &lt;alias&gt; &lt;upstream_url&gt;</code></pre>
<p>or register a managed service with <code>prot add</code>.</p>`)

	badGatewayPage = newPage("#f85149", "502 Bad Gateway", `
<p>The upstream server at <code>{{.Target}}</code> is not responding.</p>
<p>Error: <code>{{.Error}}</code></p>
<p>Check if your development service is running or inspect logs:</p>
<pre><code>prot logs &lt;service&gt;</code></pre>`)
)

func renderPage(w http.ResponseWriter, status int, page *template.Template, data pageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_ = page.Execute(w, data)
}
