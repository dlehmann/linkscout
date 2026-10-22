package server

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dlehmann/linkscout/internal/crawler"
	"github.com/dlehmann/linkscout/internal/i18n"
)

// target is the website that gets checked during the tests.
func target(t *testing.T) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<title>Start</title><a href="/ok">ok</a><a href="/broken">broken</a><img src="/a.png">`)
	})
	mux.HandleFunc("/ok", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<title>OK</title>`)
	})
	mux.HandleFunc("/a.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write([]byte("PNG"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func newUI(t *testing.T, lang i18n.Lang) (*Server, *httptest.Server) {
	s := New(Options{Lang: lang, Version: "test", Defaults: Defaults{MirrorBase: t.TempDir()}}, crawler.New())
	ui := httptest.NewServer(s.Handler())
	t.Cleanup(ui.Close)
	return s, ui
}

func do(t *testing.T, method, url string, body any) (*http.Response, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, url, r)
	req.Header.Set("X-LinkScout", "1")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

func waitIdle(t *testing.T, s *Server) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if !s.crawler.Progress().Running && !s.mirrors.running() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timeout waiting for job")
}

func TestIndexInjectsStateAndLanguage(t *testing.T) {
	_, ui := newUI(t, i18n.DE)
	resp, body := do(t, "GET", ui.URL+"/", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	page := string(body)
	if strings.Contains(page, "__INITIAL_STATE__") {
		t.Error("placeholder not replaced")
	}
	if !strings.Contains(page, `<html lang="de">`) {
		t.Error("lang attribute not set")
	}
	if !strings.Contains(page, `"tab.stats":"Übersicht"`) {
		t.Error("German messages not injected")
	}
	for _, f := range []string{"/static/app.js", "/static/style.css", "/static/favicon.svg"} {
		if r, _ := do(t, "GET", ui.URL+f, nil); r.StatusCode != 200 {
			t.Errorf("%s: %d", f, r.StatusCode)
		}
	}
}

func TestInjectedStateCannotBreakOutOfScript(t *testing.T) {
	s, ui := newUI(t, i18n.EN)
	s.crawler.Load(crawler.Config{StartURL: "https://e.com"}, []crawler.Result{{URL: "https://e.com/", Title: "</script><script>alert(1)</script>"}})
	_, body := do(t, "GET", ui.URL+"/", nil)
	if strings.Contains(string(body), "<script>alert(1)") {
		t.Error("title was injected unescaped")
	}
}

func TestGuard(t *testing.T) {
	_, ui := newUI(t, i18n.EN)
	// POST without the custom header (what a cross-site form would send).
	resp, err := http.Post(ui.URL+"/api/stop", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("POST without header: %d", resp.StatusCode)
	}
	// Foreign Host header (DNS rebinding).
	req, _ := http.NewRequest("GET", ui.URL+"/api/state", nil)
	req.Host = "evil.example"
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("foreign host: %d", resp.StatusCode)
	}
	for host, want := range map[string]bool{"127.0.0.1:8088": true, "localhost": true, "[::1]:80": true, "app.localhost:1": true, "example.com": false, "10.0.0.1:8088": false} {
		if isLoopbackHost(host) != want {
			t.Errorf("isLoopbackHost(%q) != %v", host, want)
		}
	}
}

func TestCheckRecheckExport(t *testing.T) {
	site := target(t)
	s, ui := newUI(t, i18n.EN)

	resp, body := do(t, "POST", ui.URL+"/api/check", CheckRequest{URL: "nonsense"})
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), "valid http(s) URL") {
		t.Errorf("invalid URL: %d %s", resp.StatusCode, body)
	}

	resp, _ = do(t, "POST", ui.URL+"/api/check", CheckRequest{URL: site.URL, Referer: "https://ref.example/"})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("start: %d", resp.StatusCode)
	}
	waitIdle(t, s)

	_, body = do(t, "GET", ui.URL+"/api/state", nil)
	var st State
	if err := json.Unmarshal(body, &st); err != nil {
		t.Fatal(err)
	}
	if st.Stats.Total != 4 || st.Stats.OK != 3 || st.Stats.Broken != 1 {
		t.Errorf("stats = %+v", st.Stats)
	}
	if st.Config.Fetch.Referer != "https://ref.example/" {
		t.Errorf("referer not stored: %+v", st.Config.Fetch)
	}

	// Re-check only failed URLs.
	resp, body = do(t, "POST", ui.URL+"/api/check", CheckRequest{Mode: "failed"})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("recheck: %d %s", resp.StatusCode, body)
	}
	waitIdle(t, s)
	if p := s.crawler.Progress(); p.Mode != "failed" || p.Total != 4 {
		t.Errorf("recheck progress = %+v", p)
	}

	// Export.
	resp, body = do(t, "GET", ui.URL+"/api/export", nil)
	if cd := resp.Header.Get("Content-Disposition"); !strings.Contains(cd, "attachment; filename=\"linkscout-127.0.0.1_") {
		t.Errorf("Content-Disposition = %q", cd)
	}
	var rep crawler.Report
	if err := json.Unmarshal(body, &rep); err != nil {
		t.Fatal(err)
	}
	if len(rep.Results) != 4 || rep.StartURL != site.URL+"/" || rep.Generator != "LinkScout test" {
		t.Errorf("report = %+v", rep)
	}

	// Import the report into a fresh instance.
	s2, ui2 := newUI(t, i18n.EN)
	resp, _ = do(t, "POST", ui2.URL+"/api/import", rep)
	if resp.StatusCode != 200 || len(s2.crawler.Results()) != 4 {
		t.Errorf("import: %d, %d results", resp.StatusCode, len(s2.crawler.Results()))
	}
}

func TestCheckBusy(t *testing.T) {
	block := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-block }))
	defer slow.Close()
	defer close(block)
	s, ui := newUI(t, i18n.DE)
	do(t, "POST", ui.URL+"/api/check", CheckRequest{URL: slow.URL})
	resp, body := do(t, "POST", ui.URL+"/api/check", CheckRequest{URL: slow.URL})
	if resp.StatusCode != http.StatusConflict || !strings.Contains(string(body), "läuft bereits") {
		t.Errorf("busy: %d %s", resp.StatusCode, body)
	}
	do(t, "POST", ui.URL+"/api/stop", nil)
	s.crawler.Stop()
}

func TestMirrorAndZip(t *testing.T) {
	site := target(t)
	s, ui := newUI(t, i18n.EN)

	if resp, _ := do(t, "GET", ui.URL+"/api/mirror/zip", nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("zip before mirror: %d", resp.StatusCode)
	}
	out := filepath.Join(t.TempDir(), "copy")
	resp, body := do(t, "POST", ui.URL+"/api/mirror", MirrorRequest{URL: site.URL, OutDir: out})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("mirror: %d %s", resp.StatusCode, body)
	}
	waitIdle(t, s)
	if jobs := s.mirrors.views(); len(jobs) != 1 || jobs[0].Status != JobDone || jobs[0].Progress.Files != 4 ||
		len(jobs[0].Progress.Problems) != 1 || jobs[0].Progress.Problems[0].Status != 404 || !jobs[0].Progress.Problems[0].Saved {
		t.Errorf("mirror jobs = %+v", jobs)
	}
	resp, body = do(t, "GET", ui.URL+"/api/mirror/zip", nil)
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "application/zip" {
		t.Fatalf("zip: %d", resp.StatusCode)
	}
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	if strings.Join(names, ",") != "a.png,broken,index.html,ok/index.html" { // the 404 page is kept
		t.Errorf("zip entries: %v", names)
	}
}

func TestMirrorList(t *testing.T) {
	site := target(t)
	s, ui := newUI(t, i18n.DE)
	base := t.TempDir()
	add := func(req MirrorRequest) (*http.Response, []byte) {
		return do(t, "POST", ui.URL+"/api/mirror/queue", req)
	}

	a := filepath.Join(base, "a")
	if resp, body := add(MirrorRequest{URL: site.URL, OutDir: a, UserAgent: "UA-A", Username: "joe", Password: "secret"}); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("queue a: %d %s", resp.StatusCode, body)
	}
	if resp, body := add(MirrorRequest{URL: site.URL + "/ok", OutDir: filepath.Join(base, "b"), MaxPages: 1, External: new(bool)}); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("queue b: %d %s", resp.StatusCode, body)
	}
	// Same target directory twice would mix two copies.
	if resp, body := add(MirrorRequest{URL: site.URL, OutDir: a}); resp.StatusCode != http.StatusConflict || !strings.Contains(string(body), "Zielverzeichnis") {
		t.Errorf("duplicate dir: %d %s", resp.StatusCode, body)
	}

	// Without a target directory the default one gets a free suffix.
	add(MirrorRequest{URL: site.URL})
	add(MirrorRequest{URL: site.URL + "/other"})
	if jobs := s.mirrors.views(); len(jobs) != 4 || jobs[3].OutDir != jobs[2].OutDir+"-2" {
		t.Fatalf("default dirs: %+v", jobs)
	}
	do(t, "POST", ui.URL+"/api/mirror/remove", JobRequest{ID: 3})
	do(t, "POST", ui.URL+"/api/mirror/remove", JobRequest{ID: 4})

	_, body := do(t, "GET", ui.URL+"/api/state", nil)
	if strings.Contains(string(body), "secret") {
		t.Error("password leaked into /api/state")
	}
	var st State
	if err := json.Unmarshal(body, &st); err != nil {
		t.Fatal(err)
	}
	if len(st.Mirrors) != 2 || st.Mirrors[0].Status != JobQueued || st.Mirrors[0].UserAgent != "UA-A" || st.Mirrors[1].MaxPages != 1 || !st.Mirrors[0].External || st.Mirrors[1].External {
		t.Fatalf("queued jobs = %+v", st.Mirrors)
	}
	if resp, _ := do(t, "GET", ui.URL+"/api/mirror/zip", nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("zip before start: %d", resp.StatusCode)
	}

	if resp, body := do(t, "POST", ui.URL+"/api/mirror/start", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("start all: %d %s", resp.StatusCode, body)
	}
	waitIdle(t, s)
	jobs := s.mirrors.views()
	for _, j := range jobs {
		if j.Status != JobDone {
			t.Errorf("job %d: %s %+v", j.ID, j.Status, j.Progress)
		}
	}
	if _, err := os.Stat(filepath.Join(a, "index.html")); err != nil {
		t.Error(err)
	}
	// The start directory /ok/ is the root of the copy.
	if _, err := os.Stat(filepath.Join(base, "b", "index.html")); err != nil {
		t.Error(err)
	}
	resp, _ := do(t, "GET", fmt.Sprintf("%s/api/mirror/zip?id=%d", ui.URL, jobs[1].ID), nil)
	if resp.StatusCode != 200 || !strings.Contains(resp.Header.Get("Content-Disposition"), `"b.zip"`) {
		t.Errorf("zip b: %d %s", resp.StatusCode, resp.Header.Get("Content-Disposition"))
	}

	// Remove one entry, then all finished ones.
	if resp, body := do(t, "POST", ui.URL+"/api/mirror/remove", JobRequest{ID: jobs[0].ID}); resp.StatusCode != http.StatusOK {
		t.Fatalf("remove: %d %s", resp.StatusCode, body)
	}
	if resp, _ := do(t, "POST", ui.URL+"/api/mirror/remove", JobRequest{ID: jobs[0].ID}); resp.StatusCode != http.StatusNotFound {
		t.Errorf("remove twice: %d", resp.StatusCode)
	}
	do(t, "POST", ui.URL+"/api/mirror/remove", JobRequest{Done: true})
	if n := len(s.mirrors.views()); n != 0 {
		t.Errorf("%d jobs left", n)
	}
}

// An internationalised host name is stored in its Punycode form, also in the
// target directory made from the template.
func TestMirrorPunycode(t *testing.T) {
	s, ui := newUI(t, i18n.EN)
	base := t.TempDir()
	resp, body := do(t, "POST", ui.URL+"/api/mirror/queue", MirrorRequest{URL: "https://Müller.de/über", OutDir: filepath.Join(base, "<host>")})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("queue: %d %s", resp.StatusCode, body)
	}
	j := s.mirrors.views()[0]
	if j.URL != "https://xn--mller-kva.de/%C3%BCber" || j.OutDir != filepath.Join(base, "xn--mller-kva.de") {
		t.Errorf("job = %s → %s", j.URL, j.OutDir)
	}
}

func TestMirrorListStop(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	t.Cleanup(slow.Close)
	s, ui := newUI(t, i18n.EN)
	resp, body := do(t, "POST", ui.URL+"/api/mirror", MirrorRequest{URL: slow.URL, OutDir: filepath.Join(t.TempDir(), "x")})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("mirror: %d %s", resp.StatusCode, body)
	}
	if !s.mirrors.running() {
		t.Fatal("job not running")
	}
	do(t, "POST", ui.URL+"/api/mirror/stop", nil)
	waitIdle(t, s)
	if j := s.mirrors.views()[0]; j.Status != JobStopped {
		t.Errorf("status = %s", j.Status)
	}
}

func TestExpandMirrorDir(t *testing.T) {
	now := time.Date(2026, 10, 21, 15, 30, 12, 0, time.UTC)
	for _, c := range []struct{ tpl, url, want string }{
		{"/d/<host>", "https://www.Example.com:8443/x", "/d/www.Example.com_8443"},
		{"/d/<domain>/<date>-<time>", "https://WWW.example.com:8443/x", "/d/example.com/2026-10-21-153012"},
		{"/d/<host>", "http://a/../b", "/d/a"},
		{"/d/plain", "https://example.com", "/d/plain"},
		{"/d/<host>/<domain>", "https://www.müller.de/x", "/d/www.xn--mller-kva.de/xn--mller-kva.de"},
	} {
		if got := ExpandMirrorDir(c.tpl, c.url, now); got != c.want {
			t.Errorf("ExpandMirrorDir(%q, %q) = %q, want %q", c.tpl, c.url, got, c.want)
		}
	}
}

func TestDefaultMirrorDir(t *testing.T) {
	if got := DefaultMirrorDir("/tmp/x", "https://www.example.com:8443/path"); got != "/tmp/x/linkscout/www.example.com_8443" {
		t.Errorf("DefaultMirrorDir = %q", got)
	}
	t.Setenv("HOME", "/home/test")
	if got := expandHome("~/sites"); got != "/home/test/sites" {
		t.Errorf("expandHome = %q", got)
	}
}

// The UI has no referer field; a referer from the command line must still be used.
func TestCLIRefererUsedForUIChecks(t *testing.T) {
	site := target(t)
	s := New(Options{Lang: i18n.EN, Defaults: Defaults{Referer: "https://cli.example/"}}, crawler.New())
	ui := httptest.NewServer(s.Handler())
	defer ui.Close()
	if resp, _ := do(t, "POST", ui.URL+"/api/check", CheckRequest{URL: site.URL}); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("start: %d", resp.StatusCode)
	}
	waitIdle(t, s)
	if got := s.crawler.Config().Fetch.Referer; got != "https://cli.example/" {
		t.Errorf("referer = %q", got)
	}
}

func TestAuthPasswordStaysOnServer(t *testing.T) {
	var lastPass string
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, p, _ := r.BasicAuth()
		lastPass = p
		if p != "pw" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<title>ok</title>")
	}))
	defer site.Close()
	s, ui := newUI(t, i18n.EN)

	do(t, "POST", ui.URL+"/api/check", CheckRequest{URL: site.URL, Username: "admin", Password: "pw"})
	waitIdle(t, s)
	_, body := do(t, "GET", ui.URL+"/api/state", nil)
	if strings.Contains(string(body), `"pw"`) || !strings.Contains(string(body), `"username": "admin"`) {
		t.Errorf("state leaks password or lacks username:\n%s", body)
	}
	_, page := do(t, "GET", ui.URL+"/", nil)
	_, export := do(t, "GET", ui.URL+"/api/export", nil)
	if strings.Contains(string(page), `"pw"`) || strings.Contains(string(export), `"pw"`) {
		t.Error("password leaked into page or export")
	}
	// After a reload the password field is empty: the known password is reused.
	lastPass = ""
	do(t, "POST", ui.URL+"/api/check", CheckRequest{URL: site.URL, Username: "admin"})
	waitIdle(t, s)
	if lastPass != "pw" || s.crawler.Results()[0].Status != 200 {
		t.Errorf("password not reused: sent %q, status %d", lastPass, s.crawler.Results()[0].Status)
	}
}

func TestMirrorOpenFolder(t *testing.T) {
	site := target(t)
	var opened []string
	s := New(Options{Lang: i18n.EN, Version: "test", Defaults: Defaults{MirrorBase: t.TempDir()},
		OpenDir: func(dir string) error { opened = append(opened, dir); return nil }}, crawler.New())
	ui := httptest.NewServer(s.Handler())
	t.Cleanup(ui.Close)

	out := filepath.Join(t.TempDir(), "copy")
	do(t, "POST", ui.URL+"/api/mirror/queue", MirrorRequest{URL: site.URL, OutDir: out})
	id := s.mirrors.views()[0].ID
	// Not finished yet: nothing to open.
	if resp, _ := do(t, "POST", ui.URL+"/api/mirror/open", JobRequest{ID: id}); resp.StatusCode != http.StatusNotFound {
		t.Errorf("open before download: %d", resp.StatusCode)
	}
	do(t, "POST", ui.URL+"/api/mirror/start", JobRequest{ID: id})
	waitIdle(t, s)
	if resp, body := do(t, "POST", ui.URL+"/api/mirror/open", JobRequest{ID: id}); resp.StatusCode != http.StatusOK {
		t.Fatalf("open: %d %s", resp.StatusCode, body)
	}
	if len(opened) != 1 || opened[0] != out {
		t.Errorf("opened %v, want [%s]", opened, out)
	}
	if resp, _ := do(t, "POST", ui.URL+"/api/mirror/open", JobRequest{ID: 999}); resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown id: %d", resp.StatusCode)
	}

	// Reachable from other machines: never pop up windows on the server.
	s.opts.AllowAnyHost = true
	if resp, _ := do(t, "POST", ui.URL+"/api/mirror/open", JobRequest{ID: id}); resp.StatusCode != http.StatusForbidden {
		t.Errorf("open with -listen on a public address: %d", resp.StatusCode)
	}
}

// Export → import restores the results, so a report can be re-checked later.
func TestImportRoundTrip(t *testing.T) {
	site := target(t)
	s, ui := newUI(t, i18n.EN)
	do(t, "POST", ui.URL+"/api/check", CheckRequest{URL: site.URL})
	waitIdle(t, s)
	_, report := do(t, "GET", ui.URL+"/api/export", nil)

	s2, ui2 := newUI(t, i18n.EN)
	var rep crawler.Report
	if err := json.Unmarshal(report, &rep); err != nil {
		t.Fatal(err)
	}
	resp, body := do(t, "POST", ui2.URL+"/api/import", rep)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("import: %d %s", resp.StatusCode, body)
	}
	if got, want := len(s2.crawler.Results()), len(s.crawler.Results()); got != want || got == 0 {
		t.Errorf("imported %d results, want %d", got, want)
	}
	if resp, _ := do(t, "POST", ui2.URL+"/api/import", map[string]string{"startUrl": "not a url"}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("invalid report: %d", resp.StatusCode)
	}
}

// A password typed for an offline copy is reused when the page sends an
// empty password field for the same user (after a reload).
func TestMirrorPasswordReuse(t *testing.T) {
	var got []string
	var mu sync.Mutex
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, p, _ := r.BasicAuth()
		mu.Lock()
		got = append(got, p)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<title>x</title>")
	}))
	defer site.Close()
	s, ui := newUI(t, i18n.EN)
	base := t.TempDir()
	do(t, "POST", ui.URL+"/api/mirror", MirrorRequest{URL: site.URL, OutDir: filepath.Join(base, "1"), Username: "ann", Password: "s3cret"})
	waitIdle(t, s)
	do(t, "POST", ui.URL+"/api/mirror", MirrorRequest{URL: site.URL, OutDir: filepath.Join(base, "2"), Username: "ann"})
	waitIdle(t, s)
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 || got[0] != "s3cret" || got[1] != "s3cret" {
		t.Errorf("passwords sent: %q", got)
	}
}

// Close stops running offline copies and lets them remove their temporary
// directory (important on Ctrl+C).
func TestCloseStopsOfflineCopies(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(10 * time.Second):
		}
	}))
	defer slow.Close()
	s, ui := newUI(t, i18n.EN)
	out := filepath.Join(t.TempDir(), "x")
	do(t, "POST", ui.URL+"/api/mirror", MirrorRequest{URL: slow.URL, OutDir: out})
	time.Sleep(50 * time.Millisecond)
	start := time.Now()
	s.Close(5 * time.Second)
	if s.mirrors.running() || time.Since(start) > 4*time.Second {
		t.Errorf("still running after Close (%v)", time.Since(start))
	}
	if _, err := os.Stat(filepath.Join(out, ".linkscout-tmp")); err == nil {
		t.Error("temporary directory left behind")
	}
}

func TestHelpers(t *testing.T) {
	if clamp(0, 1, 64) != 1 || clamp(100, 1, 64) != 64 || clamp(8, 1, 64) != 8 {
		t.Error("clamp")
	}
	if orDefault(0, 4) != 4 || orDefault(2, 4) != 2 {
		t.Error("orDefault")
	}
	t.Setenv("XDG_DOWNLOAD_DIR", "/data/dl")
	if DefaultMirrorBase() != "/data/dl" {
		t.Errorf("XDG_DOWNLOAD_DIR ignored: %q", DefaultMirrorBase())
	}
	if !isLoopbackHost("localhost:8088") || !isLoopbackHost("[::1]:80") || !isLoopbackHost("app.localhost") ||
		isLoopbackHost("example.com") || isLoopbackHost("192.168.1.2:8088") {
		t.Error("isLoopbackHost")
	}
	if sanitizeName("www.example.com:8443/x") != "www.example.com_8443_x" {
		t.Errorf("sanitizeName = %q", sanitizeName("www.example.com:8443/x"))
	}
}

// Every SEO check needs a name and an explanation in every language.
func TestIssueTranslations(t *testing.T) {
	for _, l := range i18n.Supported {
		m := i18n.Messages(l)
		for _, d := range crawler.Issues {
			for _, k := range []string{"issue." + d.ID, "issue." + d.ID + ".help"} {
				if m[k] == "" || m[k] == k {
					t.Errorf("%s: missing translation %q", l, k)
				}
			}
		}
		for _, sev := range []string{"sev.error", "sev.warning", "sev.notice"} {
			if m[sev] == "" {
				t.Errorf("%s: missing %q", l, sev)
			}
		}
	}
}

// The state carries the SEO issues, the export offers JSON, CSV, HTML and PDF,
// and an earlier report can be compared.
func TestSEOExportAndCompare(t *testing.T) {
	site := target(t)
	s, ui := newUI(t, i18n.DE)
	do(t, "POST", ui.URL+"/api/check", CheckRequest{URL: site.URL})
	waitIdle(t, s)

	_, body := do(t, "GET", ui.URL+"/api/state", nil)
	var st State
	json.Unmarshal(body, &st)
	if len(st.Issues) == 0 {
		t.Fatal("no SEO issues in the state")
	}
	withIssues := 0
	for _, r := range st.Results {
		if len(r.Issues) > 0 {
			withIssues++
		}
	}
	if withIssues == 0 {
		t.Fatal("results carry no issues")
	}
	someIssue := ""
	for _, r := range st.Results {
		if len(r.Issues) > 0 {
			someIssue = r.Issues[0]
			break
		}
	}

	for format, want := range map[string]string{
		"json": "application/json", "csv": "text/csv", "html": "text/html", "pdf": "application/pdf",
	} {
		resp, body := do(t, "GET", ui.URL+"/api/export?format="+format, nil)
		if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), want) ||
			!strings.Contains(resp.Header.Get("Content-Disposition"), "."+format+`"`) {
			t.Errorf("%s export: %d %s %s", format, resp.StatusCode, resp.Header.Get("Content-Type"), resp.Header.Get("Content-Disposition"))
		}
		if format == "html" && (!strings.Contains(string(body), "LinkScout-Bericht") || strings.Contains(string(body), "<script")) {
			t.Error("HTML report: wrong language or contains scripts")
		}
		// the overview charts and the details of every URL with its SEO issues
		for _, want := range []string{i18n.T(i18n.DE, "stats.chart_status"), i18n.T(i18n.DE, "seo.health"),
			i18n.T(i18n.DE, "stats.size_by_type"), `<svg class="chart"`, i18n.T(i18n.DE, "detail.found_on"),
			i18n.T(i18n.DE, "detail.seo"), i18n.T(i18n.DE, "issue."+someIssue+".help")} {
			if format == "html" && !strings.Contains(string(body), want) && !strings.Contains(string(body), template.HTMLEscapeString(want)) {
				t.Errorf("HTML report lacks %q", want)
			}
		}
		if format == "json" && !strings.Contains(string(body), `"overview"`) {
			t.Error("JSON report without overview")
		}
		if format == "csv" && !strings.HasPrefix(string(body), "url,final_url,redirects,status") {
			t.Errorf("CSV header: %.60s", body)
		}
	}

	_, report := do(t, "GET", ui.URL+"/api/export", nil)
	var rep crawler.Report
	json.Unmarshal(report, &rep)
	rep.Results = rep.Results[1:] // the earlier report did not know the first URL yet
	resp, body := do(t, "POST", ui.URL+"/api/compare", rep)
	var cmp crawler.Comparison
	if err := json.Unmarshal(body, &cmp); err != nil || resp.StatusCode != 200 || len(cmp.Added) != 1 {
		t.Errorf("compare: %d %s", resp.StatusCode, body)
	}
}

// Titles from the crawled site are escaped in the HTML report.
func TestHTMLReportEscapes(t *testing.T) {
	var buf bytes.Buffer
	rep := crawler.Report{StartURL: "https://e.com/", Results: []crawler.Result{{URL: "https://e.com/", Status: 200, Title: `<script>alert(1)</script>`}}}
	if err := WriteHTMLReport(&buf, rep, i18n.EN, false); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "<script>alert") {
		t.Error("title not escaped")
	}
}

// The PDF report is a valid, self-contained PDF: header, one page object per
// page, a cross-reference table whose offsets point at the objects, the two
// standard fonts and the escaped texts.
func TestPDFReport(t *testing.T) {
	rep := crawler.Report{StartURL: "https://e.com/", Generator: "LinkScout test",
		Issues: []crawler.IssueCount{{IssueDef: crawler.IssueDef{ID: "title_missing", Severity: crawler.SevError}, Count: 1}}}
	for i := 0; i < 120; i++ { // enough rows for several pages
		rep.Results = append(rep.Results, crawler.Result{URL: fmt.Sprintf("https://e.com/seite-%d", i), Status: 200,
			MimeType: "text/html", Title: "Größe (Ü) – Test", Issues: []string{"title_missing"}})
	}
	var buf bytes.Buffer
	if err := WritePDFReport(&buf, rep, i18n.DE, false); err != nil {
		t.Fatal(err)
	}
	pdf := buf.String()
	if !strings.HasPrefix(pdf, "%PDF-1.4") || !strings.HasSuffix(pdf, "%%EOF\n") {
		t.Fatal("not a PDF")
	}
	pages := strings.Count(pdf, "/Type /Page ")
	if pages < 3 {
		t.Errorf("%d pages for 120 rows", pages)
	}
	if !strings.Contains(pdf, "/BaseFont /Helvetica-Bold") || !strings.Contains(pdf, "/Count "+fmt.Sprint(pages)) {
		t.Error("fonts or page count missing")
	}
	// "Größe (Ü) – Test": umlauts as WinAnsi octal escapes, parentheses escaped
	if !strings.Contains(pdf, `Gr\366\337e \(\334\) \226 Test`) {
		t.Error("text not encoded as WinAnsi")
	}
	// every xref entry points at "n 0 obj"
	i := strings.LastIndex(pdf, "\nxref\n")
	lines := strings.Split(pdf[i+6:], "\n")
	var count int
	fmt.Sscanf(lines[0], "0 %d", &count)
	for n := 1; n < count; n++ {
		off, _ := strconv.Atoi(strings.Fields(lines[1+n])[0])
		if !strings.HasPrefix(pdf[off:], fmt.Sprintf("%d 0 obj", n)) {
			t.Fatalf("xref entry %d points to %q", n, pdf[off:off+10])
		}
	}
}

func TestPDFHelpers(t *testing.T) {
	if got := fit("https://e.com/a/very/long/path/page.html", 60, 8, false, true); !strings.HasPrefix(got, "…") || !strings.HasSuffix(got, "page.html") {
		t.Errorf("fit from start = %q", got)
	}
	if got := fit("short", 100, 8, false, false); got != "short" {
		t.Errorf("fit = %q", got)
	}
	if pdfString("a → b ✓ 👋🏽") != "a > b ? " {
		t.Errorf("pdfString = %q", pdfString("a → b ✓"))
	}
}

func TestFormatter(t *testing.T) {
	de, en := formatter{i18n.DE}, formatter{i18n.EN}
	for _, c := range []struct{ got, want string }{
		{de.num(1234567), "1.234.567"}, {en.num(1234), "1,234"}, {en.num(12), "12"},
		{de.bytes(1536), "1,5 KiB"}, {en.bytes(500), "500 B"}, {en.bytes(0), "–"},
		{de.pct(1, 3), "33,3 %"}, {en.pct(1, 2), "50 %"},
		{de.quote("x"), "„x“"}, {en.quote("x"), "“x”"}, {fmtMs(1500), "1.5 s"},
	} {
		if c.got != c.want {
			t.Errorf("got %q, want %q", c.got, c.want)
		}
	}
}

// Long words (URLs) are broken anywhere, every line fits.
func TestWrap(t *testing.T) {
	s := "see https://e.com/" + strings.Repeat("abcdefghij", 20) + " and more words here"
	lines := wrap(s, 150, 8, false)
	if len(lines) < 3 || strings.ReplaceAll(strings.Join(lines, ""), " ", "") != strings.ReplaceAll(s, " ", "") {
		t.Errorf("lines = %q", lines)
	}
	for _, l := range lines {
		if textWidth(l, 8, false) > 150 {
			t.Errorf("line too wide: %q", l)
		}
	}
}

// The PDF has the overview and a clickable detail view per URL.
func TestPDFReportDetails(t *testing.T) {
	rep := crawler.Report{StartURL: "https://e.com/", Results: []crawler.Result{
		{URL: "https://e.com/", Status: 200, Kind: "internal", Internal: true, SEO: &crawler.PageSEO{Titles: []string{"Hi"}}},
		{URL: "https://e.com/old", Status: 200, Kind: "internal", Redirects: []string{"https://e.com/old"}, FinalURL: "https://e.com/new", Depth: 1, FoundOn: []string{"https://e.com/"}},
	}}
	var buf bytes.Buffer
	if err := WritePDFReport(&buf, rep, i18n.EN, false); err != nil {
		t.Fatal(err)
	}
	pdf := buf.String()
	for _, want := range []string{"/URI (https://e.com/new)", "(" + pdfString(i18n.T(i18n.EN, "seo.health")) + ")",
		"(" + pdfString(i18n.T(i18n.EN, "issue.title_short")) + ")", "(" + pdfString(i18n.T(i18n.EN, "detail.redirects")) + ")"} {
		if !strings.Contains(pdf, want) {
			t.Errorf("PDF lacks %s", want)
		}
	}
}

// Long lists are never shortened in the PDF: a bar list longer than a page
// continues on the next one, every linking page is listed.
func TestPDFReportLongLists(t *testing.T) {
	rep := crawler.Report{StartURL: "https://e.com/"}
	var foundOn []string
	for i := 0; i < 80; i++ {
		foundOn = append(foundOn, fmt.Sprintf("https://e.com/from-%d", i))
	}
	rep.Results = append(rep.Results, crawler.Result{URL: "https://e.com/", Status: 200, Kind: "internal", FoundOn: foundOn})
	for i := 0; i < 120; i++ {
		rep.Results = append(rep.Results, crawler.Result{URL: fmt.Sprintf("https://e.com/f%d", i), Status: 200, Kind: "file", MimeType: fmt.Sprintf("type/x-%d", i)})
	}
	var buf bytes.Buffer
	if err := WritePDFReport(&buf, rep, i18n.EN, false); err != nil {
		t.Fatal(err)
	}
	pdf := buf.String()
	for _, want := range []string{"(type/x-119)", "(https://e.com/from-79)"} {
		if !strings.Contains(pdf, want) {
			t.Errorf("PDF lacks %s", want)
		}
	}
	if n := strings.Count(pdf, "(type/x-77)"); n != 2 { // bar list "by MIME type" and the details
		t.Errorf("type/x-77 appears %d times, want 2", n)
	}
}

// The sitemap tab gets the site structure as JSON; sitemap.xml and the HTML
// list are downloads.
func TestSitemapEndpoint(t *testing.T) {
	site := target(t)
	s, ui := newUI(t, i18n.EN)
	do(t, "POST", ui.URL+"/api/check", CheckRequest{URL: site.URL})
	waitIdle(t, s)

	resp, body := do(t, "GET", ui.URL+"/api/sitemap", nil)
	var tree crawler.SiteNode
	if err := json.Unmarshal(body, &tree); err != nil || resp.StatusCode != 200 || tree.Pages == 0 || tree.URL == "" {
		t.Fatalf("tree: %d %s", resp.StatusCode, body)
	}
	resp, body = do(t, "GET", ui.URL+"/api/sitemap?format=xml", nil)
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/xml") ||
		!strings.Contains(resp.Header.Get("Content-Disposition"), `filename="sitemap.xml"`) ||
		!strings.Contains(string(body), "<loc>"+tree.URL+"</loc>") {
		t.Errorf("sitemap.xml: %s %s", resp.Header, body)
	}
	resp, body = do(t, "GET", ui.URL+"/api/sitemap?format=html", nil)
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") || !strings.Contains(string(body), "<ul>") ||
		!strings.Contains(string(body), `href="`+tree.URL+`"`) || strings.Contains(string(body), "<script") {
		t.Errorf("sitemap HTML: %s", body)
	}
}
