package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dlehmann/linkscout/internal/crawler"
	"github.com/dlehmann/linkscout/internal/i18n"
)

func TestLangFromArgs(t *testing.T) {
	t.Setenv("LANGUAGE", "")
	t.Setenv("LC_ALL", "")
	t.Setenv("LC_MESSAGES", "")
	t.Setenv("LANG", "de_DE.UTF-8")
	if l := langFromArgs(nil); l != i18n.DE {
		t.Errorf("env: %s", l)
	}
	if l := langFromArgs([]string{"-lang=en"}); l != i18n.EN {
		t.Errorf("-lang=en: %s", l)
	}
	if l := langFromArgs([]string{"--lang", "en", "https://x"}); l != i18n.EN {
		t.Errorf("--lang en: %s", l)
	}
}

func TestHeadlessReportAndRecheck(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<title>Home</title><a href="/gone">x</a>`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	out := filepath.Join(t.TempDir(), "report.json")
	if code := run([]string{"-report", out, "-fail-on-broken", srv.URL}); code != 3 {
		t.Errorf("exit code with broken link = %d, want 3", code)
	}
	var rep crawler.Report
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Stats.Total != 2 || rep.Stats.Broken != 1 {
		t.Errorf("report stats = %+v", rep.Stats)
	}

	// Fix the site and re-check only the failed URL, updating the file in place.
	mux.HandleFunc("/gone", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "<title>back</title>") })
	if code := run([]string{"-recheck", out, "-fail-on-broken"}); code != 0 {
		t.Errorf("exit code after fix = %d", code)
	}
	b, _ = os.ReadFile(out)
	json.Unmarshal(b, &rep)
	if rep.Stats.Broken != 0 || rep.Stats.OK != 2 {
		t.Errorf("after recheck stats = %+v", rep.Stats)
	}
}

func TestMirrorCLI(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<title>Home</title>`)
	}))
	defer srv.Close()
	dir := t.TempDir()
	if code := run([]string{"-mirror", dir, srv.URL}); code != 0 {
		t.Fatalf("exit code %d", code)
	}
	if _, err := os.Stat(filepath.Join(dir, "index.html")); err != nil {
		t.Error(err)
	}
	if code := run([]string{"-mirror", dir}); code != 2 {
		t.Errorf("mirror without URL: exit %d", code)
	}
}

func TestInvalidURL(t *testing.T) {
	if code := run([]string{"-report", "-", "http://"}); code != 2 {
		t.Errorf("exit code %d", code)
	}
}

func TestCredentialsInURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); !ok || u != "admin" || p != "pw" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<title>ok</title>`)
	}))
	defer srv.Close()
	out := filepath.Join(t.TempDir(), "r.json")
	target := strings.Replace(srv.URL, "http://", "http://admin:pw@", 1)
	if code := run([]string{"-report", out, "-fail-on-broken", target}); code != 0 {
		t.Fatalf("exit code %d", code)
	}
	b, _ := os.ReadFile(out)
	if strings.Contains(string(b), "pw") {
		t.Error("password ended up in the report")
	}
}

func TestIsLinkScout(t *testing.T) {
	ls := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"progress":{"running":false}}`)
	}))
	defer ls.Close()
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<html>something else</html>`)
	}))
	defer other.Close()
	if !isLinkScout(ls.URL) {
		t.Error("LinkScout instance not recognised")
	}
	if isLinkScout(other.URL) || isLinkScout("http://127.0.0.1:1") {
		t.Error("foreign server recognised as LinkScout")
	}
}

func TestIsLoopbackListen(t *testing.T) {
	for addr, want := range map[string]bool{
		"127.0.0.1:8088": true, "localhost:8088": true, "[::1]:8088": true,
		"0.0.0.0:8088": false, "192.168.1.10:8088": false, ":8088": false,
	} {
		if got := isLoopbackListen(addr); got != want {
			t.Errorf("isLoopbackListen(%q) = %v want %v", addr, got, want)
		}
	}
}

// The help text is translated, including the "default" label and the list of
// languages.
func TestPrintUsage(t *testing.T) {
	_, fs := parseFlags(nil, i18n.DE)
	var buf strings.Builder
	fs.SetOutput(&buf)
	printUsage(fs, i18n.DE)
	out := buf.String()
	for _, want := range []string{"Aufruf: linkscout", "-listen", "(Standard: 127.0.0.1:8088)", "(en, de)"} {
		if !strings.Contains(out, want) {
			t.Errorf("usage lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "(Standard: false)") {
		t.Error("false defaults must not be printed")
	}
}

// "-report -" writes the JSON report to stdout (for pipes).
func TestReportToStdout(t *testing.T) {
	r, w, _ := os.Pipe()
	orig := os.Stdout
	os.Stdout = w
	err := writeReport("-", crawler.Report{StartURL: "https://e.com/"})
	w.Close()
	os.Stdout = orig
	if err != nil {
		t.Fatal(err)
	}
	var rep crawler.Report
	if err := json.NewDecoder(r).Decode(&rep); err != nil || rep.StartURL != "https://e.com/" {
		t.Errorf("stdout report: %+v %v", rep, err)
	}
}
