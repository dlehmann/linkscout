package crawler

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dlehmann/linkscout/internal/fetch"
)

// site is a small fake website. The "external" site is the same kind of test
// server but addressed via "localhost" instead of "127.0.0.1", so the crawler
// treats it as a different host.
type site struct {
	internal, external *httptest.Server
	hits               sync.Map // path → *int32
	flaky              atomic.Int32
	referers           sync.Map // path → referer
}

func (s *site) hit(path string) {
	v, _ := s.hits.LoadOrStore(path, new(int32))
	atomic.AddInt32(v.(*int32), 1)
}

func (s *site) count(path string) int32 {
	v, ok := s.hits.Load(path)
	if !ok {
		return 0
	}
	return atomic.LoadInt32(v.(*int32))
}

func html(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, body)
}

func newSite(t *testing.T) *site {
	s := &site{}
	ext := http.NewServeMux()
	ext.HandleFunc("/page", func(w http.ResponseWriter, r *http.Request) {
		s.referers.Store("ext/page", r.Header.Get("Referer"))
		html(w, "<title>External Page</title>")
	})
	ext.HandleFunc("/moved", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/new-home", http.StatusMovedPermanently)
	})
	ext.HandleFunc("/new-home", func(w http.ResponseWriter, r *http.Request) {
		html(w, "<title>New Home</title>")
	})
	ext.HandleFunc("/broken", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	s.external = httptest.NewServer(ext)
	extURL := strings.Replace(s.external.URL, "127.0.0.1", "localhost", 1)

	in := http.NewServeMux()
	in.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		s.hit(r.URL.Path)
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		html(w, `<html><head><title>Home</title><link rel="stylesheet" href="/style.css"></head><body>
			<a href="/about">About</a>
			<a href="/missing">Missing</a>
			<a href="/flaky">Flaky</a>
			<a href="/files/doc.pdf">PDF</a>
			<img src="/img/logo.png">
			<a href="/old">Old</a>
			<a href="mailto:info@example.com">Mail</a>
			<a href="`+extURL+`/page">Ext</a>
			<a href="`+extURL+`/moved">Moved</a>
			<a href="`+extURL+`/broken">Broken ext</a>
		</body></html>`)
	})
	in.HandleFunc("/about", func(w http.ResponseWriter, r *http.Request) {
		s.hit(r.URL.Path)
		s.referers.Store("/about", r.Header.Get("Referer"))
		html(w, `<title>About us</title><a href="/">Home</a><a href="/deep">Deep</a><a href="/files/doc.pdf">PDF</a>`)
	})
	in.HandleFunc("/deep", func(w http.ResponseWriter, r *http.Request) {
		s.hit(r.URL.Path)
		html(w, `<title>Deep</title><a href="/deeper">Deeper</a>`)
	})
	in.HandleFunc("/deeper", func(w http.ResponseWriter, r *http.Request) {
		s.hit(r.URL.Path)
		html(w, `<title>Deeper</title>`)
	})
	in.HandleFunc("/old", func(w http.ResponseWriter, r *http.Request) {
		s.hit(r.URL.Path)
		http.Redirect(w, r, "/about", http.StatusMovedPermanently)
	})
	in.HandleFunc("/flaky", func(w http.ResponseWriter, r *http.Request) {
		s.hit(r.URL.Path)
		if s.flaky.Load() == 0 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		html(w, `<title>Flaky</title><a href="/new-after-fix">new</a>`)
	})
	in.HandleFunc("/new-after-fix", func(w http.ResponseWriter, r *http.Request) {
		s.hit(r.URL.Path)
		html(w, `<title>New</title>`)
	})
	in.HandleFunc("/files/doc.pdf", func(w http.ResponseWriter, r *http.Request) {
		s.hit(r.URL.Path)
		w.Header().Set("Content-Type", "application/pdf")
		w.Write([]byte("%PDF"))
	})
	in.HandleFunc("/img/logo.png", func(w http.ResponseWriter, r *http.Request) {
		s.hit(r.URL.Path)
		w.Header().Set("Content-Type", "image/png")
		w.Write([]byte{0x89, 'P', 'N', 'G'})
	})
	in.HandleFunc("/style.css", func(w http.ResponseWriter, r *http.Request) {
		s.hit(r.URL.Path)
		w.Header().Set("Content-Type", "text/css")
		w.Write([]byte("body{}"))
	})
	s.internal = httptest.NewServer(in)
	t.Cleanup(func() { s.internal.Close(); s.external.Close() })
	return s
}

func (s *site) extURL() string { return strings.Replace(s.external.URL, "127.0.0.1", "localhost", 1) }

func byURL(rs []Result) map[string]Result {
	m := map[string]Result{}
	for _, r := range rs {
		m[r.URL] = r
	}
	return m
}

func runCrawl(t *testing.T, c *Crawler, cfg Config) map[string]Result {
	t.Helper()
	if err := c.Start(cfg); err != nil {
		t.Fatal(err)
	}
	c.Wait()
	return byURL(c.Results())
}

func TestCrawl(t *testing.T) {
	s := newSite(t)
	base := s.internal.URL
	c := New()
	res := runCrawl(t, c, Config{StartURL: base, CheckExternal: true, Concurrency: 4})

	check := func(u string, status int, kind, typ, title string) {
		t.Helper()
		r, ok := res[u]
		if !ok {
			t.Errorf("missing result for %s", u)
			return
		}
		if r.Status != status || r.Kind != kind || r.Type != typ || r.Title != title {
			t.Errorf("%s: got status=%d kind=%s type=%s title=%q; want %d %s %s %q (err=%s)",
				u, r.Status, r.Kind, r.Type, r.Title, status, kind, typ, title, r.Error)
		}
		if r.Pending {
			t.Errorf("%s still pending", u)
		}
	}
	check(base+"/", 200, KindInternal, "html", "Home")
	check(base+"/about", 200, KindInternal, "html", "About us")
	check(base+"/deep", 200, KindInternal, "html", "Deep")
	check(base+"/deeper", 200, KindInternal, "html", "Deeper")
	check(base+"/missing", 404, KindInternal, "html", "")
	check(base+"/flaky", 503, KindInternal, "html", "")
	check(base+"/files/doc.pdf", 200, KindFile, "pdf", "")
	check(base+"/img/logo.png", 200, KindFile, "png", "")
	check(base+"/style.css", 200, KindFile, "css", "")
	check(s.extURL()+"/page", 200, KindExternal, "html", "External Page")
	check(s.extURL()+"/moved", 200, KindExternal, "html", "New Home")
	check(s.extURL()+"/broken", 404, KindExternal, "html", "")

	// Redirect targets are reported.
	if got := res[s.extURL()+"/moved"].FinalURL; got != s.extURL()+"/new-home" {
		t.Errorf("external redirect target = %q", got)
	}
	if got := res[base+"/old"].FinalURL; got != base+"/about" {
		t.Errorf("internal redirect target = %q", got)
	}
	// mailto: links are ignored.
	for u := range res {
		if strings.HasPrefix(u, "mailto:") {
			t.Errorf("mailto link checked: %s", u)
		}
	}
	// FoundOn lists every referring page once.
	if fo := res[base+"/files/doc.pdf"].FoundOn; len(fo) != 2 {
		t.Errorf("doc.pdf FoundOn = %v", fo)
	}
	// Every internal page is fetched exactly once although linked repeatedly.
	if n := s.count("/"); n != 1 {
		t.Errorf("start page fetched %d times", n)
	}
	// Referer emulates Firefox: same origin → full URL, cross origin → origin.
	if ref, _ := s.referers.Load("/about"); ref != base+"/" {
		t.Errorf("referer for /about = %v", ref)
	}
	if ref, _ := s.referers.Load("ext/page"); ref != base+"/" {
		t.Errorf("referer for external page = %v", ref)
	}
	p := c.Progress()
	if p.Running || p.Total != len(res) || p.Done != p.Total {
		t.Errorf("progress = %+v", p)
	}
}

func TestFixedReferer(t *testing.T) {
	s := newSite(t)
	c := New()
	runCrawl(t, c, Config{StartURL: s.internal.URL, CheckExternal: true, Fetch: fetch.Options{Referer: "https://search.example/"}})
	if ref, _ := s.referers.Load("/about"); ref != "https://search.example/" {
		t.Errorf("fixed referer not used: %v", ref)
	}
}

func TestRecheckFailed(t *testing.T) {
	s := newSite(t)
	base := s.internal.URL
	c := New()
	res := runCrawl(t, c, Config{StartURL: base, CheckExternal: true})
	if res[base+"/flaky"].Status != 503 {
		t.Fatalf("precondition: flaky should fail first")
	}
	homeHits, aboutHits := s.count("/"), s.count("/about")

	s.flaky.Store(1) // the server is fixed now
	if err := c.RecheckFailed(Config{CheckExternal: true}); err != nil {
		t.Fatal(err)
	}
	c.Wait()
	res = byURL(c.Results())

	if r := res[base+"/flaky"]; r.Status != 200 || r.Title != "Flaky" {
		t.Errorf("flaky after recheck: %+v", r)
	}
	if r := res[base+"/missing"]; r.Status != 404 {
		t.Errorf("missing after recheck: %+v", r)
	}
	if s.count("/missing") != 2 {
		t.Errorf("/missing should have been re-requested")
	}
	// Successful pages must not be requested again.
	if s.count("/") != homeHits || s.count("/about") != aboutHits {
		t.Errorf("OK pages were re-checked: / %d→%d, /about %d→%d", homeHits, s.count("/"), aboutHits, s.count("/about"))
	}
	// Links on a page that works now are followed.
	if r, ok := res[base+"/new-after-fix"]; !ok || r.Status != 200 {
		t.Errorf("link found on fixed page not checked: %+v", r)
	}
	if c.Progress().Mode != "failed" {
		t.Errorf("mode = %q", c.Progress().Mode)
	}
}

func TestRecheckWithoutRun(t *testing.T) {
	if err := New().RecheckFailed(Config{}); err == nil {
		t.Error("expected error when nothing was checked yet")
	}
}

func TestMaxDepthAndPages(t *testing.T) {
	s := newSite(t)
	base := s.internal.URL
	c := New()
	res := runCrawl(t, c, Config{StartURL: base, MaxDepth: 1})
	// depth 0: /, depth 1: /about … are checked; /about is not parsed, so
	// /deep (depth 2) is never seen.
	if _, ok := res[base+"/about"]; !ok {
		t.Error("/about should be checked")
	}
	if _, ok := res[base+"/deep"]; ok {
		t.Error("/deep must not be reached with MaxDepth 1")
	}
	c = New()
	res = runCrawl(t, c, Config{StartURL: base, MaxDepth: 2})
	if _, ok := res[base+"/deep"]; !ok {
		t.Error("/deep should be checked with MaxDepth 2")
	}
	if _, ok := res[base+"/deeper"]; ok {
		t.Error("/deeper must not be reached with MaxDepth 2")
	}

	c = New()
	res = runCrawl(t, c, Config{StartURL: base, MaxPages: 1})
	if _, ok := res[base+"/deep"]; ok {
		t.Error("only the start page may be parsed with MaxPages 1")
	}
}

func TestNoExternal(t *testing.T) {
	s := newSite(t)
	c := New()
	res := runCrawl(t, c, Config{StartURL: s.internal.URL, CheckExternal: false})
	r := res[s.extURL()+"/page"]
	if r.Status != 0 || r.Error != "not checked" {
		t.Errorf("external link should be skipped: %+v", r)
	}
}

func TestBusyAndInvalid(t *testing.T) {
	c := New()
	if err := c.Start(Config{StartURL: "not a url"}); err == nil {
		t.Error("invalid URL accepted")
	}
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-block }))
	defer srv.Close()
	if err := c.Start(Config{StartURL: srv.URL}); err != nil {
		t.Fatal(err)
	}
	if err := c.Start(Config{StartURL: srv.URL}); err != ErrBusy {
		t.Errorf("second Start = %v, want ErrBusy", err)
	}
	c.Stop()
	close(block)
	c.Wait()
	p := c.Progress()
	if !p.Stopped || p.Running {
		t.Errorf("progress after stop = %+v", p)
	}
	if r := c.Results()[0]; r.Error != "aborted" {
		t.Errorf("aborted result = %+v", r)
	}
}

func TestLoadAndSummarize(t *testing.T) {
	rs := []Result{
		{URL: "https://e.com/", Status: 200, Kind: KindInternal, Type: "html", DurationMs: 100, Size: 1000},
		{URL: "https://e.com/a", Status: 404, Kind: KindInternal, Type: "html", DurationMs: 50, FoundOn: []string{"https://e.com/"}},
		{URL: "https://e.com/b.pdf", Status: 200, Kind: KindFile, Type: "pdf", DurationMs: 300, Redirects: []string{"https://e.com/old.pdf"}},
		{URL: "https://x.org/", Status: 0, Error: "timeout", Kind: KindExternal, Type: "html"},
		{URL: "https://x.org/s", Status: 503, Kind: KindExternal, Type: "html", DurationMs: 10},
	}
	s := Summarize(rs)
	if s.Total != 5 || s.OK != 2 || s.Broken != 2 || s.Errors != 1 || s.Redirected != 1 || s.InternalPages != 1 {
		t.Errorf("stats = %+v", s)
	}
	if s.AvgMs != (100+50+300+10)/4 {
		t.Errorf("avg = %d", s.AvgMs)
	}
	if s.Slowest[0].URL != "https://e.com/b.pdf" {
		t.Errorf("slowest = %v", s.Slowest[0].URL)
	}
	if len(s.BrokenList) != 3 {
		t.Errorf("broken list = %d", len(s.BrokenList))
	}
	if fmt.Sprint(s.ByStatusClass) != "[{2xx 2} {4xx 1} {5xx 1} {error 1}]" {
		t.Errorf("by class = %v", s.ByStatusClass)
	}
	if s.ByType[0] != (Count{"html", 4}) {
		t.Errorf("by type = %v", s.ByType)
	}

	c := New()
	if err := c.Load(Config{StartURL: "https://e.com"}, rs); err != nil {
		t.Fatal(err)
	}
	if len(c.Results()) != 5 || c.Progress().Total != 5 {
		t.Error("Load did not keep results")
	}
	now := time.Now()
	c.Load(Config{StartURL: "https://e.com"}, []Result{
		{URL: "https://e.com/", Status: 200, CheckedAt: now.Add(-time.Minute), DurationMs: 1000},
		{URL: "https://e.com/a", Status: 200, CheckedAt: now},
	})
	if p := c.Progress(); !p.FinishedAt.Equal(now) || !p.StartedAt.Equal(now.Add(-61*time.Second)) {
		t.Errorf("time span after Load = %v – %v", p.StartedAt, p.FinishedAt)
	}
}

func TestStatusClass(t *testing.T) {
	for _, tt := range []struct {
		r    Result
		want string
	}{
		{Result{Status: 200}, "2xx"}, {Result{Status: 301}, "3xx"}, {Result{Status: 404}, "4xx"},
		{Result{Status: 500}, "5xx"}, {Result{Status: 0}, "error"}, {Result{Pending: true}, "pending"},
	} {
		if got := StatusClass(tt.r); got != tt.want {
			t.Errorf("StatusClass(%d) = %s want %s", tt.r.Status, got, tt.want)
		}
	}
}

func TestCrawlBasicAuth(t *testing.T) {
	var extAuth atomic.Value
	ext := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		extAuth.Store(r.Header.Get("Authorization"))
		html(w, "<title>ext</title>")
	}))
	defer ext.Close()
	extURL := strings.Replace(ext.URL, "127.0.0.1", "localhost", 1)
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); !ok || u != "admin" || p != "pw" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/":
			html(w, `<title>Secret</title><a href="/inner">i</a><a href="`+extURL+`/">e</a>`)
		case "/inner":
			html(w, `<title>Inner</title>`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer site.Close()

	c := New()
	res := runCrawl(t, c, Config{StartURL: site.URL, CheckExternal: true, Fetch: fetch.Options{Username: "admin", Password: "pw"}})
	if r := res[site.URL+"/inner"]; r.Status != 200 || r.Title != "Inner" {
		t.Errorf("protected inner page: %+v", r)
	}
	if v, _ := extAuth.Load().(string); v != "" {
		t.Errorf("credentials sent to external site: %q", v)
	}

	res = runCrawl(t, New(), Config{StartURL: site.URL})
	if r := res[site.URL+"/"]; r.Status != 401 || len(res) != 1 {
		t.Errorf("without credentials: %+v (%d results)", r, len(res))
	}
}

// Reports from other tools or merged by hand may contain duplicates and empty
// rows; Load keeps the first occurrence and a valid report afterwards.
func TestLoadSkipsDuplicatesAndEmptyRows(t *testing.T) {
	c := New()
	err := c.Load(Config{StartURL: "https://e.com"}, []Result{
		{URL: "https://e.com/", Status: 200, Pending: true},
		{URL: ""},
		{URL: "https://e.com/", Status: 404},
		{URL: "https://e.com/a", Status: 500},
	})
	if err != nil {
		t.Fatal(err)
	}
	rs := c.Results()
	if len(rs) != 2 || rs[0].Status != 200 || rs[0].Pending || rs[1].URL != "https://e.com/a" {
		t.Errorf("results = %+v", rs)
	}
	if c.Config().StartURL != "https://e.com/" {
		t.Errorf("start URL not normalised: %q", c.Config().StartURL)
	}
	if err := c.Load(Config{StartURL: "mailto:x@y"}, nil); err == nil {
		t.Error("invalid start URL accepted")
	}
}

// The report carries everything needed to re-check it later.
func TestNewReport(t *testing.T) {
	c := New()
	c.Load(Config{StartURL: "https://e.com/", Fetch: fetch.Options{Referer: "https://ref.example/"}},
		[]Result{{URL: "https://e.com/", Status: 200, Kind: KindInternal}})
	rep := NewReport(c, "LinkScout test")
	if rep.Generator != "LinkScout test" || rep.StartURL != "https://e.com/" || rep.Referer != "https://ref.example/" ||
		rep.UserAgent != fetch.FirefoxUA || rep.Stats.Total != 1 || len(rep.Results) != 1 || rep.CreatedAt.IsZero() {
		t.Errorf("report = %+v", rep)
	}
	sorted := SortedByURL([]Result{{URL: "b"}, {URL: "a"}})
	if sorted[0].URL != "a" {
		t.Errorf("SortedByURL = %v", sorted)
	}
}
