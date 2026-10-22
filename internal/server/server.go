// Package server provides the local web UI and its JSON API.
//
// The UI is a single page (web/index.html + app.js + style.css) embedded into
// the binary. The initial state – translations, defaults and current results –
// is injected into index.html so the page renders completely without waiting
// for a first API round trip. While a check runs the page polls /api/state.
//
// API overview (all JSON):
//
//	GET  /api/state         progress, results, statistics, mirror progress
//	POST /api/check         start a check {mode:"all"|"failed", url, referer, …}
//	POST /api/stop          stop the running check
//	GET  /api/export        download the report (?format=json|csv|html|pdf, default json;
//	                        q, mode, regex=1, case=1 add a content search)
//	POST /api/export/list   download the results list as filtered and sorted in the UI
//	                        {format: csv|json|pdf|pdf-summary, urls, filters, sort, search}
//	POST /api/search        search the content of the checked pages {q, mode, regex, caseSensitive}
//	POST /api/compare       compare the results with an earlier JSON report (body)
//	POST /api/import        load a JSON report (to re-check it later)
//	POST /api/mirror        add an offline copy to the list and start it {url, outDir, external, …}
//	POST /api/mirror/queue  add an offline copy to the list without starting it
//	POST /api/mirror/start  start one list entry {id} or all pending ones {}
//	POST /api/mirror/stop   stop one list entry {id} or all {}
//	POST /api/mirror/remove remove one list entry {id} or all finished ones {done:true}
//	POST /api/mirror/open   open the folder of a finished entry {id} in the file manager
//	GET  /api/mirror/zip    download an offline copy as ZIP (?id=, default: last finished)
//
// State changing requests must carry the header "X-LinkScout: 1". Browsers
// do not send custom headers cross-origin without a CORS preflight (which this
// server never approves), so other websites cannot drive the local instance.
package server

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/dlehmann/linkscout/internal/crawler"
	"github.com/dlehmann/linkscout/internal/desktop"
	"github.com/dlehmann/linkscout/internal/fetch"
	"github.com/dlehmann/linkscout/internal/i18n"
	"github.com/dlehmann/linkscout/internal/links"
	"github.com/dlehmann/linkscout/internal/mirror"
)

//go:embed web
var webFS embed.FS

// Defaults pre-fill the forms of the UI.
type Defaults struct {
	URL           string `json:"url"`
	Referer       string `json:"referer"`
	UserAgent     string `json:"userAgent"`
	Username      string `json:"username"`
	Password      string `json:"-"` // never sent to the page
	Concurrency   int    `json:"concurrency"`
	MaxPages      int    `json:"maxPages"`
	DelayMs       int    `json:"delayMs"`
	CheckExternal bool   `json:"checkExternal"`
	MirrorBase    string `json:"mirrorBase"` // parent directory for offline copies
	MirrorDir     string `json:"mirrorDir"`  // target directory template, see ExpandMirrorDir
	// -rate, -bandwidth, -max-file-size: RatePerHost applies to every check
	// and copy started from the UI, the others prefill the offline copy form.
	RatePerHost    float64 `json:"ratePerHost"`
	BandwidthKiB   int     `json:"bandwidthKiB"`
	MaxFileSizeMiB int     `json:"maxFileSizeMiB"`
}

// Options configures the server.
type Options struct {
	Lang     i18n.Lang
	Version  string
	Defaults Defaults
	// AllowAnyHost disables the Host header check (needed when the server
	// listens on a non-loopback address on purpose).
	AllowAnyHost bool
	// OpenDir shows a directory in the desktop's file manager; nil means
	// desktop.Open (xdg-open, or open on macOS).
	OpenDir func(dir string) error
}

// Server is the HTTP handler of the UI.
type Server struct {
	opts    Options
	crawler *crawler.Crawler
	mirrors *mirrorJobs
}

// New creates a server around an existing crawler (which may already be
// running, e.g. when a URL was passed on the command line).
func New(opts Options, c *crawler.Crawler) *Server {
	if opts.Defaults.UserAgent == "" {
		opts.Defaults.UserAgent = fetch.FirefoxUA
	}
	if opts.Defaults.Concurrency == 0 {
		opts.Defaults.Concurrency = 8
	}
	if opts.OpenDir == nil {
		opts.OpenDir = desktop.Open
	}
	if opts.Defaults.MirrorDir == "" {
		opts.Defaults.MirrorDir = DefaultMirrorTemplate(opts.Defaults.MirrorBase)
	}
	return &Server{opts: opts, crawler: c, mirrors: &mirrorJobs{}}
}

// Close stops running offline copies (so their temporary directories are
// removed) and waits up to timeout for them. The crawler is stopped by its
// owner.
func (s *Server) Close(timeout time.Duration) {
	s.mirrors.shutdown(timeout)
}

// Handler returns the root HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	static, _ := fs.Sub(webFS, "web")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(static))))
	mux.HandleFunc("GET /{$}", s.handleIndex)
	mux.HandleFunc("GET /api/state", s.handleState)
	mux.HandleFunc("POST /api/check", s.handleCheck)
	mux.HandleFunc("POST /api/stop", s.handleStop)
	mux.HandleFunc("GET /api/export", s.handleExport)
	mux.HandleFunc("GET /api/sitemap", s.handleSitemap)
	mux.HandleFunc("POST /api/search", s.handleSearch)
	mux.HandleFunc("POST /api/export/list", s.handleListExport)
	mux.HandleFunc("POST /api/import", s.handleImport)
	mux.HandleFunc("POST /api/compare", s.handleCompare)
	mux.HandleFunc("POST /api/mirror", s.handleMirror)
	mux.HandleFunc("POST /api/mirror/queue", s.handleMirrorQueue)
	mux.HandleFunc("POST /api/mirror/start", s.handleMirrorStart)
	mux.HandleFunc("POST /api/mirror/stop", s.handleMirrorStop)
	mux.HandleFunc("POST /api/mirror/remove", s.handleMirrorRemove)
	mux.HandleFunc("POST /api/mirror/open", s.handleMirrorOpen)
	mux.HandleFunc("GET /api/mirror/zip", s.handleMirrorZip)
	return s.guard(mux)
}

// guard rejects requests with foreign Host headers (DNS rebinding) and state
// changing requests without the custom header (CSRF).
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.opts.AllowAnyHost && !isLoopbackHost(r.Host) {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Header.Get("X-LinkScout") != "1" {
			http.Error(w, "missing X-LinkScout header", http.StatusForbidden)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

// isLoopbackHost reports whether a Host header names this machine
// (localhost, *.localhost or a loopback IP), with or without port.
func isLoopbackHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// State is the payload of GET /api/state and part of the initial page state.
type State struct {
	Progress crawler.Progress     `json:"progress"`
	Results  []crawler.Result     `json:"results"`
	Stats    crawler.Stats        `json:"stats"`
	Issues   []crawler.IssueCount `json:"issues"` // SEO issues of the results
	// ThirdParty are the external services of the analysed pages (nil
	// without any), Searchable the pages the content search can read.
	ThirdParty *crawler.ThirdPartyOverview `json:"thirdParty,omitempty"`
	Searchable int                         `json:"searchable"`
	Mirrors    []JobView                   `json:"mirrors"` // offline copy list
	Config     crawler.Config              `json:"config"`
}

// state collects the complete UI state.
func (s *Server) state() State {
	rs, issues := s.crawler.Analyzed()
	cfg := s.crawler.Config()
	return State{
		Issues:     issues,
		ThirdParty: crawler.NewThirdParty(rs),
		Searchable: s.crawler.Searchable(),
		Progress:   s.crawler.Progress(),
		Results:    rs,
		Stats:      crawler.Summarize(rs),
		Mirrors:    s.mirrors.views(),
		Config:     cfg,
	}
}

// handleIndex serves the page with the initial state injected, so the UI
// renders without waiting for a first API round trip.
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	tpl, err := webFS.ReadFile("web/index.html")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	initial := map[string]any{
		"lang":      s.opts.Lang,
		"messages":  i18n.Messages(s.opts.Lang),
		"version":   s.opts.Version,
		"defaults":  s.opts.Defaults,
		"firefoxUA": fetch.FirefoxUA,
		"state":     s.state(),
	}
	js, err := json.Marshal(initial) // escapes <, > and & – safe inside <script>
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	page := bytes.Replace(tpl, []byte("/*__INITIAL_STATE__*/null"), js, 1)
	page = bytes.Replace(page, []byte(`lang="en"`), []byte(`lang="`+string(s.opts.Lang)+`"`), 1)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(page)
}

// handleState serves GET /api/state, polled by the page while a job runs.
func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.state())
}

// CheckRequest is the body of POST /api/check.
type CheckRequest struct {
	Mode          string `json:"mode"` // "all" (default) or "failed"
	URL           string `json:"url"`
	Referer       string `json:"referer"`
	UserAgent     string `json:"userAgent"`
	Username      string `json:"username"` // HTTP authentication of the checked site
	Password      string `json:"password"`
	Concurrency   int    `json:"concurrency"`
	MaxPages      int    `json:"maxPages"`
	DelayMs       int    `json:"delayMs"`
	CheckExternal *bool  `json:"checkExternal"`
	Sitemap       *bool  `json:"sitemap"` // default true
}

// handleCheck starts a full check or re-checks the failed URLs.
func (s *Server) handleCheck(w http.ResponseWriter, r *http.Request) {
	var req CheckRequest
	if err := decode(r, &req); err != nil {
		s.fail(w, http.StatusBadRequest, err)
		return
	}
	cfg := crawler.Config{
		StartURL:      req.URL,
		Concurrency:   clamp(orDefault(req.Concurrency, s.opts.Defaults.Concurrency), 1, 64),
		MaxPages:      max(req.MaxPages, 0),
		Delay:         time.Duration(max(req.DelayMs, 0)) * time.Millisecond,
		CheckExternal: req.CheckExternal == nil || *req.CheckExternal,
		Sitemap:       req.Sitemap == nil || *req.Sitemap,
		Fetch:         s.fetchOptions(req.Referer, req.UserAgent, req.Username, req.Password),
	}
	var err error
	if req.Mode == "failed" {
		err = s.crawler.RecheckFailed(cfg)
	} else {
		if _, ok := links.Normalize(req.URL, nil); !ok {
			s.fail(w, http.StatusBadRequest, errors.New(i18n.T(s.opts.Lang, "error.no_url")))
			return
		}
		err = s.crawler.Start(cfg)
	}
	if errors.Is(err, crawler.ErrBusy) {
		s.fail(w, http.StatusConflict, errors.New(i18n.T(s.opts.Lang, "error.busy")))
		return
	}
	if err != nil {
		s.fail(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusAccepted, s.state())
}

// fetchOptions builds the HTTP options of a check or offline copy from the
// form values, filling in what the page cannot know (CLI referer, stored
// password).
func (s *Server) fetchOptions(referer, ua, user, pass string) fetch.Options {
	// The UI has no referer field; a referer given on the command line
	// (-referer) applies to every check started from the UI.
	if strings.TrimSpace(referer) == "" {
		referer = s.opts.Defaults.Referer
	}
	// The password is never sent back to the page, so after a reload the
	// field is empty. Keep the password that is already known for the same
	// user name (from the last check or from the command line).
	user = strings.TrimSpace(user)
	if pass == "" && user != "" {
		if last := s.crawler.Config().Fetch; last.Username == user {
			pass = last.Password
		} else if s.opts.Defaults.Username == user {
			pass = s.opts.Defaults.Password
		} else if p := s.mirrors.password(user); p != "" {
			pass = p
		}
	}
	return fetch.Options{
		Referer:        strings.TrimSpace(referer),
		UserAgent:      strings.TrimSpace(ua),
		Username:       user,
		Password:       pass,
		AcceptLanguage: i18n.AcceptLanguage(s.opts.Lang),
		RatePerHost:    s.opts.Defaults.RatePerHost,
	}
}

// handleStop stops the running check.
func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
	s.crawler.Stop()
	writeJSON(w, http.StatusOK, s.state())
}

// handleExport downloads the current results as JSON report.
func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	rep := crawler.NewReport(s.crawler, "LinkScout "+s.opts.Version)
	format := r.URL.Query().Get("format")
	// external=1: count links to other websites in the figures, like the
	// overview page does when its checkbox is set
	external := r.URL.Query().Get("external") == "1"
	if external {
		ov := crawler.NewOverview(rep.Results, true)
		rep.Overview = &ov
	}
	if format != "csv" && format != "html" && format != "pdf" {
		format = "json"
	}
	// q=…: the report includes the content search the list shows
	if q := r.URL.Query(); q.Get("q") != "" {
		res, err := s.crawler.Search(crawler.SearchQuery{Query: q.Get("q"), Mode: q.Get("mode"),
			Regex: q.Get("regex") == "1", CaseSensitive: q.Get("case") == "1"})
		if err != nil {
			s.fail(w, http.StatusBadRequest, s.searchError(err))
			return
		}
		rep.Search = &res
	}
	name := "linkscout-report." + format
	if u, err := url.Parse(rep.StartURL); err == nil && u.Host != "" {
		name = fmt.Sprintf("linkscout-%s-%s.%s", sanitizeName(u.Host), time.Now().Format("20060102-150405"), format)
	}
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	switch format {
	case "csv":
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		crawler.WriteCSV(w, rep.Results, rep.Search)
	case "html":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		WriteHTMLReport(w, rep, s.opts.Lang, external)
	case "pdf":
		w.Header().Set("Content-Type", "application/pdf")
		WritePDFReport(w, rep, s.opts.Lang, external)
	default:
		rep.Content = s.crawler.Content() // searchable again after an import
		writeJSON(w, http.StatusOK, rep)
	}
}

// ListExportRequest is the body of POST /api/export/list: the URLs of the
// results list in the order shown, how the list was filtered and sorted
// (as text for the report header) and the content search it shows.
type ListExportRequest struct {
	Format  string               `json:"format"` // csv, json, pdf (detailed) or pdf-summary
	URLs    []string             `json:"urls"`
	Filters string               `json:"filters"`
	Sort    string               `json:"sort"`
	Search  *crawler.SearchQuery `json:"search"`
}

// handleListExport downloads the results list of the UI. CSV and JSON
// carry everything (the JSON is a report that can be imported again), the
// detailed PDF is the full report restricted to the list, the summary PDF
// one entry per URL like the list itself.
func (s *Server) handleListExport(w http.ResponseWriter, r *http.Request) {
	var req ListExportRequest
	if err := decode(r, &req); err != nil {
		s.fail(w, http.StatusBadRequest, err)
		return
	}
	rep := crawler.NewReport(s.crawler, "LinkScout "+s.opts.Version)
	if req.Search != nil && req.Search.Query != "" {
		res, err := s.crawler.Search(*req.Search)
		if err != nil {
			s.fail(w, http.StatusBadRequest, s.searchError(err))
			return
		}
		rep.Search = &res
	}
	if req.Format == "json" {
		rep.Content = s.crawler.Content()
	}
	rep = rep.Select(req.URLs, crawler.ListInfo{Filters: req.Filters, Sort: req.Sort})

	ext, suffix := req.Format, "list"
	switch req.Format {
	case "csv", "json", "pdf":
	case "pdf-summary":
		ext, suffix = "pdf", "list-summary"
	default:
		s.fail(w, http.StatusBadRequest, fmt.Errorf("unknown format %q", req.Format))
		return
	}
	name := "linkscout-" + suffix + "." + ext
	if u, err := url.Parse(rep.StartURL); err == nil && u.Host != "" {
		name = fmt.Sprintf("linkscout-%s-%s-%s.%s", sanitizeName(u.Host), suffix, time.Now().Format("20060102-150405"), ext)
	}
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	switch req.Format {
	case "csv":
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		crawler.WriteCSV(w, rep.Results, rep.Search)
	case "pdf":
		w.Header().Set("Content-Type", "application/pdf")
		WritePDFReport(w, rep, s.opts.Lang, true)
	case "pdf-summary":
		w.Header().Set("Content-Type", "application/pdf")
		WritePDFList(w, rep, s.opts.Lang)
	default:
		writeJSON(w, http.StatusOK, rep)
	}
}

// handleSearch searches the content of the checked pages (POST /api/search,
// body crawler.SearchQuery) and answers with a crawler.SearchResult.
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	var q crawler.SearchQuery
	if err := decode(r, &q); err != nil {
		s.fail(w, http.StatusBadRequest, err)
		return
	}
	res, err := s.crawler.Search(q)
	if err != nil {
		s.fail(w, http.StatusBadRequest, s.searchError(err))
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// searchError translates the errors of crawler.Search.
func (s *Server) searchError(err error) error {
	if errors.Is(err, crawler.ErrEmptyQuery) {
		return errors.New(i18n.T(s.opts.Lang, "search.empty"))
	}
	return errors.New(strings.ReplaceAll(i18n.T(s.opts.Lang, "search.invalid"), "%error%", err.Error()))
}

// handleCompare compares the current results with an earlier report sent
// as body and answers with a crawler.Comparison.
func (s *Server) handleCompare(w http.ResponseWriter, r *http.Request) {
	var old crawler.Report
	if err := decode(r, &old); err != nil {
		s.fail(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, crawler.Compare(old, crawler.NewReport(s.crawler, "")))
}

// handleImport loads a JSON report so it can be viewed and re-checked.
func (s *Server) handleImport(w http.ResponseWriter, r *http.Request) {
	var rep crawler.Report
	if err := decode(r, &rep); err != nil {
		s.fail(w, http.StatusBadRequest, err)
		return
	}
	cfg := crawler.Config{StartURL: rep.StartURL, Fetch: fetch.Options{Referer: rep.Referer, UserAgent: rep.UserAgent}, CheckExternal: true}
	if err := s.crawler.LoadReport(cfg, rep); err != nil {
		s.fail(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, s.state())
}

// MirrorRequest is the body of POST /api/mirror.
type MirrorRequest struct {
	URL         string `json:"url"`
	OutDir      string `json:"outDir"`
	External    *bool  `json:"external"` // default true: CDN images, CSS, scripts are part of the site
	Referer     string `json:"referer"`
	UserAgent   string `json:"userAgent"`
	Username    string `json:"username"`
	Password    string `json:"password"`
	Concurrency int    `json:"concurrency"`
	MaxPages    int    `json:"maxPages"`
	DelayMs     int    `json:"delayMs"`
	// What belongs to the copy (nil = default true; Robots is off by
	// default) and how fast it is loaded.
	ExternalFiles  *bool   `json:"externalFiles"`
	Robots         bool    `json:"robots"`
	Sitemap        *bool   `json:"sitemap"`
	Rules          string  `json:"rules"` // "+pattern -pattern …", see mirror.Rules
	Fresh          bool    `json:"fresh"` // download everything again instead of updating
	RatePerHost    float64 `json:"ratePerHost"`
	BandwidthKiB   int     `json:"bandwidthKiB"`
	MaxFileSizeMiB int     `json:"maxFileSizeMiB"`
}

// orTrue returns *b, or true when the field was left out.
func orTrue(b *bool) bool { return b == nil || *b }

// handleMirror adds an offline copy to the list and starts it.
func (s *Server) handleMirror(w http.ResponseWriter, r *http.Request) {
	s.addMirror(w, r, true)
}

// handleMirrorQueue adds an offline copy to the list without starting it.
func (s *Server) handleMirrorQueue(w http.ResponseWriter, r *http.Request) {
	s.addMirror(w, r, false)
}

// addMirror adds a list entry with the settings of the request and starts it
// if requested. The settings are copied, so the form can be changed for the
// next website afterwards.
func (s *Server) addMirror(w http.ResponseWriter, r *http.Request, start bool) {
	var req MirrorRequest
	if err := decode(r, &req); err != nil {
		s.fail(w, http.StatusBadRequest, err)
		return
	}
	norm, ok := links.Normalize(links.ToASCII(req.URL), nil) // müller.de → xn--mller-kva.de, also for <host>
	if !ok {
		s.fail(w, http.StatusBadRequest, errors.New(i18n.T(s.opts.Lang, "error.no_url")))
		return
	}
	rules, err := mirror.ParseRules([]string{req.Rules})
	if err != nil {
		var re *mirror.RuleError
		if errors.As(err, &re) {
			err = fmt.Errorf(i18n.T(s.opts.Lang, "error.invalid_rule"), re.Rule)
		}
		s.fail(w, http.StatusBadRequest, err)
		return
	}
	tpl := strings.TrimSpace(req.OutDir)
	if tpl == "" {
		tpl = s.opts.Defaults.MirrorDir
	}
	out := expandHome(ExpandMirrorDir(tpl, norm, time.Now()))
	// A template may produce the same directory for several entries (two
	// URLs on one host); those get a suffix. A literal path is the user's
	// explicit choice and is rejected when it is already in use.
	auto := out != expandHome(tpl)
	cfg := mirror.Config{
		StartURL:    norm,
		OutDir:      out,
		External:    req.External == nil || *req.External,
		Concurrency: clamp(orDefault(req.Concurrency, 4), 1, 32),
		MaxPages:    max(req.MaxPages, 0),
		Delay:       time.Duration(max(req.DelayMs, 0)) * time.Millisecond,
		Fetch:       s.fetchOptions(req.Referer, req.UserAgent, req.Username, req.Password),

		ExternalFiles: orTrue(req.ExternalFiles),
		Robots:        req.Robots,
		Sitemap:       orTrue(req.Sitemap),
		Rules:         rules.Strings(),
		Fresh:         req.Fresh,
		MaxFileSize:   int64(max(req.MaxFileSizeMiB, 0)) << 20,
	}
	cfg.Fetch.RatePerHost = max(req.RatePerHost, 0)
	cfg.Fetch.Bandwidth = int64(max(req.BandwidthKiB, 0)) << 10
	job, err := s.mirrors.add(cfg, auto)
	if errors.Is(err, errDirInUse) {
		s.fail(w, http.StatusConflict, errors.New(i18n.T(s.opts.Lang, "error.dir_in_use")))
		return
	}
	if err == nil && start {
		err = s.mirrors.start(job.id)
	}
	if err != nil {
		s.fail(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusAccepted, s.state())
}

// JobRequest is the body of /api/mirror/start, /stop and /remove. ID 0 means
// "all entries"; Done (remove only) removes every finished entry.
type JobRequest struct {
	ID   int  `json:"id"`
	Done bool `json:"done"`
}

// jobRequest decodes the optional JobRequest body, runs apply and answers
// with the new state (or the error).
func (s *Server) jobRequest(w http.ResponseWriter, r *http.Request, apply func(JobRequest) error) {
	var req JobRequest
	if r.ContentLength != 0 {
		if err := decode(r, &req); err != nil && !errors.Is(err, io.EOF) {
			s.fail(w, http.StatusBadRequest, err)
			return
		}
	}
	if err := apply(req); err != nil {
		code := http.StatusBadRequest
		if errors.Is(err, errJobNotFound) {
			code = http.StatusNotFound
		}
		s.fail(w, code, err)
		return
	}
	writeJSON(w, http.StatusOK, s.state())
}

// handleMirrorStart starts one list entry or all pending ones.
func (s *Server) handleMirrorStart(w http.ResponseWriter, r *http.Request) {
	s.jobRequest(w, r, func(req JobRequest) error { return s.mirrors.start(req.ID) })
}

// handleMirrorStop stops one list entry or all running ones.
func (s *Server) handleMirrorStop(w http.ResponseWriter, r *http.Request) {
	s.jobRequest(w, r, func(req JobRequest) error { return s.mirrors.stop(req.ID) })
}

// handleMirrorRemove removes one list entry or all finished ones.
func (s *Server) handleMirrorRemove(w http.ResponseWriter, r *http.Request) {
	s.jobRequest(w, r, func(req JobRequest) error {
		if req.ID == 0 && !req.Done {
			return errors.New("id or done required")
		}
		return s.mirrors.remove(req.ID, req.Done)
	})
}

// handleMirrorOpen opens the target directory of a finished list entry in
// the file manager of the machine LinkScout runs on. Only directories of the
// list can be opened, never arbitrary paths.
func (s *Server) handleMirrorOpen(w http.ResponseWriter, r *http.Request) {
	if s.opts.AllowAnyHost {
		// The UI may be used from another machine: a window would pop up on
		// the server's desktop instead.
		s.fail(w, http.StatusForbidden, errors.New(i18n.T(s.opts.Lang, "error.open_local_only")))
		return
	}
	var req JobRequest
	if err := decode(r, &req); err != nil {
		s.fail(w, http.StatusBadRequest, err)
		return
	}
	dir, ok := s.mirrors.finishedDir(req.ID)
	if req.ID == 0 || !ok {
		http.Error(w, "no finished offline copy", http.StatusNotFound)
		return
	}
	if err := s.opts.OpenDir(dir); err != nil {
		s.fail(w, http.StatusInternalServerError, fmt.Errorf("%s: %w", i18n.T(s.opts.Lang, "error.open_failed"), err))
		return
	}
	writeJSON(w, http.StatusOK, s.state())
}

// handleMirrorZip streams a finished offline copy as ZIP archive.
func (s *Server) handleMirrorZip(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(r.URL.Query().Get("id"))
	abs, ok := s.mirrors.finishedDir(id)
	if !ok {
		http.Error(w, "no finished offline copy", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+sanitizeName(filepath.Base(abs))+`.zip"`)
	if err := mirror.ZipDir(abs, w); err != nil {
		// Headers are already sent; the truncated archive signals the error.
		return
	}
}

// DefaultMirrorTemplate returns the default target directory template
// <base>/linkscout/<host>.
func DefaultMirrorTemplate(base string) string {
	if base == "" {
		base = DefaultMirrorBase()
	}
	return filepath.Join(base, "linkscout", "<host>")
}

// ExpandMirrorDir replaces the placeholders of a target directory template:
//
//	<host>    host name including the port, e.g. www.example.com or localhost_8080
//	<domain>  host name without "www." and port, e.g. example.com
//	<date>    current date, e.g. 2026-10-21
//	<time>    current time, e.g. 153012
//
// Host values are sanitised, so they cannot add path separators.
func ExpandMirrorDir(tpl, startURL string, now time.Time) string {
	host, domain := "site", "site"
	if u, err := url.Parse(startURL); err == nil && u.Host != "" {
		u.Host = links.ASCIIHost(u.Host) // müller.de → xn--mller-kva.de
		host = sanitizeName(u.Host)
		domain = sanitizeName(strings.TrimPrefix(strings.ToLower(u.Hostname()), "www."))
	}
	return strings.NewReplacer(
		"<host>", host,
		"<domain>", domain,
		"<date>", now.Format("2006-01-02"),
		"<time>", now.Format("150405"),
	).Replace(tpl)
}

// DefaultMirrorDir returns <base>/linkscout/<host> for a start URL. If base is empty the
// XDG download directory (or ~/Downloads, or the working directory) is used.
func DefaultMirrorDir(base, startURL string) string {
	return ExpandMirrorDir(DefaultMirrorTemplate(base), startURL, time.Now())
}

// DefaultMirrorBase picks the parent directory for offline copies.
func DefaultMirrorBase() string {
	if d := os.Getenv("XDG_DOWNLOAD_DIR"); d != "" {
		return expandHome(d)
	}
	if home, err := os.UserHomeDir(); err == nil {
		dl := filepath.Join(home, "Downloads")
		if st, err := os.Stat(dl); err == nil && st.IsDir() {
			return dl
		}
		return home
	}
	wd, _ := os.Getwd()
	return wd
}

// expandHome replaces a leading "~" and "$HOME" by the home directory.
func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[1:])
		}
	}
	return strings.ReplaceAll(p, "$HOME", os.Getenv("HOME"))
}

// sanitizeName keeps letters, digits, ".", "-" and "_" and replaces
// everything else by "_" (for file names derived from host names).
func sanitizeName(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '.' || r == '-' || r == '_' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			return r
		}
		return '_'
	}, s)
}

// fail answers with a JSON error object {"error": "..."}.
func (s *Server) fail(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

// decode parses a JSON request body (at most 256 MiB, reports can be big).
func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 256<<20))
	return dec.Decode(v)
}

// writeJSON answers with v as indented JSON and disables caching.
func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(v)
}

// orDefault returns def when v is zero.
func orDefault(v, def int) int {
	if v == 0 {
		return def
	}
	return v
}

// clamp limits v to the range [lo, hi].
func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
