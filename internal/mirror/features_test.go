package mirror

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRules(t *testing.T) {
	if _, err := ParseRules([]string{"*.zip"}); err == nil {
		t.Error("rule without +/- accepted")
	}
	rs, err := ParseRules([]string{"-*.zip -/private/*\n+/private/public/*", "+cdn.example.com/*"})
	if err != nil {
		t.Fatal(err)
	}
	if got := rs.Strings(); !reflect.DeepEqual(got, []string{"-*.zip", "-/private/*", "+/private/public/*", "+cdn.example.com/*"}) {
		t.Errorf("Strings = %v", got)
	}
	for raw, want := range map[string][2]bool{ // decided, include
		"https://e.com/a.zip":             {true, false},
		"https://e.com/a.zip?x=1":         {false, false}, // the pattern must match the whole URL
		"https://e.com/private/x":         {true, false},
		"https://e.com/private/public/x":  {true, true}, // the last matching rule wins
		"https://e.com/page":              {false, false},
		"https://CDN.example.com/lib.js":  {true, true},
		"https://e.com/%C3%BCber/a.zip":   {true, false},
		"https://other.com/private/x.pdf": {true, false}, // path rules apply to every host
	} {
		u, _ := url.Parse(raw)
		if d, i := rs.Match(u); d != want[0] || i != want[1] {
			t.Errorf("Match(%s) = %v %v, want %v", raw, d, i, want)
		}
	}
	umlaut, _ := ParseRules([]string{"-/über/*"})
	if u, _ := url.Parse("https://e.com/%C3%BCber/x"); !mustDecide(umlaut, u) {
		t.Error("decoded path not matched")
	}
}

func mustDecide(rs Rules, u *url.URL) bool { d, _ := rs.Match(u); return d }

func TestScriptURLs(t *testing.T) {
	js := `const a = "/img/slide-2.jpg", b = 'https://e.com/data/list.json';
		fetch("/api/users"); el.src = ` + "`/img/${n}.png`" + `; x = "img/rel.png";
		y = "not a url"; z = "/css/*.css"; w = "/team.html"; v = "/img/a.png'`
	if got, want := scriptURLs(js, false), []string{"/img/slide-2.jpg", "https://e.com/data/list.json", "/team.html"}; !reflect.DeepEqual(got, want) {
		t.Errorf("file: %v, want %v", got, want)
	}
	if got := scriptURLs(js, true); !contains(got, "img/rel.png") {
		t.Errorf("inline: %v lacks the relative path", got)
	}
	scripts := inlineScripts([]byte(`<script>a="/x.png"</script><script type="application/ld+json">{"logo":"/l.png"}</script><p>"/no.png"</p>`))
	if len(scripts) != 1 || scripts[0] != `a="/x.png"` {
		t.Errorf("inline scripts = %q", scripts)
	}
}

// featureSite is a test site with ETags, a robots.txt, a sitemap, scripts
// and a page on another host. hits counts the requests per path (200
// responses only, see served).
type featureSite struct {
	URL, other string
	mu         sync.Mutex
	served     map[string]int // full responses (not 304) per path
	pages      map[string]string
	fail       map[string]int // status to answer instead
}

func newFeatureSite(t *testing.T) *featureSite {
	fs := &featureSite{served: map[string]int{}, fail: map[string]int{}}
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		fmt.Fprint(w, "%PDF other")
	}))
	t.Cleanup(other.Close)
	fs.other = strings.Replace(other.URL, "127.0.0.1", "localhost", 1)
	fs.pages = map[string]string{
		"/": `<title>Home</title><a href="/a">A</a><a href="/old">Old</a><a href="/private/secret">Secret</a>
			<a href="` + fs.other + `/doc.pdf">PDF elsewhere</a><a href="` + fs.other + `/page">Page elsewhere</a>
			<img src="/img/logo.png"><script src="/js/app.js"></script>
			<script>var hero = "/img/hero.png", missing = "/img/missing.png";</script>`,
		"/a":              `<title>A</title><a href="/">Home</a>`,
		"/old":            `<title>Old</title>`,
		"/orphan":         `<title>Orphan</title>`,
		"/private/secret": `<title>Secret</title>`,
		"/img/logo.png":   "PNG logo",
		"/img/hero.png":   "PNG hero",
		"/img/chart.png":  "PNG chart",
		"/js/app.js":      `document.querySelector("img").src = "/img/chart.png";`,
		"/robots.txt":     "User-agent: *\nDisallow: /private/\n",
		"/sitemap.xml":    `<urlset><url><loc>%s/orphan</loc></url></urlset>`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fs.mu.Lock()
		body, ok := fs.pages[r.URL.Path]
		status := fs.fail[r.URL.Path]
		fs.mu.Unlock()
		if status != 0 {
			w.WriteHeader(status)
			return
		}
		if !ok {
			http.NotFound(w, r)
			return
		}
		if r.URL.Path == "/sitemap.xml" {
			body = fmt.Sprintf(body, "http://"+r.Host)
		}
		switch {
		case strings.HasSuffix(r.URL.Path, ".png"):
			w.Header().Set("Content-Type", "image/png")
		case strings.HasSuffix(r.URL.Path, ".js"):
			w.Header().Set("Content-Type", "text/javascript")
		case strings.HasSuffix(r.URL.Path, ".txt"), strings.HasSuffix(r.URL.Path, ".xml"):
			w.Header().Set("Content-Type", "text/plain")
		default:
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
		}
		etag := fmt.Sprintf(`"%x"`, hash(body)[:12])
		w.Header().Set("ETag", etag)
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		fs.mu.Lock()
		fs.served[r.URL.Path]++
		fs.mu.Unlock()
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	fs.URL = srv.URL
	return fs
}

// reset clears the request counters.
func (fs *featureSite) reset() map[string]int {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	old := fs.served
	fs.served = map[string]int{}
	return old
}

func (fs *featureSite) set(path, body string) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	fs.pages[path] = body
}

func TestMirrorFeatures(t *testing.T) {
	fs := newFeatureSite(t)
	dir := t.TempDir()
	cfg := Config{StartURL: fs.URL, OutDir: dir, External: true, ExternalFiles: true, Robots: true, Sitemap: true}
	p, err := Run(context.Background(), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	// robots.txt: /private/ is listed, not downloaded.
	if _, err := os.Stat(filepath.Join(dir, "private")); err == nil {
		t.Error("page disallowed by robots.txt was saved")
	}
	var robotsProblem, guessedProblem bool
	for _, pr := range p.Problems {
		robotsProblem = robotsProblem || pr.Robots && strings.HasSuffix(pr.URL, "/private/secret")
		guessedProblem = guessedProblem || strings.HasSuffix(pr.URL, "missing.png")
	}
	if !robotsProblem || guessedProblem {
		t.Errorf("problems = %+v", p.Problems)
	}
	// Sitemap: the orphan page; scripts: hero (inline) and chart (app.js).
	for _, f := range []string{"orphan/index.html", "img/hero.png", "img/chart.png", "img/logo.png", "a/index.html"} {
		read(t, dir, f)
	}
	// External linked file yes, external page no.
	host := sanitize(strings.TrimPrefix(fs.other, "http://"))
	read(t, dir, "_external/"+host+"/doc.pdf")
	if _, err := os.Stat(filepath.Join(dir, "_external", host, "page")); err == nil {
		t.Error("external page saved")
	}
	home := read(t, dir, "index.html")
	if !strings.Contains(home, `href="_external/`+host+`/doc.pdf"`) || !strings.Contains(home, `href="`+fs.other+`/page"`) {
		t.Errorf("external links not rewritten as expected:\n%s", home)
	}
	if !strings.Contains(home, `"/img/hero.png"`) {
		t.Error("scripts must not be rewritten")
	}

	// Without ExternalFiles the PDF elsewhere is left alone.
	dir2 := t.TempDir()
	if _, err := Run(context.Background(), Config{StartURL: fs.URL, OutDir: dir2, External: true}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir2, "_external", host, "doc.pdf")); err == nil {
		t.Error("external file saved without ExternalFiles")
	}
	// Without robots.txt and sitemap: the private page is saved, the orphan not.
	read(t, dir2, "private/secret/index.html")
	if _, err := os.Stat(filepath.Join(dir2, "orphan")); err == nil {
		t.Error("orphan saved without Sitemap")
	}

	// Rules: exclude the images folder, include nothing else.
	dir3 := t.TempDir()
	if _, err := Run(context.Background(), Config{StartURL: fs.URL, OutDir: dir3, Rules: []string{"-/img/*"}}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir3, "img")); err == nil {
		t.Error("excluded folder saved")
	}
	if !strings.Contains(read(t, dir3, "index.html"), `src="`+fs.URL+`/img/logo.png"`) {
		t.Error("excluded image must keep its online URL")
	}
	if _, err := Run(context.Background(), Config{StartURL: fs.URL, OutDir: dir3, Rules: []string{"*.png"}}, nil); err == nil {
		t.Error("invalid rule accepted")
	}

	// A copy of a sub-directory (a project site) takes only the sitemap
	// pages below it: /orphan is outside of /a/.
	dir4 := t.TempDir()
	if _, err := Run(context.Background(), Config{StartURL: fs.URL + "/a/", OutDir: dir4, Sitemap: true}, nil); err != nil {
		t.Fatal(err)
	}
	read(t, dir4, "index.html")
	if _, err := os.Stat(filepath.Join(dir4, "_site", "orphan")); err == nil {
		t.Error("sitemap page outside of the start directory saved")
	}
}

func TestMirrorUpdate(t *testing.T) {
	fs := newFeatureSite(t)
	dir := t.TempDir()
	cfg := Config{StartURL: fs.URL, OutDir: dir, Robots: true}
	first, err := Run(context.Background(), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.Unchanged != 0 {
		t.Errorf("first run: %d unchanged", first.Unchanged)
	}
	fs.reset()
	homeBefore := read(t, dir, "index.html")

	// Nothing changed: everything is answered with 304 and reused.
	second, err := Run(context.Background(), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	served := fs.reset()
	delete(served, "/robots.txt") // loaded every time
	if len(served) != 0 || second.Unchanged != second.Files || second.Files != first.Files {
		t.Errorf("unchanged site: served %v, progress %+v (first %d files)", served, second, first.Files)
	}
	if read(t, dir, "index.html") != homeBefore {
		t.Error("index.html differs after an update without changes")
	}

	// One page changed, one is gone, one fails temporarily.
	fs.set("/a", `<title>A2</title><a href="/">Home</a>`)
	fs.set("/", strings.Replace(fs.pages["/"], `<a href="/old">Old</a>`, "", 1))
	fs.mu.Lock()
	fs.fail["/img/logo.png"] = http.StatusServiceUnavailable
	fs.mu.Unlock()
	third, err := Run(context.Background(), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	served = fs.reset()
	if served["/a"] != 1 || served["/"] != 1 || served["/js/app.js"] != 0 {
		t.Errorf("changed site: served %v", served)
	}
	if !strings.Contains(read(t, dir, "a/index.html"), "A2") {
		t.Error("changed page not updated")
	}
	if _, err := os.Stat(filepath.Join(dir, "old")); err == nil || third.Removed != 1 {
		t.Errorf("page that is gone: removed %d, err %v", third.Removed, err)
	}
	if read(t, dir, "img/logo.png") != "PNG logo" {
		t.Error("temporarily failing file not kept")
	}
	kept := false
	for _, pr := range third.Problems {
		kept = kept || pr.Kept && pr.Status == 503 && strings.HasSuffix(pr.URL, "/img/logo.png")
	}
	if !kept {
		t.Errorf("problems = %+v", third.Problems)
	}

	// Fresh downloads everything again.
	fs.mu.Lock()
	delete(fs.fail, "/img/logo.png")
	fs.mu.Unlock()
	if _, err := Run(context.Background(), Config{StartURL: fs.URL, OutDir: dir, Robots: true, Fresh: true}, nil); err != nil {
		t.Fatal(err)
	}
	if served := fs.reset(); served["/js/app.js"] != 1 || served["/img/logo.png"] != 1 {
		t.Errorf("fresh run: served %v", served)
	}
	// LinkScout's own data is not part of the ZIP.
	var zipped strings.Builder
	if err := ZipDir(dir, &zipped); err != nil { // names are stored uncompressed
		t.Fatal(err)
	}
	if strings.Contains(zipped.String(), stateDirName+"/") {
		t.Error("state directory in the ZIP")
	}
}

// A stopped run keeps what it downloaded; the next run continues without
// requesting those files again.
func TestMirrorContinue(t *testing.T) {
	fs := newFeatureSite(t)
	block := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow.png" {
			select {
			case <-block:
			case <-r.Context().Done():
				return
			}
		}
		// everything else comes from the feature site
		resp, err := http.Get(fs.URL + r.URL.Path)
		if err != nil {
			w.WriteHeader(502)
			return
		}
		defer resp.Body.Close()
		w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
		w.WriteHeader(resp.StatusCode)
		buf := make([]byte, 64<<10)
		n, _ := resp.Body.Read(buf)
		w.Write(buf[:n])
	}))
	defer slow.Close()
	fs.set("/", fs.pages["/"]+`<img src="/slow.png">`)
	fs.set("/slow.png", "PNG slow")

	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	var once sync.Once
	cfg := Config{StartURL: slow.URL, OutDir: dir}
	_, err := Run(ctx, cfg, func(p Progress) {
		if p.Total > 3 && p.Done == p.Total-1 { // only /slow.png is missing
			once.Do(cancel)
		}
	})
	if err == nil {
		t.Fatal("cancelled run without error")
	}
	if !isFile(filepath.Join(dir, stateDirName, partialName)) {
		t.Fatal("no partial.json after a stopped run")
	}
	fs.reset()
	close(block)
	p, err := Run(context.Background(), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	served := fs.reset()
	if served["/slow.png"] != 1 || served["/"] != 0 || served["/a"] != 0 || p.Unchanged == 0 {
		t.Errorf("continued run: served %v, progress %+v", served, p)
	}
	if read(t, dir, "slow.png") != "PNG slow" || isFile(filepath.Join(dir, stateDirName, partialName)) {
		t.Error("continued copy incomplete or partial.json left")
	}
	if _, err := os.Stat(filepath.Join(dir, tmpDirName)); err == nil {
		t.Error("temporary directory left behind")
	}
}

func TestMirrorRateLimitOption(t *testing.T) {
	fs := newFeatureSite(t)
	start := time.Now()
	cfg := Config{StartURL: fs.URL, OutDir: t.TempDir(), Concurrency: 8}
	cfg.Fetch.RatePerHost = 50 // 20 ms apart
	p, err := Run(context.Background(), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if min := time.Duration(p.Done-1) * 20 * time.Millisecond; time.Since(start) < min {
		t.Errorf("%d requests in %v at 50/s", p.Done, time.Since(start))
	}
}
