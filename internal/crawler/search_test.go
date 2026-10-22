package crawler

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// searchSite has two pages with text, one with a match only in the source
// and an external font.
func searchSite(t *testing.T) *httptest.Server {
	mux := http.NewServeMux()
	html := func(body string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, body)
		}
	}
	mux.HandleFunc("/{$}", html(`<title>Home</title><link rel="stylesheet" href="https://fonts.googleapis.com/css2?family=X">
		<p>Mail us: info@old-company.example or INFO@OLD-COMPANY.EXAMPLE</p><a href="/contact">c</a><a href="/about">a</a>`))
	mux.HandleFunc("/contact", html(`<title>Contact</title><p>Phone 0123 456</p><p>info@old-company.example</p>`))
	mux.HandleFunc("/about", html(`<title>About</title><script>gtag("G-ABC123")</script><p>About us</p>`))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestSearch(t *testing.T) {
	srv := searchSite(t)
	c := New()
	if err := c.Start(Config{StartURL: srv.URL}); err != nil {
		t.Fatal(err)
	}
	c.Wait()
	if n := c.Searchable(); n != 3 {
		t.Fatalf("searchable = %d", n)
	}
	path := func(u string) string { return strings.TrimPrefix(u, srv.URL) }

	res, err := c.Search(SearchQuery{Query: "info@old-company.example"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Searched != 3 || res.Total != 3 || len(res.Matches) != 2 ||
		path(res.Matches[0].URL) != "/" || res.Matches[0].Count != 2 || path(res.Matches[1].URL) != "/contact" {
		t.Fatalf("search = %+v", res)
	}
	if sn := res.Matches[0].Snippets[0]; sn.Before != "Mail us: " || sn.Match != "info@old-company.example" || !strings.HasPrefix(sn.After, " or INFO") {
		t.Errorf("snippet = %+v", sn)
	}

	// case sensitive: only the lower-case address
	res, _ = c.Search(SearchQuery{Query: "INFO@", CaseSensitive: true})
	if res.Total != 1 {
		t.Errorf("case sensitive = %+v", res)
	}
	// scripts are not visible text, but part of the source
	if res, _ = c.Search(SearchQuery{Query: "G-ABC123"}); len(res.Matches) != 0 {
		t.Errorf("script text found in visible text: %+v", res)
	}
	if res, _ = c.Search(SearchQuery{Query: "G-ABC123", Mode: SearchSource}); len(res.Matches) != 1 || path(res.Matches[0].URL) != "/about" {
		t.Errorf("source search = %+v", res)
	}
	// regular expressions; empty matches do not count
	if res, _ = c.Search(SearchQuery{Query: `0123\s*\d+`, Regex: true}); res.Total != 1 || res.Matches[0].Snippets[0].Match != "0123 456" {
		t.Errorf("regex = %+v", res)
	}
	if res, _ = c.Search(SearchQuery{Query: `ß*`, Regex: true}); res.Total != 0 {
		t.Errorf("empty matches counted: %+v", res)
	}
	// a pattern with special characters is plain text without Regex
	if res, _ = c.Search(SearchQuery{Query: "(G-"}); res.Total != 0 {
		t.Errorf("plain text = %+v", res)
	}
	if _, err := c.Search(SearchQuery{Query: "  "}); !errors.Is(err, ErrEmptyQuery) {
		t.Errorf("empty query: %v", err)
	}
	if _, err := c.Search(SearchQuery{Query: "a(", Regex: true}); err == nil || strings.Contains(err.Error(), "(?i)") || !strings.Contains(err.Error(), "`a(`") {
		t.Errorf("invalid regex: %v", err)
	}

	// The external font is recorded on the start page.
	for _, r := range c.Results() {
		if path(r.URL) == "/" && (r.SEO == nil || len(r.SEO.ThirdParty) != 1 || !strings.Contains(r.SEO.ThirdParty[0], "fonts.googleapis.com")) {
			t.Errorf("third party = %+v", r.SEO)
		}
	}

	// Load forgets the content, LoadContent brings it back (as a JSON
	// report carries it: plain text, deflated HTML); unknown URLs are ignored.
	content := c.Content()
	if len(content) != 3 || !strings.Contains(content[0].Text, "Mail us") || len(content[0].HTML) == 0 {
		t.Fatalf("content = %+v", content)
	}
	if err := c.Load(c.Config(), c.Results()); err != nil {
		t.Fatal(err)
	}
	if res, _ = c.Search(SearchQuery{Query: "info"}); c.Searchable() != 0 || res.Searched != 0 || len(res.Matches) != 0 {
		t.Errorf("after load: %+v", res)
	}
	c.LoadContent(append(content, PageContent{URL: "https://unknown.example/", Text: "info"}))
	if res, _ = c.Search(SearchQuery{Query: "info@old-company.example"}); c.Searchable() != 3 || res.Total != 3 {
		t.Errorf("after LoadContent: %+v", res)
	}
	if res, _ = c.Search(SearchQuery{Query: "G-ABC123", Mode: SearchSource}); res.Total != 1 {
		t.Errorf("source after LoadContent: %+v", res)
	}
}

func TestSnippet(t *testing.T) {
	long := strings.Repeat("ä", 100)
	// 60 characters of context, the four whitespace characters included
	sn := snippet(long+"\n\n  hit  \t"+long, len(long)+4, len(long)+7)
	if sn.Match != "hit" || !strings.HasPrefix(sn.Before, "…ä") || !strings.HasSuffix(sn.Before, "ä ") ||
		!strings.HasSuffix(sn.After, "ä…") || len([]rune(sn.Before)) != 1+56+1 {
		t.Errorf("snippet = %+v (%d)", sn, len([]rune(sn.Before)))
	}
	if sn := snippet("a hit b", 2, 5); sn.Before != "a " || sn.After != " b" {
		t.Errorf("short snippet = %+v", sn)
	}
}

// Select keeps the listed results in the given order with the issues found
// on the whole site, and restricts figures, search matches and content.
func TestReportSelect(t *testing.T) {
	rep := Report{
		Results: []Result{
			{URL: "https://e.com/a", Status: 200, Issues: []string{"title_duplicate"}},
			{URL: "https://e.com/b", Status: 404},
			{URL: "https://e.com/c", Status: 200, Issues: []string{"title_duplicate"}},
		},
		Search:  &SearchResult{Searched: 2, Total: 3, Matches: []SearchMatch{{URL: "https://e.com/a", Count: 1}, {URL: "https://e.com/c", Count: 2}}},
		Content: []PageContent{{URL: "https://e.com/a", Text: "x"}, {URL: "https://e.com/c", Text: "y"}},
	}
	sel := rep.Select([]string{"https://e.com/c", "https://e.com/b", "https://e.com/c", "https://e.com/zzz"}, ListInfo{Filters: "f", Sort: "s"})
	if len(sel.Results) != 2 || sel.Results[0].URL != "https://e.com/c" || sel.Results[1].URL != "https://e.com/b" {
		t.Fatalf("results = %+v", sel.Results)
	}
	if sel.List == nil || sel.List.Total != 3 || sel.List.Filters != "f" || sel.List.Sort != "s" {
		t.Errorf("list = %+v", sel.List)
	}
	if sel.Stats.Total != 2 || len(sel.Issues) != 1 || sel.Issues[0].Count != 1 || sel.Results[0].Issues[0] != "title_duplicate" {
		t.Errorf("stats %+v, issues %+v", sel.Stats, sel.Issues)
	}
	if sel.Search.Total != 2 || len(sel.Search.Matches) != 1 || sel.Search.Searched != 2 || len(rep.Search.Matches) != 2 {
		t.Errorf("search = %+v (original %+v)", sel.Search, rep.Search)
	}
	if len(sel.Content) != 1 || sel.Content[0].URL != "https://e.com/c" {
		t.Errorf("content = %+v", sel.Content)
	}
}

// An imported list export keeps the issues found on the whole site until
// the next check; a complete report is analysed again as before.
func TestLoadReportList(t *testing.T) {
	seo := &PageSEO{Titles: []string{"Same"}, Canonicals: []string{"https://e.com/a"}}
	rep := Report{StartURL: "https://e.com/", Results: []Result{
		{URL: "https://e.com/a", Status: 200, Kind: KindInternal, Internal: true, MimeType: "text/html", SEO: seo, Issues: []string{"title_duplicate"}},
	}}
	c := New()
	listed := rep
	listed.List = &ListInfo{Total: 2}
	if err := c.LoadReport(Config{StartURL: rep.StartURL}, listed); err != nil {
		t.Fatal(err)
	}
	rs, issues := c.Analyzed()
	if c.List() == nil || !contains(rs[0].Issues, "title_duplicate") || len(issues) == 0 {
		t.Errorf("list import lost its issues: %v %v", rs[0].Issues, issues)
	}
	if r := NewReport(c, "test"); r.List == nil || r.List.Total != 2 {
		t.Errorf("report of an imported list: %+v", r.List)
	}
	// Compare keeps the issues of a list export on both sides.
	if cmp := Compare(listed, NewReport(c, "test")); len(cmp.NewIssues) != 0 || len(cmp.FixedIssues) != 0 {
		t.Errorf("compare list with itself: %+v", cmp)
	}

	if err := c.LoadReport(Config{StartURL: rep.StartURL}, rep); err != nil {
		t.Fatal(err)
	}
	if rs, _ := c.Analyzed(); c.List() != nil || contains(rs[0].Issues, "title_duplicate") {
		t.Errorf("a complete report is analysed again: %v", rs[0].Issues)
	}
}
