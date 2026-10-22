// Package robots reads robots.txt files and sitemaps.
//
// The crawler only marks URLs blocked by robots.txt (LinkScout is the site
// owner's tool) and adds the sitemap URLs after the link crawl; the offline
// copy can respect robots.txt and also downloads the pages of the sitemap.
package robots

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/xml"
	"io"
	"net/url"
	"regexp"
	"strings"

	"github.com/dlehmann/linkscout/internal/fetch"
	"github.com/dlehmann/linkscout/internal/links"
)

// Limits for robots.txt and sitemaps (the sitemap protocol allows 50,000
// URLs and 50 MiB per file).
const (
	maxRobotsSize  = 512 << 10
	maxSitemapSize = 50 << 20
	maxSitemaps    = 50
	maxSitemapURLs = 50000
)

// Robots holds the rules of a robots.txt that apply to every crawler
// ("User-agent: *") plus the announced sitemaps.
type Robots struct {
	rules    []robotsRule
	Sitemaps []string
}

type robotsRule struct {
	allow   bool
	pattern string
	re      *regexp.Regexp
}

// Parse reads a robots.txt. Only the group for "*" is used: LinkScout
// does not have rules of its own, and search engines fall back to it too.
func Parse(body string) *Robots {
	r := &Robots{}
	inStar, groupHasRules := false, false
	for _, line := range strings.Split(body, "\n") {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		val = strings.TrimSpace(val)
		switch key {
		case "user-agent":
			// Consecutive user-agent lines form one group; a user-agent line
			// after rules starts a new group.
			if groupHasRules {
				inStar, groupHasRules = false, false
			}
			if val == "*" {
				inStar = true
			}
		case "allow", "disallow":
			groupHasRules = true
			if inStar && val != "" {
				r.rules = append(r.rules, robotsRule{allow: key == "allow", pattern: val, re: robotsPattern(val)})
			}
		case "sitemap":
			if val != "" {
				r.Sitemaps = append(r.Sitemaps, val)
			}
		}
	}
	return r
}

// robotsPattern turns a robots.txt path pattern into a regular expression:
// "*" matches any characters, a trailing "$" anchors the end.
func robotsPattern(p string) *regexp.Regexp {
	anchored := strings.HasSuffix(p, "$")
	p = strings.TrimSuffix(p, "$")
	parts := strings.Split(p, "*")
	for i, part := range parts {
		parts[i] = regexp.QuoteMeta(part)
	}
	expr := "^" + strings.Join(parts, ".*")
	if anchored {
		expr += "$"
	}
	return regexp.MustCompile(expr)
}

// Blocked reports whether the path (with query) of u is disallowed. The most
// specific (longest) matching rule wins; on a tie Allow wins, as in Google's
// implementation.
func (r *Robots) Blocked(u *url.URL) bool {
	if r == nil || u == nil {
		return false
	}
	p := u.EscapedPath()
	if u.RawQuery != "" {
		p += "?" + u.RawQuery
	}
	best, allow := -1, true
	for _, rule := range r.rules {
		if !rule.re.MatchString(p) {
			continue
		}
		if l := len(rule.pattern); l > best || (l == best && rule.allow) {
			best, allow = l, rule.allow
		}
	}
	return !allow
}

// Fetch loads /robots.txt of the start URL; a missing file means
// "everything allowed".
func Fetch(ctx context.Context, c *fetch.Client, start *url.URL) *Robots {
	u := start.Scheme + "://" + start.Host + "/robots.txt"
	var buf bytes.Buffer
	resp := c.Download(ctx, u, "", &buf, maxRobotsSize)
	if resp.Err != nil || resp.Status != 200 {
		return &Robots{}
	}
	return Parse(buf.String())
}

// sitemapXML covers both <urlset> and <sitemapindex> documents.
type sitemapXML struct {
	XMLName xml.Name
	URLs    []struct {
		Loc string `xml:"loc"`
	} `xml:"url"`
	Sitemaps []struct {
		Loc string `xml:"loc"`
	} `xml:"sitemap"`
}

// ParseSitemap returns the page URLs and the nested sitemap URLs of a
// sitemap or sitemap index (optionally gzip compressed).
func ParseSitemap(body []byte) (pages, sitemaps []string) {
	if len(body) > 2 && body[0] == 0x1f && body[1] == 0x8b {
		if zr, err := gzip.NewReader(bytes.NewReader(body)); err == nil {
			if b, err := io.ReadAll(io.LimitReader(zr, maxSitemapSize)); err == nil {
				body = b
			}
		}
	}
	var doc sitemapXML
	if err := xml.Unmarshal(body, &doc); err != nil {
		return nil, nil
	}
	for _, u := range doc.URLs {
		if loc := strings.TrimSpace(u.Loc); loc != "" {
			pages = append(pages, loc)
		}
	}
	for _, s := range doc.Sitemaps {
		if loc := strings.TrimSpace(s.Loc); loc != "" {
			sitemaps = append(sitemaps, loc)
		}
	}
	return pages, sitemaps
}

// SitemapURLs collects the page URLs of all sitemaps of the site: the
// ones announced in robots.txt, otherwise /sitemap.xml. Sitemap indexes are
// followed (at most maxSitemaps files); only URLs of the checked site count.
func SitemapURLs(ctx context.Context, c *fetch.Client, start *url.URL, robots *Robots) []string {
	var queue []string
	if robots != nil {
		queue = append(queue, robots.Sitemaps...)
	}
	if len(queue) == 0 {
		queue = []string{start.Scheme + "://" + start.Host + "/sitemap.xml"}
	}
	seenMap, seenURL := map[string]bool{}, map[string]bool{}
	var out []string
	for len(queue) > 0 && len(seenMap) < maxSitemaps && ctx.Err() == nil {
		sm := queue[0]
		queue = queue[1:]
		if seenMap[sm] {
			continue
		}
		seenMap[sm] = true
		var buf bytes.Buffer
		resp := c.Download(ctx, sm, "", &buf, maxSitemapSize)
		if resp.Err != nil || resp.Status != 200 {
			continue
		}
		pages, nested := ParseSitemap(buf.Bytes())
		queue = append(queue, nested...)
		for _, p := range pages {
			n, ok := links.Normalize(p, nil)
			if !ok || seenURL[n] || len(out) >= maxSitemapURLs {
				continue
			}
			if pu, err := url.Parse(n); err == nil && links.SameSite(start, pu) {
				seenURL[n] = true
				out = append(out, n)
			}
		}
	}
	return out
}
