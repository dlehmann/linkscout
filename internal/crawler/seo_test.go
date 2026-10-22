package crawler

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/dlehmann/linkscout/internal/fetch"
	"github.com/dlehmann/linkscout/internal/links"
)

func fetchResponse(body string) fetch.Response {
	return fetch.Response{Status: 200, ContentType: "text/html", Body: []byte(body)}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// page builds an internal HTML result with SEO facts parsed from body.
func page(t *testing.T, u, body string, mutate ...func(*Result)) Result {
	t.Helper()
	pu, _ := url.Parse(u)
	doc := links.ParseHTML([]byte(body), pu)
	r := Result{URL: u, Status: 200, Kind: KindInternal, Internal: true, Depth: 1,
		FoundOn: []string{"https://e.com/", "https://e.com/x"}, Category: links.CatPage,
		SEO: pageSEO(doc, fetchResponse(body), pu)}
	for _, m := range mutate {
		m(&r)
	}
	return r
}

const goodHead = `<html lang="en"><head><meta name="viewport" content="width=device-width">
<meta property="og:title" content="x"><meta property="og:image" content="/i.png">`

func good(title, extra string) string {
	return goodHead + `<title>` + title + `</title><meta name="description" content="A description that is long enough to pass the minimum length.">` + extra + `</head><body><h1>` + title + `</h1>`
}

func issuesOf(rs []Result, u string) []string {
	for _, r := range rs {
		if r.URL == u {
			out := append([]string(nil), r.Issues...)
			sort.Strings(out)
			return out
		}
	}
	return nil
}

func TestAnalyzePageChecks(t *testing.T) {
	long := strings.Repeat("Long title ", 8)
	rs := []Result{
		page(t, "https://e.com/ok", good("A good page title", `<link rel="canonical" href="/ok">`)),
		page(t, "https://e.com/bad", `<html><head><title>x</title><title>y</title><meta name="robots" content="noindex,follow"></head><body>
			<h2>a</h2><h4 id="here">b</h4><img src="/a.png"><a href="/ok#nothere">x</a><a href="#here">y</a>
			<script type="application/ld+json">{bad json}</script></body>`),
		page(t, "https://e.com/long", good(long, `<link rel="canonical" href="/gone">`)),
		{URL: "https://e.com/gone", Status: 404, Kind: KindInternal, Internal: true, Depth: 2, FoundOn: []string{"https://e.com/long"}},
	}
	Analyze(rs)
	if got := issuesOf(rs, "https://e.com/ok"); got != nil {
		t.Errorf("a clean page has issues: %v", got)
	}
	want := []string{"canonical_missing", "desc_missing", "fragment_missing", "h1_missing", "heading_skip",
		"img_no_alt", "jsonld_invalid", "lang_missing", "noindex", "social_missing", "title_multiple", "title_short", "viewport_missing"}
	if got := issuesOf(rs, "https://e.com/bad"); !reflect.DeepEqual(got, want) {
		t.Errorf("bad page:\n got %v\nwant %v", got, want)
	}
	want = []string{"canonical_broken", "canonical_other", "title_long"}
	if got := issuesOf(rs, "https://e.com/long"); !reflect.DeepEqual(got, want) {
		t.Errorf("long page:\n got %v\nwant %v", got, want)
	}
}

func TestAnalyzeDuplicates(t *testing.T) {
	body := good("Same title for both", "")
	rs := []Result{
		page(t, "https://e.com/a", body),
		page(t, "https://e.com/b", body),
		// a canonicalised copy is not a duplicate problem
		page(t, "https://e.com/c", good("Same title for both", `<link rel="canonical" href="https://e.com/a">`)),
	}
	Analyze(rs)
	for _, u := range []string{"https://e.com/a", "https://e.com/b"} {
		got := issuesOf(rs, u)
		for _, id := range []string{"title_duplicate", "desc_duplicate", "h1_duplicate", "duplicate_content"} {
			if !contains(got, id) {
				t.Errorf("%s lacks %s: %v", u, id, got)
			}
		}
	}
	if got := issuesOf(rs, "https://e.com/c"); contains(got, "title_duplicate") {
		t.Errorf("canonicalised page counted as duplicate: %v", got)
	}
}

func TestAnalyzeURLAndSitemapChecks(t *testing.T) {
	rs := []Result{
		{URL: "https://e.com/Old_Page?id=1", Status: 200, Kind: KindInternal, Internal: true, Depth: 5,
			Redirects: []string{"https://e.com/Old_Page?id=1", "https://e.com/mid"}, FinalURL: "https://e.com/new",
			FoundOn: []string{"https://e.com/"}},
		{URL: "https://e.com/orphan", Status: 200, Kind: KindInternal, Internal: true, Depth: NotLinked, InSitemap: true},
		{URL: "https://e.com/gone", Status: 404, Kind: KindInternal, Internal: true, Depth: NotLinked, InSitemap: true},
		{URL: "https://e.com/blocked", Status: 200, Kind: KindInternal, Internal: true, Depth: 1, RobotsBlocked: true,
			FoundOn: []string{"https://e.com/", "https://e.com/a"}},
		{URL: "https://e.com/big.jpg", Status: 200, Kind: KindFile, Internal: true, Category: links.CatImage, Size: 500 << 10, Depth: 1,
			FoundOn: []string{"https://e.com/", "https://e.com/a"}},
		{URL: "https://e.com/loop", Kind: KindInternal, Internal: true, Error: "redirect loop", Depth: 1,
			FoundOn: []string{"https://e.com/", "https://e.com/a"}},
		{URL: "https://e.com/" + strings.Repeat("a", 120), Status: 200, Kind: KindInternal, Internal: true, Depth: 1,
			FoundOn: []string{"https://e.com/", "https://e.com/a"}},
	}
	Analyze(rs)
	for u, want := range map[string][]string{
		rs[0].URL: {"deep_page", "link_to_redirect", "redirect_chain", "url_params", "url_underscore", "url_uppercase"}, // a redirect is not "linked once"
		rs[1].URL: {"sitemap_orphan"},
		rs[2].URL: {"sitemap_error", "sitemap_orphan"},
		rs[3].URL: {"robots_blocked"},
		rs[4].URL: {"img_large"},
		rs[5].URL: {"redirect_loop"},
		rs[6].URL: {"url_long"},
	} {
		if got := issuesOf(rs, u); !reflect.DeepEqual(got, want) {
			t.Errorf("%s:\n got %v\nwant %v", u, got, want)
		}
	}
	// redirected pages are not "linked only once" pages: few_inlinks only for 200 without redirect
	if contains(issuesOf(rs, rs[2].URL), "few_inlinks") {
		t.Error("few_inlinks on an error page")
	}
}

func TestAnalyzeHreflangAndSiteChecks(t *testing.T) {
	rs := []Result{
		page(t, "https://e.com/", good("Start page title", `<link rel="canonical" href="/">
			<link rel="alternate" hreflang="de" href="/de"><link rel="alternate" hreflang="english" href="/en">`),
			func(r *Result) {
				r.Depth = 0
				r.SEO.Security = []string{"Strict-Transport-Security"}
				r.SEO.CertNotAfter = time.Now().Add(10 * 24 * time.Hour)
			}),
		page(t, "https://e.com/de", good("Deutsche Startseite", `<link rel="canonical" href="/de">`)), // no return link
		page(t, "https://e.com/en", `<html><head><title>English start page</title></head><body><h1>x</h1>`, func(r *Result) { r.Status = 404 }),
		page(t, "https://e.com/copy", good("Copy of the start", `<link rel="canonical" href="/"><link rel="alternate" hreflang="de" href="/de">`)),
	}
	Analyze(rs)
	start := issuesOf(rs, "https://e.com/")
	for _, id := range []string{"hreflang_invalid", "hreflang_no_return", "security_headers", "cert_expiring"} {
		if !contains(start, id) {
			t.Errorf("start page lacks %s: %v", id, start)
		}
	}
	if !contains(start, "hreflang_broken") { // /en answers 404
		t.Errorf("start page lacks hreflang_broken: %v", start)
	}
	if got := issuesOf(rs, "https://e.com/copy"); !contains(got, "hreflang_canonical_conflict") {
		t.Errorf("copy: %v", got)
	}
	// Security headers and the certificate are only reported on the start page.
	if got := issuesOf(rs, "https://e.com/de"); contains(got, "security_headers") || contains(got, "cert_expiring") {
		t.Errorf("site-wide checks repeated on other pages: %v", got)
	}
}

func TestAnalyzeCounts(t *testing.T) {
	rs := []Result{
		page(t, "https://e.com/a", `<title>x</title>`),
		page(t, "https://e.com/b", `<title>y</title>`),
		{URL: "https://e.com/p", Pending: true, Issues: []string{"stale"}},
	}
	counts := Analyze(rs)
	byID := map[string]int{}
	for i, c := range counts {
		byID[c.ID] = c.Count
		if i > 0 && indexOf(counts[i-1].ID) > indexOf(c.ID) {
			t.Errorf("counts not in catalogue order: %v", counts)
		}
	}
	if byID["h1_missing"] != 2 || byID["title_short"] != 2 {
		t.Errorf("counts = %v", byID)
	}
	if rs[2].Issues != nil {
		t.Error("pending results must not keep issues")
	}
}

func indexOf(id string) int {
	for i, d := range Issues {
		if d.ID == id {
			return i
		}
	}
	return -1
}

func TestIssueIDsUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, d := range Issues {
		if seen[d.ID] {
			t.Errorf("duplicate issue id %q", d.ID)
		}
		seen[d.ID] = true
		if d.Severity != SevError && d.Severity != SevWarning && d.Severity != SevNotice {
			t.Errorf("%s: unknown severity %q", d.ID, d.Severity)
		}
	}
}

func TestPageSEOFromResponse(t *testing.T) {
	pu, _ := url.Parse("https://e.com/p")
	body := `<html><head><title>T</title><link rel="stylesheet" href="http://cdn.example/x.css"></head><body><img src="http://e.com/i.png" alt=""></body>`
	resp := fetchResponse(body)
	resp.Header = http.Header{
		"X-Robots-Tag":    {"NoIndex"},
		"Link":            {`<https://e.com/canonical>; rel="canonical"`},
		"X-Frame-Options": {"DENY"},
	}
	p := pageSEO(links.ParseHTML([]byte(body), pu), resp, pu)
	if !p.noindex() || !reflect.DeepEqual(p.Canonicals, []string{"https://e.com/canonical"}) {
		t.Errorf("header robots/canonical: %v %v", p.Robots, p.Canonicals)
	}
	if len(p.Mixed) != 2 || len(p.ImagesNoAlt) != 0 {
		t.Errorf("mixed=%v noAlt=%v", p.Mixed, p.ImagesNoAlt)
	}
	if !reflect.DeepEqual(p.Security, []string{"Strict-Transport-Security", "X-Content-Type-Options", "Referrer-Policy"}) {
		t.Errorf("security = %v", p.Security)
	}
	if p.ContentHash == "" {
		t.Error("no content hash")
	}
}

func TestCompareAndCSV(t *testing.T) {
	old := Report{CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), Results: []Result{
		{URL: "https://e.com/", Status: 200, Kind: KindInternal},
		{URL: "https://e.com/a", Status: 200, Kind: KindInternal},
		{URL: "https://e.com/gone", Status: 200, Kind: KindInternal},
	}}
	cur := []Result{
		{URL: "https://e.com/", Status: 200, Kind: KindInternal},
		{URL: "https://e.com/a", Status: 404, Kind: KindInternal},
		{URL: "https://e.com/new", Status: 200, Kind: KindInternal, Internal: true, Depth: 1, FoundOn: []string{"https://e.com/"}},
		{URL: "https://e.com/pending", Pending: true},
	}
	c := Compare(old, cur)
	if !reflect.DeepEqual(c.Added, []string{"https://e.com/new"}) || !reflect.DeepEqual(c.Removed, []string{"https://e.com/gone"}) ||
		len(c.Status) != 1 || c.Status[0] != (StatusChange{"https://e.com/a", 200, 404}) || !c.OldCreatedAt.Equal(old.CreatedAt) {
		t.Errorf("comparison = %+v", c)
	}

	var buf bytes.Buffer
	Analyze(cur)
	if err := WriteCSV(&buf, cur); err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(&buf).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	col := func(name string) int { return slices.Index(CSVHeader, name) }
	if len(rows) != len(cur)+1 || !reflect.DeepEqual(rows[0], CSVHeader) || rows[3][0] != "https://e.com/new" ||
		rows[3][col("issues")] != "few_inlinks" || rows[3][col("worst_issue")] != SevNotice {
		t.Errorf("csv = %v", rows)
	}
	for i, row := range rows {
		if len(row) != len(CSVHeader) {
			t.Errorf("row %d has %d columns, want %d", i, len(row), len(CSVHeader))
		}
	}
}

// A full crawl adds the sitemap URLs after the link crawl: an orphan page is
// found and marked, robots.txt rules are applied.
func TestCrawlSitemapAndRobots(t *testing.T) {
	mux := http.NewServeMux()
	var base string
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "User-agent: *\nDisallow: /secret\nSitemap: %s/map.xml\n", base)
	})
	mux.HandleFunc("/map.xml", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `<urlset><url><loc>%[1]s/</loc></url><url><loc>%[1]s/orphan</loc></url><url><loc>https://other.example/x</loc></url></urlset>`, base)
	})
	html := func(body string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, body)
		}
	}
	mux.HandleFunc("/{$}", html(`<title>Home</title><a href="/secret">s</a>`))
	mux.HandleFunc("/secret", html(`<title>Secret</title>`))
	mux.HandleFunc("/orphan", html(`<title>Orphan</title><a href="/orphan-child">c</a>`))
	mux.HandleFunc("/orphan-child", html(`<title>Child</title>`))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	base = srv.URL

	c := New()
	if err := c.Start(Config{StartURL: srv.URL, Sitemap: true}); err != nil {
		t.Fatal(err)
	}
	c.Wait()
	got := map[string]Result{}
	for _, r := range c.Results() {
		got[strings.TrimPrefix(r.URL, srv.URL)] = r
	}
	if r := got["/secret"]; !r.RobotsBlocked || r.InSitemap || r.Depth != 1 {
		t.Errorf("/secret = %+v", r)
	}
	if r := got["/orphan"]; !r.InSitemap || r.Depth != NotLinked || r.Status != 200 {
		t.Errorf("/orphan = %+v", r)
	}
	if r := got["/orphan-child"]; r.Depth != NotLinked {
		t.Errorf("links of an orphan page are not reachable from the start: %+v", r)
	}
	if r := got["/"]; !r.InSitemap || r.Depth != 0 {
		t.Errorf("start = %+v", r)
	}
	if _, ok := got["https://other.example/x"]; ok {
		t.Error("foreign sitemap entries must be ignored")
	}

	// Without the option the sitemap is not read.
	c2 := New()
	c2.Start(Config{StartURL: srv.URL})
	c2.Wait()
	for _, r := range c2.Results() {
		if r.InSitemap || strings.HasSuffix(r.URL, "/orphan") {
			t.Errorf("sitemap used although disabled: %s", r.URL)
		}
	}
}
