// Package fetch wraps net/http with browser-like defaults.
//
// By default every request looks like it was sent by a current Firefox on
// Linux: the same User-Agent, Accept headers and – most importantly – the same
// Referer behaviour (Firefox' default policy "strict-origin-when-cross-origin").
// Callers may override the User-Agent and pin the Referer to a fixed value.
package fetch

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"
)

// FirefoxUA is the default User-Agent (Firefox ESR/release on 64-bit Linux).
const FirefoxUA = "Mozilla/5.0 (X11; Linux x86_64; rv:144.0) Gecko/20100101 Firefox/144.0"

// Options configures a Client.
type Options struct {
	// UserAgent sent with every request. Empty means FirefoxUA.
	UserAgent string `json:"userAgent"`
	// Referer, when non-empty, is sent verbatim with every request (also after
	// redirects). When empty the referer is derived from the page the link was
	// found on, exactly like Firefox does it.
	Referer string `json:"referer"`
	// AcceptLanguage header; empty means "en-US,en;q=0.5".
	AcceptLanguage string `json:"acceptLanguage"`
	// Timeout per request including redirects and body. 0 means 20 seconds.
	// Downloads (Client.Download) use it as an idle timeout instead: they may
	// take as long as needed as long as data keeps arriving.
	Timeout time.Duration `json:"timeout"`
	// MaxRedirects before giving up. 0 means 10.
	MaxRedirects int `json:"maxRedirects"`

	// HTTP authentication (Basic) for password protected sites. The
	// credentials are sent only to AuthHost (a leading "www." and the scheme
	// are ignored, like everywhere else in LinkScout), never to other hosts.
	// The password is never serialised, so it cannot end up in /api/state or
	// in a JSON report.
	Username string `json:"username,omitempty"`
	Password string `json:"-"`
	AuthHost string `json:"-"`
}

// HasAuth reports whether credentials are configured.
func (o Options) HasAuth() bool { return o.Username != "" || o.Password != "" }

// authFor reports whether the credentials apply to the given host.
func (o Options) authFor(host string) bool {
	return o.HasAuth() && o.AuthHost != "" && bareHost(host) == bareHost(o.AuthHost)
}

// bareHost lower-cases a host, strips the port and a leading "www.", so
// credentials for example.com also apply to www.example.com:443.
func bareHost(h string) string {
	if i := strings.LastIndexByte(h, ':'); i >= 0 && !strings.Contains(h[i:], "]") {
		h = h[:i] // strip port
	}
	return strings.TrimPrefix(strings.ToLower(strings.Trim(h, "[]")), "www.")
}

// Client performs browser-like requests. It is safe for concurrent use.
type Client struct {
	opts Options
	hc   *http.Client // checks: Timeout covers the whole request
	dl   *http.Client // downloads: no overall timeout, see Download
}

// Response is the outcome of a single (possibly redirected) request.
type Response struct {
	FinalURL    string        // URL after following all redirects
	Redirects   []string      // every URL that answered with a redirect, in order (first = requested URL)
	Status      int           // final HTTP status code, 0 on transport errors
	ContentType string        // media type without parameters, lower case
	Body        []byte        // body (only when requested, truncated to the limit)
	Size        int64         // Content-Length or number of body bytes read, -1 if unknown
	Duration    time.Duration // wall clock time of the whole request
	Err         error         // transport error (DNS, TLS, timeout, too many redirects …)
}

// New creates a client with the given options.
func New(opts Options) *Client {
	if opts.UserAgent == "" {
		opts.UserAgent = FirefoxUA
	}
	if opts.AcceptLanguage == "" {
		opts.AcceptLanguage = "en-US,en;q=0.5"
	}
	if opts.Timeout == 0 {
		opts.Timeout = 20 * time.Second
	}
	if opts.MaxRedirects == 0 {
		opts.MaxRedirects = 10
	}
	jar, _ := cookiejar.New(nil) // a browser keeps cookies, so do we
	c := &Client{opts: opts}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.MaxIdleConnsPerHost = 16
	tr.ResponseHeaderTimeout = opts.Timeout // also bounds downloads until the first byte
	c.hc = &http.Client{
		Jar:       jar,
		Timeout:   opts.Timeout,
		Transport: tr,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= c.opts.MaxRedirects {
				return errTooManyRedirects
			}
			// net/http replaces the Referer with the previous hop. Browsers
			// keep the referer of the original navigation, so restore it and
			// re-apply the policy for the new target URL.
			orig := via[0]
			ref := orig.Header.Get("Referer")
			if c.opts.Referer == "" {
				if src := refererSource(orig); src != "" {
					ref = Referer(src, req.URL.String())
				}
			}
			if ref == "" {
				req.Header.Del("Referer")
			} else {
				req.Header.Set("Referer", ref)
			}
			// Credentials follow redirects only within the protected site.
			c.setAuth(req)
			return nil
		},
	}
	dl := *c.hc
	dl.Timeout = 0
	c.dl = &dl
	return c
}

// errTooManyRedirects is reported when a redirect chain exceeds MaxRedirects.
var errTooManyRedirects = errors.New("too many redirects")

// Options returns the effective options (defaults applied).
func (c *Client) Options() Options { return c.opts }

// sourceKey stores the page a request originates from inside the request
// context so the redirect hook can recompute the policy based referer.
type sourceKey struct{}

// refererSource returns the page a request originates from (see sourceKey).
func refererSource(r *http.Request) string {
	s, _ := r.Context().Value(sourceKey{}).(string)
	return s
}

// Get performs a GET request. If readBody is true and the response is an
// HTML document, at most maxBody bytes of the body are read into
// Response.Body (0 means 10 MiB). Bodies of other content types are never
// downloaded, so a link to a large binary does not stall the check.
func (c *Client) Get(ctx context.Context, target, source string, readBody bool, maxBody int64) Response {
	if !readBody {
		return c.do(ctx, http.MethodGet, target, source, nil)
	}
	if maxBody <= 0 {
		maxBody = 10 << 20
	}
	var buf []byte
	r := c.do(ctx, http.MethodGet, target, source, func(resp *http.Response) (int64, error) {
		if !isHTML(mediaType(resp.Header.Get("Content-Type"))) {
			return 0, nil
		}
		var err error
		buf, err = io.ReadAll(io.LimitReader(resp.Body, maxBody))
		if len(buf) > 0 {
			err = nil // a partially read page is still good enough for links and title
		}
		return int64(len(buf)), err
	})
	r.Body = buf
	return r
}

// isHTML reports whether a body of this media type may be read for links.
// An empty type is accepted, many servers omit it for HTML.
func isHTML(ct string) bool {
	return ct == "text/html" || ct == "application/xhtml+xml" || ct == ""
}

// Download performs a GET request and streams the body into w. At most
// maxBytes are copied (0 means unlimited). Unlike the other methods there is
// no limit for the whole transfer – a large video may take minutes – but the
// request is aborted when no data arrives for Options.Timeout.
func (c *Client) Download(parent context.Context, target, source string, w io.Writer, maxBytes int64) Response {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	idle := time.AfterFunc(c.opts.Timeout, cancel) // until the response headers arrive
	defer idle.Stop()
	r := c.doWith(c.dl, ctx, http.MethodGet, target, source, func(resp *http.Response) (int64, error) {
		var body io.Reader = &idleReader{r: resp.Body, timer: idle, d: c.opts.Timeout}
		if maxBytes > 0 {
			body = io.LimitReader(body, maxBytes)
		}
		return io.Copy(w, body)
	})
	if r.Err != nil && ctx.Err() != nil && parent.Err() == nil {
		r.Err = errStalled // the idle timer fired, the caller did not cancel
	}
	return r
}

// errStalled is reported when a download receives no data for Options.Timeout.
var errStalled = errors.New("download stalled (no data received within the timeout)")

// idleReader restarts timer after every successful read, so the timer only
// fires when the connection stalls.
type idleReader struct {
	r     io.Reader
	timer *time.Timer
	d     time.Duration
}

// Read implements io.Reader.
func (ir *idleReader) Read(p []byte) (int, error) {
	n, err := ir.r.Read(p)
	if n > 0 {
		ir.timer.Reset(ir.d)
	}
	return n, err
}

// Probe checks a URL without downloading its body: it sends a HEAD request and
// falls back to GET when the server does not handle HEAD properly (many
// servers answer HEAD with 403, 404, 405 or 501 although GET works).
func (c *Client) Probe(ctx context.Context, target, source string) Response {
	r := c.do(ctx, http.MethodHead, target, source, nil)
	if r.Err != nil || r.Status >= 400 {
		g := c.do(ctx, http.MethodGet, target, source, nil)
		if g.Err == nil || r.Err != nil {
			g.Duration += r.Duration
			return g
		}
	}
	return r
}

// do executes the request. consume, if not nil, receives the response to
// read its body and returns the number of bytes it read.
func (c *Client) do(ctx context.Context, method, target, source string, consume func(*http.Response) (int64, error)) Response {
	return c.doWith(c.hc, ctx, method, target, source, consume)
}

// doWith is do with an explicit http.Client (checks or downloads).
func (c *Client) doWith(hc *http.Client, ctx context.Context, method, target, source string, consume func(*http.Response) (int64, error)) Response {
	start := time.Now()
	res := Response{Size: -1}
	ctx = context.WithValue(ctx, sourceKey{}, source)
	req, err := http.NewRequestWithContext(ctx, method, target, nil)
	if err != nil {
		res.Err = err
		return res
	}
	c.setHeaders(req, source)

	resp, err := hc.Do(req)
	res.Duration = time.Since(start)
	if err != nil {
		res.Err = unwrapURLError(err)
		res.FinalURL = target
		return res
	}
	defer resp.Body.Close()

	res.Status = resp.StatusCode
	res.FinalURL = resp.Request.URL.String()
	res.Redirects = redirectChain(resp)
	res.ContentType = mediaType(resp.Header.Get("Content-Type"))
	res.Size = resp.ContentLength

	if consume != nil {
		n, err := consume(resp)
		if err != nil {
			res.Err = unwrapURLError(err)
		}
		if n > 0 && (res.Size < 0 || resp.Uncompressed) {
			res.Size = n
		}
	}
	res.Duration = time.Since(start)
	return res
}

// setHeaders adds the Firefox-like request headers.
func (c *Client) setHeaders(req *http.Request, source string) {
	h := req.Header
	h.Set("User-Agent", c.opts.UserAgent)
	h.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	h.Set("Accept-Language", c.opts.AcceptLanguage)
	h.Set("Upgrade-Insecure-Requests", "1")
	h.Set("Sec-Fetch-Dest", "document")
	h.Set("Sec-Fetch-Mode", "navigate")
	if source == "" {
		h.Set("Sec-Fetch-Site", "none")
	} else if sameOrigin(source, req.URL.String()) {
		h.Set("Sec-Fetch-Site", "same-origin")
	} else {
		h.Set("Sec-Fetch-Site", "cross-site")
	}
	ref := c.opts.Referer
	if ref == "" && source != "" {
		ref = Referer(source, req.URL.String())
	}
	if ref != "" {
		h.Set("Referer", ref)
	}
	c.setAuth(req)
}

// setAuth adds (or removes) the Authorization header depending on the target
// host. Credentials are sent preemptively, like a browser does after the
// first successful login, which saves one 401 round trip per URL.
func (c *Client) setAuth(req *http.Request) {
	if c.opts.authFor(req.URL.Host) {
		req.SetBasicAuth(c.opts.Username, c.opts.Password)
	} else {
		req.Header.Del("Authorization")
	}
}

// Referer computes the Referer header Firefox would send when navigating from
// page source to target under its default policy
// "strict-origin-when-cross-origin":
//
//   - same origin:               full URL of the source (without fragment)
//   - cross origin:              only the origin of the source plus "/"
//   - HTTPS → HTTP (downgrade):  no referer at all
func Referer(source, target string) string {
	s, err := url.Parse(source)
	if err != nil || (s.Scheme != "http" && s.Scheme != "https") {
		return ""
	}
	t, err := url.Parse(target)
	if err != nil {
		return ""
	}
	if s.Scheme == "https" && t.Scheme == "http" {
		return ""
	}
	if sameOrigin(source, target) {
		c := *s
		c.Fragment, c.RawFragment, c.User = "", "", nil
		return c.String()
	}
	return s.Scheme + "://" + s.Host + "/"
}

// sameOrigin reports whether two URLs share scheme and host (including the
// port), the browser definition of an origin.
func sameOrigin(a, b string) bool {
	ua, err1 := url.Parse(a)
	ub, err2 := url.Parse(b)
	if err1 != nil || err2 != nil {
		return false
	}
	return strings.EqualFold(ua.Scheme, ub.Scheme) && strings.EqualFold(ua.Host, ub.Host)
}

// redirectChain walks back through resp.Request.Response to collect all URLs
// that answered with a redirect.
func redirectChain(resp *http.Response) []string {
	var chain []string
	for r := resp.Request.Response; r != nil; r = r.Request.Response {
		chain = append([]string{r.Request.URL.String()}, chain...)
	}
	return chain
}

// mediaType returns the lower-case media type of a Content-Type header
// without parameters ("text/html; charset=utf-8" → "text/html").
func mediaType(ct string) string {
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	return strings.ToLower(strings.TrimSpace(ct))
}

// unwrapURLError strips the noisy `Get "https://…":` prefix of *url.Error.
func unwrapURLError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}
