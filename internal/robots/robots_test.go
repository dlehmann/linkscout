package robots

import (
	"bytes"
	"compress/gzip"
	"net/url"
	"reflect"
	"testing"
)

func TestParse(t *testing.T) {
	r := Parse(`# comment
User-agent: Googlebot
Disallow: /only-google

User-agent: *
Disallow: /private
Disallow: /*.pdf$
Allow: /private/public
Disallow: /tmp*
Sitemap: https://e.com/sitemap.xml
Sitemap: https://e.com/news.xml
`)
	for path, want := range map[string]bool{
		"/":                    false,
		"/only-google":         false, // another group
		"/private":             true,
		"/private/x.html":      true,
		"/private/public/a":    false, // longer Allow wins
		"/docs/a.pdf":          true,
		"/docs/a.pdf?download": false, // $ anchors the end
		"/tmpfile":             true,
	} {
		u, _ := url.Parse("https://e.com" + path)
		if got := r.Blocked(u); got != want {
			t.Errorf("Blocked(%s) = %v want %v", path, got, want)
		}
	}
	if !reflect.DeepEqual(r.Sitemaps, []string{"https://e.com/sitemap.xml", "https://e.com/news.xml"}) {
		t.Errorf("sitemaps = %v", r.Sitemaps)
	}
	var nilRobots *Robots
	if nilRobots.Blocked(&url.URL{Path: "/x"}) {
		t.Error("no robots.txt must allow everything")
	}
}

func TestParseSitemap(t *testing.T) {
	pages, nested := ParseSitemap([]byte(`<?xml version="1.0"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <url><loc> https://e.com/a </loc></url><url><loc>https://e.com/b</loc></url>
</urlset>`))
	if !reflect.DeepEqual(pages, []string{"https://e.com/a", "https://e.com/b"}) || nested != nil {
		t.Errorf("urlset: %v %v", pages, nested)
	}
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	zw.Write([]byte(`<sitemapindex><sitemap><loc>https://e.com/s1.xml</loc></sitemap></sitemapindex>`))
	zw.Close()
	pages, nested = ParseSitemap(gz.Bytes())
	if pages != nil || !reflect.DeepEqual(nested, []string{"https://e.com/s1.xml"}) {
		t.Errorf("gzipped index: %v %v", pages, nested)
	}
	if p, n := ParseSitemap([]byte("not xml")); p != nil || n != nil {
		t.Error("garbage must yield nothing")
	}
}
