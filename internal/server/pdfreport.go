package server

import (
	"fmt"
	"io"
	"math"
	"strings"

	"github.com/dlehmann/linkscout/internal/crawler"
	"github.com/dlehmann/linkscout/internal/i18n"
)

var (
	pdfErr    = rgb{0.56, 0.27, 0.68}
	pdfAccent = rgb{0.18, 0.44, 0.87}
	pdfZone   = rgb{0.91, 0.96, 0.93}
	pdfTrack  = rgb{0.93, 0.94, 0.96}
)

// toneColor maps the tones of the report view to the PDF palette (the light
// theme of the UI).
func toneColor(tone string) rgb {
	switch tone {
	case "ok":
		return pdfOK
	case "info":
		return pdfInfo
	case "bad":
		return pdfBad
	case "warn":
		return pdfWarn
	case "err":
		return pdfErr
	case "accent":
		return pdfAccent
	case "muted":
		return pdfMuted
	}
	return pdfText
}

// WritePDFReport renders rep as PDF (A4 portrait) in the given language:
// the figures and charts of the overview page, then the detail view of every
// URL. External links are counted in the figures with external.
func WritePDFReport(w io.Writer, rep crawler.Report, lang i18n.Lang, external bool) error {
	v := newReportView(rep, lang, reportOptions{External: external})
	d := &pdfDoc{}
	d.newPage()
	left, right := pdfMargin, pdfW-pdfMargin

	d.y += 18
	for i, l := range wrap(v.Title, right-left, 18, true) {
		if i > 0 {
			d.y += 22
		}
		d.text(left, d.y, 18, true, pdfText, l)
	}
	d.y += 16
	d.text(left, d.y, 9, false, pdfMuted, v.Created)
	for _, line := range []string{v.Scope, v.Search} {
		if line != "" {
			d.y += 12
			d.text(left, d.y, 9, false, pdfMuted, fit(line, right-left, 9, false, false))
		}
	}

	// Key figures as a row of boxes.
	d.y += 14
	n := float64(len(v.KPIs))
	boxW := (right - left - (n-1)*6) / n
	for i, k := range v.KPIs {
		x := left + float64(i)*(boxW+6)
		d.rect(x, d.y, boxW, 40, pdfStripe)
		d.text(x+6, d.y+20, 14, true, toneColor(k.Tone), fit(k.Value, boxW-12, 14, true, false))
		d.text(x+6, d.y+33, 7, false, pdfMuted, fit(k.Label, boxW-12, 7, false, false))
	}
	d.y += 52

	// The charts, row by row; the page is narrower than the overview page,
	// so the bar lists get a row each.
	var rows [][]rChart
	for _, row := range v.Rows {
		if len(row) > 2 {
			for _, c := range row {
				rows = append(rows, []rChart{c})
			}
			continue
		}
		rows = append(rows, row)
	}
	for _, row := range rows {
		if len(row) == 1 && row[0].Kind == "bars" {
			drawBarsFlow(d, row[0], left, right-left)
			continue
		}
		gap := 12.0
		cw := (right - left - gap*float64(len(row)-1)) / float64(len(row))
		h := 0.0
		for _, c := range row {
			h = math.Max(h, chartHeight(c, cw))
		}
		d.need(h + 12)
		top := d.y
		for i, c := range row {
			x := left + float64(i)*(cw+gap)
			d.frame(x, top, cw, h, pdfBorder)
			drawChart(d, c, x+10, top+10, cw-20)
		}
		d.y = top + h + 12
	}

	// Every URL with its details.
	d.need(60)
	d.y += 14
	d.text(left, d.y, 13, true, pdfText, fmt.Sprintf("%s (%d)", i18n.T(lang, "report.results"), len(v.Details)))
	d.y += 6
	for _, det := range v.Details {
		drawDetail(d, det, left, right)
	}

	// Page numbers ("3 / 12") and the start URL in the footer.
	for i, p := range d.pages {
		d.cur = p
		label := fmt.Sprintf("%d / %d", i+1, len(d.pages))
		d.text(right-textWidth(label, 8, false), pdfH-pdfMargin+4, 8, false, pdfMuted, label)
		d.text(left, pdfH-pdfMargin+4, 8, false, pdfMuted, fit(v.StartURL, right-left-60, 8, false, false))
	}
	return d.write(w, v.Title)
}

// Chart geometry in the PDF.
const (
	chartColumnsH = 130.0 // plot area of a column chart incl. labels
	barRowH       = 12.0
)

// chartHeight is the height of a chart panel of width w.
func chartHeight(c rChart, w float64) float64 {
	h := 10 + 12.0 // padding, title
	if c.Sub != "" {
		h += 11
	}
	switch {
	case len(c.Items) == 0:
		h += 14
	case c.Kind == "share":
		if c.Hero != "" {
			h += 26
		}
		h += 18 + 12*float64(len(legendLines(c, w-20)))
	case c.Kind == "columns":
		h += chartColumnsH
		if anyZone(c.Items) {
			h += 14
		}
	default:
		h += 6 + barRowH*float64(len(c.Items))
	}
	return h + 10
}

// legendLines distributes the legend entries of a share bar over lines.
func legendLines(c rChart, w float64) [][]rItem {
	var lines [][]rItem
	x := w + 1
	for _, it := range c.Items {
		iw := legendWidth(it)
		if x+iw > w && x > 0 {
			lines = append(lines, nil)
			x = 0
		}
		lines[len(lines)-1] = append(lines[len(lines)-1], it)
		x += iw + 14
	}
	return lines
}

func legendWidth(it rItem) float64 {
	return 12 + textWidth(it.Label+" ", 8, false) + textWidth(it.Value, 8, true) + textWidth(" "+it.Share, 8, false)
}

// drawChart draws a chart panel's content with its top left corner at (x, y).
func drawChart(d *pdfDoc, c rChart, x, y, w float64) {
	y += 10
	title := c.Title
	d.text(x, y, 10, true, pdfText, fit(title, w-textWidth(c.Total, 8, false)-10, 10, true, false))
	if c.Total != "" {
		d.text(x+w-textWidth(c.Total, 8, false), y, 8, false, pdfMuted, c.Total)
	}
	if c.Sub != "" {
		y += 11
		d.text(x, y, 7.5, false, pdfMuted, fit(c.Sub, w, 7.5, false, false))
	}
	if len(c.Items) == 0 {
		d.text(x, y+14, 8, false, pdfMuted, c.Empty)
		return
	}
	switch c.Kind {
	case "share":
		if c.Hero != "" {
			y += 26
			d.text(x, y, 20, true, pdfText, c.Hero)
			d.text(x+textWidth(c.Hero, 20, true)+8, y, 9, false, pdfMuted, c.HeroText)
		}
		y += 8
		total := 0.0
		for _, it := range c.Items {
			total += it.N
		}
		const gap = 1.5
		avail := w - gap*float64(len(c.Items)-1)
		sx := x
		for _, it := range c.Items {
			sw := math.Max(2, avail*it.N/total)
			d.rect(sx, y, sw, 12, toneColor(it.Tone))
			sx += sw + gap
		}
		y += 12
		for _, line := range legendLines(c, w) {
			y += 12
			lx := x
			for _, it := range line {
				d.rect(lx, y-6.5, 7, 7, toneColor(it.Tone))
				lx += 11
				d.text(lx, y, 8, false, pdfText, it.Label+" ")
				lx += textWidth(it.Label+" ", 8, false)
				d.text(lx, y, 8, true, pdfText, it.Value)
				lx += textWidth(it.Value, 8, true)
				d.text(lx, y, 8, false, pdfMuted, " "+it.Share)
				lx += textWidth(" "+it.Share, 8, false) + 14
			}
		}
	case "columns":
		drawColumns(d, c, x, y+4, w)
	default:
		drawBars(d, c, x, y+4, w)
	}
}

// drawColumns draws a column chart (see columnsSVG) below y.
func drawColumns(d *pdfDoc, c rChart, x, y, w float64) {
	const top, bottom, left = 14.0, 16.0, 28.0
	plotH := chartColumnsH - top - bottom - 4
	plotW := w - left
	maxN := 0.0
	for _, it := range c.Items {
		maxN = math.Max(maxN, it.N)
	}
	step, maxV := niceScale(maxN)
	base := y + top + plotH
	yv := func(v float64) float64 { return base - v/maxV*plotH }
	band := plotW / math.Max(1, float64(len(c.Items)))
	barW := math.Min(18, band*0.6)
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
		d.rect(x+left+band*float64(from), y+top, band*float64(to-from+1), plotH, pdfZone)
	}
	for v := 0.0; v <= maxV; v += step {
		yy := yv(v)
		d.hline(x+left, x+w, yy, pdfBorder)
		label := fmt.Sprintf("%g", v)
		d.text(x+left-4-textWidth(label, 7, false), yy+2.5, 7, false, pdfMuted, label)
	}
	for i, it := range c.Items {
		cx := x + left + band*float64(i) + band/2
		if it.N > 0 {
			h := math.Max(0.8, base-yv(it.N))
			d.rect(cx-barW/2, base-h, barW, h, toneColor(it.Tone))
			d.text(cx-textWidth(it.Value, 7, true)/2, base-h-3, 7, true, pdfText, it.Value)
		}
		label := fit(it.Label, band-2, 7, false, false)
		d.text(cx-textWidth(label, 7, false)/2, base+11, 7, false, pdfMuted, label)
	}
	d.hline(x+left, x+w, base, pdfMuted)
	if from >= 0 {
		ly := base + bottom + 8
		d.rect(x, ly-6.5, 7, 7, pdfZone)
		d.frame(x, ly-6.5, 7, 7, pdfOK)
		d.text(x+11, ly, 8, false, pdfText, c.Zone)
	}
}

// barLayout holds the columns of a bar list: label, track, value.
type barLayout struct {
	x, w, labelW, trackX, trackW, maxN float64
}

func newBarLayout(c rChart, x, w float64) barLayout {
	l := barLayout{x: x, w: w}
	valueW := 0.0
	for _, it := range c.Items {
		l.maxN = math.Max(l.maxN, it.N)
		l.labelW = math.Max(l.labelW, textWidth(it.Label, 8, false))
		valueW = math.Max(valueW, textWidth(it.Value, 8, false))
	}
	l.labelW = math.Min(l.labelW, w*0.6)
	l.trackX = x + l.labelW + 8
	l.trackW = w - l.labelW - valueW - 16
	return l
}

// row draws one entry with its baseline at y.
func (l barLayout) row(d *pdfDoc, it rItem, y float64) {
	d.text(l.x, y, 8, false, pdfText, fit(it.Label, l.labelW, 8, false, false))
	d.rect(l.trackX, y-7, l.trackW, 7, pdfTrack)
	if l.maxN > 0 {
		d.rect(l.trackX, y-7, math.Max(1, l.trackW*it.N/l.maxN), 7, toneColor(it.Tone))
	}
	d.text(l.x+l.w-textWidth(it.Value, 8, false), y, 8, false, pdfText, it.Value)
}

// drawBars draws a bar list (label · bar · value) below y.
func drawBars(d *pdfDoc, c rChart, x, y, w float64) {
	l := newBarLayout(c, x, w)
	for _, it := range c.Items {
		y += barRowH
		l.row(d, it, y)
	}
}

// drawBarsFlow draws a bar list panel over the full width that may be
// longer than a page: it continues on the next page, every part framed.
func drawBarsFlow(d *pdfDoc, c rChart, left, w float64) {
	bottom := pdfH - pdfMargin - 14
	d.need(10 + 12 + 6 + 3*barRowH + 10)
	top := d.y
	head := rChart{Kind: c.Kind, Title: c.Title, Total: c.Total, Sub: c.Sub} // title only
	if len(c.Items) == 0 {
		head.Empty = c.Empty
	}
	drawChart(d, head, left+10, top+10, w-20)
	y := top + 10 + 10 + 4
	if c.Sub != "" {
		y += 11
	}
	if len(c.Items) == 0 {
		y += 14
	}
	l := newBarLayout(c, left+10, w-20)
	for _, it := range c.Items {
		if y+barRowH+10 > bottom {
			d.frame(left, top, w, y+10-top, pdfBorder)
			d.newPage()
			top = d.y
			y = top + 6
		}
		y += barRowH
		l.row(d, it, y)
	}
	d.frame(left, top, w, y+10-top, pdfBorder)
	d.y = y + 10 + 12
}

// Geometry of the detail views.
const (
	detailSize  = 8.5
	detailLineH = 11.0
	detailLabel = 140.0 // width of the label column
	indentW     = 12.0
)

// drawDetail draws the detail view of one URL: the URL as heading, then the
// fields as label/value rows; long values wrap and continue on the next page.
func drawDetail(d *pdfDoc, det rDetail, left, right float64) {
	head := wrap(det.URL, right-left, 10, true)
	d.need(float64(len(head))*13 + 3*detailLineH + 10)
	d.y += 12
	d.hline(left, right, d.y-2, pdfBorder)
	for _, l := range head {
		d.y += 13
		d.text(left, d.y, 10, true, pdfAccent, l)
		if safeHref(det.URL) {
			d.link(left, d.y-9, textWidth(l, 10, true), 12, det.URL)
		}
	}
	d.y += 3
	valueX := left + detailLabel
	valueW := right - valueX
	for _, f := range det.Fields {
		first := true
		for _, ln := range f.Lines {
			if ln.Gap {
				d.y += 5
			}
			x := valueX + float64(ln.Indent)*indentW
			if ln.Bullet != "" {
				x += indentW
			}
			avail := valueX + valueW - x
			prefixW := 0.0
			if ln.Prefix != "" {
				prefixW = textWidth(ln.Prefix+" ", detailSize, false)
			}
			badgeW := 0.0
			if ln.Badge != "" {
				badgeW = textWidth(ln.Badge, 7, true) + 14
			}
			parts := wrapFirst(ln.Text, avail-prefixW, avail, detailSize, ln.Bold, badgeW)
			color := toneColor(ln.Tone)
			if ln.Muted {
				color = pdfMuted
			} else if ln.Link {
				color = pdfAccent
			}
			for i, p := range parts {
				d.need(detailLineH)
				d.y += detailLineH
				if first {
					d.text(left, d.y, detailSize, false, pdfMuted, fit(f.Label, detailLabel-10, detailSize, false, false))
					first = false
				}
				px := x
				if i == 0 {
					if ln.Bullet != "" {
						d.text(x-indentW, d.y, detailSize, false, pdfMuted, ln.Bullet)
					}
					if ln.Prefix != "" {
						d.text(x, d.y, detailSize, false, pdfMuted, ln.Prefix)
						px += prefixW
					}
				}
				d.text(px, d.y, detailSize, ln.Bold, color, p)
				if ln.Link && safeHref(ln.Text) {
					d.link(px, d.y-8, textWidth(p, detailSize, ln.Bold), 10.5, ln.Text)
				}
				if i == len(parts)-1 && ln.Badge != "" {
					bx := px + textWidth(p, detailSize, ln.Bold) + 6
					bw := textWidth(ln.Badge, 7, true) + 6
					d.frame(bx, d.y-7.5, bw, 9.5, color)
					d.text(bx+3, d.y-0.5, 7, true, color, ln.Badge)
				}
			}
		}
	}
	d.y += 4
}

// wrap breaks s into lines of at most width points (long words such as
// URLs are broken anywhere).
func wrap(s string, width, size float64, bold bool) []string {
	return wrapFirst(s, width, width, size, bold, 0)
}

// wrapFirst is wrap with a different width for the first line and room
// for reserve points after the last line.
func wrapFirst(s string, firstW, restW, size float64, bold bool, reserve float64) []string {
	var lines []string
	cur := ""
	width := func() float64 {
		if len(lines) == 0 {
			return firstW
		}
		return restW
	}
	for _, word := range strings.Fields(s) {
		cand := word
		if cur != "" {
			cand = cur + " " + word
		}
		if textWidth(cand, size, bold) <= width() {
			cur = cand
			continue
		}
		if cur != "" {
			lines = append(lines, cur)
			cur = ""
		}
		// a word that is longer than a line is broken anywhere
		for textWidth(word, size, bold) > width() {
			rs := []rune(word)
			n := len(rs) - 1
			for n > 1 && textWidth(string(rs[:n]), size, bold) > width() {
				n--
			}
			lines = append(lines, string(rs[:n]))
			word = string(rs[n:])
		}
		cur = word
	}
	if cur != "" || len(lines) == 0 {
		lines = append(lines, cur)
	}
	lastW := restW
	if len(lines) == 1 {
		lastW = firstW
	}
	if reserve > 0 && textWidth(lines[len(lines)-1], size, bold)+reserve > lastW {
		lines = append(lines, "")
	}
	return lines
}
