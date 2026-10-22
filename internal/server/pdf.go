package server

// A small PDF writer for the report, without external dependencies.
//
// It only needs what a table report needs: text in the two standard fonts
// Helvetica and Helvetica-Bold (every PDF viewer has them, nothing is
// embedded), WinAnsi encoding (covers German umlauts, typographic quotes,
// dashes and the euro sign), filled rectangles and lines, A4 landscape
// pages with a repeated table header and page numbers.

import (
	"bytes"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/dlehmann/linkscout/internal/crawler"
	"github.com/dlehmann/linkscout/internal/i18n"
)

// Page geometry in PDF points (1/72 inch): A4 landscape.
const (
	pdfW, pdfH = 842.0, 595.0
	pdfMargin  = 36.0
)

// rgb is a fill/stroke colour with components between 0 and 1.
type rgb struct{ r, g, b float64 }

var (
	pdfText   = rgb{0.11, 0.14, 0.19}
	pdfMuted  = rgb{0.40, 0.45, 0.52}
	pdfBorder = rgb{0.87, 0.88, 0.91}
	pdfStripe = rgb{0.965, 0.97, 0.98}
	pdfOK     = rgb{0.12, 0.54, 0.30}
	pdfInfo   = rgb{0.19, 0.47, 0.78}
	pdfBad    = rgb{0.79, 0.21, 0.17}
	pdfWarn   = rgb{0.70, 0.42, 0.00}
)

// pdfDoc collects the content streams of the pages.
type pdfDoc struct {
	pages []*bytes.Buffer
	cur   *bytes.Buffer
	y     float64 // current baseline, from the top of the page
	onNew func()  // called after a page break (repeats table headers)
}

func (d *pdfDoc) newPage() {
	d.cur = &bytes.Buffer{}
	d.pages = append(d.pages, d.cur)
	d.y = pdfMargin
	if d.onNew != nil {
		d.onNew()
	}
}

// need starts a new page when less than h points are left.
func (d *pdfDoc) need(h float64) {
	if d.cur == nil || d.y+h > pdfH-pdfMargin-14 { // 14: room for the page number
		d.newPage()
	}
}

// text draws s with its baseline at (x, y) – y measured from the top.
func (d *pdfDoc) text(x, y, size float64, bold bool, c rgb, s string) {
	font := "F1"
	if bold {
		font = "F2"
	}
	fmt.Fprintf(d.cur, "BT /%s %s Tf %s %s %s rg %s %s Td (%s) Tj ET\n",
		font, num(size), num(c.r), num(c.g), num(c.b), num(x), num(pdfH-y), pdfString(s))
}

// rect fills a rectangle whose top left corner is (x, y).
func (d *pdfDoc) rect(x, y, w, h float64, c rgb) {
	fmt.Fprintf(d.cur, "%s %s %s rg %s %s %s %s re f\n", num(c.r), num(c.g), num(c.b), num(x), num(pdfH-y-h), num(w), num(h))
}

// hline draws a horizontal hairline at y.
func (d *pdfDoc) hline(x1, x2, y float64, c rgb) {
	fmt.Fprintf(d.cur, "%s %s %s RG 0.5 w %s %s m %s %s l S\n", num(c.r), num(c.g), num(c.b), num(x1), num(pdfH-y), num(x2), num(pdfH-y))
}

// write assembles the PDF file: catalog, page tree, the two fonts, one
// page object and content stream per page, cross-reference table, trailer.
func (d *pdfDoc) write(w io.Writer, title string) error {
	var out bytes.Buffer
	var offsets []int
	obj := func(body string) int {
		offsets = append(offsets, out.Len())
		n := len(offsets)
		fmt.Fprintf(&out, "%d 0 obj\n%s\nendobj\n", n, body)
		return n
	}
	out.WriteString("%PDF-1.4\n%\xe2\xe3\xcf\xd3\n")
	// Fixed object numbers: 1 catalog, 2 pages, 3/4 fonts, 5 info, then two per page.
	pageIDs := make([]string, len(d.pages))
	for i := range d.pages {
		pageIDs[i] = fmt.Sprintf("%d 0 R", 6+2*i)
	}
	obj("<< /Type /Catalog /Pages 2 0 R >>")
	obj(fmt.Sprintf("<< /Type /Pages /Count %d /Kids [%s] >>", len(d.pages), strings.Join(pageIDs, " ")))
	obj("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>")
	obj("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica-Bold /Encoding /WinAnsiEncoding >>")
	obj(fmt.Sprintf("<< /Title (%s) /Producer (LinkScout) /CreationDate (D:%s) >>", pdfString(title), time.Now().UTC().Format("20060102150405Z")))
	for i, content := range d.pages {
		obj(fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %s %s] /Resources << /Font << /F1 3 0 R /F2 4 0 R >> >> /Contents %d 0 R >>",
			num(pdfW), num(pdfH), 7+2*i))
		obj(fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", content.Len(), content.String()))
	}
	xref := out.Len()
	fmt.Fprintf(&out, "xref\n0 %d\n0000000000 65535 f \n", len(offsets)+1)
	for _, o := range offsets {
		fmt.Fprintf(&out, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&out, "trailer\n<< /Size %d /Root 1 0 R /Info 5 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets)+1, xref)
	_, err := w.Write(out.Bytes())
	return err
}

// num formats a coordinate compactly ("12.5", "0").
func num(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// winAnsi maps the characters outside Latin-1 that WinAnsiEncoding (code
// page 1252) offers; Latin-1 itself maps 1:1.
var winAnsi = map[rune]byte{
	'€': 0x80, '‚': 0x82, '„': 0x84, '…': 0x85, '‹': 0x8b, '‘': 0x91, '’': 0x92,
	'“': 0x93, '”': 0x94, '•': 0x95, '–': 0x96, '—': 0x97, '™': 0x99, '›': 0x9b,
}

// pdfString encodes s as WinAnsi and escapes it for a PDF string literal.
// Characters the encoding lacks become "?" (arrows become ">").
func pdfString(s string) string {
	var b strings.Builder
	for _, r := range s {
		var c byte
		switch {
		case r == '(' || r == ')' || r == '\\':
			b.WriteByte('\\')
			c = byte(r)
		case r == '→' || r == '↳':
			c = '>'
		case r < 0x20:
			c = ' '
		case r < 0x80 || (r >= 0xa0 && r <= 0xff):
			c = byte(r)
		default:
			var ok bool
			if c, ok = winAnsi[r]; !ok {
				c = '?'
			}
		}
		if c >= 0x80 {
			fmt.Fprintf(&b, "\\%03o", c) // octal escape keeps the content stream 7-bit clean
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

// helvetica holds the widths (per 1000 units of the font size) of the
// printable ASCII characters 32–126 of Helvetica, from the standard AFM.
var helvetica = [...]int{
	278, 278, 355, 556, 556, 889, 667, 191, 333, 333, 389, 584, 278, 333, 278, 278,
	556, 556, 556, 556, 556, 556, 556, 556, 556, 556, 278, 278, 584, 584, 584, 556,
	1015, 667, 667, 722, 722, 667, 611, 778, 722, 278, 500, 667, 556, 833, 722, 778,
	667, 778, 722, 667, 611, 722, 667, 944, 667, 667, 611, 278, 278, 278, 469, 556,
	333, 556, 556, 500, 556, 556, 278, 556, 556, 222, 222, 500, 222, 833, 556, 556,
	556, 556, 333, 500, 278, 556, 500, 722, 500, 500, 500, 334, 260, 334, 584,
}

// textWidth estimates the width of s in points. Helvetica-Bold is about 7 %
// wider; characters outside ASCII use the width of a typical lower-case letter.
func textWidth(s string, size float64, bold bool) float64 {
	w := 0
	for _, r := range s {
		if r >= 32 && r <= 126 {
			w += helvetica[r-32]
		} else {
			w += 556
		}
	}
	f := float64(w) * size / 1000
	if bold {
		f *= 1.07
	}
	return f
}

// fit shortens s with "…" until it fits into width. With fromStart the
// beginning is cut instead of the end (for URLs, whose end matters).
func fit(s string, width, size float64, bold, fromStart bool) string {
	if textWidth(s, size, bold) <= width {
		return s
	}
	rs := []rune(s)
	for len(rs) > 1 {
		if fromStart {
			rs = rs[1:]
		} else {
			rs = rs[:len(rs)-1]
		}
		var cand string
		if fromStart {
			cand = "…" + string(rs)
		} else {
			cand = string(rs) + "…"
		}
		if textWidth(cand, size, bold) <= width {
			return cand
		}
	}
	return "…"
}

// statusColor returns the colour of a status code (the status palette of the UI).
func statusColor(status int) rgb {
	switch {
	case status >= 200 && status < 300:
		return pdfOK
	case status >= 300 && status < 400:
		return pdfInfo
	case status >= 500:
		return pdfWarn
	default:
		return pdfBad
	}
}

func severityColor(sev string) rgb {
	switch sev {
	case crawler.SevError:
		return pdfBad
	case crawler.SevWarning:
		return pdfWarn
	}
	return pdfInfo
}

// WritePDFReport renders rep as PDF (A4 landscape) in the given language:
// title, key figures, SEO issues and the table of all URLs.
func WritePDFReport(w io.Writer, rep crawler.Report, lang i18n.Lang) error {
	T := func(k string) string { return i18n.T(lang, k) }
	d := &pdfDoc{}
	d.newPage()
	left, right := pdfMargin, pdfW-pdfMargin

	title := strings.ReplaceAll(T("report.title"), "%url%", rep.StartURL)
	d.y += 18
	d.text(left, d.y, 18, true, pdfText, fit(title, right-left, 18, true, false))
	d.y += 16
	created := strings.NewReplacer("%date%", rep.CreatedAt.Local().Format(time.DateTime), "%generator%", rep.Generator).Replace(T("report.created"))
	d.text(left, d.y, 9, false, pdfMuted, created)

	// Key figures as a row of boxes.
	d.y += 16
	figures := []struct {
		label string
		value int
		c     rgb
	}{
		{T("stats.total"), rep.Stats.Total, pdfText},
		{T("stats.ok"), rep.Stats.OK, pdfOK},
		{T("stats.redirected"), rep.Stats.Redirected, pdfInfo},
		{T("stats.broken"), rep.Stats.Broken, pdfBad},
		{T("stats.errors"), rep.Stats.Errors, pdfBad},
		{T("stats.pages"), rep.Stats.InternalPages, pdfText},
	}
	boxW := (right - left - 5*8) / 6
	for i, f := range figures {
		x := left + float64(i)*(boxW+8)
		d.rect(x, d.y, boxW, 40, pdfStripe)
		d.text(x+8, d.y+20, 16, true, f.c, strconv.Itoa(f.value))
		d.text(x+8, d.y+33, 8, false, pdfMuted, fit(f.label, boxW-16, 8, false, false))
	}
	d.y += 58

	// SEO issues.
	d.text(left, d.y, 12, true, pdfText, T("report.issues"))
	d.y += 6
	if len(rep.Issues) == 0 {
		d.y += 12
		d.text(left, d.y, 9, false, pdfMuted, T("seo.none"))
	}
	for _, ic := range rep.Issues {
		d.need(16)
		d.y += 14
		d.text(left, d.y, 8, true, severityColor(ic.Severity), T("sev."+ic.Severity))
		d.text(left+60, d.y, 9, false, pdfText, fit(T("issue."+ic.ID), right-left-110, 9, false, false))
		count := strconv.Itoa(ic.Count)
		d.text(right-textWidth(count, 9, false), d.y, 9, false, pdfMuted, count)
		d.hline(left, right, d.y+4, pdfBorder)
	}
	d.y += 24

	// Results table; the header is repeated on every page.
	cols := []struct {
		key string
		x   float64
	}{{"col.status", left}, {"col.url", left + 46}, {"col.type", left + 400}, {"col.title", left + 510}, {"col.seo", right - 24}}
	header := func() {
		d.y += 12
		for _, c := range cols {
			label := strings.ToUpper(T(c.key))
			x := c.x
			if c.key == "col.seo" { // right-aligned like the counts below it
				x = right - textWidth(label, 8, true)
			}
			d.text(x, d.y, 8, true, pdfMuted, label)
		}
		d.hline(left, right, d.y+4, pdfBorder)
		d.y += 2
	}
	d.need(40)
	d.text(left, d.y, 12, true, pdfText, T("report.results"))
	d.y += 4
	header()
	d.onNew = header
	for i, r := range rep.Results {
		d.need(14)
		d.y += 13
		if i%2 == 1 {
			d.rect(left, d.y-9.5, right-left, 13, pdfStripe)
		}
		status := "ERR"
		if r.Status > 0 {
			status = strconv.Itoa(r.Status)
		}
		d.text(cols[0].x, d.y, 8, true, statusColor(r.Status), status)
		d.text(cols[1].x, d.y, 8, false, pdfText, fit(r.URL, cols[2].x-cols[1].x-8, 8, false, true))
		mime := r.MimeType
		if mime == "" {
			mime = r.Type
		}
		d.text(cols[2].x, d.y, 8, false, pdfMuted, fit(mime, cols[3].x-cols[2].x-8, 8, false, false))
		title := r.Title
		if title == "" {
			title = r.Error
		}
		d.text(cols[3].x, d.y, 8, false, pdfText, fit(title, cols[4].x-cols[3].x-8, 8, false, false))
		if n := len(r.Issues); n > 0 {
			s := strconv.Itoa(n)
			d.text(right-textWidth(s, 8, true), d.y, 8, true, pdfInfo, s)
		}
	}

	// Page numbers ("3 / 12").
	for i, p := range d.pages {
		d.cur = p
		label := fmt.Sprintf("%d / %d", i+1, len(d.pages))
		d.text(right-textWidth(label, 8, false), pdfH-pdfMargin+4, 8, false, pdfMuted, label)
		d.text(left, pdfH-pdfMargin+4, 8, false, pdfMuted, fit(rep.StartURL, 400, 8, false, false))
	}
	return d.write(w, title)
}
