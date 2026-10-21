package links

import (
	"net/url"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/net/html/atom"
)

func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestNormalize(t *testing.T) {
	base := mustURL(t, "https://Example.com/dir/page.html?x=1")
	tests := []struct {
		in, want string
		ok       bool
	}{
		{"other.html", "https://example.com/dir/other.html", true},
		{"/abs", "https://example.com/abs", true},
		{"../up", "https://example.com/up", true},
		{"?q=2", "https://example.com/dir/page.html?q=2", true},
		{"https://EXAMPLE.com:443/a#frag", "https://example.com/a", true},
		{"http://example.com:80", "http://example.com/", true},
		{"http://example.com:8080/x", "http://example.com:8080/x", true},
		{"//cdn.example.org/lib.js", "https://cdn.example.org/lib.js", true},
		{"https://user:pw@example.com/", "https://example.com/", true},
		{"  /trimmed  ", "https://example.com/trimmed", true},
		{"#top", "", false},
		{"", "", false},
		{"mailto:a@b.c", "", false},
		{"tel:+49123", "", false},
		{"javascript:void(0)", "", false},
		{"data:image/png;base64,AAAA", "", false},
		{"ftp://example.com/file", "", false},
	}
	for _, tt := range tests {
		got, ok := Normalize(tt.in, base)
		if ok != tt.ok || got != tt.want {
			t.Errorf("Normalize(%q) = %q,%v want %q,%v", tt.in, got, ok, tt.want, tt.ok)
		}
	}
	if _, ok := Normalize("/relative", nil); ok {
		t.Error("relative URL without base must be rejected")
	}
}

func TestSameSite(t *testing.T) {
	start := mustURL(t, "https://www.example.com/")
	for in, want := range map[string]bool{
		"https://example.com/a":     true,
		"http://www.example.com/b":  true,
		"https://EXAMPLE.com:8443/": true,
		"https://blog.example.com/": false,
		"https://example.org/":      false,
	} {
		if got := SameSite(start, mustURL(t, in)); got != want {
			t.Errorf("SameSite(%q) = %v want %v", in, got, want)
		}
	}
}

const page = `<!doctype html>
<html><head>
<title>
  Hello   World
</title>
<link rel="stylesheet" href="/css/site.css">
<link rel="canonical" href="https://example.com/canonical">
<link rel="icon" href="favicon.ico">
<meta http-equiv="refresh" content="30; URL='/refreshed'">
<style>body{background:url("img/bg.png")}</style>
</head><body>
<svg><title>svg title must be ignored</title></svg>
<a href="about.html">About</a>
<a href="about.html#team">About again</a>
<a href="https://external.org/page">ext</a>
<a href="mailto:me@example.com">mail</a>
<a href="#top">top</a>
<a href="javascript:alert(1)">js</a>
<img src="img/logo.png" srcset="img/logo-2x.png 2x, img/logo-3x.png 3x">
<div style="background-image: url('img/div.jpg')"></div>
<script src="https://cdn.example.net/lib.js"></script>
<video poster="poster.jpg"><source src="movie.mp4"></video>
<a href="docs/manual.pdf">PDF</a>
</body></html>`

func TestParseHTML(t *testing.T) {
	doc := ParseHTML([]byte(page), mustURL(t, "https://example.com/sub/index.html"))
	if doc.Title != "Hello World" {
		t.Errorf("title = %q", doc.Title)
	}
	got := map[string]bool{}
	for _, l := range doc.Links {
		got[l.URL] = l.Embedded
	}
	want := map[string]bool{
		"https://example.com/css/site.css":        true,
		"https://example.com/canonical":           false,
		"https://example.com/sub/favicon.ico":     true,
		"https://example.com/refreshed":           false,
		"https://example.com/sub/img/bg.png":      true,
		"https://example.com/sub/about.html":      false,
		"https://external.org/page":               false,
		"https://example.com/sub/img/logo.png":    true,
		"https://example.com/sub/img/logo-2x.png": true,
		"https://example.com/sub/img/logo-3x.png": true,
		"https://example.com/sub/img/div.jpg":     true,
		"https://cdn.example.net/lib.js":          true,
		"https://example.com/sub/poster.jpg":      true,
		"https://example.com/sub/movie.mp4":       true,
		"https://example.com/sub/docs/manual.pdf": false,
	}
	if !reflect.DeepEqual(got, want) {
		for k, v := range want {
			if g, ok := got[k]; !ok || g != v {
				t.Errorf("missing/wrong %s: got %v,%v want %v", k, g, ok, v)
			}
		}
		for k := range got {
			if _, ok := want[k]; !ok {
				t.Errorf("unexpected link %s", k)
			}
		}
	}
	// about.html and about.html#team must be reported only once
	n := 0
	for _, l := range doc.Links {
		if strings.HasSuffix(l.URL, "/about.html") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("about.html reported %d times", n)
	}
}

func TestParseHTMLBaseHref(t *testing.T) {
	body := `<html><head><base href="https://static.example.com/v2/"></head>
	<body><a href="page">x</a><img src="/root.png"></body></html>`
	doc := ParseHTML([]byte(body), mustURL(t, "https://example.com/a/b"))
	if len(doc.Links) != 2 {
		t.Fatalf("links: %+v", doc.Links)
	}
	if doc.Links[0].URL != "https://static.example.com/v2/page" {
		t.Errorf("base not honoured: %s", doc.Links[0].URL)
	}
	if doc.Links[1].URL != "https://static.example.com/root.png" {
		t.Errorf("absolute path with base: %s", doc.Links[1].URL)
	}
}

func TestParseSrcset(t *testing.T) {
	tests := map[string][]string{
		"a.jpg 1x, b.jpg 2x": {"a.jpg", "b.jpg"},
		"a.jpg":              {"a.jpg"},
		"a.jpg,b.jpg":        {"a.jpg,b.jpg"}, // ambiguous: comma inside URL
		"a.jpg, b.jpg":       {"a.jpg", "b.jpg"},
		"https://cdn/x/w_100,h_50/img.png 100w, y.png 200w": {"https://cdn/x/w_100,h_50/img.png", "y.png"},
		"": nil,
	}
	for in, want := range tests {
		if got := ParseSrcset(in); !reflect.DeepEqual(got, want) {
			t.Errorf("ParseSrcset(%q) = %q want %q", in, got, want)
		}
	}
}

func TestCSSURLs(t *testing.T) {
	css := `@import "reset.css";
	@import 'print.css' print;
	body { background: url(bg.png) }
	.a { background: url( "quoted.gif" ) }
	.b { background: url('single.svg#frag') }
	.c { background: url(data:image/png;base64,AAAA) }
	@font-face { src: url(/fonts/x.woff2) format("woff2"), url(/fonts/x.woff) }`
	want := []string{"reset.css", "print.css", "bg.png", "quoted.gif", "single.svg#frag", "/fonts/x.woff2", "/fonts/x.woff"}
	if got := CSSURLs(css); !reflect.DeepEqual(got, want) {
		t.Errorf("CSSURLs = %q want %q", got, want)
	}
}

func TestRewriteCSS(t *testing.T) {
	css := `@import "a.css"; x{background:url(b.png)} y{background:url(data:x)} z{background:url('keep.png')}`
	got := RewriteCSS(css, func(ref string) string {
		if ref == "keep.png" {
			return ""
		}
		return "new/" + ref
	})
	want := `@import "new/a.css"; x{background:url("new/b.png")} y{background:url(data:x)} z{background:url('keep.png')}`
	if got != want {
		t.Errorf("RewriteCSS:\n got %s\nwant %s", got, want)
	}
}

func TestExtensionCategoryType(t *testing.T) {
	tests := []struct {
		url, ctype, ext, cat, typ string
	}{
		{"https://e.com/", "text/html", "", CatPage, "html"},
		{"https://e.com/about", "", "", CatPage, "html"},
		{"https://e.com/a/doc.PDF", "", "pdf", CatPDF, "pdf"},
		{"https://e.com/img/photo.jpeg?w=100", "", "jpeg", CatImage, "jpg"},
		{"https://e.com/index.htm", "", "htm", CatPage, "html"},
		{"https://e.com/x.php?id=1", "text/html", "php", CatPage, "php"},
		{"https://e.com/style.css", "", "css", CatStyle, "css"},
		{"https://e.com/archive.tar.gz", "", "gz", CatArchive, "gz"},
		{"https://e.com/download?id=3", "application/pdf", "", CatPage, "pdf"},
		{"https://e.com/image", "image/webp", "", CatPage, "webp"},
		{"https://e.com/news/2024.10", "text/html", "", CatPage, "html"},
		{"https://e.com/tag/v6.12.113", "", "", CatPage, "html"},
		{"https://e.com/x.mp3", "", "mp3", CatMedia, "mp3"},
		{"https://e.com/linux.tar.sign", "", "sign", CatOther, "sign"},
		{"https://e.com/file.xyz", "", "xyz", CatOther, "xyz"},
	}
	for _, tt := range tests {
		if got := Extension(tt.url); got != tt.ext {
			t.Errorf("Extension(%q) = %q want %q", tt.url, got, tt.ext)
		}
		if got := Category(Extension(tt.url)); got != tt.cat {
			t.Errorf("Category(%q) = %q want %q", tt.url, got, tt.cat)
		}
		if got := TypeOf(tt.url, tt.ctype); got != tt.typ {
			t.Errorf("TypeOf(%q,%q) = %q want %q", tt.url, tt.ctype, got, tt.typ)
		}
	}
}

func TestIsFile(t *testing.T) {
	for cat, want := range map[string]bool{CatPage: false, CatOther: false, CatPDF: true, CatImage: true, CatStyle: true, CatArchive: true} {
		if IsFile(cat) != want {
			t.Errorf("IsFile(%q) != %v", cat, want)
		}
	}
	if CategoryOfType("html") != CatPage || CategoryOfType("png") != CatImage {
		t.Error("CategoryOfType")
	}
}

func TestMimeType(t *testing.T) {
	for _, c := range []struct {
		url, ct string
		ok      bool
		want    string
	}{
		{"https://example.com/a.png", "image/png", true, "image/png"},
		{"https://example.com/a.png", "text/html", false, "image/png"}, // 404 page
		{"https://example.com/about", "", false, "text/html"},
		{"https://example.com/file.go", "application/octet-stream", true, "application/octet-stream"},
		{"https://example.com/report.PDF", "", false, "application/pdf"},
		{"https://example.com/x.unknownext", "", false, ""},
	} {
		if got := MimeType(c.url, c.ct, c.ok); got != c.want {
			t.Errorf("MimeType(%q, %q, %v) = %q, want %q", c.url, c.ct, c.ok, got, c.want)
		}
	}
}

func TestRefresh(t *testing.T) {
	for _, c := range []struct{ content, prefix, target string }{
		{"0; url=/new", "0; url=", "/new"},
		{"5; URL = 'https://e.com/x'", "5; URL = ", "https://e.com/x"},
		{`3;url="a.html"`, "3;url=", "a.html"},
	} {
		p, tgt, ok := RefreshTarget(c.content)
		if !ok || p != c.prefix || tgt != c.target || refreshURL(c.content) != c.target {
			t.Errorf("RefreshTarget(%q) = %q, %q, %v", c.content, p, tgt, ok)
		}
	}
	if _, _, ok := RefreshTarget("30"); ok || refreshURL("30") != "" {
		t.Error("a refresh without URL has no target")
	}
	doc := ParseHTML([]byte(`<meta http-equiv="Refresh" content="0; URL = /moved">`), mustURL(t, "https://e.com/"))
	if len(doc.Links) != 1 || doc.Links[0].URL != "https://e.com/moved" || doc.Links[0].Embedded {
		t.Errorf("meta refresh link = %+v", doc.Links)
	}
}

func TestTypeFromContentType(t *testing.T) {
	for ct, want := range map[string]string{
		"text/html": "html", "application/xhtml+xml": "html", "application/pdf": "pdf",
		"image/jpeg": "jpg", "image/svg+xml": "svg", "image/webp": "webp", "image/x-icon": "icon",
		"video/mp4": "mp4", "font/woff2": "woff2", "text/css": "css", "application/javascript": "js",
		"application/ld+json": "json", "application/rss+xml": "xml", "text/plain": "txt",
		"application/zip": "zip", "application/octet-stream": "", "": "",
	} {
		if got := typeFromContentType(ct); got != want {
			t.Errorf("typeFromContentType(%q) = %q want %q", ct, got, want)
		}
	}
}

func TestURLAttrsAndIsHTML(t *testing.T) {
	if !reflect.DeepEqual(URLAttrs(atom.Img), []string{"src", "srcset", "data-src"}) || URLAttrs(atom.P) != nil {
		t.Errorf("URLAttrs: %v / %v", URLAttrs(atom.Img), URLAttrs(atom.P))
	}
	if !IsHTML("text/html") || !IsHTML("application/xhtml+xml") || IsHTML("text/plain") || IsHTML("") {
		t.Error("IsHTML")
	}
}
