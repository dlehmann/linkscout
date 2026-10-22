package server

// The content of the HTML and PDF reports, already translated and
// formatted, so both renderers show the same thing: the key figures and
// charts of the overview page and, for every URL, what the detail dialog of
// the results list shows (see openDetail, seoRows and issueContext in
// web/app.js – keep them in step).

import (
	"cmp"
	"fmt"
	"math"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dlehmann/linkscout/internal/crawler"
	"github.com/dlehmann/linkscout/internal/i18n"
)

// reportView is everything a report shows.
type reportView struct {
	Lang     i18n.Lang
	Title    string
	Created  string
	StartURL string
	Scope    string // whether external links are counted in the figures
	KPIs     []rKPI
	Rows     [][]rChart // charts, row by row (1–3 per row)
	Details  []rDetail
}

type rKPI struct{ Value, Label, Tone string }

// rChart is one panel of the overview. Kind "share" is a 100 % bar with a
// legend, "columns" a column chart, "bars" a bar list.
type rChart struct {
	Kind     string
	Title    string
	Sub      string
	Total    string // shown right of the title (size by MIME type)
	Hero     string // big figure above a share bar ("92 %")
	HeroText string
	Zone     string // legend label of the recommended columns
	Items    []rItem
	Empty    string // shown instead of the chart when there are no items
}

type rItem struct {
	Label  string
	N      float64 // raw value, for the scale
	Value  string  // formatted value
	Share  string  // share of the total ("45.5 %"), share bars only
	Tone   string  // ok, info, bad, warn, err, accent
	InZone bool    // column inside the recommended range
}

// rDetail is the detail view of one URL.
type rDetail struct {
	URL    string
	Fields []rField
}

type rField struct {
	Label string
	Lines []rLine
}

// rLine is one line (or wrapped paragraph) of a detail value.
type rLine struct {
	Text   string
	Link   bool   // Text is a URL
	Prefix string // muted label in front of the text
	Bullet string // "•" or "1." …
	Indent int
	Bold   bool
	Muted  bool
	Tone   string // colour of the text
	Badge  string // severity label after the text (issue headings)
	Gap    bool   // space above (between two issues)
}

// reportOptions tune the report content.
type reportOptions struct {
	External bool // count external links in the figures
}

// newReportView builds the report content in the given language.
func newReportView(rep crawler.Report, lang i18n.Lang, opt reportOptions) reportView {
	f := formatter{lang}
	v := reportView{
		Lang:     lang,
		StartURL: rep.StartURL,
		Title:    strings.ReplaceAll(f.t("report.title"), "%url%", rep.StartURL),
		Created: strings.NewReplacer("%date%", f.dateTime(rep.CreatedAt),
			"%generator%", rep.Generator).Replace(f.t("report.created")),
	}
	// The issues decide the SEO health; old reports may lack them.
	results := slices.Clone(rep.Results)
	crawler.Analyze(results)
	ov := crawler.NewOverview(results, opt.External)
	if ov.ExternalIncluded {
		v.Scope = f.t("report.scope_external")
	} else if ov.ExternalCount > 0 {
		v.Scope = f.tv("report.scope_site", "n", f.num(ov.ExternalCount))
	}
	v.KPIs = []rKPI{
		{f.num(ov.Total), f.t("stats.total"), ""},
		{f.num(ov.OK), f.t("stats.ok"), "ok"},
		{f.num(ov.Redirected), f.t("stats.redirected"), "info"},
		{f.num(ov.Broken), f.t("stats.broken"), toneIf(ov.Broken > 0, "bad")},
		{f.num(ov.Errors), f.t("stats.errors"), toneIf(ov.Errors > 0, "err")},
		{f.num(ov.InternalPages), f.t("stats.pages"), ""},
		{fmtMs(ov.AvgMs), f.t("stats.avg_time"), ""},
	}
	v.Rows = overviewCharts(ov, f)
	d := detailBuilder{f: f, results: results, byURL: map[string]*crawler.Result{}}
	for i := range results {
		d.byURL[results[i].URL] = &results[i]
	}
	for _, r := range results {
		v.Details = append(v.Details, d.detail(r))
	}
	return v
}

func toneIf(ok bool, tone string) string {
	if ok {
		return tone
	}
	return ""
}

// overviewCharts lays out the charts of the overview page in its order.
func overviewCharts(ov crawler.Overview, f formatter) [][]rChart {
	none := f.t("stats.none")
	share := func(items []crawler.Count, label, tone func(string) string) []rItem {
		total := 0
		for _, c := range items {
			total += c.Count
		}
		var out []rItem
		for _, c := range items {
			out = append(out, rItem{Label: label(c.Key), N: float64(c.Count), Value: f.num(c.Count),
				Share: f.pct(c.Count, total), Tone: tone(c.Key)})
		}
		return out
	}
	var rows [][]rChart
	rows = append(rows, []rChart{{Kind: "share", Title: f.t("stats.chart_status"), Empty: none,
		Items: share(ov.StatusClasses, func(k string) string { return f.t("status." + k) }, classTone)}})

	if s := ov.SEO; s != nil {
		sevLabel := func(k string) string {
			if k == crawler.SevError || k == crawler.SevWarning {
				return f.t("sev." + k)
			}
			return f.t("filter.sev_" + k)
		}
		rows = append(rows, []rChart{{Kind: "share", Title: f.t("seo.health"), Sub: f.t("seo.health_sub"),
			Hero:     strconv.Itoa(int(math.Round(float64(s.PagesWithoutErrors)/float64(s.Pages)*100))) + " %",
			HeroText: f.t("seo.hero"), Items: share(s.Health, sevLabel, severityTone), Empty: none}})
		lengths := func(title, sub string, bs []crawler.LengthBucket) rChart {
			c := rChart{Kind: "columns", Title: title, Sub: sub, Zone: f.t("seo.recommended")}
			for i, b := range bs {
				c.Items = append(c.Items, rItem{Label: lengthLabel(f, bs, i), N: float64(b.Count),
					Value: f.num(b.Count), Tone: "accent", InZone: b.Recommended})
			}
			return c
		}
		rows = append(rows, []rChart{
			lengths(f.t("seo.title_len"), f.t("seo.title_len_sub"), s.TitleLength),
			lengths(f.t("seo.desc_len"), f.t("seo.desc_len_sub"), s.DescriptionLength),
		})
	}

	times := rChart{Kind: "columns", Title: f.t("stats.chart_time"), Sub: f.t("stats.chart_time_sub")}
	for i, b := range ov.ResponseTimes {
		times.Items = append(times.Items, rItem{Label: timeLabel(f, i), N: float64(b.Count), Value: f.num(b.Count), Tone: "accent"})
	}
	clicks := rChart{Kind: "columns", Title: f.t("stats.chart_depth"), Sub: f.t("stats.chart_depth_sub")}
	for _, c := range ov.Clicks {
		d, _ := strconv.Atoi(strings.TrimSuffix(c.Key, "+"))
		clicks.Items = append(clicks.Items, rItem{Label: clickLabel(f, d), N: float64(c.Count), Value: f.num(c.Count), Tone: "accent"})
	}
	rows = append(rows, []rChart{times, clicks})

	codes := rChart{Kind: "bars", Title: f.t("stats.by_code"), Empty: none}
	for _, c := range ov.StatusCodes {
		code, _ := strconv.Atoi(c.Key)
		codes.Items = append(codes.Items, rItem{Label: c.Key, N: float64(c.Count), Value: f.num(c.Count), Tone: classTone(strconv.Itoa(code/100) + "xx")})
	}
	mimeLabel := func(k string) string {
		if k == "" {
			return f.t("stats.unknown_type")
		}
		return k
	}
	types := rChart{Kind: "bars", Title: f.t("stats.by_type"), Empty: none}
	for _, c := range ov.MimeTypes {
		types.Items = append(types.Items, rItem{Label: mimeLabel(c.Key), N: float64(c.Count), Value: f.num(c.Count), Tone: "accent"})
	}
	sizes := rChart{Kind: "bars", Title: f.t("stats.size_by_type"), Empty: none,
		Total: strings.ReplaceAll(f.t("stats.size_total"), "%size%", f.bytes(ov.TotalBytes))}
	for _, c := range ov.SizeByMime {
		sizes.Items = append(sizes.Items, rItem{Label: mimeLabel(c.Key), N: float64(c.Bytes), Value: f.bytes(c.Bytes), Tone: "accent"})
	}
	return append(rows, []rChart{codes, types, sizes})
}

// classTone is the colour of a status class (the status palette of the UI).
func classTone(k string) string {
	switch k {
	case "2xx":
		return "ok"
	case "3xx":
		return "info"
	case "4xx":
		return "bad"
	case "5xx":
		return "warn"
	case "error":
		return "err"
	}
	return "muted"
}

func severityTone(s string) string {
	switch s {
	case crawler.SevError:
		return "bad"
	case crawler.SevWarning:
		return "warn"
	case crawler.SevNotice:
		return "info"
	}
	return "ok"
}

func statusTone(r crawler.Result) string {
	if r.Pending {
		return "muted"
	}
	return classTone(crawler.StatusClass(r))
}

// lengthLabel: "missing", "≤ 9", "10–25", "> 80".
func lengthLabel(f formatter, bs []crawler.LengthBucket, i int) string {
	b := bs[i]
	switch {
	case i == 0:
		return f.t("seo.missing")
	case i == 1:
		return "≤ " + strconv.Itoa(b.Max)
	case b.Max == 0:
		return "> " + strconv.Itoa(b.Min-1)
	}
	return fmt.Sprintf("%d–%d", b.Min, b.Max)
}

// timeLabel of response time bucket i ("< 100 ms", "1–2 s" …).
func timeLabel(f formatter, i int) string {
	labels := []string{"< 100 ms", "100–250 ms", "250–500 ms", f.decimal("0.5") + "–1 s", "1–2 s", "2–5 s", "≥ 5 s"}
	if i < len(labels) {
		return labels[i]
	}
	return ""
}

// clickLabel: "Start page", "1 click", "2 clicks", "10+ clicks", "not linked".
func clickLabel(f formatter, d int) string {
	switch {
	case d == crawler.NotLinked:
		return f.t("stats.not_linked")
	case d == 0:
		return f.t("stats.start_page")
	case d == 1:
		return f.t("stats.one_click")
	case d >= crawler.MaxClicks:
		return f.tv("stats.n_clicks", "n", strconv.Itoa(crawler.MaxClicks)+"+")
	}
	return f.tv("stats.n_clicks", "n", strconv.Itoa(d))
}

// --- details ------------------------------------------------------------------

type detailBuilder struct {
	f       formatter
	results []crawler.Result
	byURL   map[string]*crawler.Result
}

func text(s string) []rLine { return []rLine{{Text: s}} }
func link(u string) []rLine { return []rLine{{Text: u, Link: true}} }

// list turns items into bullet lines – all of them: unlike the detail
// dialog of the UI, a report never shortens a list.
func (d detailBuilder) list(items []string, isLink bool, quote bool) []rLine {
	var out []rLine
	for _, s := range items {
		if quote {
			s = d.f.quote(s)
		}
		out = append(out, rLine{Text: s, Link: isLink, Bullet: "•"})
	}
	return out
}

// one shows a single value as text and several as a list ("–" for none).
func (d detailBuilder) one(items []string) []rLine {
	switch len(items) {
	case 0:
		return text("–")
	case 1:
		return text(items[0])
	}
	return d.list(items, false, false)
}

func (d detailBuilder) detail(r crawler.Result) rDetail {
	f := d.f
	out := rDetail{URL: r.URL}
	add := func(key string, lines []rLine) { out.Fields = append(out.Fields, rField{f.t(key), lines}) }
	status := "ERR"
	if r.Pending {
		status = "…"
	} else if r.Status > 0 {
		status = strconv.Itoa(r.Status)
	}
	add("col.status", []rLine{{Text: status, Bold: true, Tone: statusTone(r)}})
	if r.Error != "" {
		add("detail.error", text(errText(f, r.Error)))
	}
	if r.Title != "" {
		add("col.title", text(r.Title))
	}
	if r.FinalURL != "" {
		add("detail.final_url", link(r.FinalURL))
	}
	if len(r.Redirects) > 0 {
		add("detail.redirects", chain(r))
	}
	add("col.type", text(cmp.Or(crawler.ResultMime(r), "–")))
	add("col.kind", text(f.t("kind."+r.Kind)))
	if r.ContentType != "" {
		add("detail.content_type", text(r.ContentType))
	}
	add("detail.size", text(f.bytes(r.Size)))
	add("detail.duration", text(fmtMs(r.DurationMs)))
	add("detail.depth", text(strconv.Itoa(r.Depth)))
	if !r.CheckedAt.IsZero() {
		add("detail.checked_at", text(f.dateTime(r.CheckedAt)))
	}
	if len(r.FoundOn) > 0 {
		add("detail.found_on", d.list(r.FoundOn, true, false))
	}
	if r.Internal && r.Depth > 0 {
		n := strconv.Itoa(len(r.FoundOn))
		if len(r.FoundOn) >= 50 {
			n = "50+"
		}
		add("detail.inlinks", text(n))
	}
	if r.InSitemap {
		add("detail.in_sitemap", text(f.t("misc.yes")))
	}
	if r.RobotsBlocked {
		add("detail.robots_blocked", text(f.t("misc.yes")))
	}

	if len(r.Issues) > 0 {
		rank := map[string]int{crawler.SevError: 0, crawler.SevWarning: 1, crawler.SevNotice: 2}
		ids := slices.Clone(r.Issues)
		slices.SortStableFunc(ids, func(a, b string) int {
			return rank[crawler.IssueSeverity(a)] - rank[crawler.IssueSeverity(b)]
		})
		var lines []rLine
		for i, id := range ids {
			sev := crawler.IssueSeverity(id)
			lines = append(lines,
				rLine{Text: f.t("issue." + id), Bold: true, Tone: severityTone(sev), Badge: f.t("sev." + sev), Gap: i > 0},
				rLine{Text: f.t("issue." + id + ".help"), Muted: true})
			for _, c := range d.issueContext(id, r) {
				c.Indent++
				lines = append(lines, c)
			}
		}
		add("detail.seo", lines)
	}

	if p := r.SEO; p != nil {
		if len(p.Titles) > 1 {
			add("detail.seo_title", d.one(p.Titles))
		}
		add("detail.description", d.one(p.Descriptions))
		if len(p.Robots) > 0 {
			add("detail.robots", d.one(p.Robots))
		}
		if len(p.Canonicals) > 0 {
			add("detail.canonical", d.links(p.Canonicals))
		} else {
			add("detail.canonical", text("–"))
		}
		add("detail.h1", d.one(p.H1))
		add("detail.lang", text(cmp.Or(p.Lang, "–")))
		if len(p.Hreflang) > 0 {
			add("detail.hreflang", hreflang(p))
		}
		if p.JSONLD > 0 {
			s := strconv.Itoa(p.JSONLD)
			if len(p.JSONLDErrors) > 0 {
				s += " – " + strings.Join(p.JSONLDErrors, "; ")
			}
			add("detail.jsonld", text(s))
		}
		if r.Depth == 0 && !p.CertNotAfter.IsZero() {
			add("detail.cert", text(f.date(p.CertNotAfter)))
		}
	}
	return out
}

func (d detailBuilder) links(urls []string) []rLine {
	var out []rLine
	for _, u := range urls {
		out = append(out, rLine{Text: u, Link: true})
	}
	return out
}

// chain lists the redirect hops and the final target.
func chain(r crawler.Result) []rLine {
	hops := slices.Clone(r.Redirects)
	if r.FinalURL != "" {
		hops = append(hops, r.FinalURL)
	}
	var out []rLine
	for i, u := range hops {
		out = append(out, rLine{Text: u, Link: true, Bullet: strconv.Itoa(i+1) + "."})
	}
	return out
}

func hreflang(p *crawler.PageSEO) []rLine {
	var out []rLine
	for _, a := range p.Hreflang {
		out = append(out, rLine{Prefix: a.Lang + ":", Text: a.URL, Link: true, Bullet: "•"})
	}
	return out
}

// issueContext is what exactly an issue found on this URL (issueContext in app.js).
func (d detailBuilder) issueContext(id string, r crawler.Result) []rLine {
	f := d.f
	p := r.SEO
	if p == nil {
		p = &crawler.PageSEO{}
	}
	chars := func(s string) string {
		return f.tv("detail.ctx_chars", "n", f.num(utf8.RuneCountInString(s)))
	}
	labelled := func(key string, lines []rLine) []rLine {
		if len(lines) == 0 {
			return nil
		}
		return append([]rLine{{Text: f.t(key) + ":", Muted: true}}, lines...)
	}
	others := func(value func(*crawler.PageSEO) string) []rLine {
		own := value(p)
		var urls []string
		for _, x := range d.results {
			if x.URL != r.URL && x.SEO != nil && own != "" && value(x.SEO) == own {
				urls = append(urls, x.URL)
			}
		}
		return labelled("detail.ctx_also_on", d.list(urls, true, false))
	}
	quoted := func(list []string) []rLine { return []rLine{{Text: f.quote(first(list))}} }
	withLength := func(list []string) []rLine {
		return []rLine{{Text: f.quote(first(list)) + " – " + chars(first(list))}}
	}
	switch id {
	case "title_long", "title_short":
		return withLength(p.Titles)
	case "title_multiple":
		return d.list(p.Titles, false, true)
	case "title_duplicate":
		return append(quoted(p.Titles), others(func(s *crawler.PageSEO) string { return first(s.Titles) })...)
	case "desc_long", "desc_short":
		return withLength(p.Descriptions)
	case "desc_multiple":
		return d.list(p.Descriptions, false, true)
	case "desc_duplicate":
		return append(quoted(p.Descriptions), others(func(s *crawler.PageSEO) string { return first(s.Descriptions) })...)
	case "h1_multiple":
		return d.list(p.H1, false, true)
	case "h1_duplicate":
		return append(quoted(p.H1), others(func(s *crawler.PageSEO) string { return first(s.H1) })...)
	case "duplicate_content":
		return others(func(s *crawler.PageSEO) string { return s.ContentHash })
	case "heading_skip":
		var hs []string
		for i, l := range p.Headings {
			if i == 30 {
				break
			}
			hs = append(hs, "h"+strconv.Itoa(l))
		}
		return []rLine{{Prefix: f.t("detail.ctx_outline") + ":", Text: strings.Join(hs, " → ")}}
	case "noindex":
		return []rLine{{Prefix: f.t("detail.robots") + ":", Text: strings.Join(p.Robots, " · ")}}
	case "canonical_broken", "canonical_other", "canonical_multiple":
		return labelled("detail.canonical", d.links(p.Canonicals))
	case "img_no_alt":
		return d.list(p.ImagesNoAlt, true, false)
	case "mixed_content":
		return d.list(p.Mixed, true, false)
	case "fragment_missing":
		return d.list(crawler.BrokenFragments(p, func(u string) *crawler.Result { return d.byURL[u] }), true, false)
	case "jsonld_invalid":
		return d.list(p.JSONLDErrors, false, false)
	case "security_headers":
		return d.list(p.Security, false, false)
	case "cert_expiring":
		return []rLine{{Prefix: f.t("detail.cert") + ":", Text: f.date(p.CertNotAfter)}}
	case "redirect_chain", "link_to_redirect":
		out := chain(r)
		if id == "link_to_redirect" && len(r.FoundOn) > 0 {
			out = append(out, labelled("detail.found_on", d.list(r.FoundOn, true, false))...)
		}
		return out
	case "few_inlinks":
		return labelled("detail.found_on", d.list(r.FoundOn, true, false))
	case "deep_page":
		return text(clickLabel(f, r.Depth))
	case "img_large":
		return text(f.bytes(r.Size))
	case "url_long":
		return text(chars(r.URL))
	case "hreflang_invalid", "hreflang_broken", "hreflang_no_return", "hreflang_canonical_conflict":
		return hreflang(p)
	}
	return nil
}

func first(list []string) string {
	if len(list) == 0 {
		return ""
	}
	return list[0]
}

// errText translates the fixed error markers set by the crawler.
func errText(f formatter, e string) string {
	switch e {
	case "aborted":
		return f.t("error.aborted")
	case "not checked":
		return f.t("error.not_checked")
	}
	return e
}

// --- formatting -----------------------------------------------------------------

// formatter formats numbers, sizes and dates like the UI does for a language
// (German: 1.234,5; English: 1,234.5).
type formatter struct{ lang i18n.Lang }

func (f formatter) t(key string) string { return i18n.T(f.lang, key) }

// tv translates key and replaces the placeholder %name%.
func (f formatter) tv(key, name, value string) string {
	return strings.ReplaceAll(i18n.T(f.lang, key), "%"+name+"%", value)
}

func (f formatter) separators() (group, dec string) {
	if f.lang == i18n.DE {
		return ".", ","
	}
	return ",", "."
}

// num formats an integer with digit grouping.
func (f formatter) num(n int) string { return f.num64(int64(n)) }

func (f formatter) num64(n int64) string {
	group, _ := f.separators()
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteString(group)
		}
		b.WriteRune(c)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

// decimal swaps the decimal point of a formatted number for the language's one.
func (f formatter) decimal(s string) string {
	_, dec := f.separators()
	return strings.Replace(s, ".", dec, 1)
}

// fixed formats x with the given number of decimals and digit grouping.
func (f formatter) fixed(x float64, digits int) string {
	s := strconv.FormatFloat(x, 'f', digits, 64)
	whole, frac, _ := strings.Cut(s, ".")
	n, _ := strconv.ParseInt(whole, 10, 64)
	out := f.num64(n)
	if frac != "" {
		_, dec := f.separators()
		out += dec + frac
	}
	return out
}

// bytes uses binary units like the UI: B, KiB, MiB, GiB, TiB; "–" for unknown.
func (f formatter) bytes(n int64) string {
	if n <= 0 {
		return "–"
	}
	units := []string{"B", "KiB", "MiB", "GiB", "TiB"}
	x := float64(n)
	i := 0
	for x >= 1024 && i < len(units)-1 {
		x /= 1024
		i++
	}
	digits := 0
	if i > 0 {
		digits = 1
	}
	return f.fixed(x, digits) + " " + units[i]
}

// pct is n of total in percent with at most one decimal ("45.5 %", "50 %").
func (f formatter) pct(n, total int) string {
	if total == 0 {
		return "0 %"
	}
	s := f.fixed(math.Round(float64(n)/float64(total)*1000)/10, 1)
	_, dec := f.separators()
	return strings.TrimSuffix(s, dec+"0") + " %"
}

// quote puts s in the quotation marks of the language.
func (f formatter) quote(s string) string {
	if f.lang == i18n.DE {
		return "„" + s + "“"
	}
	return "“" + s + "”"
}

func (f formatter) dateTime(t time.Time) string {
	if f.lang == i18n.DE {
		return t.Local().Format("02.01.2006, 15:04:05")
	}
	return t.Local().Format(time.DateTime)
}

func (f formatter) date(t time.Time) string {
	if f.lang == i18n.DE {
		return t.Local().Format("02.01.2006")
	}
	return t.Local().Format(time.DateOnly)
}

// fmtMs formats a duration in milliseconds like the UI ("120 ms", "1.5 s").
func fmtMs(ms int64) string {
	if ms < 1000 {
		return strconv.FormatInt(ms, 10) + " ms"
	}
	return strconv.FormatFloat(float64(ms)/1000, 'f', 1, 64) + " s"
}

// safeHref keeps http(s) URLs as link targets; anything else is not linked.
func safeHref(u string) bool {
	p, err := url.Parse(u)
	return err == nil && (p.Scheme == "http" || p.Scheme == "https")
}
