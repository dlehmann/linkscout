package crawler

// Content search: the crawler keeps the HTML and the visible text of every
// analysed page (internal, 200, HTML, no redirect) compressed in memory –
// not in the JSON report, so imported reports cannot be searched until the
// pages are checked again.

import (
	"bytes"
	"compress/flate"
	"errors"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Search modes.
const (
	SearchText   = "text"   // visible text of the page
	SearchSource = "source" // HTML source
)

// Limits of a search.
const (
	maxQueryLen      = 1000
	maxPageMatches   = 1000 // matches counted per page
	maxSnippets      = 3    // snippets per page
	snippetContext   = 60   // characters before and after a match
	maxSnippetMatchL = 200  // characters of the match itself
)

// SearchQuery is what to look for.
type SearchQuery struct {
	Query         string `json:"q"`
	Mode          string `json:"mode"` // text (default) or source
	Regex         bool   `json:"regex"`
	CaseSensitive bool   `json:"caseSensitive"`
}

// SearchResult lists the pages that contain the query. It is also part of
// the JSON report (Report.Search) when an export was made with a search.
type SearchResult struct {
	Query    SearchQuery   `json:"query"`
	Searched int           `json:"searched"` // pages whose content was available
	Total    int           `json:"total"`    // matches on all pages (each page counts at most 1000)
	Matches  []SearchMatch `json:"matches"`  // pages with matches, in discovery order
}

// SearchMatch is one page with matches.
type SearchMatch struct {
	URL      string    `json:"url"`
	Count    int       `json:"count"`
	Snippets []Snippet `json:"snippets"`
}

// Snippet is a match with some text around it ("…" marks cut text).
type Snippet struct {
	Before string `json:"before"`
	Match  string `json:"match"`
	After  string `json:"after"`
}

// ErrEmptyQuery is returned for a search without text.
var ErrEmptyQuery = errors.New("empty search")

// pageContent is the compressed content of one page.
type pageContent struct{ html, text []byte }

func compress(b []byte) []byte {
	var buf bytes.Buffer
	w, _ := flate.NewWriter(&buf, flate.BestSpeed)
	w.Write(b)
	w.Close()
	return buf.Bytes()
}

func decompress(b []byte) string {
	out, _ := io.ReadAll(flate.NewReader(bytes.NewReader(b)))
	return string(out)
}

// Compile turns the query into a regular expression (plain text is quoted;
// without CaseSensitive the search ignores case).
func (q SearchQuery) Compile() (*regexp.Regexp, error) {
	if strings.TrimSpace(q.Query) == "" {
		return nil, ErrEmptyQuery
	}
	if len(q.Query) > maxQueryLen {
		return nil, errors.New("search text too long")
	}
	pattern := q.Query
	if !q.Regex {
		pattern = regexp.QuoteMeta(pattern)
	}
	if !q.CaseSensitive {
		pattern = "(?i)" + pattern
	}
	return regexp.Compile(pattern)
}

// Searchable returns the number of pages whose content can be searched.
func (c *Crawler) Searchable() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.content)
}

// Search looks for q in the stored pages. Go's regexp runs in linear time,
// so user supplied patterns cannot hang the server.
func (c *Crawler) Search(q SearchQuery) (SearchResult, error) {
	if q.Mode != SearchSource {
		q.Mode = SearchText
	}
	re, err := q.Compile()
	if err != nil {
		return SearchResult{}, err
	}
	type page struct {
		url     string
		content pageContent
	}
	c.mu.Lock()
	pages := make([]page, 0, len(c.content))
	for _, u := range c.order {
		if pc, ok := c.content[u]; ok {
			pages = append(pages, page{u, pc})
		}
	}
	c.mu.Unlock()

	res := SearchResult{Query: q, Searched: len(pages), Matches: []SearchMatch{}}
	for _, p := range pages {
		b := p.content.text
		if q.Mode == SearchSource {
			b = p.content.html
		}
		s := decompress(b)
		var m SearchMatch
		for _, loc := range re.FindAllStringIndex(s, -1) {
			if loc[0] == loc[1] {
				continue // empty matches ("a*") are no hits
			}
			m.Count++
			if len(m.Snippets) < maxSnippets {
				m.Snippets = append(m.Snippets, snippet(s, loc[0], loc[1]))
			}
			if m.Count >= maxPageMatches {
				break
			}
		}
		if m.Count > 0 {
			m.URL = p.url
			res.Total += m.Count
			res.Matches = append(res.Matches, m)
		}
	}
	return res, nil
}

// snippet cuts the match s[start:end] with some context; whitespace is
// collapsed (the HTML source has line breaks and indentation).
func snippet(s string, start, end int) Snippet {
	from := start
	for n := 0; from > 0 && n < snippetContext; n++ {
		_, size := utf8.DecodeLastRuneInString(s[:from])
		from -= size
	}
	to := end
	for n := 0; to < len(s) && n < snippetContext; n++ {
		_, size := utf8.DecodeRuneInString(s[to:])
		to += size
	}
	sn := Snippet{Before: collapse(s[from:start]), Match: collapse(s[start:end]), After: collapse(s[end:to])}
	if r := []rune(sn.Match); len(r) > maxSnippetMatchL {
		sn.Match = string(r[:maxSnippetMatchL]) + "…"
	}
	if from > 0 {
		sn.Before = "…" + strings.TrimLeft(sn.Before, " ")
	}
	if to < len(s) {
		sn.After = strings.TrimRight(sn.After, " ") + "…"
	}
	return sn
}

// collapse replaces every run of whitespace by one space, keeping a leading
// or trailing one (it separates the match from its context).
func collapse(s string) string {
	var b strings.Builder
	space := false
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '\f' {
			space = true
			continue
		}
		if space {
			b.WriteByte(' ')
			space = false
		}
		b.WriteRune(r)
	}
	if space {
		b.WriteByte(' ')
	}
	return b.String()
}
