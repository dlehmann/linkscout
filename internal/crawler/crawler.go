// Package crawler walks a website and checks every URL it finds.
//
// Starting from one URL the crawler downloads all internal HTML pages (same
// host, "www." and http/https are treated as equal), extracts their links and
// checks each of them exactly once:
//
//   - internal pages are fetched with GET and parsed for further links
//   - external pages are fetched with GET (body capped) to get the <title>;
//     redirects are followed and the final target is recorded
//   - files (PDF, images, archives, CSS, JS, fonts …) are probed with HEAD and
//     fall back to GET when the server rejects HEAD
//
// A crawler instance keeps its results between runs so that a second run can
// re-check only the URLs that did not answer with 200 (see RecheckFailed).
package crawler

import (
	"context"
	"errors"
	"net/url"
	"sort"
	"sync"
	"time"

	"github.com/dlehmann/linkscout/internal/fetch"
	"github.com/dlehmann/linkscout/internal/links"
	"github.com/dlehmann/linkscout/internal/robots"
)

// Kinds of results.
const (
	KindInternal = "internal" // page on the checked site
	KindExternal = "external" // page on another site
	KindFile     = "file"     // linked or embedded file (PDF, image, CSS …), internal or external
)

// maxFoundOn limits how many referring pages are stored per URL.
const maxFoundOn = 50

// Result describes one checked URL. It is also the JSON export format.
type Result struct {
	URL         string    `json:"url"`
	FinalURL    string    `json:"finalUrl,omitempty"`  // set when the URL redirected
	Redirects   []string  `json:"redirects,omitempty"` // redirect hops (first = URL)
	Status      int       `json:"status"`              // final HTTP status, 0 = error/pending
	Error       string    `json:"error,omitempty"`
	Kind        string    `json:"kind"` // internal | external | file
	Internal    bool      `json:"internal"`
	Type        string    `json:"type"`     // by file extension, e.g. html, pdf, jpg
	Category    string    `json:"category"` // page, image, pdf, document …
	Title       string    `json:"title,omitempty"`
	ContentType string    `json:"contentType,omitempty"`
	MimeType    string    `json:"mimeType,omitempty"` // shown as type in the UI, see links.MimeType
	Size        int64     `json:"size"`
	DurationMs  int64     `json:"durationMs"`
	Depth       int       `json:"depth"` // clicks from the start page, -1 = only in the sitemap
	FoundOn     []string  `json:"foundOn,omitempty"`
	Pending     bool      `json:"pending,omitempty"`
	CheckedAt   time.Time `json:"checkedAt,omitempty"`
	// LastModified is the Last-Modified header of a successful response.
	LastModified time.Time `json:"lastModified,omitempty"`

	// SEO data (all optional, so older reports stay valid).
	InSitemap     bool     `json:"inSitemap,omitempty"`     // listed in sitemap.xml
	RobotsBlocked bool     `json:"robotsBlocked,omitempty"` // disallowed by robots.txt
	SEO           *PageSEO `json:"seo,omitempty"`           // facts of internal HTML pages
	Issues        []string `json:"issues,omitempty"`        // IDs of the SEO checks that failed, see Analyze
}

// NotLinked is the depth of URLs that are only known from the sitemap.
const NotLinked = -1

// PageSEO holds what the SEO checks need from an internal HTML page.
type PageSEO struct {
	Titles       []string          `json:"titles,omitempty"`
	Descriptions []string          `json:"descriptions,omitempty"`
	Robots       []string          `json:"robots,omitempty"` // meta robots and X-Robots-Tag values
	Canonicals   []string          `json:"canonicals,omitempty"`
	H1           []string          `json:"h1,omitempty"`
	Headings     []int             `json:"headings,omitempty"`
	ImagesNoAlt  []string          `json:"imagesNoAlt,omitempty"`
	Mixed        []string          `json:"mixed,omitempty"` // http:// resources on an https page
	Hreflang     []links.Alternate `json:"hreflang,omitempty"`
	JSONLD       int               `json:"jsonld,omitempty"`       // number of JSON-LD blocks
	JSONLDErrors []string          `json:"jsonldErrors,omitempty"` // parse errors
	Social       []string          `json:"social,omitempty"`       // og:* / twitter:* present
	Lang         string            `json:"lang,omitempty"`
	Viewport     bool              `json:"viewport,omitempty"`
	IDs          []string          `json:"ids,omitempty"`             // fragment targets
	Fragments    []string          `json:"fragments,omitempty"`       // links with #fragment
	ContentHash  string            `json:"contentHash,omitempty"`     // SHA-1 of the HTML
	Security     []string          `json:"securityMissing,omitempty"` // missing security headers
	CertNotAfter time.Time         `json:"certNotAfter,omitempty"`
	ThirdParty   []string          `json:"thirdParty,omitempty"` // embedded resources from other hosts, see NewThirdParty
}

// OK reports whether the URL answered with status 200 (after redirects).
func (r *Result) OK() bool { return r.Status == 200 }

// Config controls a crawl.
type Config struct {
	StartURL      string        `json:"url"`
	Fetch         fetch.Options `json:"fetch"`
	Concurrency   int           `json:"concurrency"`   // parallel requests, default 8
	MaxPages      int           `json:"maxPages"`      // internal pages to parse, 0 = unlimited
	MaxDepth      int           `json:"maxDepth"`      // link depth, 0 = unlimited
	Delay         time.Duration `json:"delay"`         // pause before each request of a worker
	CheckExternal bool          `json:"checkExternal"` // probe external URLs
	Sitemap       bool          `json:"sitemap"`       // also check the URLs of sitemap.xml
}

// Progress is a snapshot of the crawler state.
type Progress struct {
	Running    bool      `json:"running"`
	Mode       string    `json:"mode,omitempty"` // "all" or "failed"
	Total      int       `json:"total"`
	Done       int       `json:"done"`
	StartedAt  time.Time `json:"startedAt,omitempty"`
	FinishedAt time.Time `json:"finishedAt,omitempty"`
	Stopped    bool      `json:"stopped,omitempty"`
	StartURL   string    `json:"startUrl,omitempty"`
}

// ErrBusy is returned when a run is requested while another one is active.
var ErrBusy = errors.New("crawler is already running")

// Crawler is safe for concurrent use. Create it with New.
type Crawler struct {
	mu       sync.Mutex
	cfg      Config
	start    *url.URL
	client   *fetch.Client
	results  map[string]*Result
	order    []string // insertion order of results
	pages    int      // internal pages parsed in the current run
	progress Progress
	cancel   context.CancelFunc
	done     chan struct{}
	wg       sync.WaitGroup
	sem      chan struct{}
	ctx      context.Context
	visited  map[string]bool        // URLs processed in the current run
	robots   *robots.Robots         // rules of the site's robots.txt (loaded by Start)
	content  map[string]pageContent // compressed HTML and text of the analysed pages, see Search
	list     *ListInfo              // set while the results are an imported list export, see LoadReport
}

// New creates an idle crawler.
func New() *Crawler {
	return &Crawler{results: map[string]*Result{}, content: map[string]pageContent{}}
}

// Start begins a full crawl with cfg in the background. Previous results are
// discarded. Use Wait to block until it is finished.
func (c *Crawler) Start(cfg Config) error {
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = 8
	}
	norm, ok := links.Normalize(cfg.StartURL, nil)
	if !ok {
		return errors.New("invalid start URL")
	}
	start, _ := url.Parse(norm)

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.progress.Running {
		return ErrBusy
	}
	cfg.StartURL = norm
	cfg.Fetch.AuthHost = start.Host // credentials belong to the checked site only
	c.cfg = cfg
	c.start = start
	c.client = fetch.New(cfg.Fetch)
	c.results = map[string]*Result{}
	c.order = nil
	c.robots = nil
	c.content = map[string]pageContent{}
	c.list = nil
	c.beginLocked("all")
	// robots.txt is loaded first (it marks the URLs), then the crawl begins.
	c.wg.Add(1)
	go func(ctx context.Context, client *fetch.Client) {
		defer c.wg.Done()
		rules := robots.Fetch(ctx, client, start)
		c.mu.Lock()
		defer c.mu.Unlock()
		c.robots = rules
		c.enqueueLocked(norm, 0, "")
	}(c.ctx, c.client)
	c.finishWhenIdle()
	return nil
}

// RecheckFailed checks again every URL whose last status was not 200,
// keeping all other results. New links discovered on re-checked internal pages
// are followed as usual. The configuration may be changed (e.g. another
// referer); the start URL is taken from the previous run.
func (c *Crawler) RecheckFailed(cfg Config) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.progress.Running {
		return ErrBusy
	}
	if c.start == nil {
		return errors.New("nothing to re-check, run a full check first")
	}
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = 8
	}
	cfg.StartURL = c.cfg.StartURL
	cfg.Fetch.AuthHost = c.start.Host
	c.cfg = cfg
	c.client = fetch.New(cfg.Fetch)
	c.list = nil // re-checked pages need a fresh analysis
	c.beginLocked("failed")
	// Successful pages count against MaxPages, they are not parsed again.
	for _, u := range c.order {
		r := c.results[u]
		if r.OK() {
			c.visited[u] = true
			if r.Kind == KindInternal {
				c.pages++
			}
		}
	}
	for _, u := range c.order {
		r := c.results[u]
		if r.OK() {
			continue
		}
		c.dispatchLocked(r)
	}
	c.finishWhenIdle()
	return nil
}

// beginLocked resets per-run state. c.mu must be held.
func (c *Crawler) beginLocked(mode string) {
	ctx, cancel := context.WithCancel(context.Background())
	c.ctx, c.cancel = ctx, cancel
	c.sem = make(chan struct{}, c.cfg.Concurrency)
	c.done = make(chan struct{})
	c.visited = map[string]bool{}
	c.pages = 0
	c.progress = Progress{Running: true, Mode: mode, StartedAt: time.Now(), StartURL: c.cfg.StartURL}
	c.recountLocked()
}

// finishWhenIdle closes the run as soon as no job is outstanding. A full
// check first adds the sitemap URLs once the link crawl is idle.
func (c *Crawler) finishWhenIdle() {
	done := c.done
	go func() {
		c.wg.Wait()
		c.mu.Lock()
		sitemap := c.cfg.Sitemap && c.progress.Mode == "all" && c.ctx.Err() == nil
		ctx, client, start, rules := c.ctx, c.client, c.start, c.robots
		c.mu.Unlock()
		if sitemap {
			urls := robots.SitemapURLs(ctx, client, start, rules)
			c.mu.Lock()
			for _, u := range urls {
				if r, ok := c.results[u]; ok {
					r.InSitemap = true
					continue
				}
				c.enqueueLocked(u, NotLinked, "")
				c.results[u].InSitemap = true
			}
			c.mu.Unlock()
			c.wg.Wait()
		}
		c.mu.Lock()
		c.progress.Running = false
		c.progress.FinishedAt = time.Now()
		if c.ctx.Err() != nil {
			c.progress.Stopped = true
		}
		c.recountLocked()
		c.cancel()
		c.mu.Unlock()
		close(done)
	}()
}

// Stop cancels the running crawl. Unfinished URLs keep an "aborted" error so
// they are picked up by a later RecheckFailed.
func (c *Crawler) Stop() {
	c.mu.Lock()
	cancel := c.cancel
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// Wait blocks until the current run has finished.
func (c *Crawler) Wait() {
	c.mu.Lock()
	done := c.done
	c.mu.Unlock()
	if done != nil {
		<-done
	}
}

// Progress returns a snapshot of the state.
func (c *Crawler) Progress() Progress {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.recountLocked()
	return c.progress
}

// Config returns the configuration of the last run.
func (c *Crawler) Config() Config {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cfg
}

// Results returns a copy of all results in discovery order.
func (c *Crawler) Results() []Result {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]Result, 0, len(c.order))
	for _, u := range c.order {
		r := *c.results[u]
		r.FoundOn = append([]string(nil), r.FoundOn...)
		r.Redirects = append([]string(nil), r.Redirects...)
		out = append(out, r)
	}
	return out
}

// Load replaces the results with previously exported ones (e.g. from a JSON
// report) so that RecheckFailed can continue from there.
func (c *Crawler) Load(cfg Config, results []Result) error {
	norm, ok := links.Normalize(cfg.StartURL, nil)
	if !ok {
		return errors.New("invalid start URL")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.progress.Running {
		return ErrBusy
	}
	cfg.StartURL = norm
	c.cfg = cfg
	c.start, _ = url.Parse(norm)
	c.results = map[string]*Result{}
	c.order = nil
	c.content = map[string]pageContent{} // see LoadContent
	c.list = nil
	c.progress = Progress{StartURL: norm}
	for i := range results {
		r := results[i]
		// Hand-edited or merged reports may contain empty or duplicate rows;
		// the first occurrence wins.
		if _, dup := c.results[r.URL]; r.URL == "" || dup {
			continue
		}
		r.Pending = false
		c.results[r.URL] = &r
		c.order = append(c.order, r.URL)
		// Reconstruct the time span of the original run for the UI.
		if t := r.CheckedAt; !t.IsZero() {
			if c.progress.StartedAt.IsZero() || t.Before(c.progress.StartedAt) {
				c.progress.StartedAt = t.Add(-time.Duration(r.DurationMs) * time.Millisecond)
			}
			if t.After(c.progress.FinishedAt) {
				c.progress.FinishedAt = t
			}
		}
	}
	c.recountLocked()
	return nil
}

// recountLocked updates Total/Done from the results. It is O(n) and therefore
// only called when a snapshot is requested, not per processed URL. c.mu must
// be held.
func (c *Crawler) recountLocked() {
	done := 0
	for _, r := range c.results {
		if !r.Pending {
			done++
		}
	}
	c.progress.Total = len(c.results)
	c.progress.Done = done
}

// enqueueLocked registers a URL found on page source and schedules its check
// if it has not been seen in this run. c.mu must be held.
func (c *Crawler) enqueueLocked(u string, depth int, source string) {
	if r, ok := c.results[u]; ok {
		addFoundOn(r, source)
		if c.visited[u] {
			return
		}
		// Known from a previous run but not yet processed in this one.
		if r.OK() && c.progress.Mode == "failed" {
			return
		}
		if depth >= 0 && (r.Depth < 0 || depth < r.Depth) {
			r.Depth = depth
		}
		c.dispatchLocked(r)
		return
	}
	pu, _ := url.Parse(u)
	internal := links.SameSite(c.start, pu)
	cat := links.Category(links.Extension(u))
	kind := KindExternal
	switch {
	case links.IsFile(cat):
		kind = KindFile
	case internal:
		kind = KindInternal
	}
	r := &Result{
		URL:           u,
		Kind:          kind,
		Internal:      internal,
		Type:          links.TypeOf(u, ""),
		Category:      cat,
		Depth:         depth,
		RobotsBlocked: internal && c.robots.Blocked(pu),
	}
	addFoundOn(r, source)
	c.results[u] = r
	c.order = append(c.order, u)
	c.dispatchLocked(r)
}

// dispatchLocked starts the worker goroutine for r. c.mu must be held.
func (c *Crawler) dispatchLocked(r *Result) {
	if c.visited[r.URL] {
		return
	}
	c.visited[r.URL] = true
	r.Pending = true
	c.wg.Add(1)
	go c.work(r.URL)
}

// addFoundOn records that the URL was linked from source (at most
// maxFoundOn distinct pages are kept).
func addFoundOn(r *Result, source string) {
	if source == "" || len(r.FoundOn) >= maxFoundOn {
		return
	}
	for _, s := range r.FoundOn {
		if s == source {
			return
		}
	}
	r.FoundOn = append(r.FoundOn, source)
}

// work checks one URL. It runs in its own goroutine, limited by c.sem.
func (c *Crawler) work(u string) {
	defer c.wg.Done()
	ctx := c.ctx

	select {
	case c.sem <- struct{}{}:
	case <-ctx.Done():
		c.finish(u, func(r *Result) { r.Error = "aborted"; r.Status = 0 })
		return
	}
	defer func() { <-c.sem }()

	c.mu.Lock()
	r := *c.results[u]
	cfg := c.cfg
	client := c.client
	source := ""
	if len(r.FoundOn) > 0 {
		source = r.FoundOn[0]
	}
	parse := r.Kind == KindInternal && (cfg.MaxDepth == 0 || r.Depth < cfg.MaxDepth)
	start := c.start
	c.mu.Unlock()

	if !r.Internal && !cfg.CheckExternal {
		c.finish(u, func(r *Result) { r.Error = "not checked"; r.Status = 0 })
		return
	}
	if cfg.Delay > 0 {
		select {
		case <-time.After(cfg.Delay):
		case <-ctx.Done():
		}
	}
	if ctx.Err() != nil {
		c.finish(u, func(r *Result) { r.Error = "aborted"; r.Status = 0 })
		return
	}

	var resp fetch.Response
	switch r.Kind {
	case KindFile:
		resp = client.Probe(ctx, u, source)
	case KindInternal:
		resp = client.Get(ctx, u, source, true, 10<<20)
	default:
		// External page: read only the beginning, the <title> sits in <head>.
		resp = client.Get(ctx, u, source, true, 512<<10)
	}

	var doc links.Document
	finalURL, _ := url.Parse(resp.FinalURL)
	isHTML := links.IsHTML(resp.ContentType) && resp.Status == 200
	if isHTML && len(resp.Body) > 0 && finalURL != nil {
		doc = links.ParseHTML(resp.Body, finalURL)
	}
	// A URL that redirects is not a page of its own: its facts belong to the
	// target, which is checked as a URL of its own (see below).
	var seo *PageSEO
	var content *pageContent
	if isHTML && r.Kind == KindInternal && len(resp.Redirects) == 0 && finalURL != nil && links.SameSite(start, finalURL) {
		seo = pageSEO(doc, resp, finalURL)
		content = &pageContent{html: compress(resp.Body), text: compress([]byte(doc.Text))}
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	res := c.results[u]
	res.Pending = false
	res.CheckedAt = time.Now()
	res.Status = resp.Status
	res.Error = ""
	if resp.Err != nil {
		res.Error = resp.Err.Error()
		if ctx.Err() != nil {
			res.Error = "aborted"
		}
	}
	res.ContentType = resp.ContentType
	res.LastModified = time.Time{}
	if resp.Status >= 200 && resp.Status < 300 {
		res.LastModified = resp.LastModified
	}
	res.Size = resp.Size
	res.DurationMs = resp.Duration.Milliseconds()
	res.Redirects = resp.Redirects
	res.FinalURL = ""
	if len(resp.Redirects) > 0 && resp.FinalURL != u {
		res.FinalURL = resp.FinalURL
	}
	// Error pages are usually HTML or plain text regardless of the requested
	// file, so only a successful response may refine the type.
	ctype := ""
	if resp.Status >= 200 && resp.Status < 300 {
		ctype = resp.ContentType
	}
	res.Type = links.TypeOf(u, ctype)
	res.MimeType = links.MimeType(u, ctype, ctype != "")
	res.Category = links.CategoryOfType(res.Type)
	if doc.Title != "" {
		res.Title = doc.Title
	}
	res.SEO = seo
	if content != nil {
		c.content[u] = *content
	} else {
		delete(c.content, u)
	}
	// An extension-less URL may turn out to be a file (e.g. /download?id=3).
	if res.Kind != KindFile && links.IsFile(res.Category) && res.Category != links.CatData {
		res.Kind = KindFile
	}

	// Follow links only for internal HTML pages that stayed on the site.
	if !parse || !isHTML || finalURL == nil || !links.SameSite(c.start, finalURL) {
		return
	}
	// An internal redirect (e.g. /old → /new): check the target as a URL of
	// its own instead of parsing the same content under two names.
	if final, ok := links.Normalize(resp.FinalURL, nil); ok && final != u {
		c.enqueueLocked(final, res.Depth, "")
		return
	}
	if c.cfg.MaxPages > 0 && c.pages >= c.cfg.MaxPages {
		return
	}
	c.pages++
	childDepth := res.Depth + 1
	if res.Depth == NotLinked { // links of an orphan page are not reachable from the start either
		childDepth = NotLinked
	}
	for _, l := range doc.Links {
		c.enqueueLocked(l.URL, childDepth, u)
	}
}

// finish updates a result outside of a regular check (abort, skipped).
func (c *Crawler) finish(u string, fn func(*Result)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r := c.results[u]
	r.Pending = false
	r.CheckedAt = time.Now()
	fn(r)
}

// SortedByURL returns results sorted by URL (helper for deterministic output).
func SortedByURL(rs []Result) []Result {
	out := append([]Result(nil), rs...)
	sort.Slice(out, func(i, j int) bool { return out[i].URL < out[j].URL })
	return out
}
