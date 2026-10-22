// Command linkscout checks all links of a website and can create an offline
// copy of it.
//
// Without -report or -mirror it starts a local web UI (default
// http://127.0.0.1:8088) and opens it in the default browser. If a URL is
// given the check starts immediately.
//
// Headless use (for scripts / CI):
//
//	linkscout -report report.json https://example.com
//	linkscout -recheck report.json -report report-new.json   # only non-200 URLs
//	linkscout -mirror ./offline https://example.com
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/dlehmann/linkscout/internal/crawler"
	"github.com/dlehmann/linkscout/internal/desktop"
	"github.com/dlehmann/linkscout/internal/fetch"
	"github.com/dlehmann/linkscout/internal/i18n"
	"github.com/dlehmann/linkscout/internal/links"
	"github.com/dlehmann/linkscout/internal/mirror"
	"github.com/dlehmann/linkscout/internal/server"
)

// version is set at build time: -ldflags "-X main.version=1.2.3".
var version = "dev"

// defaultListen is the address of the web UI unless -listen is given.
const defaultListen = "127.0.0.1:8088"

// options holds the parsed command line flags.
type options struct {
	listen       string
	noBrowser    bool
	referer      string
	user         string
	password     string
	userAgent    string
	concurrency  int
	maxPages     int
	maxDepth     int
	delay        time.Duration
	timeout      time.Duration
	noExternal   bool
	noSitemap    bool
	report       string
	recheck      string
	recheckAll   bool
	failOnBroken bool
	compare      string
	mirrorDir    string
	mirrorExt    bool
	lang         string
	showVersion  bool
}

// main is a thin wrapper so that run can be tested without os.Exit.
func main() {
	os.Exit(run(os.Args[1:]))
}

// run parses args, picks the mode (web UI, headless report, offline copy)
// and returns the process exit code: 0 success, 1 error, 2 usage error,
// 3 broken links found with -fail-on-broken.
func run(args []string) int {
	lang := langFromArgs(args)
	o, fs := parseFlags(args, lang)
	if o == nil {
		return 2
	}
	if o.showVersion {
		fmt.Println("linkscout", version)
		return 0
	}
	if o.lang != "" {
		if l, ok := i18n.Parse(o.lang); ok {
			lang = l
		}
	}
	T := func(k string) string { return i18n.T(lang, k) }

	target := ""
	if fs.NArg() > 0 {
		target = fs.Arg(0)
		if !strings.Contains(target, "://") {
			target = "https://" + target // "example.com" is a common shorthand
		}
		if _, ok := links.Normalize(target, nil); !ok {
			fmt.Fprintf(os.Stderr, T("cli.invalid_url")+"\n", target)
			return 2
		}
	}

	// Credentials may also be given in the URL (https://user:pass@site/).
	if u, err := url.Parse(target); err == nil && u.User != nil {
		if o.user == "" {
			o.user = u.User.Username()
		}
		if p, ok := u.User.Password(); ok && o.password == "" {
			o.password = p
		}
		u.User = nil // keep the password out of the UI and the report
		target = u.String()
	}
	if o.password == "" {
		o.password = os.Getenv("LINKSCOUT_PASSWORD")
	}
	fopts := fetch.Options{Referer: o.referer, UserAgent: o.userAgent, Timeout: o.timeout,
		Username: o.user, Password: o.password}
	fopts.AcceptLanguage = i18n.AcceptLanguage(lang)
	cfg := crawler.Config{
		StartURL:      target,
		Fetch:         fopts,
		Concurrency:   o.concurrency,
		MaxPages:      o.maxPages,
		MaxDepth:      o.maxDepth,
		Delay:         o.delay,
		CheckExternal: !o.noExternal,
		Sitemap:       !o.noSitemap,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch {
	case o.mirrorDir != "":
		if target == "" {
			fmt.Fprintln(os.Stderr, T("cli.need_url"))
			return 2
		}
		return runMirror(ctx, lang, mirror.Config{
			StartURL: target, OutDir: o.mirrorDir, Fetch: fopts, External: o.mirrorExt,
			Concurrency: min(o.concurrency, 8), MaxPages: o.maxPages, Delay: o.delay,
		})
	case o.report != "" || o.recheck != "":
		return runHeadless(ctx, lang, o, cfg)
	default:
		return runUI(ctx, lang, o, cfg)
	}
}

// langFromArgs pre-scans the arguments for -lang so that even the -help
// output is shown in the requested language.
func langFromArgs(args []string) i18n.Lang {
	for i, a := range args {
		a = strings.TrimLeft(a, "-")
		if v, ok := strings.CutPrefix(a, "lang="); ok {
			if l, ok := i18n.Parse(v); ok {
				return l
			}
		}
		if a == "lang" && i+1 < len(args) {
			if l, ok := i18n.Parse(args[i+1]); ok {
				return l
			}
		}
	}
	return i18n.Detect()
}

// parseFlags defines all flags with translated help texts and parses args.
// It returns nil options on a parse error (the flag package already printed
// it) and exits for -help.
func parseFlags(args []string, lang i18n.Lang) (*options, *flag.FlagSet) {
	T := func(k string) string { return i18n.T(lang, k) }
	o := &options{}
	fs := flag.NewFlagSet("linkscout", flag.ContinueOnError)
	fs.StringVar(&o.listen, "listen", defaultListen, T("flag.listen"))
	fs.BoolVar(&o.noBrowser, "no-browser", false, T("flag.no_browser"))
	fs.StringVar(&o.referer, "referer", "", T("flag.referer"))
	fs.StringVar(&o.user, "user", "", T("flag.user"))
	fs.StringVar(&o.password, "password", "", T("flag.password"))
	fs.StringVar(&o.userAgent, "user-agent", "", T("flag.user_agent"))
	fs.IntVar(&o.concurrency, "concurrency", 8, T("flag.concurrency"))
	fs.IntVar(&o.maxPages, "max-pages", 0, T("flag.max_pages"))
	fs.IntVar(&o.maxDepth, "max-depth", 0, T("flag.max_depth"))
	fs.DurationVar(&o.delay, "delay", 0, T("flag.delay"))
	fs.DurationVar(&o.timeout, "timeout", 20*time.Second, T("flag.timeout"))
	fs.BoolVar(&o.noExternal, "no-external", false, T("flag.no_external"))
	fs.BoolVar(&o.noSitemap, "no-sitemap", false, T("flag.no_sitemap"))
	fs.StringVar(&o.report, "report", "", T("flag.report"))
	fs.StringVar(&o.recheck, "recheck", "", T("flag.recheck"))
	fs.BoolVar(&o.recheckAll, "recheck-all", false, T("flag.recheck_all"))
	fs.BoolVar(&o.failOnBroken, "fail-on-broken", false, T("flag.fail_on_broken"))
	fs.StringVar(&o.compare, "compare", "", T("flag.compare"))
	fs.StringVar(&o.mirrorDir, "mirror", "", T("flag.mirror"))
	fs.BoolVar(&o.mirrorExt, "mirror-external", false, T("flag.mirror_external"))
	fs.StringVar(&o.lang, "lang", "", fmt.Sprintf(T("flag.lang"), i18n.Codes()))
	fs.BoolVar(&o.showVersion, "version", false, T("flag.version"))
	fs.Usage = func() { printUsage(fs, lang) }
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(0)
		}
		return nil, fs
	}
	return o, fs
}

// printUsage is flag.PrintDefaults with a translated "default" label.
func printUsage(fs *flag.FlagSet, lang i18n.Lang) {
	w := fs.Output()
	fmt.Fprintln(w, i18n.T(lang, "cli.usage"))
	fmt.Fprintln(w)
	fs.VisitAll(func(f *flag.Flag) {
		name, usage := flag.UnquoteUsage(f)
		fmt.Fprintf(w, "  -%s", f.Name)
		if name != "" {
			fmt.Fprintf(w, " %s", name)
		}
		fmt.Fprintf(w, "\n    \t%s", usage)
		if f.DefValue != "" && f.DefValue != "false" && f.DefValue != "0" && f.DefValue != "0s" {
			fmt.Fprintf(w, " (%s: %s)", i18n.T(lang, "cli.default"), f.DefValue)
		}
		fmt.Fprintln(w)
	})
}

// runUI starts the web interface and blocks until Ctrl+C.
func runUI(ctx context.Context, lang i18n.Lang, o *options, cfg crawler.Config) int {
	c := crawler.New()
	if cfg.StartURL != "" {
		if err := c.Start(cfg); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	srv := server.New(server.Options{
		Lang:    lang,
		Version: version,
		Defaults: server.Defaults{
			URL: cfg.StartURL, Referer: o.referer, UserAgent: o.userAgent,
			Username: o.user, Password: o.password,
			Concurrency: o.concurrency, MaxPages: o.maxPages, DelayMs: int(o.delay.Milliseconds()),
			CheckExternal: !o.noExternal, MirrorBase: server.DefaultMirrorBase(),
		},
		AllowAnyHost: !isLoopbackListen(o.listen),
	}, c)

	ln, err := net.Listen("tcp", o.listen)
	if err != nil && o.listen == defaultListen {
		// Default port taken. If it is another LinkScout and no URL was given
		// (typical for the desktop launcher), just show that instance.
		if cfg.StartURL == "" && isLinkScout("http://"+defaultListen) {
			if !o.noBrowser {
				openBrowser("http://" + defaultListen + "/")
			}
			fmt.Printf(i18n.T(lang, "cli.already_running")+"\n", "http://"+defaultListen+"/")
			return 0
		}
		// Otherwise fall back to a free port.
		ln, err = net.Listen("tcp", "127.0.0.1:0")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	addr := ln.Addr().(*net.TCPAddr)
	host := "127.0.0.1"
	if !addr.IP.IsLoopback() && !addr.IP.IsUnspecified() {
		host = addr.IP.String()
	}
	uiURL := fmt.Sprintf("http://%s/", net.JoinHostPort(host, fmt.Sprint(addr.Port)))
	fmt.Printf(i18n.T(lang, "cli.listening")+"\n", uiURL)

	hs := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		c.Stop()
		srv.Close(3 * time.Second) // stop offline copies, remove their temp dirs
		sctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		hs.Shutdown(sctx)
	}()
	if !o.noBrowser {
		go openBrowser(uiURL)
	}
	if err := hs.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

// isLinkScout reports whether a LinkScout UI answers at base.
func isLinkScout(base string) bool {
	hc := http.Client{Timeout: 2 * time.Second}
	resp, err := hc.Get(base + "/api/state")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var st struct {
		Progress *json.RawMessage `json:"progress"`
	}
	return resp.StatusCode == 200 && json.NewDecoder(resp.Body).Decode(&st) == nil && st.Progress != nil
}

// isLoopbackListen reports whether the -listen address is only reachable
// from this machine. Otherwise the Host header check of the server has to be
// relaxed and machine-local features (opening folders) are disabled.
func isLoopbackListen(addr string) bool {
	h, _, err := net.SplitHostPort(addr)
	if err != nil {
		return true
	}
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// openBrowser opens url in the default browser; failures are not fatal,
// the address is printed anyway.
func openBrowser(url string) {
	if err := desktop.Open(url); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
}

// runHeadless checks the site (or re-checks a previous report) and writes a
// JSON report.
func runHeadless(ctx context.Context, lang i18n.Lang, o *options, cfg crawler.Config) int {
	T := func(k string) string { return i18n.T(lang, k) }
	c := crawler.New()
	var err error
	if o.recheck != "" {
		var rep crawler.Report
		rep, err = readReport(o.recheck)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if cfg.StartURL == "" {
			cfg.StartURL = rep.StartURL
		}
		if err = c.Load(cfg, rep.Results); err == nil {
			if o.recheckAll {
				err = c.Start(cfg)
			} else {
				err = c.RecheckFailed(cfg)
			}
		}
	} else {
		if cfg.StartURL == "" {
			fmt.Fprintln(os.Stderr, T("cli.need_url"))
			return 2
		}
		err = c.Start(cfg)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Fprintf(os.Stderr, T("cli.checking")+"\n", c.Config().StartURL)

	go func() { <-ctx.Done(); c.Stop() }()
	waitWithProgress(c, T("cli.progress"))

	rep := crawler.NewReport(c, "LinkScout "+version)
	s := rep.Stats
	fmt.Fprintf(os.Stderr, T("cli.summary")+"\n", s.Total, s.OK, s.Redirected, s.Broken, s.Errors)

	if o.compare != "" {
		old, err := readReport(o.compare)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		cmp := crawler.Compare(old, rep.Results)
		fmt.Fprintf(os.Stderr, T("cli.compare_summary")+"\n", o.compare,
			len(cmp.Added), len(cmp.Removed), len(cmp.Status), len(cmp.NewIssues))
	}

	out := o.report
	if out == "" {
		out = o.recheck // re-check without -report updates the file in place
	}
	if err := writeReport(out, rep, lang); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Fprintf(os.Stderr, T("cli.report_written")+"\n", out)
	if o.failOnBroken && (s.Broken > 0 || s.Errors > 0) {
		return 3
	}
	return 0
}

// waitWithProgress blocks until the crawl is finished and prints a progress
// line to stderr twice per second when stderr is a terminal.
func waitWithProgress(c *crawler.Crawler, format string) {
	done := make(chan struct{})
	go func() { c.Wait(); close(done) }()
	tty := isTerminal(os.Stderr)
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-done:
			if tty {
				p := c.Progress()
				fmt.Fprintf(os.Stderr, format+"\n", p.Done, p.Total)
			}
			return
		case <-tick.C:
			if tty {
				p := c.Progress()
				fmt.Fprintf(os.Stderr, format, p.Done, p.Total)
			}
		}
	}
}

// isTerminal reports whether f is a character device (an interactive
// terminal) rather than a file or pipe.
func isTerminal(f *os.File) bool {
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

// readReport loads a JSON report written by -report or the web UI export.
func readReport(path string) (crawler.Report, error) {
	var rep crawler.Report
	b, err := os.ReadFile(path)
	if err != nil {
		return rep, err
	}
	err = json.Unmarshal(b, &rep)
	return rep, err
}

// writeReport writes rep to path, or to stdout for "-". The format follows
// the file extension: .csv (table), .html / .pdf (readable report), otherwise JSON
// (the format -recheck and the web UI can read again).
func writeReport(path string, rep crawler.Report, lang i18n.Lang) error {
	var buf bytes.Buffer
	switch strings.ToLower(filepath.Ext(path)) {
	case ".csv":
		if err := crawler.WriteCSV(&buf, rep.Results); err != nil {
			return err
		}
	case ".html", ".htm":
		if err := server.WriteHTMLReport(&buf, rep, lang, false); err != nil {
			return err
		}
	case ".pdf":
		if err := server.WritePDFReport(&buf, rep, lang, false); err != nil {
			return err
		}
	default:
		b, err := json.MarshalIndent(rep, "", "  ")
		if err != nil {
			return err
		}
		buf.Write(append(b, '\n'))
	}
	if path == "-" {
		_, err := os.Stdout.Write(buf.Bytes())
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

// runMirror creates an offline copy without UI and prints a progress
// counter while it runs.
func runMirror(ctx context.Context, lang i18n.Lang, cfg mirror.Config) int {
	tty := isTerminal(os.Stderr)
	var mu sync.Mutex // the callback is called from the download goroutines
	last := time.Time{}
	p, err := mirror.Run(ctx, cfg, func(p mirror.Progress) {
		mu.Lock()
		defer mu.Unlock()
		if tty && time.Since(last) > 300*time.Millisecond {
			last = time.Now()
			fmt.Fprintf(os.Stderr, "\r%d/%d", p.Done, p.Total)
		}
	})
	if tty {
		fmt.Fprintln(os.Stderr)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Fprintf(os.Stderr, i18n.T(lang, "cli.mirror_done")+"\n", p.OutDir, p.Files)
	if len(p.Problems) > 0 {
		fmt.Fprintf(os.Stderr, i18n.T(lang, "cli.mirror_problems")+"\n", len(p.Problems))
		for _, pr := range p.Problems {
			what := pr.Error
			switch {
			case pr.TooLarge:
				what = i18n.T(lang, "cli.mirror_too_large")
			case what == "":
				what = fmt.Sprintf("HTTP %d", pr.Status)
			}
			if pr.Saved {
				what += " – " + i18n.T(lang, "cli.mirror_saved")
			}
			fmt.Fprintf(os.Stderr, "  %s  %s\n", pr.URL, what)
		}
	}
	return 0
}
