package server

import (
	"fmt"
	"html/template"
	"io"
	"math"
	"strings"

	"github.com/dlehmann/linkscout/internal/crawler"
	"github.com/dlehmann/linkscout/internal/i18n"
)

// reportTemplate renders a self-contained HTML report (no scripts, no
// external resources) that can be archived or sent to someone: the figures
// and charts of the overview page and the detail view of every URL.
// html/template escapes every value, URLs and titles come from the crawled
// site; the column charts are SVG built by columnsSVG, which escapes too.
var reportTemplate = template.Must(template.New("report").Funcs(template.FuncMap{
	"t":       func(lang i18n.Lang, key string) string { return i18n.T(lang, key) },
	"columns": columnsSVG,
	"flex":    func(n float64) string { return fmt.Sprintf("%g", n) },
	"width": func(n float64, items []rItem) string {
		maxN := 0.0
		for _, i := range items {
			maxN = math.Max(maxN, i.N)
		}
		if maxN == 0 {
			return "0%"
		}
		return fmt.Sprintf("%.2f%%", n/maxN*100)
	},
	"href":    safeHref,
	"indent":  func(n int) string { return fmt.Sprintf("%dem", n) },
	"anyZone": func(items []rItem) bool { return anyZone(items) },
}).Parse(`<!doctype html>
<html lang="{{.Lang}}">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}}</title>
<style>
:root { --bg:#f5f6f8; --surface:#fff; --text:#1c2330; --muted:#677285; --border:#dde1e7; --accent:#2f6fde;
  --ok:#1f8a4c; --warn:#b26a00; --bad:#c9352b; --err:#8e44ad; --info:#3178c6; color-scheme: light dark; }
@media (prefers-color-scheme: dark) { :root { --bg:#12151b; --surface:#1a1e26; --text:#e3e7ee; --muted:#96a0b2; --border:#2e3542;
  --accent:#5b8ff0; --ok:#46c27a; --warn:#e0a03a; --bad:#f0675c; --err:#c38be0; --info:#6aa7f0; } }
* { box-sizing: border-box; }
body { margin: 0 auto; max-width: 1200px; padding: 24px 16px; background: var(--bg); color: var(--text); font: 14px/1.45 system-ui, sans-serif; }
h1 { font-size: 22px; margin: 0 0 4px; overflow-wrap: anywhere; }
h2 { font-size: 18px; margin: 32px 0 12px; }
h3 { font-size: 14px; margin: 0 0 2px; display: flex; justify-content: space-between; gap: 12px; }
h3 span { font-weight: 400; color: var(--muted); }
a { color: var(--accent); }
.muted, .sub { color: var(--muted); }
.sub { font-size: 12px; margin: 0 0 10px; }
.scope { margin: 8px 0 0; color: var(--muted); }
.kpis { display: grid; grid-template-columns: repeat(auto-fill, minmax(140px, 1fr)); gap: 12px; margin: 16px 0; }
.kpi, .panel, .url { background: var(--surface); border: 1px solid var(--border); border-radius: 10px; }
.kpi { padding: 10px 14px; }
.kpi b { display: block; font-size: 22px; font-variant-numeric: tabular-nums; }
.kpi span { color: var(--muted); font-size: 12px; }
.row { display: grid; gap: 16px; margin-bottom: 16px; }
.row.n2 { grid-template-columns: repeat(auto-fit, minmax(320px, 1fr)); }
.row.n3 { grid-template-columns: repeat(auto-fit, minmax(260px, 1fr)); }
.panel { padding: 16px 18px; min-width: 0; }
.hero { display: flex; align-items: baseline; gap: 10px; margin: 4px 0 10px; }
.hero b { font-size: 28px; }
.share { display: flex; gap: 2px; height: 24px; margin-top: 8px; }
.share i { min-width: 3px; }
.share i:first-child { border-radius: 4px 0 0 4px; } .share i:last-child { border-radius: 0 4px 4px 0; } .share i:only-child { border-radius: 4px; }
.legend { display: flex; flex-wrap: wrap; gap: 6px 18px; margin-top: 10px; font-size: 12px; }
.legend span { display: inline-flex; align-items: center; gap: 6px; }
.legend i { width: 10px; height: 10px; border-radius: 3px; display: inline-block; }
.legend i.zone { background: color-mix(in srgb, var(--ok) 25%, transparent); border: 1px solid color-mix(in srgb, var(--ok) 60%, transparent); }
svg.chart { display: block; width: 100%; height: auto; margin-top: 6px; }
svg .grid { stroke: var(--border); } svg .axis { stroke: var(--muted); opacity: .6; }
svg .tick { fill: var(--muted); font-size: 11px; } svg .cap { fill: var(--text); font-size: 11px; font-weight: 600; }
svg .zone { fill: var(--ok); fill-opacity: .1; }
.bars { display: grid; grid-template-columns: minmax(0, max-content) 1fr auto; gap: 6px 10px; align-items: center; font-size: 12px; }
.bars .lbl { overflow-wrap: anywhere; } .bars .n { text-align: right; font-variant-numeric: tabular-nums; }
.bars .track { height: 10px; background: color-mix(in srgb, var(--border) 50%, transparent); border-radius: 3px; overflow: hidden; }
.bars .track i { display: block; height: 100%; border-radius: 3px; }
.url { padding: 14px 18px; margin-bottom: 12px; break-inside: avoid-page; }
.url h3 { font-size: 14px; margin-bottom: 10px; overflow-wrap: anywhere; }
dl { display: grid; grid-template-columns: minmax(120px, 200px) 1fr; gap: 6px 16px; margin: 0; font-size: 13px; }
dt { color: var(--muted); } dd { margin: 0; min-width: 0; overflow-wrap: anywhere; }
.ln { display: flex; gap: 6px; } .ln.gap { margin-top: 10px; } .ln .b { flex: none; color: var(--muted); }
.badge { font-size: 11px; font-weight: 600; border: 1px solid currentColor; border-radius: 4px; padding: 0 5px; margin-left: 6px; }
.t-ok { color: var(--ok); } .t-info { color: var(--info); } .t-bad { color: var(--bad); } .t-warn { color: var(--warn); } .t-err { color: var(--err); } .t-muted { color: var(--muted); }
.f-ok { background: var(--ok); } .f-info { background: var(--info); } .f-bad { background: var(--bad); } .f-warn { background: var(--warn); } .f-err { background: var(--err); } .f-accent { background: var(--accent); } .f-muted { background: var(--muted); }
@media (max-width: 600px) { dl { grid-template-columns: 1fr; gap: 0; } dd { margin-bottom: 8px; } }
@media print { body { background: #fff; } .panel, .url, .kpi { break-inside: avoid; } }
</style>
</head>
<body>
<h1>{{.Title}}</h1>
<p class="muted">{{.Created}}</p>
{{with .Scope}}<p class="scope">{{.}}</p>{{end}}
<div class="kpis">{{range .KPIs}}<div class="kpi"><b{{with .Tone}} class="t-{{.}}"{{end}}>{{.Value}}</b><span>{{.Label}}</span></div>{{end}}</div>
{{range .Rows}}<div class="row n{{len .}}">{{range .}}
<div class="panel">
  <h3>{{.Title}}{{with .Total}}<span>{{.}}</span>{{end}}</h3>
  {{with .Sub}}<p class="sub">{{.}}</p>{{end}}
  {{if not .Items}}<p class="muted">{{.Empty}}</p>
  {{else if eq .Kind "share"}}
    {{if .Hero}}<div class="hero"><b>{{.Hero}}</b><span class="muted">{{.HeroText}}</span></div>{{end}}
    <div class="share">{{range .Items}}<i class="f-{{.Tone}}" style="flex-grow:{{flex .N}}" title="{{.Label}}: {{.Value}} · {{.Share}}"></i>{{end}}</div>
    <div class="legend">{{range .Items}}<span><i class="f-{{.Tone}}"></i>{{.Label}} <b>{{.Value}}</b> <span class="muted">{{.Share}}</span></span>{{end}}</div>
  {{else if eq .Kind "columns"}}
    {{columns .}}
    {{if anyZone .Items}}<div class="legend"><span><i class="zone"></i>{{.Zone}}</span></div>{{end}}
  {{else}}{{$items := .Items}}
    <div class="bars">{{range .Items}}<span class="lbl">{{.Label}}</span><span class="track"><i class="f-{{.Tone}}" style="width:{{width .N $items}}"></i></span><span class="n">{{.Value}}</span>{{end}}</div>
  {{end}}
</div>{{end}}
</div>{{end}}
<h2>{{t .Lang "report.results"}} <span class="muted">({{len .Details}})</span></h2>
{{range .Details}}<section class="url">
<h3>{{if href .URL}}<a href="{{.URL}}">{{.URL}}</a>{{else}}{{.URL}}{{end}}</h3>
<dl>{{range .Fields}}<dt>{{.Label}}</dt><dd>{{range .Lines}}<div class="ln{{if .Gap}} gap{{end}}"{{if .Indent}} style="padding-left:{{indent .Indent}}"{{end}}>{{with .Bullet}}<span class="b">{{.}}</span>{{end}}<span{{if .Muted}} class="muted"{{else if .Tone}} class="t-{{.Tone}}"{{end}}>{{with .Prefix}}<span class="muted">{{.}}</span> {{end}}{{if .Bold}}<b>{{end}}{{if and .Link (href .Text)}}<a href="{{.Text}}">{{.Text}}</a>{{else}}{{.Text}}{{end}}{{if .Bold}}</b>{{end}}{{with .Badge}}<span class="badge">{{.}}</span>{{end}}</span></div>{{end}}</dd>{{end}}</dl>
</section>
{{end}}
</body>
</html>
`))

// WriteHTMLReport renders rep as HTML in the given language. External
// links are counted in the figures with external.
func WriteHTMLReport(w io.Writer, rep crawler.Report, lang i18n.Lang, external bool) error {
	return reportTemplate.Execute(w, newReportView(rep, lang, reportOptions{External: external}))
}

func anyZone(items []rItem) bool {
	for _, i := range items {
		if i.InZone {
			return true
		}
	}
	return false
}

// niceScale returns a clean axis step (1, 2 or 5 × 10^k, at most 5 ticks
// above 0) and the axis maximum for a largest value v.
func niceScale(v float64) (step, maxV float64) {
	raw := math.Max(1, v) / 4
	p := math.Pow(10, math.Floor(math.Log10(raw)))
	step = 10 * p
	for _, m := range []float64{1, 2, 5, 10} {
		if m*p >= raw {
			step = m * p
			break
		}
	}
	step = math.Max(1, step)
	return step, math.Ceil(math.Max(1, v)/step) * step
}

// columnsSVG draws a column chart like columnChart in app.js, at a fixed
// size that the page scales to the panel width.
func columnsSVG(c rChart) template.HTML {
	const W, H, top, bottom, left, right = 560.0, 220.0, 20.0, 28.0, 40.0, 8.0
	plotW, plotH := W-left-right, H-top-bottom
	maxN := 0.0
	for _, i := range c.Items {
		maxN = math.Max(maxN, i.N)
	}
	step, maxV := niceScale(maxN)
	band := plotW / math.Max(1, float64(len(c.Items)))
	barW := math.Min(24, band*0.6)
	y := func(v float64) float64 { return top + plotH - v/maxV*plotH }
	esc := template.HTMLEscapeString
	var b strings.Builder
	fmt.Fprintf(&b, `<svg class="chart" viewBox="0 0 %g %g" role="img" aria-label="%s">`, W, H, esc(c.Title))
	// recommended range behind the columns
	from, to := -1, -1
	for i, it := range c.Items {
		if it.InZone {
			if from < 0 {
				from = i
			}
			to = i
		}
	}
	if from >= 0 {
		fmt.Fprintf(&b, `<rect class="zone" x="%.1f" y="%g" width="%.1f" height="%g"/>`, left+band*float64(from), top, band*float64(to-from+1), plotH)
	}
	for v := 0.0; v <= maxV; v += step {
		yy := math.Round(y(v)) + 0.5
		fmt.Fprintf(&b, `<line class="grid" x1="%g" x2="%g" y1="%g" y2="%g"/>`, left, left+plotW, yy, yy)
		fmt.Fprintf(&b, `<text class="tick" x="%g" y="%g" text-anchor="end">%g</text>`, left-6, yy+4, v)
	}
	for i, it := range c.Items {
		cx := left + band*float64(i) + band/2
		if it.N > 0 {
			y1 := y(it.N)
			h := math.Max(1, top+plotH-y1)
			fmt.Fprintf(&b, `<path d="%s" style="fill:var(--accent)"><title>%s: %s</title></path>`, colPath(cx-barW/2, top+plotH-h, barW, h), esc(it.Label), esc(it.Value))
			fmt.Fprintf(&b, `<text class="cap" x="%.1f" y="%.1f" text-anchor="middle">%s</text>`, cx, y1-6, esc(it.Value))
		}
		fmt.Fprintf(&b, `<text class="tick" x="%.1f" y="%g" text-anchor="middle">%s</text>`, cx, H-8, esc(it.Label))
	}
	fmt.Fprintf(&b, `<line class="axis" x1="%g" x2="%g" y1="%g" y2="%g"/></svg>`, left, left+plotW, top+plotH+0.5, top+plotH+0.5)
	return template.HTML(b.String())
}

// colPath is a column with a 4px rounded top and a square base.
func colPath(x, y, w, h float64) string {
	r := math.Min(4, math.Min(w/2, h))
	return fmt.Sprintf("M%.1f,%.1fV%.1fQ%.1f,%.1f %.1f,%.1fH%.1fQ%.1f,%.1f %.1f,%.1fV%.1fZ",
		x, y+h, y+r, x, y, x+r, y, x+w-r, x+w, y, x+w, y+r, y+h)
}
