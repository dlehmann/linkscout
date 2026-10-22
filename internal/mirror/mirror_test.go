package mirror

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLocalPath(t *testing.T) {
	tests := []struct {
		url, ctype string
		external   bool
		want       string
	}{
		{"https://e.com/", "text/html", false, "index.html"},
		{"https://e.com/about", "text/html", false, "about/index.html"},
		{"https://e.com/about/", "text/html", false, "about/index.html"},
		{"https://e.com/a/page.html", "text/html", false, "a/page.html"},
		{"https://e.com/page.php", "text/html", false, "page.php.html"},
		{"https://e.com/img/logo.png", "image/png", false, "img/logo.png"},
		{"https://e.com/styles", "text/css", false, "styles.css"},
		{"https://e.com/files/doc.pdf", "application/pdf", false, "files/doc.pdf"},
		{"https://e.com/a b/ü.png", "image/png", false, "a b/ü.png"},
		{"https://e.com/x:y*z.png", "image/png", false, "x_y_z.png"},
		{"https://cdn.org/lib/x.js", "text/javascript", true, "_external/cdn.org/lib/x.js"},
	}
	for _, tt := range tests {
		u, _ := url.Parse(tt.url)
		if got := LocalPath(u, tt.ctype, tt.external); got != tt.want {
			t.Errorf("LocalPath(%q) = %q want %q", tt.url, got, tt.want)
		}
	}
	// Different queries must map to different files with the same extension.
	u1, _ := url.Parse("https://e.com/list.php?page=1")
	u2, _ := url.Parse("https://e.com/list.php?page=2")
	p1, p2 := LocalPath(u1, "text/html", false), LocalPath(u2, "text/html", false)
	if p1 == p2 || !strings.HasSuffix(p1, ".html") || !strings.HasPrefix(p1, "list.php_q") {
		t.Errorf("query paths: %q %q", p1, p2)
	}
	// Path traversal is impossible.
	u3, _ := url.Parse("https://e.com/a/%2e%2e/%2e%2e/etc/passwd")
	if p := LocalPath(u3, "text/plain", false); strings.Contains(p, "..") {
		t.Errorf("traversal: %q", p)
	}
}

func TestSanitizeLongNames(t *testing.T) {
	long := strings.Repeat("a", 300) + ".png"
	got := sanitize(long)
	if len(got) > 210 || !strings.HasSuffix(got, ".png") {
		t.Errorf("sanitize long name: len=%d %q", len(got), got[len(got)-20:])
	}
}

func TestRelPath(t *testing.T) {
	tests := []struct{ from, to, want string }{
		{"index.html", "img/a.png", "img/a.png"},
		{"about/index.html", "index.html", "../index.html"},
		{"about/index.html", "about/team/index.html", "team/index.html"},
		{"a/b/c.html", "x/y.css", "../../x/y.css"},
		{"index.html", "a b/c#d.png", "a%20b/c%23d.png"},
	}
	for _, tt := range tests {
		if got := RelPath(tt.from, tt.to); got != tt.want {
			t.Errorf("RelPath(%q,%q) = %q want %q", tt.from, tt.to, got, tt.want)
		}
	}
}

// testSite serves a small website with pages, CSS, images and a CDN host.
func testSite(t *testing.T) (site, cdn string) {
	cdnMux := http.NewServeMux()
	cdnMux.HandleFunc("/lib.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/javascript")
		fmt.Fprint(w, "console.log(1)")
	})
	cdnSrv := httptest.NewServer(cdnMux)
	t.Cleanup(cdnSrv.Close)
	cdn = strings.Replace(cdnSrv.URL, "127.0.0.1", "localhost", 1)

	mux := http.NewServeMux()
	page := func(body string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, body)
		}
	}
	mux.HandleFunc("/{$}", page(`<!doctype html><html><head><title>Home</title>
		<base href="/">
		<link rel="stylesheet" href="css/site.css" integrity="sha384-abc">
		<script src="`+cdn+`/lib.js"></script>
		</head><body>
		<a href="about">About</a>
		<a href="about#team">Team</a>
		<a href="list.php?page=2">List</a>
		<a href="https://elsewhere.example/">Elsewhere</a>
		<a href="missing">Missing</a>
		<a href="gone">Gone</a>
		<a href="empty">Empty</a>
		<a href="mailto:x@y.z">Mail</a>
		<img src="img/logo.png" srcset="img/logo.png 1x, img/logo@2x.png 2x">
		<div style="background:url(img/bg.png)"></div>
		</body></html>`))
	mux.HandleFunc("/about", page(`<title>About</title><a href="/">Home</a><a href="/files/doc.pdf">Doc</a><img src="/img/logo.png">`))
	// A custom error page is part of the site: saved and parsed anyway.
	mux.HandleFunc("/gone", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusGone)
		fmt.Fprint(w, `<title>Gone</title><a href="/">Home</a><img src="/img/gone.png"><img src="/img/broken.png">`)
	})
	mux.HandleFunc("/img/gone.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write([]byte("PNG"))
	})
	mux.HandleFunc("/img/broken.png", http.NotFound) // resource: error body not saved
	mux.HandleFunc("/empty", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	mux.HandleFunc("/list.php", page(`<title>List</title><a href="/about">About</a>`))
	mux.HandleFunc("/css/site.css", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css")
		fmt.Fprint(w, `@import "print.css"; body{background:url("../img/bg.png")} @font-face{src:url(/fonts/f.woff2)}`)
	})
	mux.HandleFunc("/css/print.css", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css")
		fmt.Fprint(w, `p{}`)
	})
	for _, p := range []string{"/img/logo.png", "/img/logo@2x.png", "/img/bg.png"} {
		mux.HandleFunc(p, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "image/png")
			w.Write([]byte("PNG"))
		})
	}
	mux.HandleFunc("/fonts/f.woff2", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "font/woff2")
		w.Write([]byte("WOFF"))
	})
	mux.HandleFunc("/files/doc.pdf", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		w.Write([]byte("%PDF"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL, cdn
}

func read(t *testing.T, dir, p string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, p))
	if err != nil {
		t.Fatalf("expected file %s: %v", p, err)
	}
	return string(b)
}

func TestMirrorRun(t *testing.T) {
	site, cdn := testSite(t)
	dir := t.TempDir()
	p, err := Run(context.Background(), Config{StartURL: site, OutDir: dir, External: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.Running || p.Files < 10 || p.Error != "" {
		t.Errorf("progress = %+v", p)
	}

	home := read(t, dir, "index.html")
	for _, want := range []string{
		`href="about/index.html"`,
		`href="about/index.html#team"`,
		`href="css/site.css"`,
		`src="img/logo.png"`,
		`srcset="img/logo.png 1x, img/logo@2x.png 2x"`,
		`url(&#34;img/bg.png&#34;)`,
		`href="https://elsewhere.example/"`,
		`href="gone/index.html"`,    // 410 with content → saved
		`href="` + site + `/empty"`, // 500 without body → stays absolute
		`href="mailto:x@y.z"`,
		`src="_external/` + sanitize(strings.TrimPrefix(cdn, "http://")) + `/lib.js"`, // host:port → host_port
	} {
		if !strings.Contains(home, want) {
			t.Errorf("index.html lacks %s\n%s", want, home)
		}
	}
	if strings.Contains(home, "integrity") {
		t.Error("integrity attribute must be removed")
	}
	if strings.Contains(home, "<base") {
		t.Error("<base> must be removed")
	}

	about := read(t, dir, "about/index.html")
	if !strings.Contains(about, `href="../index.html"`) || !strings.Contains(about, `href="../files/doc.pdf"`) {
		t.Errorf("about not rewritten:\n%s", about)
	}
	css := read(t, dir, "css/site.css")
	if css != `@import "print.css"; body{background:url("../img/bg.png")} @font-face{src:url("../fonts/f.woff2")}` {
		t.Errorf("css = %s", css)
	}
	for _, f := range []string{"css/print.css", "img/logo@2x.png", "fonts/f.woff2", "files/doc.pdf"} {
		read(t, dir, f)
	}
	gone := read(t, dir, "gone/index.html")
	if !strings.Contains(gone, `href="../index.html"`) || !strings.Contains(gone, `src="../img/gone.png"`) {
		t.Errorf("error page not rewritten:\n%s", gone)
	}
	read(t, dir, "img/gone.png") // linked only from the error page
	if !strings.Contains(gone, `src="`+site+`/img/broken.png"`) {
		t.Errorf("broken image must stay absolute:\n%s", gone)
	}
	want := []Problem{
		{URL: site + "/empty", Source: site + "/", Status: 500},
		{URL: site + "/gone", Source: site + "/", Status: 410, Saved: true},
		{URL: site + "/img/broken.png", Source: site + "/gone", Status: 404},
		{URL: site + "/missing", Source: site + "/", Status: 404, Saved: true},
	}
	if fmt.Sprint(p.Problems) != fmt.Sprint(want) {
		t.Errorf("problems = %+v\nwant %+v", p.Problems, want)
	}

	// list.php?page=2 is stored with a hashed query suffix and linked.
	matches, _ := filepath.Glob(filepath.Join(dir, "list.php_q*.html"))
	if len(matches) != 1 {
		t.Errorf("query page not saved: %v", matches)
	}
	if _, err := os.Stat(filepath.Join(dir, tmpDirName)); !os.IsNotExist(err) {
		t.Error("temp dir not removed")
	}
}

func TestMirrorWithoutExternal(t *testing.T) {
	site, cdn := testSite(t)
	dir := t.TempDir()
	if _, err := Run(context.Background(), Config{StartURL: site, OutDir: dir}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "_external")); !os.IsNotExist(err) {
		t.Error("external resources downloaded although disabled")
	}
	if home := read(t, dir, "index.html"); !strings.Contains(home, `src="`+cdn+`/lib.js"`) {
		t.Error("external script must stay absolute")
	}
}

func TestMirrorMaxPages(t *testing.T) {
	site, _ := testSite(t)
	dir := t.TempDir()
	if _, err := Run(context.Background(), Config{StartURL: site, OutDir: dir, MaxPages: 2}, nil); err != nil {
		t.Fatal(err)
	}
	pages, _ := filepath.Glob(filepath.Join(dir, "*.html"))
	sub, _ := filepath.Glob(filepath.Join(dir, "*", "index.html"))
	if n := len(pages) + len(sub); n != 2 {
		t.Errorf("MaxPages 2: %d pages written (%v %v)", n, pages, sub)
	}
	// Resources of the saved pages are complete nonetheless.
	read(t, dir, "css/site.css")
}

func TestZipDir(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "a"), 0o755)
	os.WriteFile(filepath.Join(dir, "index.html"), []byte("hi"), 0o644)
	os.WriteFile(filepath.Join(dir, "a", "b.txt"), []byte("b"), 0o644)
	os.MkdirAll(filepath.Join(dir, tmpDirName), 0o755)
	os.WriteFile(filepath.Join(dir, tmpDirName, "junk"), []byte("x"), 0o644)

	var buf bytes.Buffer
	if err := ZipDir(dir, &buf); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	if strings.Join(names, ",") != "a/b.txt,index.html" {
		t.Errorf("zip entries = %v", names)
	}
}

func TestMirrorBackground(t *testing.T) {
	site, _ := testSite(t)
	m := &Mirror{}
	if err := m.Start(Config{StartURL: "nope", OutDir: t.TempDir()}); err == nil {
		t.Error("invalid URL accepted")
	}
	if err := m.Start(Config{StartURL: site, OutDir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	p := m.Progress()
	if p.Running || p.Files == 0 || p.Error != "" {
		t.Errorf("progress = %+v", p)
	}
}

func TestBaseDirAndLocalPathIn(t *testing.T) {
	for _, c := range []struct{ start, want string }{
		{"https://site/", "/"},
		{"https://site", "/"},
		{"https://user.github.io/edith", "/edith/"},
		{"https://user.github.io/edith/", "/edith/"},
		{"https://user.github.io/edith/index.html", "/edith/"},
		{"https://site/news/2024.10", "/news/2024.10/"}, // not a file extension
	} {
		u, _ := url.Parse(c.start)
		if got := BaseDir(u); got != c.want {
			t.Errorf("BaseDir(%q) = %q want %q", c.start, got, c.want)
		}
	}
	for _, c := range []struct{ url, ctype, want string }{
		{"https://user.github.io/edith", "text/html", "index.html"},
		{"https://user.github.io/edith/", "text/html", "index.html"},
		{"https://user.github.io/edith/impressum.html", "text/html", "impressum.html"},
		{"https://user.github.io/edith/screenshots/main.png", "image/png", "screenshots/main.png"},
		{"https://user.github.io/assets/site.css", "text/css", "_site/assets/site.css"},
		{"https://user.github.io/polonaise/", "text/html", "_site/polonaise/index.html"},
	} {
		u, _ := url.Parse(c.url)
		if got := LocalPathIn(u, c.ctype, false, "/edith/"); got != c.want {
			t.Errorf("LocalPathIn(%q) = %q want %q", c.url, got, c.want)
		}
	}
}

// A project site in a sub-directory (like GitHub Pages) is mirrored with its
// own index.html at the root of the target directory.
func TestRunProjectSiteInSubdirectory(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/proj", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/proj/", http.StatusMovedPermanently)
	})
	mux.HandleFunc("/proj/{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<title>Proj</title><link rel="stylesheet" href="/shared.css"><img src="img/a.png"><a href="about.html">About</a><a href="/other/">Other</a>`)
	})
	mux.HandleFunc("/proj/about.html", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<title>About</title><a href="./">Home</a><img src="img/a.png">`)
	})
	mux.HandleFunc("/proj/img/a.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write([]byte("PNG"))
	})
	mux.HandleFunc("/shared.css", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css")
		fmt.Fprint(w, `p{}`)
	})
	mux.HandleFunc("/other/{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<title>Other</title>`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	out := t.TempDir()
	if _, err := Run(context.Background(), Config{StartURL: srv.URL + "/proj", OutDir: out}, nil); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"index.html", "about.html", "img/a.png", "_site/shared.css", "_site/other/index.html"} {
		if _, err := os.Stat(filepath.Join(out, f)); err != nil {
			t.Errorf("missing %s: %v", f, err)
		}
	}
	if _, err := os.Stat(filepath.Join(out, "proj")); err == nil {
		t.Error("the start directory must not be repeated inside the target directory")
	}
	index := read(t, out, "index.html")
	for _, want := range []string{`href="_site/shared.css"`, `src="img/a.png"`, `href="about.html"`, `href="_site/other/index.html"`} {
		if !strings.Contains(index, want) {
			t.Errorf("index.html lacks %s:\n%s", want, index)
		}
	}
	if about := read(t, out, "about.html"); !strings.Contains(about, `href="index.html"`) {
		t.Errorf("about.html home link not rewritten:\n%s", about)
	}
}

// edgeSite serves the cases that broke offline copies before: a srcset with
// commas inside the URLs, a meta refresh, a file larger than the limit, a
// file named like a directory and an image without file extension.
func edgeSite(t *testing.T) string {
	mux := http.NewServeMux()
	html := func(w http.ResponseWriter, body string) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, body)
	}
	mux.HandleFunc("/{$}", func(w http.ResponseWriter, r *http.Request) {
		html(w, `<title>Edge</title>
			<meta http-equiv="refresh" content="30; URL = /about/team.html">
			<img srcset="/img/w_100,h_50/a.png 1x, /img/w_200,h_100/a.png 2x">
			<a href="/big.bin">big</a>
			<a href="/about">about (a file, no extension)</a>
			<a href="/about/team.html">team</a>
			<img src="/pic">`)
	})
	mux.HandleFunc("/about/team.html", func(w http.ResponseWriter, r *http.Request) { html(w, `<title>Team</title>`) })
	mux.HandleFunc("/about", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write([]byte("BLOB"))
	})
	mux.HandleFunc("/big.bin", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write(bytes.Repeat([]byte("x"), 4096))
	})
	mux.HandleFunc("/pic", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write([]byte("PNG"))
	})
	mux.HandleFunc("/img/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write([]byte("PNG"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestMirrorEdgeCases(t *testing.T) {
	site := edgeSite(t)
	dir := t.TempDir()
	if _, err := Run(context.Background(), Config{StartURL: site, OutDir: dir, MaxFileSize: 1024}, nil); err != nil {
		t.Fatal(err)
	}
	home := read(t, dir, "index.html")

	// srcset URLs with commas are kept intact and point to the local files.
	if !strings.Contains(home, `srcset="img/w_100,h_50/a.png 1x, img/w_200,h_100/a.png 2x"`) {
		t.Errorf("srcset not rewritten correctly:\n%s", home)
	}
	read(t, dir, "img/w_200,h_100/a.png")

	// The refresh target is rewritten, the delay kept.
	if !strings.Contains(home, `content="30; URL = about/team.html"`) {
		t.Errorf("meta refresh not rewritten:\n%s", home)
	}

	// A file over the size limit is not saved half; the link stays online.
	if _, err := os.Stat(filepath.Join(dir, "big.bin")); err == nil {
		t.Error("truncated big.bin was written")
	}
	if !strings.Contains(home, `href="`+site+`/big.bin"`) {
		t.Errorf("link to the skipped file must stay absolute:\n%s", home)
	}

	// /about (a file) and /about/team.html (a directory entry) both survive.
	read(t, dir, "about/team.html")
	entries, _ := filepath.Glob(filepath.Join(dir, "about_*"))
	if len(entries) != 1 || read(t, dir, filepath.Base(entries[0])) != "BLOB" {
		t.Errorf("file named like a directory: %v", entries)
	}

	// An image without extension gets one from its content type.
	if read(t, dir, "pic.png") != "PNG" || !strings.Contains(home, `src="pic.png"`) {
		t.Errorf("extension-less image:\n%s", home)
	}

	// Nothing of the temporary directory is left behind.
	if _, err := os.Stat(filepath.Join(dir, tmpDirName)); err == nil {
		t.Error("temporary directory left behind")
	}
}

// Stopping a background job ends it quickly, reports the cancellation and
// cleans up.
func TestMirrorStop(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select { // a page that never finishes loading
		case <-r.Context().Done():
		case <-time.After(10 * time.Second):
		}
	}))
	defer srv.Close()
	dir := t.TempDir()
	m := &Mirror{}
	if err := m.Start(Config{StartURL: srv.URL, OutDir: dir}); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(Config{StartURL: srv.URL, OutDir: dir}); !errors.Is(err, ErrBusy) {
		t.Errorf("second Start: %v, want ErrBusy", err)
	}
	time.Sleep(50 * time.Millisecond)
	start := time.Now()
	m.Stop()
	m.Wait()
	if time.Since(start) > 3*time.Second {
		t.Errorf("Stop took %v", time.Since(start))
	}
	if p := m.Progress(); p.Running || p.Error == "" {
		t.Errorf("progress after Stop = %+v", p)
	}
	if _, err := os.Stat(filepath.Join(dir, tmpDirName)); err == nil {
		t.Error("temporary directory left behind")
	}
}

func TestExtForType(t *testing.T) {
	for ct, want := range map[string]string{
		"text/css": ".css", "application/javascript": ".js", "image/jpeg": ".jpg",
		"image/svg+xml": ".svg", "font/woff2": ".woff2", "application/pdf": ".pdf",
		"application/octet-stream": "",
	} {
		if got := extForType(ct); got != want {
			t.Errorf("extForType(%q) = %q want %q", ct, got, want)
		}
	}
}
