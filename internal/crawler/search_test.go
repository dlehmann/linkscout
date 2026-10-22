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
	if _, err := c.Search(SearchQuery{Query: "(", Regex: true}); err == nil {
		t.Error("invalid regex accepted")
	}

	// The external font is recorded on the start page.
	for _, r := range c.Results() {
		if path(r.URL) == "/" && (r.SEO == nil || len(r.SEO.ThirdParty) != 1 || !strings.Contains(r.SEO.ThirdParty[0], "fonts.googleapis.com")) {
			t.Errorf("third party = %+v", r.SEO)
		}
	}

	// A loaded report carries no page content.
	if err := c.Load(c.Config(), c.Results()); err != nil {
		t.Fatal(err)
	}
	if res, _ = c.Search(SearchQuery{Query: "info"}); c.Searchable() != 0 || res.Searched != 0 || len(res.Matches) != 0 {
		t.Errorf("after load: %+v", res)
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
