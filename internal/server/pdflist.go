package server

// The summary PDF of the results list: one entry per URL, as the list of
// the UI shows it (resultRow in app.js) – the URL, then status, title and
// the values on a second line, the search matches on a third.

import (
	"io"
	"strconv"
	"strings"

	"github.com/dlehmann/linkscout/internal/crawler"
	"github.com/dlehmann/linkscout/internal/i18n"
)

// reportHeader draws the title, the creation line and what the report
// covers (list, external links, content search) at the top of page 1.
func reportHeader(d *pdfDoc, v reportView, left, right float64) {
	d.y += 18
	for i, l := range wrap(v.Title, right-left, 18, true) {
		if i > 0 {
			d.y += 22
		}
		d.text(left, d.y, 18, true, pdfText, l)
	}
	d.y += 16
	d.text(left, d.y, 9, false, pdfMuted, v.Created)
	for _, line := range []string{v.List, v.Scope, v.Search} {
		if line == "" {
			continue
		}
		for _, l := range wrap(line, right-left, 9, false) {
			d.y += 12
			d.text(left, d.y, 9, false, pdfMuted, l)
		}
	}
}

// WritePDFList renders the summary of a results list (crawler.Report.Select).
func WritePDFList(w io.Writer, rep crawler.Report, lang i18n.Lang) error {
	v := newReportView(rep, lang, reportOptions{})
	f := formatter{lang}
	d := &pdfDoc{}
	d.newPage()
	left, right := pdfMargin, pdfW-pdfMargin
	reportHeader(d, v, left, right)
	d.y += 10

	matches := map[string]crawler.SearchMatch{}
	if rep.Search != nil {
		for _, m := range rep.Search.Matches {
			matches[m.URL] = m
		}
	}
	const size, lineH = 8.5, 11.0
	statusW := 30.0
	for _, r := range rep.Results {
		urlLines := wrap(r.URL, right-left, size+0.5, true)
		values := listValues(f, r)
		valueLines := wrap(strings.Join(values, " · "), right-left-statusW, size, false)
		var snip []string
		if m, ok := matches[r.URL]; ok {
			parts := []string{f.tv("search.n_matches", "n", f.num(m.Count))}
			for _, sn := range m.Snippets {
				parts = append(parts, sn.Before+f.quote(sn.Match)+sn.After)
			}
			snip = wrap(strings.Join(parts, " · "), right-left-statusW, size, false)
		}
		d.need(float64(len(urlLines)+len(valueLines)+len(snip))*lineH + 8)
		d.y += 4
		for _, l := range urlLines {
			d.y += lineH
			d.text(left, d.y, size+0.5, true, pdfAccent, l)
			if safeHref(r.URL) {
				d.link(left, d.y-8, textWidth(l, size+0.5, true), lineH, r.URL)
			}
		}
		status := "ERR"
		if r.Pending {
			status = "…"
		} else if r.Status > 0 {
			status = strconv.Itoa(r.Status)
		}
		for i, l := range valueLines {
			d.y += lineH
			if i == 0 {
				d.text(left, d.y, size, true, toneColor(statusTone(r)), status)
			}
			d.text(left+statusW, d.y, size, false, pdfText, l)
		}
		for _, l := range snip {
			d.y += lineH
			d.text(left+statusW, d.y, size, false, pdfMuted, l)
		}
		d.y += 4
		d.hline(left, right, d.y, pdfBorder)
	}
	if len(rep.Results) == 0 {
		d.y += 14
		d.text(left, d.y, 9, false, pdfMuted, f.t("table.empty"))
	}
	pageFooter(d, v, left, right)
	return d.write(w, v.Title)
}

// listValues are the values of the second line of a list entry: title (or
// error), MIME type, kind, clicks, last modified, size, time, SEO issues.
func listValues(f formatter, r crawler.Result) []string {
	var out []string
	if t := r.Title; t != "" {
		out = append(out, t)
	} else if r.Error != "" {
		out = append(out, errText(f, r.Error))
	}
	if m := crawler.ResultMime(r); m != "" {
		out = append(out, m)
	}
	out = append(out, f.t("kind."+r.Kind))
	if !r.Pending {
		out = append(out, clickLabel(f, r.Depth))
	}
	if !r.LastModified.IsZero() {
		out = append(out, f.date(r.LastModified))
	}
	if r.Size > 0 {
		out = append(out, f.bytes(r.Size))
	}
	if !r.CheckedAt.IsZero() {
		out = append(out, fmtMs(r.DurationMs))
	}
	switch n := len(r.Issues); {
	case n == 1:
		out = append(out, f.t("list.one_issue"))
	case n > 1:
		out = append(out, f.tv("list.n_issues", "n", f.num(n)))
	}
	return out
}
