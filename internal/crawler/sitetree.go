package crawler

import (
	"encoding/xml"
	"io"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/dlehmann/linkscout/internal/links"
)

// SiteNode is a node of the site structure: the internal HTML pages ordered
// by their URL path (/, /blog/, /blog/post …). A node without URL is a
// directory that has pages below it but is no page itself.
type SiteNode struct {
	Name         string      `json:"name"`          // path segment; the host for the root
	Path         string      `json:"path"`          // path (and query) up to this node
	URL          string      `json:"url,omitempty"` // the page at this path
	Title        string      `json:"title,omitempty"`
	LastModified time.Time   `json:"lastModified,omitempty"`
	Depth        int         `json:"depth"`              // clicks from the start page
	Excluded     string      `json:"excluded,omitempty"` // why the page is not in sitemap.xml: noindex, canonical, robots
	Pages        int         `json:"pages"`              // pages in this subtree, this one included
	Children     []*SiteNode `json:"children,omitempty"`
	// Origin (root only) is scheme and host the site answers under, e.g.
	// "https://example.com": the start URL after its redirects. sitemap.xml
	// uses it for every URL.
	Origin string `json:"origin,omitempty"`
}

// IsSitePage reports whether a result is a page of the site structure: an
// internal HTML page that answered 200 without redirect.
func IsSitePage(r Result) bool {
	if r.Pending || r.Kind != KindInternal || r.Status != 200 || r.FinalURL != "" || len(r.Redirects) > 0 {
		return false
	}
	m := ResultMime(r)
	return m == "text/html" || m == "application/xhtml+xml" || m == "html"
}

// sitemapExclusion tells why a page should not be listed in sitemap.xml:
// it asks not to be indexed, names another URL as canonical or is blocked
// by robots.txt. Run Analyze first.
func sitemapExclusion(r Result) string {
	switch {
	case slices.Contains(r.Issues, "noindex"):
		return "noindex"
	case slices.Contains(r.Issues, "canonical_other"):
		return "canonical"
	case r.RobotsBlocked:
		return "robots"
	}
	return ""
}

// SiteTree builds the site structure from the results. Directories and
// pages are sorted by name, the start page of a directory is the directory
// node itself. Run Analyze first for the sitemap exclusions.
func SiteTree(rs []Result, startURL string) *SiteNode {
	root := &SiteNode{Path: "/"}
	if u, err := url.Parse(startURL); err == nil {
		root.Name = strings.TrimPrefix(u.Host, "www.")
		root.Origin = siteOrigin(rs, u)
	}
	for _, r := range rs {
		if !IsSitePage(r) {
			continue
		}
		u, err := url.Parse(r.URL)
		if err != nil {
			continue
		}
		node := root
		path := "/"
		for _, seg := range strings.Split(strings.Trim(u.Path, "/"), "/") {
			if seg == "" {
				continue
			}
			path += seg + "/"
			node = node.child(seg, path)
		}
		if u.RawQuery != "" { // /list?page=2 is a page below /list
			q := "?" + u.RawQuery
			node = node.child(q, strings.TrimSuffix(path, "/")+q)
		}
		if node.URL != "" && node.URL != r.URL {
			// the same path twice (/a and /a/): the second one gets a node of its own
			node = node.child(u.Path, u.Path+"#"+r.URL)
		}
		node.URL = r.URL
		node.Path = u.EscapedPath()
		if u.RawQuery != "" {
			node.Path += "?" + u.RawQuery
		}
		node.Title = r.Title
		node.LastModified = r.LastModified
		node.Depth = r.Depth
		node.Excluded = sitemapExclusion(r)
	}
	root.finish()
	return root
}

// siteOrigin returns scheme and host the site answers under: those of the
// start page after its redirects (http://example.com →
// https://www.example.com), as long as they stay on the site. When the start
// URL was not checked (an imported report), the origin most site pages use.
func siteOrigin(rs []Result, start *url.URL) string {
	counts := map[string]int{}
	best := start.Scheme + "://" + start.Host
	for _, r := range rs {
		u, err := url.Parse(r.URL)
		if err != nil {
			continue
		}
		if r.URL == start.String() {
			if f, err := url.Parse(r.FinalURL); r.FinalURL != "" && err == nil && f.Host != "" && links.SameSite(start, f) {
				return f.Scheme + "://" + f.Host
			}
			return u.Scheme + "://" + u.Host
		}
		if IsSitePage(r) {
			counts[u.Scheme+"://"+u.Host]++
		}
	}
	for o, n := range counts {
		if n > counts[best] || (n == counts[best] && o < best) {
			best = o
		}
	}
	return best
}

// child returns the child with the given name, creating it when needed.
func (n *SiteNode) child(name, path string) *SiteNode {
	for _, c := range n.Children {
		if c.Name == name {
			return c
		}
	}
	c := &SiteNode{Name: name, Path: path}
	n.Children = append(n.Children, c)
	return c
}

// finish sorts the children and counts the pages of every subtree.
func (n *SiteNode) finish() int {
	slices.SortFunc(n.Children, func(a, b *SiteNode) int { return strings.Compare(a.Name, b.Name) })
	n.Pages = 0
	if n.URL != "" {
		n.Pages = 1
	}
	for _, c := range n.Children {
		n.Pages += c.finish()
	}
	return n.Pages
}

// Walk calls fn for every node in tree order (parents before children).
func (n *SiteNode) Walk(fn func(node *SiteNode, level int)) {
	n.walk(fn, 0)
}

func (n *SiteNode) walk(fn func(*SiteNode, int), level int) {
	fn(n, level)
	for _, c := range n.Children {
		c.walk(fn, level+1)
	}
}

// sitemapMax is the limit of URLs per sitemap file of the sitemap protocol.
const sitemapMax = 50000

// WriteSitemapXML writes the pages of the tree as sitemap.xml (sitemap
// protocol 0.9, https://www.sitemaps.org/protocol.html) with the
// Last-Modified date where the server sent one. Pages that are excluded
// (noindex, other canonical, robots.txt) are left out; so is everything after
// the 50,000th URL, the limit of a single sitemap file.
//
// LinkScout counts www/non-www and http/https as one site, a search engine
// does not: it only accepts the URLs of the property the sitemap belongs to.
// So every URL gets the scheme and host of tree.Origin, and URLs that become
// equal by that are listed once.
func WriteSitemapXML(w io.Writer, tree *SiteNode) error {
	type entry struct {
		Loc     string `xml:"loc"`
		LastMod string `xml:"lastmod,omitempty"`
	}
	set := struct {
		XMLName xml.Name `xml:"urlset"`
		NS      string   `xml:"xmlns,attr"`
		URLs    []entry  `xml:"url"`
	}{NS: "http://www.sitemaps.org/schemas/sitemap/0.9"}
	origin, _ := url.Parse(tree.Origin)
	seen := map[string]bool{}
	tree.Walk(func(n *SiteNode, _ int) {
		if n.URL == "" || n.Excluded != "" || len(set.URLs) >= sitemapMax {
			return
		}
		loc := n.URL
		if u, err := url.Parse(n.URL); err == nil && origin != nil && origin.Host != "" {
			u.Scheme, u.Host = origin.Scheme, origin.Host
			loc = u.String()
		}
		if seen[loc] {
			return
		}
		seen[loc] = true
		e := entry{Loc: loc}
		if !n.LastModified.IsZero() {
			e.LastMod = n.LastModified.UTC().Format(time.RFC3339)
		}
		set.URLs = append(set.URLs, e)
	})
	if _, err := io.WriteString(w, xml.Header); err != nil {
		return err
	}
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	if err := enc.Encode(set); err != nil {
		return err
	}
	_, err := io.WriteString(w, "\n")
	return err
}
