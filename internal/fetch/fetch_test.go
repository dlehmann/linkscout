package fetch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRefererPolicy(t *testing.T) {
	tests := []struct {
		source, target, want string
	}{
		{"https://a.com/page?x=1#frag", "https://a.com/other", "https://a.com/page?x=1"},
		{"https://a.com/page", "https://b.com/", "https://a.com/"},
		{"https://a.com/page", "https://sub.a.com/", "https://a.com/"},
		{"https://a.com/page", "http://a.com/insecure", ""}, // downgrade
		{"http://a.com/page", "https://b.com/", "http://a.com/"},
		{"http://a.com/page", "http://a.com/x", "http://a.com/page"},
		{"https://user:pw@a.com/p", "https://a.com/q", "https://a.com/p"},
		{"mailto:x@y", "https://a.com/", ""},
	}
	for _, tt := range tests {
		if got := Referer(tt.source, tt.target); got != tt.want {
			t.Errorf("Referer(%q,%q) = %q want %q", tt.source, tt.target, got, tt.want)
		}
	}
}

// recorder is a test server that records the request headers per path.
type recorder struct {
	mu      sync.Mutex
	headers map[string]http.Header
	methods map[string][]string
}

func newRecorder() *recorder {
	return &recorder{headers: map[string]http.Header{}, methods: map[string][]string{}}
}

func (r *recorder) record(req *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.headers[req.URL.Path] = req.Header.Clone()
	r.methods[req.URL.Path] = append(r.methods[req.URL.Path], req.Method)
}

func (r *recorder) header(path, key string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.headers[path].Get(key)
}

func testServer(t *testing.T) (*httptest.Server, *recorder) {
	rec := newRecorder()
	mux := http.NewServeMux()
	mux.HandleFunc("/page", func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte("<title>Page</title>"))
	})
	mux.HandleFunc("/redirect1", func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		http.Redirect(w, r, "/redirect2", http.StatusMovedPermanently)
	})
	mux.HandleFunc("/redirect2", func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		http.Redirect(w, r, "/page", http.StatusFound)
	})
	mux.HandleFunc("/loop", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/loop", http.StatusFound)
	})
	mux.HandleFunc("/nohead.pdf", func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/pdf")
		w.Write([]byte("%PDF-1.4"))
	})
	mux.HandleFunc("/big.bin", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write(bytes.Repeat([]byte("x"), 1<<20))
	})
	mux.HandleFunc("/gone", func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		w.WriteHeader(http.StatusGone)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, rec
}

func TestFirefoxHeaders(t *testing.T) {
	srv, rec := testServer(t)
	c := New(Options{})
	r := c.Get(context.Background(), srv.URL+"/page", srv.URL+"/start", true, 0)
	if r.Err != nil || r.Status != 200 {
		t.Fatalf("unexpected response %+v", r)
	}
	if got := rec.header("/page", "User-Agent"); got != FirefoxUA {
		t.Errorf("User-Agent = %q", got)
	}
	if got := rec.header("/page", "Referer"); got != srv.URL+"/start" {
		t.Errorf("same-origin Referer = %q", got)
	}
	if !strings.HasPrefix(rec.header("/page", "Accept"), "text/html") {
		t.Error("Accept header missing")
	}
	if string(r.Body) != "<title>Page</title>" || r.ContentType != "text/html" {
		t.Errorf("body/ctype: %q %q", r.Body, r.ContentType)
	}
}

func TestCrossOriginReferer(t *testing.T) {
	srv, rec := testServer(t)
	c := New(Options{})
	c.Get(context.Background(), srv.URL+"/page", "https://other.example/some/page?q=1", false, 0)
	if got := rec.header("/page", "Referer"); got != "" {
		// https source → http target is a downgrade: no referer
		t.Errorf("downgrade must not send a referer, got %q", got)
	}
	c.Get(context.Background(), srv.URL+"/page", "http://other.example/some/page?q=1", false, 0)
	if got := rec.header("/page", "Referer"); got != "http://other.example/" {
		t.Errorf("cross-origin referer must be the origin only, got %q", got)
	}
}

func TestFixedRefererAndUA(t *testing.T) {
	srv, rec := testServer(t)
	c := New(Options{Referer: "https://google.com/", UserAgent: "MyBot/1.0"})
	r := c.Get(context.Background(), srv.URL+"/redirect1", srv.URL+"/start", false, 0)
	if r.Status != 200 {
		t.Fatalf("status %d err %v", r.Status, r.Err)
	}
	for _, p := range []string{"/redirect1", "/redirect2", "/page"} {
		if got := rec.header(p, "Referer"); got != "https://google.com/" {
			t.Errorf("%s: fixed referer not kept, got %q", p, got)
		}
		if got := rec.header(p, "User-Agent"); got != "MyBot/1.0" {
			t.Errorf("%s: UA = %q", p, got)
		}
	}
}

func TestRedirectChain(t *testing.T) {
	srv, rec := testServer(t)
	c := New(Options{})
	r := c.Get(context.Background(), srv.URL+"/redirect1", srv.URL+"/start", false, 0)
	if r.FinalURL != srv.URL+"/page" {
		t.Errorf("FinalURL = %q", r.FinalURL)
	}
	want := []string{srv.URL + "/redirect1", srv.URL + "/redirect2"}
	if strings.Join(r.Redirects, " ") != strings.Join(want, " ") {
		t.Errorf("Redirects = %v want %v", r.Redirects, want)
	}
	// Firefox keeps the original referer across redirects.
	if got := rec.header("/page", "Referer"); got != srv.URL+"/start" {
		t.Errorf("referer after redirect = %q", got)
	}
}

func TestTooManyRedirects(t *testing.T) {
	srv, _ := testServer(t)
	c := New(Options{MaxRedirects: 3})
	r := c.Get(context.Background(), srv.URL+"/loop", "", false, 0)
	if r.Err == nil || !strings.Contains(r.Err.Error(), "redirect") {
		t.Errorf("expected redirect error, got %+v", r)
	}
}

func TestProbeFallsBackToGet(t *testing.T) {
	srv, rec := testServer(t)
	c := New(Options{})
	r := c.Probe(context.Background(), srv.URL+"/nohead.pdf", "")
	if r.Status != 200 || r.ContentType != "application/pdf" {
		t.Errorf("Probe = %+v", r)
	}
	if got := strings.Join(rec.methods["/nohead.pdf"], ","); got != "HEAD,GET" {
		t.Errorf("methods = %s", got)
	}
	r = c.Probe(context.Background(), srv.URL+"/gone", "")
	if r.Status != http.StatusGone {
		t.Errorf("gone status = %d", r.Status)
	}
}

func TestBodyLimitAndDownload(t *testing.T) {
	srv, _ := testServer(t)
	c := New(Options{})
	r := c.Get(context.Background(), srv.URL+"/page", "", true, 5)
	if string(r.Body) != "<titl" {
		t.Errorf("body limit not applied: %q", r.Body)
	}
	var buf bytes.Buffer
	r = c.Download(context.Background(), srv.URL+"/page", "", &buf, 0)
	if buf.String() != "<title>Page</title>" || r.Size != int64(buf.Len()) {
		t.Errorf("Download = %q size %d", buf.String(), r.Size)
	}
}

func TestGetSkipsNonHTMLBodies(t *testing.T) {
	srv, _ := testServer(t)
	r := New(Options{}).Get(context.Background(), srv.URL+"/big.bin", "", true, 0)
	if r.Status != 200 || len(r.Body) != 0 || r.ContentType != "application/octet-stream" {
		t.Errorf("binary body must not be read: status=%d len=%d ct=%s", r.Status, len(r.Body), r.ContentType)
	}
}

func TestTransportError(t *testing.T) {
	c := New(Options{Timeout: 2 * time.Second})
	r := c.Get(context.Background(), "http://127.0.0.1:1/", "", false, 0)
	if r.Err == nil || r.Status != 0 {
		t.Errorf("expected connection error, got %+v", r)
	}
	if strings.HasPrefix(r.Err.Error(), "Get ") {
		t.Errorf("url.Error prefix not stripped: %v", r.Err)
	}
}

func TestBasicAuthScopedToHost(t *testing.T) {
	var gotSite, gotOther string
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotOther = r.Header.Get("Authorization")
	}))
	defer other.Close()
	otherURL := strings.Replace(other.URL, "127.0.0.1", "localhost", 1)
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/away" {
			http.Redirect(w, r, otherURL+"/landing", http.StatusFound)
			return
		}
		user, pass, ok := r.BasicAuth()
		if !ok || user != "admin" || pass != "s3cret" {
			w.Header().Set("WWW-Authenticate", `Basic realm="test"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		gotSite = r.Header.Get("Authorization")
	}))
	defer site.Close()

	host := strings.TrimPrefix(site.URL, "http://")
	c := New(Options{Username: "admin", Password: "s3cret", AuthHost: host})
	if r := c.Get(context.Background(), site.URL+"/", "", false, 0); r.Status != 200 || gotSite == "" {
		t.Errorf("protected page: status %d, auth %q", r.Status, gotSite)
	}
	// Other hosts never see the credentials – neither directly …
	c.Get(context.Background(), otherURL+"/", site.URL+"/", false, 0)
	if gotOther != "" {
		t.Errorf("credentials leaked to other host: %q", gotOther)
	}
	// … nor after a redirect away from the protected site.
	if r := c.Get(context.Background(), site.URL+"/away", "", false, 0); r.Status != 200 || gotOther != "" {
		t.Errorf("credentials leaked through redirect: %q (status %d)", gotOther, r.Status)
	}
	// Without credentials the site answers 401.
	if r := New(Options{}).Get(context.Background(), site.URL+"/", "", false, 0); r.Status != 401 {
		t.Errorf("without credentials: %d", r.Status)
	}
}

func TestAuthHostMatching(t *testing.T) {
	o := Options{Username: "u", Password: "p", AuthHost: "www.example.com"}
	for host, want := range map[string]bool{
		"example.com": true, "www.example.com:8443": true, "EXAMPLE.COM": true,
		"cdn.example.com": false, "example.org": false,
	} {
		if got := o.authFor(host); got != want {
			t.Errorf("authFor(%q) = %v", host, got)
		}
	}
	if (Options{Username: "u", AuthHost: ""}).authFor("example.com") {
		t.Error("credentials without AuthHost must never be sent")
	}
}

func TestPasswordNotSerialised(t *testing.T) {
	b, _ := json.Marshal(Options{Username: "admin", Password: "s3cret", AuthHost: "x"})
	if strings.Contains(string(b), "s3cret") || !strings.Contains(string(b), `"username":"admin"`) {
		t.Errorf("json = %s", b)
	}
}

// A download may take longer than Options.Timeout as long as data keeps
// arriving; only a stalled connection is aborted.
func TestDownloadIdleTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fl := w.(http.Flusher)
		switch r.URL.Path {
		case "/slow": // 6 chunks, 40ms apart = 240ms > Timeout, but never idle for long
			for i := 0; i < 6; i++ {
				fmt.Fprint(w, "chunk")
				fl.Flush()
				time.Sleep(40 * time.Millisecond)
			}
		case "/stall": // starts, then hangs
			fmt.Fprint(w, "start")
			fl.Flush()
			select {
			case <-r.Context().Done():
			case <-time.After(5 * time.Second):
			}
		}
	}))
	defer srv.Close()
	c := New(Options{Timeout: 150 * time.Millisecond})

	var buf bytes.Buffer
	if r := c.Download(context.Background(), srv.URL+"/slow", "", &buf, 0); r.Err != nil || buf.String() != strings.Repeat("chunk", 6) {
		t.Errorf("slow but steady download failed: err=%v body=%q", r.Err, buf.String())
	}

	buf.Reset()
	start := time.Now()
	r := c.Download(context.Background(), srv.URL+"/stall", "", &buf, 0)
	if !errors.Is(r.Err, errStalled) {
		t.Errorf("stalled download: err = %v, want errStalled", r.Err)
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("stall detected only after %v", time.Since(start))
	}

	// Cancelling the caller's context is not a stall.
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	if r := c.Download(ctx, srv.URL+"/stall", "", io.Discard, 0); r.Err == nil || errors.Is(r.Err, errStalled) {
		t.Errorf("cancelled download: err = %v, want a cancellation error", r.Err)
	}
}

// Checks (Get/Probe) keep the overall timeout.
func TestGetKeepsOverallTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer srv.Close()
	start := time.Now()
	r := New(Options{Timeout: 100 * time.Millisecond}).Get(context.Background(), srv.URL, "", true, 0)
	if r.Err == nil || time.Since(start) > time.Second {
		t.Errorf("Get: err=%v after %v", r.Err, time.Since(start))
	}
}

func TestRedirectLoopAndKeptHeaders(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/a", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/b", http.StatusFound) })
	mux.HandleFunc("/b", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/a", http.StatusFound) })
	mux.HandleFunc("/h", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Robots-Tag", "noindex")
		w.Header().Set("X-Powered-By", "secret")
		w.Header().Set("Content-Type", "text/html")
	})
	srv := httptest.NewTLSServer(mux)
	defer srv.Close()
	c := New(Options{})
	c.hc.Transport = srv.Client().Transport // trust the test certificate
	r := c.Get(context.Background(), srv.URL+"/a", "", false, 0)
	if !errors.Is(r.Err, ErrRedirectLoop) {
		t.Errorf("loop: err = %v", r.Err)
	}
	r = c.Get(context.Background(), srv.URL+"/h", "", false, 0)
	if r.Header.Get("X-Robots-Tag") != "noindex" || r.Header.Get("X-Powered-By") != "" {
		t.Errorf("kept headers = %v", r.Header)
	}
	if r.CertNotAfter.IsZero() {
		t.Error("certificate expiry not recorded")
	}
}

// Last-Modified is parsed from the final response; a broken value is ignored.
func TestLastModified(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/old":
			http.Redirect(w, r, "/page", http.StatusMovedPermanently)
		case "/page":
			w.Header().Set("Last-Modified", "Wed, 21 Oct 2026 07:28:00 GMT")
		case "/broken":
			w.Header().Set("Last-Modified", "yesterday")
		}
	}))
	defer srv.Close()
	c := New(Options{})
	want := time.Date(2026, 10, 21, 7, 28, 0, 0, time.UTC)
	if r := c.Get(context.Background(), srv.URL+"/old", "", false, 0); !r.LastModified.Equal(want) {
		t.Errorf("LastModified = %v, want %v", r.LastModified, want)
	}
	if r := c.Probe(context.Background(), srv.URL+"/broken", ""); !r.LastModified.IsZero() {
		t.Errorf("broken LastModified = %v", r.LastModified)
	}
}

func TestDownloadIf(t *testing.T) {
	lm := time.Date(2026, 10, 21, 7, 28, 0, 0, time.UTC)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"v1"`)
		// http.ServeContent answers If-None-Match / If-Modified-Since itself.
		http.ServeContent(w, r, "a.txt", lm, strings.NewReader("hello"))
	}))
	defer srv.Close()
	c := New(Options{})
	var buf bytes.Buffer
	r := c.DownloadIf(context.Background(), srv.URL, "", &buf, 0, Validators{})
	if r.Status != 200 || r.ETag != `"v1"` || buf.String() != "hello" {
		t.Fatalf("plain: %d %q %q", r.Status, r.ETag, buf.String())
	}
	for _, v := range []Validators{{ETag: `"v1"`}, {LastModified: lm}} {
		buf.Reset()
		if r := c.DownloadIf(context.Background(), srv.URL, "", &buf, 0, v); r.Status != 304 || buf.Len() != 0 {
			t.Errorf("%+v: %d %q", v, r.Status, buf.String())
		}
	}
	if r := c.DownloadIf(context.Background(), srv.URL, "", &buf, 0, Validators{ETag: `"old"`}); r.Status != 200 {
		t.Errorf("changed ETag: %d", r.Status)
	}
}

func TestRatePerHost(t *testing.T) {
	var mu sync.Mutex
	var times []time.Time
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		times = append(times, time.Now())
		mu.Unlock()
	}))
	defer srv.Close()
	c := New(Options{RatePerHost: 20}) // 50 ms apart
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.Download(context.Background(), srv.URL, "", io.Discard, 0)
		}()
	}
	wg.Wait()
	if len(times) != 4 {
		t.Fatalf("%d requests", len(times))
	}
	if d := times[3].Sub(times[0]); d < 140*time.Millisecond {
		t.Errorf("4 requests at 20/s took %v", d)
	}
	// A cancelled wait returns at once.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if r := c.Get(ctx, srv.URL, "", false, 0); r.Err == nil {
		t.Error("cancelled request succeeded")
	}
}

func TestBandwidth(t *testing.T) {
	body := strings.Repeat("x", 20<<10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, body)
	}))
	defer srv.Close()
	c := New(Options{Bandwidth: 64 << 10}) // 20 KiB at 64 KiB/s ≈ 310 ms
	start := time.Now()
	var buf bytes.Buffer
	r := c.Download(context.Background(), srv.URL, "", &buf, 0)
	if r.Err != nil || buf.Len() != len(body) {
		t.Fatalf("download: %v, %d bytes", r.Err, buf.Len())
	}
	if d := time.Since(start); d < 250*time.Millisecond {
		t.Errorf("20 KiB at 64 KiB/s took only %v", d)
	}
}
