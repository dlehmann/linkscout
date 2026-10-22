package crawler

// SEO checks.
//
// While crawling, the facts of every internal HTML page are stored in
// Result.SEO (see pageSEO). Analyze evaluates them together with the
// existing data (status, redirects, depth, sitemap, robots.txt) and sets
// Result.Issues. It is cheap enough (O(n)) to run on every state snapshot,
// so the checks always match the current results – also for imported
// reports.

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dlehmann/linkscout/internal/fetch"
	"github.com/dlehmann/linkscout/internal/links"
)

// Severities of SEO issues.
const (
	SevError   = "error"   // hurts users or search engines, fix it
	SevWarning = "warning" // probably a mistake
	SevNotice  = "notice"  // worth a look, may be intentional
)

// IssueDef describes one SEO check. The text lives in the i18n tables under
// "issue.<id>" (name) and "issue.<id>.help" (explanation).
type IssueDef struct {
	ID       string `json:"id"`
	Severity string `json:"severity"`
}

// IssueCount is an issue with the number of URLs that have it.
type IssueCount struct {
	IssueDef
	Count int `json:"count"`
}

// Issues lists every check in display order.
var Issues = []IssueDef{
	// Indexability
	{"noindex", SevWarning},
	{"canonical_broken", SevError},
	{"canonical_multiple", SevWarning},
	{"canonical_other", SevNotice},
	{"canonical_missing", SevNotice},
	{"robots_blocked", SevNotice},
	// Titles and descriptions
	{"title_missing", SevError},
	{"title_multiple", SevWarning},
	{"title_duplicate", SevWarning},
	{"title_long", SevNotice},
	{"title_short", SevNotice},
	{"desc_missing", SevWarning},
	{"desc_multiple", SevWarning},
	{"desc_duplicate", SevWarning},
	{"desc_long", SevNotice},
	{"desc_short", SevNotice},
	// Headings
	{"h1_missing", SevWarning},
	{"h1_multiple", SevNotice},
	{"h1_duplicate", SevNotice},
	{"heading_skip", SevNotice},
	// Redirects and links
	{"redirect_loop", SevError},
	{"redirect_chain", SevWarning},
	{"link_to_redirect", SevNotice},
	{"fragment_missing", SevWarning},
	{"mixed_content", SevError},
	// Images
	{"img_no_alt", SevWarning},
	{"img_large", SevNotice},
	// Sitemap and structure
	{"sitemap_error", SevError},
	{"sitemap_noindex", SevWarning},
	{"sitemap_orphan", SevWarning},
	{"sitemap_redirect", SevNotice},
	{"deep_page", SevNotice},
	{"few_inlinks", SevNotice},
	// International pages
	{"hreflang_invalid", SevError},
	{"hreflang_broken", SevError},
	{"hreflang_no_return", SevWarning},
	{"hreflang_canonical_conflict", SevWarning},
	// Markup
	{"jsonld_invalid", SevError},
	{"viewport_missing", SevWarning},
	{"lang_missing", SevNotice},
	{"social_missing", SevNotice},
	{"duplicate_content", SevWarning},
	// Security (checked on the start page, the headers are usually site-wide)
	{"cert_expiring", SevWarning},
	{"security_headers", SevNotice},
	// URLs
	{"url_long", SevNotice},
	{"url_uppercase", SevNotice},
	{"url_underscore", SevNotice},
	{"url_params", SevNotice},
}

// Thresholds of the checks (the usual values of SEO tools: titles are cut
// in search results at about 60 characters, descriptions at about 160).
const (
	titleMax       = 60
	titleMin       = 10
	descMax        = 160
	descMin        = 50
	urlMax         = 115
	imageMaxBytes  = 300 << 10
	deepClicks     = 3
	certWarnBefore = 30 * 24 * time.Hour
)

// hreflangRe accepts "x-default" and language[-script][-region] codes such
// as "de", "de-AT", "zh-Hant-TW" or "es-419".
var hreflangRe = regexp.MustCompile(`(?i)^(x-default|[a-z]{2,3}(-[a-z]{4})?(-([a-z]{2}|[0-9]{3}))?)$`)

// linkCanonicalRe finds the canonical URL in a Link response header.
var linkCanonicalRe = regexp.MustCompile(`<([^>]+)>\s*;[^,]*rel="?canonical"?`)

// securityHeaders are the response headers whose absence is reported, with
// alternatives that serve the same purpose.
var securityHeaders = []struct {
	name         string
	alternatives []string
	httpsOnly    bool
}{
	{"Strict-Transport-Security", nil, true},
	{"X-Content-Type-Options", nil, false},
	{"X-Frame-Options", []string{"Content-Security-Policy"}, false}, // CSP frame-ancestors also prevents framing
	{"Referrer-Policy", nil, false},
}

// pageSEO extracts the facts of an internal HTML page.
func pageSEO(doc links.Document, resp fetch.Response, page *url.URL) *PageSEO {
	f := doc.Facts
	p := &PageSEO{
		Titles:       f.Titles,
		Descriptions: f.Descriptions,
		Robots:       f.Robots,
		Canonicals:   f.Canonicals,
		H1:           f.H1,
		Headings:     f.Headings,
		ImagesNoAlt:  f.ImagesNoAlt,
		Hreflang:     f.Hreflang,
		JSONLD:       len(f.JSONLD),
		Social:       f.Social,
		Lang:         f.Lang,
		Viewport:     f.Viewport,
		IDs:          f.IDs,
		Fragments:    f.Fragments,
		CertNotAfter: resp.CertNotAfter,
	}
	for _, v := range resp.Header.Values("X-Robots-Tag") {
		p.Robots = append(p.Robots, strings.ToLower(strings.TrimSpace(v)))
	}
	for _, v := range resp.Header.Values("Link") {
		for _, m := range linkCanonicalRe.FindAllStringSubmatch(v, -1) {
			if abs, ok := links.Normalize(m[1], page); ok {
				p.Canonicals = append(p.Canonicals, abs)
			}
		}
	}
	for _, block := range f.JSONLD {
		var v any
		if err := json.Unmarshal([]byte(block), &v); err != nil {
			p.JSONLDErrors = append(p.JSONLDErrors, err.Error())
		}
	}
	if page.Scheme == "https" {
		for _, l := range doc.Links {
			if l.Embedded && strings.HasPrefix(l.URL, "http://") && len(p.Mixed) < 200 {
				p.Mixed = append(p.Mixed, l.URL)
			}
		}
	}
	if len(resp.Body) > 0 {
		h := sha1.Sum(resp.Body)
		p.ContentHash = hex.EncodeToString(h[:])
	}
	p.Security = missingSecurityHeaders(resp.Header, page.Scheme == "https")
	p.ThirdParty = thirdParty(doc, page)
	return p
}

func missingSecurityHeaders(h http.Header, https bool) []string {
	var missing []string
	for _, sh := range securityHeaders {
		if sh.httpsOnly && !https {
			continue
		}
		present := h.Get(sh.name) != ""
		for _, alt := range sh.alternatives {
			if alt == "Content-Security-Policy" && strings.Contains(strings.ToLower(h.Get(alt)), "frame-ancestors") {
				present = true
			}
		}
		if !present {
			missing = append(missing, sh.name)
		}
	}
	return missing
}

// noindex reports whether a page asks search engines not to index it.
func (p *PageSEO) noindex() bool {
	for _, v := range p.Robots {
		if strings.Contains(v, "noindex") || strings.Contains(v, "none") {
			return true
		}
	}
	return false
}

// Analyze runs every check over rs, stores the failed checks in
// Result.Issues and returns the issues that occur with their counts (in the
// order of Issues). rs is modified in place.
func Analyze(rs []Result) []IssueCount {
	byURL := make(map[string]*Result, len(rs))
	for i := range rs {
		rs[i].Issues = nil
		byURL[rs[i].URL] = &rs[i]
	}
	add := func(r *Result, id string) {
		for _, have := range r.Issues {
			if have == id {
				return
			}
		}
		r.Issues = append(r.Issues, id)
	}
	// own returns the URL a page was served from (after redirects).
	own := func(r *Result) string {
		if r.FinalURL != "" {
			return r.FinalURL
		}
		return r.URL
	}
	// lookup finds a result by URL, also when the URL is a redirect target.
	finals := map[string]*Result{}
	for i := range rs {
		if rs[i].FinalURL != "" {
			finals[rs[i].FinalURL] = &rs[i]
		}
	}
	lookup := func(u string) *Result {
		if r, ok := byURL[u]; ok {
			return r
		}
		return finals[u]
	}

	titles, descs, h1s, hashes := map[string][]*Result{}, map[string][]*Result{}, map[string][]*Result{}, map[string][]*Result{}
	for i := range rs {
		r := &rs[i]
		if r.Pending {
			continue
		}
		analyzeURL(r, add)
		p := r.SEO
		if p == nil || r.Status != 200 {
			continue
		}
		noindex := p.noindex()
		if noindex {
			add(r, "noindex")
		}
		canonicalOther := false
		switch {
		case len(p.Canonicals) == 0:
			add(r, "canonical_missing")
		case len(uniq(p.Canonicals)) > 1:
			add(r, "canonical_multiple")
		}
		if len(p.Canonicals) > 0 && p.Canonicals[0] != own(r) {
			canonicalOther = true
			add(r, "canonical_other")
			if t := lookup(p.Canonicals[0]); t != nil && !t.Pending && (t.Status != 200 || len(t.Redirects) > 0) {
				add(r, "canonical_broken")
			}
		}
		// Duplicates only matter between pages that are meant to be indexed.
		indexable := !noindex && !canonicalOther

		checkText(r, p.Titles, "title", titleMin, titleMax, add)
		checkText(r, p.Descriptions, "desc", descMin, descMax, add)
		if indexable {
			if len(p.Titles) > 0 && p.Titles[0] != "" {
				titles[p.Titles[0]] = append(titles[p.Titles[0]], r)
			}
			if len(p.Descriptions) > 0 && p.Descriptions[0] != "" {
				descs[p.Descriptions[0]] = append(descs[p.Descriptions[0]], r)
			}
			if len(p.H1) > 0 && p.H1[0] != "" {
				h1s[p.H1[0]] = append(h1s[p.H1[0]], r)
			}
			if p.ContentHash != "" {
				hashes[p.ContentHash] = append(hashes[p.ContentHash], r)
			}
		}

		switch {
		case len(p.H1) == 0:
			add(r, "h1_missing")
		case len(p.H1) > 1:
			add(r, "h1_multiple")
		}
		prev := 1
		for _, l := range p.Headings {
			if l > prev+1 {
				add(r, "heading_skip")
				break
			}
			prev = l
		}

		if len(p.ImagesNoAlt) > 0 {
			add(r, "img_no_alt")
		}
		if len(p.Mixed) > 0 {
			add(r, "mixed_content")
		}
		if len(p.JSONLDErrors) > 0 {
			add(r, "jsonld_invalid")
		}
		if !p.Viewport {
			add(r, "viewport_missing")
		}
		if p.Lang == "" {
			add(r, "lang_missing")
		}
		if !containsAny(p.Social, "og:title", "twitter:title") || !containsAny(p.Social, "og:image", "twitter:image") {
			add(r, "social_missing")
		}
		if r.Depth == 0 { // site-wide properties, reported once
			if len(p.Security) > 0 {
				add(r, "security_headers")
			}
			if !p.CertNotAfter.IsZero() && time.Until(p.CertNotAfter) < certWarnBefore {
				add(r, "cert_expiring")
			}
		}
		if noindex && r.InSitemap {
			add(r, "sitemap_noindex")
		}

		for _, f := range p.Fragments {
			target, frag, _ := strings.Cut(f, "#")
			if !fragmentFound(lookup(target), frag) {
				add(r, "fragment_missing")
				break
			}
		}

		for _, alt := range p.Hreflang {
			if !hreflangRe.MatchString(alt.Lang) {
				add(r, "hreflang_invalid")
			}
			t := lookup(alt.URL)
			if t == nil || t.Pending {
				continue
			}
			if t.Status != 200 {
				add(r, "hreflang_broken")
				continue
			}
			if t.SEO != nil && alt.URL != own(r) && !hasAlternate(t.SEO.Hreflang, own(r), r.URL) {
				add(r, "hreflang_no_return")
			}
		}
		if len(p.Hreflang) > 0 && canonicalOther {
			add(r, "hreflang_canonical_conflict")
		}
	}
	for _, group := range []struct {
		m  map[string][]*Result
		id string
	}{{titles, "title_duplicate"}, {descs, "desc_duplicate"}, {h1s, "h1_duplicate"}, {hashes, "duplicate_content"}} {
		for _, list := range group.m {
			if len(list) > 1 {
				for _, r := range list {
					add(r, group.id)
				}
			}
		}
	}

	counts := map[string]int{}
	for i := range rs {
		for _, id := range rs[i].Issues {
			counts[id]++
		}
	}
	var out []IssueCount
	for _, d := range Issues {
		if n := counts[d.ID]; n > 0 {
			out = append(out, IssueCount{IssueDef: d, Count: n})
		}
	}
	return out
}

// analyzeURL runs the checks that need no page content: redirects, images,
// sitemap, robots.txt, structure and the URL itself.
func analyzeURL(r *Result, add func(*Result, string)) {
	if strings.Contains(r.Error, fetch.ErrRedirectLoop.Error()) {
		add(r, "redirect_loop")
	}
	if r.Category == links.CatImage && r.Size > imageMaxBytes && r.Status == 200 {
		add(r, "img_large")
	}
	if !r.Internal {
		return
	}
	if len(r.Redirects) > 1 {
		add(r, "redirect_chain")
	}
	if len(r.Redirects) > 0 && len(r.FoundOn) > 0 && r.Status == 200 {
		add(r, "link_to_redirect")
	}
	if r.RobotsBlocked {
		add(r, "robots_blocked")
	}
	if r.InSitemap {
		switch {
		case r.Status != 200:
			add(r, "sitemap_error")
		case len(r.Redirects) > 0:
			add(r, "sitemap_redirect")
		}
		if r.Depth == NotLinked {
			add(r, "sitemap_orphan")
		}
	}
	if r.Kind != KindInternal {
		return
	}
	if r.Depth > deepClicks {
		add(r, "deep_page")
	}
	if r.Depth > 0 && len(r.FoundOn) == 1 && r.Status == 200 && len(r.Redirects) == 0 {
		add(r, "few_inlinks")
	}
	u, err := url.Parse(r.URL)
	if err != nil {
		return
	}
	if len(r.URL) > urlMax {
		add(r, "url_long")
	}
	if u.Path != strings.ToLower(u.Path) {
		add(r, "url_uppercase")
	}
	if strings.Contains(u.Path, "_") {
		add(r, "url_underscore")
	}
	if u.RawQuery != "" {
		add(r, "url_params")
	}
}

// checkText checks a title or description list: missing, several, too long,
// too short (the severity comes from Issues).
func checkText(r *Result, list []string, prefix string, min, max int, add func(*Result, string)) {
	if len(list) == 0 || strings.TrimSpace(list[0]) == "" {
		add(r, prefix+"_missing")
		return
	}
	if len(list) > 1 {
		add(r, prefix+"_multiple")
	}
	switch n := utf8.RuneCountInString(list[0]); {
	case n > max:
		add(r, prefix+"_long")
	case n < min:
		add(r, prefix+"_short")
	}
}

// fragmentFound reports whether frag exists on the target page. Unknown
// targets (not crawled, not HTML) count as found – nothing can be said.
func fragmentFound(target *Result, frag string) bool {
	if target == nil || target.SEO == nil || target.Status != 200 {
		return true
	}
	if dec, err := url.PathUnescape(frag); err == nil {
		frag = dec
	}
	// "#top" always works, text fragments (#:~:text=) are not element IDs.
	if frag == "" || strings.EqualFold(frag, "top") || strings.HasPrefix(frag, ":~:") {
		return true
	}
	for _, id := range target.SEO.IDs {
		if id == frag {
			return true
		}
	}
	return false
}

func hasAlternate(alts []links.Alternate, urls ...string) bool {
	for _, a := range alts {
		for _, u := range urls {
			if a.URL == u {
				return true
			}
		}
	}
	return false
}

func containsAny(list []string, vals ...string) bool {
	for _, v := range list {
		for _, w := range vals {
			if v == w {
				return true
			}
		}
	}
	return false
}

func uniq(list []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range list {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// BrokenFragments returns the #fragment links of a page whose target element
// does not exist (the links behind the fragment_missing issue). lookup
// finds a result by URL.
func BrokenFragments(p *PageSEO, lookup func(string) *Result) []string {
	if p == nil {
		return nil
	}
	var out []string
	for _, f := range p.Fragments {
		target, frag, _ := strings.Cut(f, "#")
		if !fragmentFound(lookup(target), frag) {
			out = append(out, f)
		}
	}
	return out
}
