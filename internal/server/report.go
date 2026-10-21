package server

import (
	"html/template"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/dlehmann/linkscout/internal/crawler"
	"github.com/dlehmann/linkscout/internal/i18n"
)

// reportTemplate renders a self-contained HTML report (no scripts, no
// external resources) that can be archived or sent to someone. html/template
// escapes every value, URLs and titles come from the crawled site.
var reportTemplate = template.Must(template.New("report").Funcs(template.FuncMap{
	"t": func(lang i18n.Lang, key string) string { return i18n.T(lang, key) },
}).Parse(`<!doctype html>
<html lang="{{.Lang}}">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}}</title>
<style>
:root { --bg:#fff; --text:#1c2330; --muted:#5d6879; --border:#dde1e7; --row:#f5f6f8; --ok:#1f8a4c; --bad:#c9352b; --warn:#b26a00; --info:#3178c6; color-scheme: light dark; }
@media (prefers-color-scheme: dark) { :root { --bg:#12151b; --text:#e3e7ee; --muted:#9aa4b5; --border:#2e3542; --row:#1a1e26; --ok:#46c27a; --bad:#f0675c; --warn:#e0a03a; --info:#6aa7f0; } }
body { margin: 0 auto; max-width: 1200px; padding: 24px; background: var(--bg); color: var(--text); font: 14px/1.45 system-ui, sans-serif; }
h1 { font-size: 22px; margin: 0 0 4px; } h2 { font-size: 17px; margin: 28px 0 8px; }
.muted { color: var(--muted); }
.kpis { display: flex; flex-wrap: wrap; gap: 12px; margin: 16px 0; }
.kpi { border: 1px solid var(--border); border-radius: 8px; padding: 10px 14px; min-width: 120px; }
.kpi b { display: block; font-size: 22px; }
table { width: 100%; border-collapse: collapse; }
th, td { text-align: left; padding: 6px 8px; border-bottom: 1px solid var(--border); vertical-align: top; }
th { font-size: 12px; text-transform: uppercase; color: var(--muted); }
tr:nth-child(even) td { background: var(--row); }
td.url { word-break: break-all; } td.num { text-align: right; white-space: nowrap; }
.s2 { color: var(--ok); } .s3 { color: var(--info); } .s4, .s0 { color: var(--bad); } .s5 { color: var(--warn); }
.sev-error { color: var(--bad); font-weight: 600; } .sev-warning { color: var(--warn); font-weight: 600; } .sev-notice { color: var(--info); }
</style>
</head>
<body>
<h1>{{.Title}}</h1>
<p class="muted">{{.Created}}</p>
<div class="kpis">
  <div class="kpi"><b>{{.Stats.Total}}</b>{{t .Lang "stats.total"}}</div>
  <div class="kpi"><b class="s2">{{.Stats.OK}}</b>{{t .Lang "stats.ok"}}</div>
  <div class="kpi"><b class="s3">{{.Stats.Redirected}}</b>{{t .Lang "stats.redirected"}}</div>
  <div class="kpi"><b class="s4">{{.Stats.Broken}}</b>{{t .Lang "stats.broken"}}</div>
  <div class="kpi"><b class="s0">{{.Stats.Errors}}</b>{{t .Lang "stats.errors"}}</div>
</div>
<h2>{{t .Lang "report.issues"}}</h2>
{{if .Issues}}<table>
<tr><th></th><th>{{t .Lang "tab.seo"}}</th><th class="num">URLs</th></tr>
{{range .Issues}}<tr><td class="sev-{{.Severity}}">{{.SevLabel}}</td><td><b>{{.Name}}</b><br><span class="muted">{{.Help}}</span></td><td class="num">{{.Count}}</td></tr>
{{end}}</table>{{else}}<p>{{t .Lang "seo.none"}}</p>{{end}}
<h2>{{t .Lang "report.results"}}</h2>
<table>
<tr><th>{{t .Lang "col.url"}}</th><th>{{t .Lang "col.status"}}</th><th>{{t .Lang "col.type"}}</th><th>{{t .Lang "col.title"}}</th><th>{{t .Lang "col.seo"}}</th></tr>
{{range .Rows}}<tr><td class="url">{{.URL}}{{if .FinalURL}}<br><span class="muted">↳ {{.FinalURL}}</span>{{end}}</td><td class="num s{{.Class}}">{{.Status}}</td><td>{{.Mime}}</td><td>{{.Title}}</td><td>{{.Issues}}</td></tr>
{{end}}</table>
</body>
</html>
`))

type reportIssue struct {
	crawler.IssueCount
	Name, Help, SevLabel string
}

type reportRow struct {
	URL, FinalURL, Status, Mime, Title, Issues string
	Class                                      int
}

// WriteHTMLReport renders rep as HTML in the given language.
func WriteHTMLReport(w io.Writer, rep crawler.Report, lang i18n.Lang) error {
	data := struct {
		Lang    i18n.Lang
		Title   string
		Created string
		Stats   crawler.Stats
		Issues  []reportIssue
		Rows    []reportRow
	}{
		Lang:  lang,
		Title: strings.ReplaceAll(i18n.T(lang, "report.title"), "%url%", rep.StartURL),
		Created: strings.NewReplacer("%date%", rep.CreatedAt.Local().Format(time.DateTime),
			"%generator%", rep.Generator).Replace(i18n.T(lang, "report.created")),
		Stats: rep.Stats,
	}
	for _, ic := range rep.Issues {
		data.Issues = append(data.Issues, reportIssue{IssueCount: ic,
			Name:     i18n.T(lang, "issue."+ic.ID),
			Help:     i18n.T(lang, "issue."+ic.ID+".help"),
			SevLabel: i18n.T(lang, "sev."+ic.Severity)})
	}
	for _, r := range rep.Results {
		status := "ERR"
		if r.Status > 0 {
			status = strconv.Itoa(r.Status)
		}
		mime := r.MimeType
		if mime == "" {
			mime = r.Type
		}
		names := make([]string, len(r.Issues))
		for i, id := range r.Issues {
			names[i] = i18n.T(lang, "issue."+id)
		}
		title := r.Title
		if title == "" {
			title = r.Error
		}
		data.Rows = append(data.Rows, reportRow{URL: r.URL, FinalURL: r.FinalURL, Status: status, Mime: mime,
			Title: title, Issues: strings.Join(names, ", "), Class: r.Status / 100})
	}
	return reportTemplate.Execute(w, data)
}
