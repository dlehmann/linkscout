// Package mirror creates an offline copy of a website.
//
// It works in two phases:
//
//  1. Download: starting at the start URL all internal pages are fetched and
//     parsed; every internal page, file and embedded resource (and, if
//     enabled, external embedded resources such as CDN images, fonts or
//     scripts) is streamed into a temporary directory. CSS files are scanned
//     for url(...) and @import references.
//  2. Write: once the content type of every URL is known, each one is mapped
//     to a local file name (see LocalPath), links inside HTML and CSS are
//     rewritten to relative paths and the files are moved into place.
//
// Links to pages that were not downloaded stay absolute so they keep working
// when the copy is opened in a browser.
//
// A second run into the same directory updates the copy and a stopped run
// continues where it ended (see state.go); Rules, robots.txt and the sitemap
// decide what belongs to the copy besides the linked pages.
package mirror

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"

	"github.com/dlehmann/linkscout/internal/fetch"
	"github.com/dlehmann/linkscout/internal/links"
	"github.com/dlehmann/linkscout/internal/robots"
)

// Config controls a mirror run.
type Config struct {
	StartURL    string        `json:"url"`
	OutDir      string        `json:"outDir"`
	Fetch       fetch.Options `json:"fetch"`
	Concurrency int           `json:"concurrency"` // default 4
	MaxPages    int           `json:"maxPages"`    // pages to download, 0 = unlimited
	External    bool          `json:"external"`    // also download external embedded resources
	Delay       time.Duration `json:"delay"`
	MaxFileSize int64         `json:"maxFileSize"` // per file, 0 = 512 MiB
	// ExternalFiles also downloads files (PDFs, images, archives …) that
	// pages link to on other hosts; external pages are never downloaded.
	ExternalFiles bool `json:"externalFiles"`
	// Rules include or exclude URLs, see Rules.
	Rules []string `json:"rules,omitempty"`
	// Robots skips the URLs robots.txt disallows (the start URL is always
	// downloaded).
	Robots bool `json:"robots"`
	// Sitemap also downloads the pages of the sitemap(s) that no link leads to.
	Sitemap bool `json:"sitemap"`
	// Fresh ignores the previous copy in OutDir and downloads everything.
	Fresh bool `json:"fresh"`
}

// Progress describes the state of a mirror run.
type Progress struct {
	Running    bool      `json:"running"`
	Done       int       `json:"done"`      // URLs processed
	Total      int       `json:"total"`     // URLs known
	Files      int       `json:"files"`     // files written
	Unchanged  int       `json:"unchanged"` // reused from the previous copy (not modified / continued)
	Removed    int       `json:"removed"`   // files of the previous copy that are no longer on the site
	Bytes      int64     `json:"bytes"`
	OutDir     string    `json:"outDir,omitempty"`
	Error      string    `json:"error,omitempty"`
	StartedAt  time.Time `json:"startedAt,omitempty"`
	FinishedAt time.Time `json:"finishedAt,omitempty"`
	// Problems lists every URL that did not answer with status 200, in the
	// order they were processed (sorted by URL once the run has ended).
	Problems []Problem `json:"problems,omitempty"`
}

// Problem is a URL that could not be downloaded cleanly. Pages and files
// with another status than 200 are still saved when they have a body (an
// error page is part of the site), Saved tells whether that happened.
type Problem struct {
	URL      string `json:"url"`
	Source   string `json:"source,omitempty"` // page that linked to the URL
	Status   int    `json:"status,omitempty"` // final HTTP status, 0 on transport errors
	Error    string `json:"error,omitempty"`  // transport error
	TooLarge bool   `json:"tooLarge,omitempty"`
	Robots   bool   `json:"robots,omitempty"` // disallowed by robots.txt, not requested
	Kept     bool   `json:"kept,omitempty"`   // failed, the version of the previous copy was kept
	Saved    bool   `json:"saved"`
}

// tmpDirName is the directory inside the target directory where phase 1
// stores the downloads; it is removed when the run is complete and kept when
// it was interrupted, so the next run can continue (see state.go).
const tmpDirName = ".linkscout-tmp"

// entry is one URL of the mirror.
type entry struct {
	url      string
	external bool
	page     bool // navigational target (may be HTML that needs parsing)
	tmp      string
	ctype    string
	size     int64
	ok       bool   // downloaded (also with a status other than 200)
	final    string // URL after redirects; relative links in the file resolve against it
	local    string // relative output path, set in phase 2
	status   int
	etag     string
	lastMod  time.Time
	guessed  bool // only found in JavaScript: errors are not reported
	reused   bool // file of the previous copy or of an interrupted run
}

// run holds the state of a single mirror run.
type run struct {
	cfg    Config
	start  *url.URL
	client *fetch.Client
	tmp    string
	rules  Rules
	robots *robots.Robots // nil unless Config.Robots
	prev   map[string]*prevFile
	old    *manifest // manifest of the previous copy, nil if there is none

	mu      sync.Mutex
	entries map[string]*entry // by normalised URL
	aliases map[string]*entry // redirect targets → entry
	order   []*entry
	pages   int // pages queued so far (for MaxPages)
	wg      sync.WaitGroup
	sem     chan struct{}
	prog    *Progress
	notify  func(Progress)
}

// Run mirrors the website synchronously. notify (optional) is called with
// progress snapshots. The returned Progress is the final state.
func Run(ctx context.Context, cfg Config, notify func(Progress)) (Progress, error) {
	norm, ok := links.Normalize(links.ToASCII(cfg.StartURL), nil)
	if !ok {
		return Progress{}, errors.New("invalid start URL")
	}
	if cfg.OutDir == "" {
		return Progress{}, errors.New("no output directory")
	}
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = 4
	}
	if cfg.MaxFileSize <= 0 {
		cfg.MaxFileSize = 512 << 20
	}
	rules, err := ParseRules(cfg.Rules)
	if err != nil {
		return Progress{}, err
	}
	out, err := filepath.Abs(cfg.OutDir)
	if err != nil {
		return Progress{}, err
	}
	cfg.OutDir = out
	tmp := filepath.Join(out, tmpDirName)

	start, _ := url.Parse(norm)
	cfg.Fetch.AuthHost = start.Host // never send credentials to CDNs
	r := &run{
		cfg:     cfg,
		start:   start,
		client:  fetch.New(cfg.Fetch),
		tmp:     tmp,
		rules:   rules,
		entries: map[string]*entry{},
		aliases: map[string]*entry{},
		sem:     make(chan struct{}, cfg.Concurrency),
		prog:    &Progress{Running: true, OutDir: out, StartedAt: time.Now()},
		notify:  notify,
	}
	if r.notify == nil {
		r.notify = func(Progress) {}
	}
	r.loadPrevious()
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return Progress{}, err
	}

	var rb *robots.Robots // also announces the sitemaps
	if cfg.Robots || cfg.Sitemap {
		rb = robots.Fetch(ctx, r.client, start)
	}
	if cfg.Robots {
		r.robots = rb
	}
	r.mu.Lock()
	r.add(ctx, norm, "", true, false)
	r.mu.Unlock()
	r.wg.Wait()

	// Pages of the sitemap that no link leads to come after the linked ones,
	// so MaxPages keeps the linked pages.
	if cfg.Sitemap && ctx.Err() == nil {
		urls := robots.SitemapURLs(ctx, r.client, start, rb)
		r.mu.Lock()
		for _, u := range urls {
			r.add(ctx, u, "", true, false)
		}
		r.mu.Unlock()
		r.wg.Wait()
	}

	if ctx.Err() != nil {
		r.savePartial()
		return r.finish(ctx.Err())
	}
	if err := r.write(); err != nil {
		r.savePartial()
		return r.finish(err)
	}
	os.RemoveAll(tmp)
	return r.finish(nil)
}

// finish marks the run as ended (with err, if any), notifies the listener
// one last time and returns the final progress.
func (r *run) finish(err error) (Progress, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.prog.Running = false
	r.prog.FinishedAt = time.Now()
	// Sort a copy: earlier snapshots share the backing array and may still
	// be read (JSON encoding of /api/state).
	probs := append([]Problem(nil), r.prog.Problems...)
	sort.Slice(probs, func(i, j int) bool { return probs[i].URL < probs[j].URL })
	r.prog.Problems = probs
	if err != nil {
		r.prog.Error = err.Error()
	}
	p := *r.prog
	r.notify(p)
	return p, err
}

// add registers a URL and starts its download. guessed marks URLs that were
// only found in JavaScript. r.mu must be held.
func (r *run) add(ctx context.Context, u, source string, page, guessed bool) {
	// Internationalised host names are stored and requested in their ASCII
	// form, so müller.de and xn--mller-kva.de are the same site and file.
	u = links.ToASCII(u)
	if e, ok := r.entries[u]; ok {
		if page && !e.page && !e.external {
			// first seen as resource, now as page: nothing to do, the content
			// type decides whether it gets parsed.
			e.page = true
		}
		if !guessed {
			e.guessed = false
		}
		return
	}
	if _, ok := r.aliases[u]; ok {
		return
	}
	pu, err := url.Parse(u)
	if err != nil {
		return
	}
	external := !links.SameSite(r.start, pu)
	if u != r.start.String() {
		decided, include := r.rules.Match(pu)
		switch {
		case decided && !include:
			return
		case decided:
			// included by a rule – external pages too (saved, not crawled)
		case external && page:
			// a link to another website: only its files, never its pages
			if !r.cfg.ExternalFiles || !links.IsFile(links.Category(links.Extension(u))) {
				return
			}
		case external && !r.cfg.External:
			return
		}
		if !external && r.robots.Blocked(pu) {
			// Recorded once (as an entry that is never downloaded) and listed.
			r.entries[u] = &entry{url: u, page: page, guessed: guessed}
			r.prog.Total = len(r.entries)
			r.prog.Done++
			if !guessed {
				r.prog.Problems = append(r.prog.Problems, Problem{URL: u, Source: source, Robots: true})
			}
			return
		}
	}
	// MaxPages limits the number of pages that are downloaded at all; files
	// and resources of those pages are always included.
	if page && !links.IsFile(links.Category(links.Extension(u))) {
		if r.cfg.MaxPages > 0 && r.pages >= r.cfg.MaxPages {
			return
		}
		r.pages++
	}
	e := &entry{url: u, external: external, page: page, guessed: guessed}
	r.entries[u] = e
	r.order = append(r.order, e)
	r.prog.Total = len(r.entries)
	r.wg.Add(1)
	go r.download(ctx, e, source)
}

// download fetches one entry into the temp directory and, for HTML, CSS and
// JavaScript, queues the referenced URLs.
func (r *run) download(ctx context.Context, e *entry, source string) {
	defer r.wg.Done()
	select {
	case r.sem <- struct{}{}:
	case <-ctx.Done():
		return
	}
	defer func() { <-r.sem }()
	if r.cfg.Delay > 0 {
		select {
		case <-time.After(r.cfg.Delay):
		case <-ctx.Done():
			return
		}
	}

	tmpPath := filepath.Join(r.tmp, hash(e.url))
	prev := r.prev[e.url] // read-only after loadPrevious
	var resp fetch.Response
	reused := false
	if prev != nil && prev.partial {
		// Downloaded by an interrupted run moments ago: continue with it.
		resp, reused = prev.response(""), true
	} else {
		f, err := os.Create(tmpPath)
		if err != nil {
			r.done(e, 0)
			return
		}
		resp = r.client.DownloadIf(ctx, e.url, source, f, r.cfg.MaxFileSize, prev.validators())
		f.Close()
		if ctx.Err() != nil {
			os.Remove(tmpPath)
			r.done(e, 0) // stopped: not an error of the URL
			return
		}
		if prev != nil && (resp.Status == 304 || temporaryFailure(resp)) && linkOrCopy(prev.src, tmpPath) == nil {
			if resp.Status != 304 {
				r.problem(e, Problem{URL: e.url, Source: source, Status: resp.Status, Error: errText(resp.Err), Kept: true, Saved: true})
				resp.FinalURL = "" // the previous one
			}
			resp, reused = prev.response(resp.FinalURL), true
		}
	}
	// A file at the size limit was cut off: a broken copy is worse than the
	// online link the page keeps instead. Pages with another status than
	// 200 are kept when they have a body (custom error pages, sites that
	// answer every page with 404) and listed as problems; the error body of
	// an image, script or stylesheet would be no use as that file.
	tooLarge := !reused && resp.Size >= r.cfg.MaxFileSize
	keepError := e.page && resp.Size > 0
	if resp.Err != nil || tooLarge || resp.Status != 200 && !keepError {
		os.Remove(tmpPath)
		r.problem(e, Problem{URL: e.url, Source: source, Status: resp.Status, Error: errText(resp.Err), TooLarge: tooLarge})
		r.done(e, 0)
		return
	}
	if resp.Status != 200 { // also when continued: the copy has the error page
		r.problem(e, Problem{URL: e.url, Source: source, Status: resp.Status, Saved: true})
	}

	r.mu.Lock()
	e.tmp, e.ctype, e.ok = tmpPath, resp.ContentType, true
	e.size, e.status, e.etag, e.lastMod, e.reused = resp.Size, resp.Status, resp.ETag, resp.LastModified, reused
	e.final = resp.FinalURL
	final, _ := url.Parse(resp.FinalURL)
	if n, ok := links.Normalize(resp.FinalURL, nil); ok && n != e.url {
		if _, exists := r.entries[n]; !exists {
			r.aliases[n] = e
		}
	}
	internal := !e.external && final != nil && links.SameSite(r.start, final)
	parseHTML := links.IsHTML(e.ctype) && e.page && internal
	r.mu.Unlock()

	if final == nil {
		final, _ = url.Parse(e.url)
	}
	switch {
	case parseHTML:
		body, err := os.ReadFile(tmpPath)
		if err == nil {
			doc := links.ParseHTML(body, final)
			r.mu.Lock()
			for _, l := range doc.Links {
				r.add(ctx, l.URL, e.url, !l.Embedded, false)
			}
			for _, js := range inlineScripts(body) {
				r.addGuessed(ctx, scriptURLs(js, true), final, e.url)
			}
			r.mu.Unlock()
		}
	case isCSS(e.ctype, e.url):
		body, err := os.ReadFile(tmpPath)
		if err == nil {
			r.mu.Lock()
			for _, ref := range links.CSSURLs(string(body)) {
				if abs, ok := links.Normalize(ref, final); ok {
					r.add(ctx, abs, e.url, false, false)
				}
			}
			r.mu.Unlock()
		}
	case isJS(e.ctype, e.url) && internal && resp.Size <= maxScriptSize:
		body, err := os.ReadFile(tmpPath)
		if err == nil {
			r.mu.Lock()
			r.addGuessed(ctx, scriptURLs(string(body), false), final, e.url)
			r.mu.Unlock()
		}
	}
	r.done(e, resp.Size)
}

// maxScriptSize limits the JavaScript files searched for URLs.
const maxScriptSize = 5 << 20

// addGuessed queues the URLs found in a script. Links to pages are queued as
// pages (and parsed), everything else as resource. r.mu must be held.
func (r *run) addGuessed(ctx context.Context, refs []string, base *url.URL, source string) {
	for _, ref := range refs {
		if abs, ok := links.Normalize(ref, base); ok {
			r.add(ctx, abs, source, links.Category(links.Extension(abs)) == links.CatPage, true)
		}
	}
}

// temporaryFailure reports whether a failed download is probably a passing
// problem (then the file of the previous copy is kept): no connection, a
// server error or "too many requests".
func temporaryFailure(resp fetch.Response) bool {
	return resp.Err != nil || resp.Status >= 500 || resp.Status == 429 || resp.Status == 408
}

// errText returns the message of err or "".
func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// problem records a URL that did not answer with status 200 – unless it was
// only guessed from JavaScript.
func (r *run) problem(e *entry, p Problem) {
	r.mu.Lock()
	if !e.guessed {
		r.prog.Problems = append(r.prog.Problems, p)
	}
	r.mu.Unlock()
}

// done counts a processed URL (downloaded or failed) and reports progress.
func (r *run) done(e *entry, size int64) {
	r.mu.Lock()
	r.prog.Done++
	if size > 0 {
		r.prog.Bytes += size
	}
	if e.reused {
		r.prog.Unchanged++
	}
	p := *r.prog
	r.mu.Unlock()
	r.notify(p)
}

// lookup finds the downloaded entry for an absolute URL (or redirect target).
func (r *run) lookup(u string) *entry {
	if e, ok := r.entries[u]; ok && e.ok {
		return e
	}
	if e, ok := r.aliases[u]; ok && e.ok {
		return e
	}
	return nil
}

// write is phase 2: assign local paths, rewrite and move files.
func (r *run) write() error {
	// Deterministic path assignment: sort by URL so collisions resolve the
	// same way on every run.
	ok := make([]*entry, 0, len(r.order))
	for _, e := range r.order {
		if e.ok {
			ok = append(ok, e)
		}
	}
	sort.Slice(ok, func(i, j int) bool { return ok[i].url < ok[j].url })
	used := map[string]string{}
	base := BaseDir(r.start)
	for _, e := range ok {
		u, _ := url.Parse(e.url)
		p := LocalPathIn(u, e.ctype, e.external, base)
		if first, _, _ := strings.Cut(strings.ToLower(p), "/"); first == stateDirName || first == tmpDirName {
			p = "_" + p // never write into LinkScout's own directories
		}
		if other, taken := used[strings.ToLower(p)]; taken && other != e.url {
			p = withSuffix(p, "_"+hash(e.url)[:8])
		}
		used[strings.ToLower(p)] = e.url
		e.local = p
	}
	// A file must not take the name of a directory other files live in
	// (/about without extension next to /about/team.html).
	dirs := map[string]bool{}
	for _, e := range ok {
		for d := path.Dir(e.local); d != "." && d != "/"; d = path.Dir(d) {
			dirs[strings.ToLower(d)] = true
		}
	}
	for _, e := range ok {
		if dirs[strings.ToLower(e.local)] {
			e.local = withSuffix(e.local, "_"+hash(e.url)[:8])
		}
	}

	for _, e := range ok {
		dst := filepath.Join(r.cfg.OutDir, filepath.FromSlash(e.local))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		// Relative links resolve against the URL the file was served from:
		// /edith redirects to /edith/, so "img/a.png" means /edith/img/a.png.
		base, perr := url.Parse(e.final)
		if e.final == "" || perr != nil {
			base, _ = url.Parse(e.url)
		}
		var err error
		switch {
		case links.IsHTML(e.ctype):
			err = r.rewriteFile(e, dst, func(b []byte) []byte { return r.rewriteHTML(b, base, e.local) })
		case isCSS(e.ctype, e.url):
			err = r.rewriteFile(e, dst, func(b []byte) []byte {
				return []byte(links.RewriteCSS(string(b), r.resolver(base, e.local)))
			})
		default:
			err = moveFile(e.tmp, dst)
		}
		if err == nil && needsOriginal(e.ctype, e.url) {
			err = r.keepOriginal(e) // for the next update
		}
		if err != nil {
			return fmt.Errorf("%s: %w", e.url, err)
		}
		r.mu.Lock()
		r.prog.Files++
		p := *r.prog
		r.mu.Unlock()
		r.notify(p)
	}
	removed, err := r.finishState(ok)
	r.mu.Lock()
	r.prog.Removed = removed
	r.mu.Unlock()
	return err
}

// rewriteFile reads the downloaded file of e, transforms it with fn and
// writes the result to dst.
func (r *run) rewriteFile(e *entry, dst string, fn func([]byte) []byte) error {
	b, err := os.ReadFile(e.tmp)
	if err != nil {
		return err
	}
	// A new file instead of overwriting: the old one may be a hard link of
	// a reused download (see linkOrCopy).
	os.Remove(dst)
	return os.WriteFile(dst, fn(b), 0o644)
}

// resolver returns a function that maps a reference found in a document
// located at local (with absolute URL base) to the string that should be
// written into the mirrored copy.
func (r *run) resolver(base *url.URL, local string) func(string) string {
	return func(ref string) string {
		abs, ok := links.Normalize(ref, base)
		if !ok {
			return "" // keep mailto:, #anchors, javascript: … untouched
		}
		frag := ""
		if i := strings.IndexByte(ref, '#'); i >= 0 {
			frag = ref[i:]
		}
		if e := r.lookup(abs); e != nil {
			return RelPath(local, e.local) + frag
		}
		return abs + frag
	}
}

// rewriteHTML rewrites all URL attributes, inline styles and <style> blocks.
func (r *run) rewriteHTML(body []byte, pageURL *url.URL, local string) []byte {
	root, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return body
	}
	// Respect <base href> while resolving, then drop it: in the offline copy
	// all links are relative to the file itself.
	base := pageURL
	var baseNode *html.Node
	var findBase func(n *html.Node)
	findBase = func(n *html.Node) {
		if n.Type == html.ElementNode && n.DataAtom == atom.Base && baseNode == nil {
			baseNode = n
			for _, a := range n.Attr {
				if strings.EqualFold(a.Key, "href") {
					if bu, err := url.Parse(strings.TrimSpace(a.Val)); err == nil {
						base = pageURL.ResolveReference(bu)
					}
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			findBase(c)
		}
	}
	findBase(root)
	if baseNode != nil && baseNode.Parent != nil {
		baseNode.Parent.RemoveChild(baseNode)
	}
	resolve := r.resolver(base, local)
	rewrite := func(v string) string {
		if nv := resolve(v); nv != "" {
			return nv
		}
		return v
	}

	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			attrs := links.URLAttrs(n.DataAtom)
			kept := n.Attr[:0]
			for _, a := range n.Attr {
				key := strings.ToLower(a.Key)
				switch {
				case key == "integrity":
					// Subresource integrity hashes break once the file is rewritten.
					continue
				case key == "srcset":
					a.Val = rewriteSrcset(a.Val, rewrite)
				case key == "style":
					a.Val = links.RewriteCSS(a.Val, resolve)
				case contains(attrs, key):
					a.Val = rewrite(a.Val)
				case n.DataAtom == atom.Meta && key == "content" && isRefresh(n):
					a.Val = rewriteRefresh(a.Val, rewrite)
				}
				kept = append(kept, a)
			}
			n.Attr = kept
			if n.DataAtom == atom.Style && n.FirstChild != nil && n.FirstChild.Type == html.TextNode {
				n.FirstChild.Data = links.RewriteCSS(n.FirstChild.Data, resolve)
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(root)
	var buf bytes.Buffer
	if err := html.Render(&buf, root); err != nil {
		return body
	}
	return buf.Bytes()
}

// isRefresh reports whether a <meta> element is http-equiv="refresh".
func isRefresh(n *html.Node) bool {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, "http-equiv") && strings.EqualFold(a.Val, "refresh") {
			return true
		}
	}
	return false
}

// rewriteRefresh replaces the URL of a meta refresh content attribute by
// fn(url) and keeps the delay ("5; url=…").
func rewriteRefresh(content string, fn func(string) string) string {
	prefix, target, ok := links.RefreshTarget(content)
	if !ok {
		return content
	}
	return prefix + fn(target)
}

// rewriteSrcset rewrites the URL of every srcset candidate and keeps the
// descriptors ("1x", "640w"). It splits like the parser so URLs containing
// commas survive.
func rewriteSrcset(v string, fn func(string) string) string {
	var parts []string
	for _, cand := range links.SplitSrcset(v) {
		f := strings.Fields(cand)
		if len(f) == 0 {
			continue
		}
		f[0] = fn(f[0])
		parts = append(parts, strings.Join(f, " "))
	}
	return strings.Join(parts, ", ")
}

// contains reports whether list contains s.
func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// isCSS reports whether a downloaded file is a stylesheet (by content type,
// or by extension when the server sent none).
func isCSS(ctype, u string) bool {
	return ctype == "text/css" || (ctype == "" && links.Extension(u) == "css")
}

// isJS reports whether a downloaded file is JavaScript.
func isJS(ctype, u string) bool {
	if strings.Contains(ctype, "javascript") || ctype == "text/ecmascript" {
		return true
	}
	ext := links.Extension(u)
	return (ctype == "" || ctype == "text/plain" || ctype == "application/octet-stream") && (ext == "js" || ext == "mjs")
}

// LocalPath maps a URL to a relative, slash separated file path inside the
// mirror directory:
//
//	https://site/                 → index.html
//	https://site/about            → about/index.html     (HTML)
//	https://site/about/           → about/index.html
//	https://site/page.php?id=3    → page.php_q1a2b3c4.html
//	https://site/img/logo.png     → img/logo.png
//	https://cdn.other/x/lib.js    → _external/cdn.other/x/lib.js
//
// Query strings are folded into the file name as a short hash so different
// queries do not overwrite each other.
func LocalPath(u *url.URL, ctype string, external bool) string {
	segs := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
	for i, s := range segs {
		segs[i] = sanitize(s)
	}
	p := strings.Join(segs, "/")
	if p == "" || strings.HasSuffix(p, "/") {
		p += "index.html"
	}
	ext := path.Ext(path.Base(p))
	lext := strings.ToLower(ext)
	isHTML := links.IsHTML(ctype)
	switch {
	case isHTML && ext == "":
		p += "/index.html"
	case isHTML && lext != ".html" && lext != ".htm":
		p += ".html"
	case !isHTML && ext == "":
		p += extForType(ctype)
	}
	if u.RawQuery != "" {
		p = withSuffix(p, "_q"+hash(u.RawQuery)[:8])
	}
	if external {
		p = "_external/" + sanitize(u.Host) + "/" + p
	}
	return p
}

// BaseDir returns the directory of the start URL, which becomes the root of
// the mirror: "/" for https://site/, "/edith/" for a project site such as
// https://user.github.io/edith or https://user.github.io/edith/index.html.
func BaseDir(start *url.URL) string {
	p := start.Path
	switch {
	case p == "" || p == "/":
		return "/"
	case strings.HasSuffix(p, "/"):
		return p
	case links.Extension(start.String()) != "": // a file such as /edith/index.html
		return path.Dir(p) + "/"
	default: // "/edith" is the directory /edith/ (servers redirect it there)
		return p + "/"
	}
}

// LocalPathIn is LocalPath relative to the start directory base (see
// BaseDir): URLs inside it are placed at the root of the mirror, other URLs
// of the same site go to _site/<full path>, so the site's own index.html
// ends up directly in the target directory.
func LocalPathIn(u *url.URL, ctype string, external bool, base string) string {
	if external || base == "/" {
		return LocalPath(u, ctype, external)
	}
	rel := *u
	switch {
	case strings.HasPrefix(u.Path, base):
		rel.Path = "/" + strings.TrimPrefix(u.Path, base)
	case u.Path+"/" == base: // the start URL itself without trailing slash
		rel.Path = "/"
	default:
		return "_site/" + LocalPath(u, ctype, false)
	}
	return LocalPath(&rel, ctype, false)
}

// sanitize makes a single path segment safe for every common file system.
func sanitize(s string) string {
	if s == "." || s == ".." {
		return "_"
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r < 0x20, strings.ContainsRune(`<>:"\|?*`, r):
			b.WriteByte('_')
		default:
			b.WriteRune(r)
		}
	}
	out := b.String()
	if len(out) > 200 { // stay below NAME_MAX (255 bytes) with room for suffixes
		ext := path.Ext(out)
		if len(ext) > 16 {
			ext = ""
		}
		out = out[:200-len(ext)-9] + "_" + hash(s)[:8] + ext
	}
	return out
}

// extForType returns the file extension for resources whose URL has none
// (e.g. /logo served as image/png → .png), so the copy opens correctly from
// disk where no content type exists.
func extForType(ct string) string {
	switch ct {
	case "text/css":
		return ".css"
	case "text/javascript", "application/javascript", "application/x-javascript":
		return ".js"
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "image/svg+xml":
		return ".svg"
	case "image/avif":
		return ".avif"
	case "image/x-icon", "image/vnd.microsoft.icon":
		return ".ico"
	case "font/woff2":
		return ".woff2"
	case "font/woff":
		return ".woff"
	case "application/pdf":
		return ".pdf"
	case "application/json":
		return ".json"
	case "application/xml", "text/xml":
		return ".xml"
	}
	return ""
}

// withSuffix inserts suffix before the extension of the last path element.
func withSuffix(p, suffix string) string {
	ext := path.Ext(path.Base(p))
	return strings.TrimSuffix(p, ext) + suffix + ext
}

// RelPath returns the relative URL path from the document at from to the
// file at to (both slash separated paths inside the mirror).
func RelPath(from, to string) string {
	rel, err := filepath.Rel(path.Dir("/"+from), "/"+to)
	if err != nil {
		return to
	}
	rel = filepath.ToSlash(rel)
	// Escape characters that have a meaning in URLs (space, %, ?, #).
	return (&url.URL{Path: rel}).EscapedPath()
}

// hash returns the hex SHA-1 of s; short prefixes make file names unique.
func hash(s string) string {
	h := sha1.Sum([]byte(s))
	return hex.EncodeToString(h[:])
}

// moveFile renames src to dst and falls back to copying when both are on
// different file systems.
func moveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// ZipDir writes the content of dir as a ZIP archive to w. Paths inside the
// archive are relative to dir.
func ZipDir(dir string, w io.Writer) error {
	zw := zip.NewWriter(w)
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// LinkScout's own data is not part of the copy.
			if p != dir && (d.Name() == tmpDirName || d.Name() == stateDirName) {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		zf, err := zw.Create(filepath.ToSlash(rel))
		if err != nil {
			return err
		}
		_, err = io.Copy(zf, f)
		return err
	})
	if err != nil {
		zw.Close()
		return err
	}
	return zw.Close()
}

// Mirror runs one mirror job at a time in the background; it is used by the
// web UI. It is safe for concurrent use.
type Mirror struct {
	mu     sync.Mutex
	prog   Progress
	cancel context.CancelFunc
	done   chan struct{}
}

// ErrBusy is returned by Start while a job is running.
var ErrBusy = errors.New("mirror is already running")

// Start launches a mirror job in the background.
func (m *Mirror) Start(cfg Config) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.prog.Running {
		return ErrBusy
	}
	if _, ok := links.Normalize(cfg.StartURL, nil); !ok {
		return errors.New("invalid start URL")
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.done = make(chan struct{})
	m.prog = Progress{Running: true, OutDir: cfg.OutDir, StartedAt: time.Now()}
	done := m.done
	go func() {
		defer close(done)
		defer cancel()
		p, err := Run(ctx, cfg, func(p Progress) {
			m.mu.Lock()
			m.prog = p
			m.mu.Unlock()
		})
		m.mu.Lock()
		m.prog = p
		m.prog.Running = false
		if err != nil {
			m.prog.Error = err.Error()
			if m.prog.FinishedAt.IsZero() {
				m.prog.FinishedAt = time.Now()
			}
		}
		m.mu.Unlock()
	}()
	return nil
}

// Stop cancels the running job.
func (m *Mirror) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cancel != nil {
		m.cancel()
	}
}

// Wait blocks until the running job (if any) is finished.
func (m *Mirror) Wait() {
	m.mu.Lock()
	done := m.done
	m.mu.Unlock()
	if done != nil {
		<-done
	}
}

// Progress returns the current state.
func (m *Mirror) Progress() Progress {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.prog
}
