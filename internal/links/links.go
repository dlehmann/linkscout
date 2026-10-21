// Package links contains the pure (network free) helpers around URLs:
// normalisation, link extraction from HTML and CSS, title extraction and the
// classification of a URL by its file extension.
package links

import (
	"bytes"
	"mime"
	"net/url"
	"path"
	"regexp"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// Link is a reference found in a document.
type Link struct {
	URL string // absolute, normalised URL (without fragment)
	Raw string // attribute value as written in the document
	Tag string // element name, e.g. "a", "img"
	// Embedded is true for resources the browser loads automatically to render
	// the page (images, CSS, scripts, fonts, iframes …) and false for
	// navigational links (<a href>, <area href>).
	Embedded bool
}

// Normalize resolves ref against base and returns the canonical absolute URL.
// Fragments are removed, scheme and host are lower-cased, default ports are
// dropped and an empty path becomes "/". Only http and https URLs are
// accepted; everything else (mailto:, tel:, javascript:, data: …) yields false.
func Normalize(ref string, base *url.URL) (string, bool) {
	ref = strings.TrimSpace(ref)
	if ref == "" || strings.HasPrefix(ref, "#") {
		return "", false
	}
	u, err := url.Parse(ref)
	if err != nil {
		return "", false
	}
	if base != nil {
		u = base.ResolveReference(u)
	}
	u.Scheme = strings.ToLower(u.Scheme)
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", false
	}
	if u.Host == "" {
		return "", false
	}
	u.Host = strings.ToLower(u.Host)
	if (u.Scheme == "http" && strings.HasSuffix(u.Host, ":80")) ||
		(u.Scheme == "https" && strings.HasSuffix(u.Host, ":443")) {
		u.Host = u.Host[:strings.LastIndexByte(u.Host, ':')]
	}
	u.Fragment, u.RawFragment = "", ""
	u.User = nil
	if u.Path == "" {
		u.Path = "/"
		u.RawPath = ""
	}
	return u.String(), true
}

// SameSite reports whether u belongs to the site rooted at start. Scheme
// (http/https) and a leading "www." are ignored, sub-domains are not.
func SameSite(start, u *url.URL) bool {
	return bareHost(start.Hostname()) == bareHost(u.Hostname())
}

// bareHost lower-cases a host name and strips a leading "www.".
func bareHost(h string) string {
	return strings.TrimPrefix(strings.ToLower(h), "www.")
}

// urlAttrs maps an element to its URL bearing attributes (navigational links
// as well as resources the browser fetches).
var urlAttrs = map[atom.Atom][]string{
	atom.A:      {"href"},
	atom.Area:   {"href"},
	atom.Link:   {"href"},
	atom.Img:    {"src", "srcset", "data-src"},
	atom.Script: {"src"},
	atom.Iframe: {"src"},
	atom.Frame:  {"src"},
	atom.Embed:  {"src"},
	atom.Object: {"data"},
	atom.Source: {"src", "srcset"},
	atom.Video:  {"src", "poster"},
	atom.Audio:  {"src"},
	atom.Track:  {"src"},
	atom.Input:  {"src"},
}

// Document is the result of parsing an HTML page.
type Document struct {
	Title string
	Base  *url.URL // effective base URL (honours <base href>)
	Links []Link
}

// ParseHTML extracts the title and all http(s) links of an HTML document.
// Links are de-duplicated by (URL, Embedded); the order of first appearance is
// kept. Inline styles and <style> blocks are scanned for url(...) references.
func ParseHTML(body []byte, pageURL *url.URL) Document {
	doc := Document{Base: pageURL}
	root, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return doc
	}
	// <base href> changes how every relative URL is resolved, so find it first.
	if b := findFirst(root, atom.Base); b != nil {
		if href, ok := attr(b, "href"); ok {
			if bu, err := url.Parse(strings.TrimSpace(href)); err == nil {
				doc.Base = pageURL.ResolveReference(bu)
			}
		}
	}
	seen := map[string]bool{}
	add := func(raw, tag string, embedded bool) {
		abs, ok := Normalize(raw, doc.Base)
		if !ok {
			return
		}
		key := abs
		if embedded {
			key = "e:" + abs
		}
		if seen[key] {
			return
		}
		seen[key] = true
		doc.Links = append(doc.Links, Link{URL: abs, Raw: raw, Tag: tag, Embedded: embedded})
	}

	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.DataAtom {
			case atom.Title:
				if doc.Title == "" && !insideSVG(n) {
					doc.Title = collapseSpace(textOf(n))
				}
			case atom.Style:
				for _, u := range CSSURLs(textOf(n)) {
					add(u, "style", true)
				}
			}
			if style, ok := attr(n, "style"); ok {
				for _, u := range CSSURLs(style) {
					add(u, n.Data, true)
				}
			}
			for _, name := range urlAttrs[n.DataAtom] {
				val, ok := attr(n, name)
				if !ok {
					continue
				}
				embedded := IsEmbedded(n.DataAtom, attrOr(n, "rel"))
				if name == "srcset" {
					for _, u := range ParseSrcset(val) {
						add(u, n.Data, true)
					}
					continue
				}
				if name == "poster" {
					embedded = true
				}
				add(val, n.Data, embedded)
			}
			// <meta http-equiv="refresh" content="0; url=/new">
			if n.DataAtom == atom.Meta && strings.EqualFold(attrOr(n, "http-equiv"), "refresh") {
				if u := refreshURL(attrOr(n, "content")); u != "" {
					add(u, "meta", false)
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(root)
	return doc
}

// IsEmbedded decides whether an element/rel combination loads a resource
// (true) or is a navigational link (false).
func IsEmbedded(a atom.Atom, rel string) bool {
	switch a {
	case atom.A, atom.Area:
		return false
	case atom.Link:
		for _, r := range strings.Fields(strings.ToLower(rel)) {
			switch r {
			case "stylesheet", "icon", "apple-touch-icon", "preload", "modulepreload", "manifest", "mask-icon", "prefetch":
				return true
			}
		}
		// rel=canonical, alternate, next, prev … are navigational.
		return false
	}
	return true
}

// ParseSrcset returns the URLs of a srcset attribute ("a.jpg 1x, b.jpg 2x").
func ParseSrcset(s string) []string {
	var out []string
	for _, cand := range SplitSrcset(s) {
		if f := strings.Fields(cand); len(f) > 0 {
			out = append(out, f[0])
		}
	}
	return out
}

// SplitSrcset splits a srcset attribute into its candidates ("url descriptor").
// URLs themselves may contain commas (e.g. CDN image transforms), so a comma
// only separates when it follows a whitespace-delimited descriptor or is
// followed by whitespace.
func SplitSrcset(s string) []string {
	var parts []string
	var cur strings.Builder
	inURL := true
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case ch == ' ' || ch == '\t' || ch == '\n':
			if cur.Len() > 0 {
				inURL = false
			}
			cur.WriteByte(ch)
		case ch == ',' && (!inURL || i+1 >= len(s) || s[i+1] == ' '):
			parts = append(parts, cur.String())
			cur.Reset()
			inURL = true
		default:
			cur.WriteByte(ch)
		}
	}
	if strings.TrimSpace(cur.String()) != "" {
		parts = append(parts, cur.String())
	}
	return parts
}

// cssURLRe matches url(...) in its three quoting styles and @import "..." in
// stylesheets and style attributes. The capture groups hold the reference.
var cssURLRe = regexp.MustCompile(`(?i)url\(\s*(?:"([^"]*)"|'([^']*)'|([^)'"\s]+))\s*\)|@import\s+(?:"([^"]+)"|'([^']+)')`)

// CSSURLs returns all url(...) and @import "..." references in a stylesheet.
// data: URIs are skipped.
func CSSURLs(css string) []string {
	var out []string
	for _, m := range cssURLRe.FindAllStringSubmatch(css, -1) {
		for _, g := range m[1:] {
			if g != "" {
				if !strings.HasPrefix(strings.ToLower(g), "data:") {
					out = append(out, g)
				}
				break
			}
		}
	}
	return out
}

// RewriteCSS replaces every url(...) / @import reference in css by the value
// returned from fn. If fn returns "" the reference is left unchanged.
func RewriteCSS(css string, fn func(ref string) string) string {
	return cssURLRe.ReplaceAllStringFunc(css, func(match string) string {
		m := cssURLRe.FindStringSubmatch(match)
		for i, g := range m[1:] {
			if g == "" {
				continue
			}
			if strings.HasPrefix(strings.ToLower(g), "data:") {
				return match
			}
			nu := fn(g)
			if nu == "" {
				return match
			}
			if i >= 3 { // @import "x"
				return `@import "` + nu + `"`
			}
			return `url("` + nu + `")`
		}
		return match
	})
}

// refreshRe finds the target of <meta http-equiv="refresh"
// content="5; URL='https://example.com/'"> (spaces around "=" are allowed).
var refreshRe = regexp.MustCompile(`(?i)url\s*=\s*`)

// refreshURL returns the target of a meta refresh content attribute or "".
func refreshURL(content string) string {
	loc := refreshRe.FindStringIndex(content)
	if loc == nil {
		return ""
	}
	return strings.Trim(strings.TrimSpace(content[loc[1]:]), `'"`)
}

// RefreshTarget splits the content attribute of a meta refresh into the part
// before the URL ("5; url=") and the URL itself. ok is false without a URL.
func RefreshTarget(content string) (prefix, target string, ok bool) {
	loc := refreshRe.FindStringIndex(content)
	if loc == nil {
		return content, "", false
	}
	return content[:loc[1]], strings.Trim(strings.TrimSpace(content[loc[1]:]), `'"`), true
}

// --- classification -------------------------------------------------------

// Categories group file types.
const (
	CatPage     = "page"
	CatImage    = "image"
	CatPDF      = "pdf"
	CatDocument = "document"
	CatArchive  = "archive"
	CatMedia    = "media"
	CatStyle    = "style"
	CatScript   = "script"
	CatFont     = "font"
	CatData     = "data"
	CatOther    = "other"
)

// extCategory maps known file extensions to their category. Extensions that
// are not listed are CatOther and treated like pages.
var extCategory = map[string]string{
	// pages (server side scripts deliver HTML)
	"html": CatPage, "htm": CatPage, "xhtml": CatPage, "shtml": CatPage, "php": CatPage,
	"asp": CatPage, "aspx": CatPage, "jsp": CatPage, "cfm": CatPage, "cgi": CatPage, "pl": CatPage,
	// images
	"jpg": CatImage, "jpeg": CatImage, "png": CatImage, "gif": CatImage, "svg": CatImage,
	"webp": CatImage, "avif": CatImage, "ico": CatImage, "bmp": CatImage, "tif": CatImage, "tiff": CatImage, "heic": CatImage,
	// documents
	"pdf": CatPDF,
	"doc": CatDocument, "docx": CatDocument, "xls": CatDocument, "xlsx": CatDocument, "ppt": CatDocument,
	"pptx": CatDocument, "odt": CatDocument, "ods": CatDocument, "odp": CatDocument, "rtf": CatDocument,
	"txt": CatDocument, "csv": CatDocument, "epub": CatDocument,
	// archives / binaries
	"zip": CatArchive, "gz": CatArchive, "tgz": CatArchive, "bz2": CatArchive, "xz": CatArchive, "7z": CatArchive,
	"rar": CatArchive, "tar": CatArchive, "dmg": CatArchive, "exe": CatArchive, "msi": CatArchive, "deb": CatArchive,
	"rpm": CatArchive, "apk": CatArchive, "iso": CatArchive, "appimage": CatArchive,
	// media
	"mp3": CatMedia, "wav": CatMedia, "ogg": CatMedia, "oga": CatMedia, "flac": CatMedia, "m4a": CatMedia, "aac": CatMedia,
	"mp4": CatMedia, "webm": CatMedia, "mov": CatMedia, "avi": CatMedia, "mkv": CatMedia, "m4v": CatMedia, "ogv": CatMedia, "vtt": CatMedia,
	// web resources
	"css": CatStyle,
	"js":  CatScript, "mjs": CatScript,
	"woff": CatFont, "woff2": CatFont, "ttf": CatFont, "otf": CatFont, "eot": CatFont,
	"json": CatData, "xml": CatData, "rss": CatData, "atom": CatData, "webmanifest": CatData, "map": CatData,
}

// Extension returns the lower-case file extension of the URL path (without
// the dot), or "" when the last path segment has none or only digits.
func Extension(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	base := path.Base(u.Path)
	if base == "/" || base == "." {
		return ""
	}
	ext := path.Ext(base)
	if len(ext) < 2 || len(ext) > 12 {
		return ""
	}
	// Version numbers ("v6.12.113") or dates ("2024.10") are not file
	// extensions: require at least one letter.
	if !strings.ContainsFunc(ext, func(r rune) bool { return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' }) {
		return ""
	}
	return strings.ToLower(ext[1:])
}

// Category returns the category for a file extension. Unknown non-empty
// extensions are CatOther; no extension is assumed to be a page.
func Category(ext string) string {
	if ext == "" {
		return CatPage
	}
	if c, ok := extCategory[ext]; ok {
		return c
	}
	return CatOther
}

// IsFile reports whether a category is a downloadable file as opposed to a
// web page. Unknown extensions (CatOther) are treated as pages because
// paths like "/news/2024.10" are common.
func IsFile(cat string) bool {
	return cat != CatPage && cat != CatOther
}

// TypeOf determines the display type of a URL from its file extension. When
// the URL has no (known) extension the content type is used instead, so a
// "/about" page becomes "html" and an extension-less image "png".
func TypeOf(rawURL, contentType string) string {
	ext := Extension(rawURL)
	if _, known := extCategory[ext]; known {
		switch ext {
		case "htm", "xhtml", "shtml":
			return "html"
		case "jpeg":
			return "jpg"
		}
		return ext
	}
	if t := typeFromContentType(contentType); t != "" {
		return t
	}
	if ext != "" {
		return ext
	}
	return "html"
}

// typeFromContentType derives a short display type ("html", "pdf", "png" …)
// from a media type; "" when nothing sensible can be derived.
func typeFromContentType(ct string) string {
	switch ct {
	case "":
		return ""
	case "text/html", "application/xhtml+xml":
		return "html"
	case "application/pdf":
		return "pdf"
	case "image/jpeg":
		return "jpg"
	case "image/svg+xml":
		return "svg"
	case "text/css":
		return "css"
	case "text/javascript", "application/javascript", "application/x-javascript":
		return "js"
	case "application/json", "application/ld+json":
		return "json"
	case "application/xml", "text/xml", "application/rss+xml", "application/atom+xml":
		return "xml"
	case "text/plain":
		return "txt"
	case "application/zip":
		return "zip"
	}
	if i := strings.IndexByte(ct, '/'); i >= 0 {
		major, sub := ct[:i], ct[i+1:]
		if major == "image" || major == "video" || major == "audio" || major == "font" {
			return strings.TrimPrefix(sub, "x-")
		}
	}
	return ""
}

// CategoryOfType maps a display type (see TypeOf) back to a category.
func CategoryOfType(t string) string {
	if t == "html" {
		return CatPage
	}
	return Category(t)
}

// extMime fixes the MIME types of common extensions so the result does not
// depend on the mime.types files of the system; other extensions fall back
// to mime.TypeByExtension.
var extMime = map[string]string{
	"html": "text/html", "htm": "text/html", "xhtml": "application/xhtml+xml", "shtml": "text/html",
	"jpg": "image/jpeg", "jpeg": "image/jpeg", "png": "image/png", "gif": "image/gif", "svg": "image/svg+xml",
	"webp": "image/webp", "avif": "image/avif", "ico": "image/vnd.microsoft.icon", "bmp": "image/bmp",
	"tif": "image/tiff", "tiff": "image/tiff", "heic": "image/heic",
	"pdf": "application/pdf", "txt": "text/plain", "csv": "text/csv", "rtf": "application/rtf",
	"epub": "application/epub+zip",
	"doc":  "application/msword", "xls": "application/vnd.ms-excel", "ppt": "application/vnd.ms-powerpoint",
	"docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	"xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	"pptx": "application/vnd.openxmlformats-officedocument.presentationml.presentation",
	"odt":  "application/vnd.oasis.opendocument.text", "ods": "application/vnd.oasis.opendocument.spreadsheet",
	"odp": "application/vnd.oasis.opendocument.presentation",
	"zip": "application/zip", "gz": "application/gzip", "tgz": "application/gzip", "bz2": "application/x-bzip2",
	"xz": "application/x-xz", "7z": "application/x-7z-compressed", "rar": "application/vnd.rar", "tar": "application/x-tar",
	"mp3": "audio/mpeg", "wav": "audio/wav", "ogg": "audio/ogg", "oga": "audio/ogg", "flac": "audio/flac",
	"m4a": "audio/mp4", "aac": "audio/aac", "mp4": "video/mp4", "webm": "video/webm", "mov": "video/quicktime",
	"avi": "video/x-msvideo", "mkv": "video/x-matroska", "m4v": "video/mp4", "ogv": "video/ogg", "vtt": "text/vtt",
	"css": "text/css", "js": "text/javascript", "mjs": "text/javascript",
	"woff": "font/woff", "woff2": "font/woff2", "ttf": "font/ttf", "otf": "font/otf", "eot": "application/vnd.ms-fontobject",
	"json": "application/json", "xml": "application/xml", "rss": "application/rss+xml", "atom": "application/atom+xml",
	"webmanifest": "application/manifest+json",
}

// MimeType returns the MIME type shown for a URL: the content type of a
// successful response, otherwise the type expected from the file extension
// (error pages are HTML whatever was requested). URLs without extension are
// assumed to be pages. "" means unknown.
func MimeType(rawURL, contentType string, ok bool) string {
	if ok && contentType != "" {
		return contentType
	}
	ext := Extension(rawURL)
	if ext == "" {
		return "text/html"
	}
	if m, found := extMime[ext]; found {
		return m
	}
	if m := mime.TypeByExtension("." + ext); m != "" {
		if mt, _, err := mime.ParseMediaType(m); err == nil {
			return mt
		}
	}
	return ""
}

// IsHTML reports whether a content type denotes an HTML document.
func IsHTML(ct string) bool {
	return ct == "text/html" || ct == "application/xhtml+xml"
}

// --- small DOM helpers ------------------------------------------------------

// attr returns the value of the (namespace-less) attribute name of n.
func attr(n *html.Node, name string) (string, bool) {
	for _, a := range n.Attr {
		if a.Namespace == "" && strings.EqualFold(a.Key, name) {
			return a.Val, true
		}
	}
	return "", false
}

// attrOr returns the attribute value or "" when it is missing.
func attrOr(n *html.Node, name string) string {
	v, _ := attr(n, name)
	return v
}

// textOf concatenates all text nodes below n.
func textOf(n *html.Node) string {
	var b strings.Builder
	var f func(*html.Node)
	f = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			f(c)
		}
	}
	f(n)
	return b.String()
}

// findFirst returns the first element of type a in document order, or nil.
func findFirst(n *html.Node, a atom.Atom) *html.Node {
	if n.Type == html.ElementNode && n.DataAtom == a {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if r := findFirst(c, a); r != nil {
			return r
		}
	}
	return nil
}

// insideSVG reports whether n is nested in an <svg> element (an SVG <title>
// is a tooltip, not the page title).
func insideSVG(n *html.Node) bool {
	for p := n.Parent; p != nil; p = p.Parent {
		if p.Type == html.ElementNode && (p.DataAtom == atom.Svg || p.Namespace == "svg") {
			return true
		}
	}
	return n.Namespace == "svg"
}

// collapseSpace trims s and replaces every run of whitespace by one space.
func collapseSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// URLAttrs exposes the attribute table for the mirror package, which rewrites
// exactly the attributes the extractor reads.
func URLAttrs(a atom.Atom) []string { return urlAttrs[a] }
