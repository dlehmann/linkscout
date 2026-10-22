package crawler

import (
	"bytes"
	"encoding/xml"
	"strings"
	"testing"
	"time"
)

func TestSiteTree(t *testing.T) {
	mod := time.Date(2026, 10, 1, 8, 30, 0, 0, time.UTC)
	page := func(u, title string) Result {
		return Result{URL: u, Title: title, Kind: KindInternal, Status: 200, MimeType: "text/html"}
	}
	blog := page("https://e.com/blog/", "Blog")
	blog.LastModified = mod
	hidden := page("https://e.com/blog/draft", "Draft")
	hidden.Issues = []string{"noindex"}
	rs := []Result{
		page("https://e.com/", "Home"),
		blog,
		page("https://e.com/blog/2026/first", "First"),
		hidden,
		page("https://e.com/about", "About"),
		page("https://e.com/list?page=2", "List 2"),
		{URL: "https://e.com/old", Kind: KindInternal, Status: 200, MimeType: "text/html", Redirects: []string{"https://e.com/old"}, FinalURL: "https://e.com/new"},
		{URL: "https://e.com/missing", Kind: KindInternal, Status: 404, MimeType: "text/html"},
		{URL: "https://e.com/a.pdf", Kind: KindFile, Status: 200, MimeType: "application/pdf"},
		{URL: "https://other.org/", Kind: KindExternal, Status: 200, MimeType: "text/html"},
	}
	tree := SiteTree(rs, "https://www.e.com/")
	if tree.Name != "e.com" || tree.URL != "https://e.com/" || tree.Pages != 6 {
		t.Fatalf("root = %+v", tree)
	}
	var got []string
	tree.Walk(func(n *SiteNode, level int) {
		got = append(got, strings.Repeat("  ", level)+n.Name+"|"+n.Title)
	})
	want := []string{
		"e.com|Home",
		"  about|About",
		"  blog|Blog",
		"    2026|", // a directory without page of its own
		"      first|First",
		"    draft|Draft",
		"  list|",
		"    ?page=2|List 2",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("tree:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	var buf bytes.Buffer
	if err := WriteSitemapXML(&buf, tree); err != nil {
		t.Fatal(err)
	}
	var set struct {
		URLs []struct {
			Loc     string `xml:"loc"`
			LastMod string `xml:"lastmod"`
		} `xml:"url"`
	}
	if err := xml.Unmarshal(buf.Bytes(), &set); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">`) || len(set.URLs) != 5 {
		t.Errorf("sitemap.xml:\n%s", buf.String())
	}
	for _, u := range set.URLs {
		if u.Loc == "https://e.com/blog/draft" {
			t.Error("noindex page in sitemap.xml")
		}
		if u.Loc == "https://e.com/blog/" && u.LastMod != "2026-10-01T08:30:00Z" {
			t.Errorf("lastmod = %q", u.LastMod)
		}
	}
	// the start URL (www) was not checked: the host of the pages wins
	if tree.Origin != "https://e.com" || !strings.Contains(buf.String(), "https://e.com/list?page=2") {
		t.Errorf("origin %q or query page missing", tree.Origin)
	}
}

// sitemap.xml lists every URL under the scheme and host the start page ends
// up at, once.
func TestSitemapXMLOrigin(t *testing.T) {
	page := func(u string) Result { return Result{URL: u, Kind: KindInternal, Status: 200, MimeType: "text/html"} }
	rs := []Result{
		{URL: "http://e.com/", Kind: KindInternal, Status: 200, MimeType: "text/html",
			Redirects: []string{"http://e.com/"}, FinalURL: "https://www.e.com/"},
		page("https://www.e.com/"),
		page("http://e.com/a"),
		page("https://www.e.com/a/"),
		page("https://e.com/b?x=1&y=2"),
	}
	tree := SiteTree(rs, "http://e.com/")
	if tree.Origin != "https://www.e.com" {
		t.Fatalf("origin = %q", tree.Origin)
	}
	var buf bytes.Buffer
	WriteSitemapXML(&buf, tree)
	out := buf.String()
	for _, want := range []string{"<loc>https://www.e.com/</loc>", "<loc>https://www.e.com/a</loc>",
		"<loc>https://www.e.com/a/</loc>", "<loc>https://www.e.com/b?x=1&amp;y=2</loc>"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s in\n%s", want, out)
		}
	}
	if strings.Contains(out, "<loc>http://") || strings.Contains(out, "//e.com") || strings.Count(out, "<url>") != 4 {
		t.Errorf("foreign host or duplicates:\n%s", out)
	}
}
