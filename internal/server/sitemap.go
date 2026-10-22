package server

import (
	"fmt"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/dlehmann/linkscout/internal/crawler"
	"github.com/dlehmann/linkscout/internal/i18n"
)

// handleSitemap answers with the site structure of the current results:
// format=json (default, for the sitemap tab), xml (sitemap.xml for search
// engines) or html (a nested list).
func (s *Server) handleSitemap(w http.ResponseWriter, r *http.Request) {
	rs := s.crawler.Results()
	crawler.Analyze(rs) // noindex / canonical decide what goes into sitemap.xml
	start := s.crawler.Config().StartURL
	tree := crawler.SiteTree(rs, start)
	host := "site"
	if u, err := url.Parse(start); err == nil && u.Host != "" {
		host = sanitizeName(u.Host)
	}
	switch r.URL.Query().Get("format") {
	case "xml":
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="sitemap.xml"`)
		crawler.WriteSitemapXML(w, tree)
	case "html":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="linkscout-sitemap-%s.html"`, host))
		WriteSitemapHTML(w, tree, start, "LinkScout "+s.opts.Version, s.opts.Lang)
	default:
		writeJSON(w, http.StatusOK, tree)
	}
}

// sitemapTemplate renders the site structure as nested list, self-contained
// like the HTML report. Titles and URLs come from the crawled site and are
// escaped by html/template.
var sitemapTemplate = template.Must(template.New("sitemap").Funcs(template.FuncMap{
	"t":    func(lang i18n.Lang, key string) string { return i18n.T(lang, key) },
	"href": safeHref,
}).Parse(`{{define "node"}}<li>{{if .Node.URL}}{{if href .Node.URL}}<a href="{{.Node.URL}}">{{.Node.Path}}</a>{{else}}{{.Node.Path}}{{end}}{{with .Node.Title}} <span class="title">{{.}}</span>{{end}}{{with .Date .Node}} <span class="date">{{.}}</span>{{end}}{{else}}<span class="dir">{{.Node.Name}}/</span>{{end}}
{{if .Node.Children}}<ul>{{range .Node.Children}}{{template "node" $.With .}}{{end}}</ul>{{end}}</li>
{{end}}<!doctype html>
<html lang="{{.Lang}}">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}}</title>
<style>
:root { --bg:#fff; --text:#1c2330; --muted:#677285; --accent:#2f6fde; --border:#dde1e7; color-scheme: light dark; }
@media (prefers-color-scheme: dark) { :root { --bg:#12151b; --text:#e3e7ee; --muted:#96a0b2; --accent:#5b8ff0; --border:#2e3542; } }
body { margin: 0 auto; max-width: 1100px; padding: 24px 16px; background: var(--bg); color: var(--text); font: 14px/1.6 system-ui, sans-serif; }
h1 { font-size: 22px; margin: 0 0 4px; overflow-wrap: anywhere; }
a { color: var(--accent); }
ul { list-style: none; margin: 0; padding-left: 20px; border-left: 1px solid var(--border); }
body > ul { padding-left: 0; border: 0; }
li { overflow-wrap: anywhere; }
.title { margin-left: 6px; } .date, .muted, .dir { color: var(--muted); } .date { font-size: 12px; margin-left: 6px; }
</style>
</head>
<body>
<h1>{{.Title}}</h1>
<p class="muted">{{.Sub}}</p>
<ul>{{template "node" .With .Root}}</ul>
</body>
</html>
`))

// sitemapView is the data of sitemapTemplate; With switches the node for
// the recursive template.
type sitemapView struct {
	Lang       i18n.Lang
	Title, Sub string
	Root       *crawler.SiteNode
	Node       *crawler.SiteNode
	f          formatter
}

func (v sitemapView) With(n *crawler.SiteNode) sitemapView { v.Node = n; return v }

// Date is the formatted Last-Modified date of a page ("" when unknown).
func (v sitemapView) Date(n *crawler.SiteNode) string {
	if n.LastModified.IsZero() {
		return ""
	}
	return v.f.date(n.LastModified)
}

// WriteSitemapHTML renders the site structure as HTML page.
func WriteSitemapHTML(w io.Writer, tree *crawler.SiteNode, startURL, generator string, lang i18n.Lang) error {
	f := formatter{lang}
	v := sitemapView{Lang: lang, Root: tree, f: f,
		Title: f.tv("sitemap.export_title", "url", startURL),
		Sub: f.tv("sitemap.export_sub", "n", f.num(tree.Pages)) + " · " +
			strings.NewReplacer("%date%", f.dateTime(time.Now()), "%generator%", generator).Replace(f.t("report.created")),
	}
	return sitemapTemplate.Execute(w, v)
}
