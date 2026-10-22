package crawler

import (
	"sort"
	"strconv"
	"time"

	"github.com/dlehmann/linkscout/internal/fetch"
)

// Count is a labelled number, used for the bar lists of the statistics page.
type Count struct {
	Key   string `json:"key"`
	Count int    `json:"count"`
}

// Stats summarises a list of results.
type Stats struct {
	Total         int      `json:"total"`
	Pending       int      `json:"pending"`
	OK            int      `json:"ok"`         // status 200
	Redirected    int      `json:"redirected"` // followed at least one redirect
	Broken        int      `json:"broken"`     // final status >= 400
	Errors        int      `json:"errors"`     // no HTTP status (DNS, TLS, timeout …)
	InternalPages int      `json:"internalPages"`
	TotalBytes    int64    `json:"totalBytes"` // sum of known sizes (Content-Length), not bytes transferred
	AvgMs         int64    `json:"avgMs"`
	ByStatusClass []Count  `json:"byStatusClass"` // 2xx, 3xx, 4xx, 5xx, error, pending
	ByCode        []Count  `json:"byCode"`        // "200", "404" … sorted by code
	ByKind        []Count  `json:"byKind"`
	ByType        []Count  `json:"byType"` // sorted by count desc
	Slowest       []Result `json:"slowest"`
	BrokenList    []Result `json:"brokenList"` // broken + errors, with FoundOn
}

// StatusClass returns "2xx".."5xx", "error" or "pending" for a result.
func StatusClass(r Result) string {
	switch {
	case r.Pending:
		return "pending"
	case r.Status >= 200 && r.Status < 600:
		return strconv.Itoa(r.Status/100) + "xx"
	case r.Status >= 100:
		return "1xx"
	}
	return "error"
}

// Summarize computes statistics over results.
func Summarize(rs []Result) Stats {
	s := Stats{Total: len(rs)}
	class := map[string]int{}
	codes := map[int]int{}
	kinds := map[string]int{}
	types := map[string]int{}
	var timed int
	var sumMs int64
	var checked []Result

	for _, r := range rs {
		class[StatusClass(r)]++
		kinds[r.Kind]++
		types[r.Type]++
		if r.Pending {
			s.Pending++
			continue
		}
		if r.Status > 0 {
			codes[r.Status]++
			timed++
			sumMs += r.DurationMs
			checked = append(checked, r)
		}
		if r.Size > 0 {
			s.TotalBytes += r.Size
		}
		if len(r.Redirects) > 0 {
			s.Redirected++
		}
		switch {
		case r.Status == 200:
			s.OK++
		case r.Status >= 400:
			s.Broken++
		case r.Status == 0:
			s.Errors++
		}
		if r.Status >= 400 || r.Status == 0 {
			s.BrokenList = append(s.BrokenList, r)
		}
		if r.Kind == KindInternal && r.Status == 200 {
			s.InternalPages++
		}
	}
	if timed > 0 {
		s.AvgMs = sumMs / int64(timed)
	}

	for _, k := range []string{"2xx", "3xx", "4xx", "5xx", "error", "pending"} {
		if class[k] > 0 {
			s.ByStatusClass = append(s.ByStatusClass, Count{k, class[k]})
		}
	}
	codeKeys := make([]int, 0, len(codes))
	for c := range codes {
		codeKeys = append(codeKeys, c)
	}
	sort.Ints(codeKeys)
	for _, c := range codeKeys {
		s.ByCode = append(s.ByCode, Count{strconv.Itoa(c), codes[c]})
	}
	for _, k := range []string{KindInternal, KindExternal, KindFile} {
		if kinds[k] > 0 {
			s.ByKind = append(s.ByKind, Count{k, kinds[k]})
		}
	}
	for t, n := range types {
		s.ByType = append(s.ByType, Count{t, n})
	}
	sort.Slice(s.ByType, func(i, j int) bool {
		if s.ByType[i].Count != s.ByType[j].Count {
			return s.ByType[i].Count > s.ByType[j].Count
		}
		return s.ByType[i].Key < s.ByType[j].Key
	})

	sort.SliceStable(checked, func(i, j int) bool { return checked[i].DurationMs > checked[j].DurationMs })
	if len(checked) > 10 {
		checked = checked[:10]
	}
	s.Slowest = checked
	sort.SliceStable(s.BrokenList, func(i, j int) bool { return s.BrokenList[i].URL < s.BrokenList[j].URL })
	return s
}

// Report is the JSON export format (CLI -report and "Export JSON" in the UI).
type Report struct {
	Generator string       `json:"generator"`
	StartURL  string       `json:"startUrl"`
	CreatedAt time.Time    `json:"createdAt"`
	UserAgent string       `json:"userAgent"`
	Referer   string       `json:"referer"` // empty = automatic (Firefox policy)
	Stats     Stats        `json:"stats"`
	Issues    []IssueCount `json:"issues,omitempty"` // SEO issues, see Analyze
	// Overview holds the figures of the overview page (added in 1.3.0;
	// readers of older reports compute it with NewOverview).
	Overview *Overview `json:"overview,omitempty"`
	// Search holds the result of the content search the export was made
	// with (added in 1.5.0, only then).
	Search  *SearchResult `json:"search,omitempty"`
	Results []Result      `json:"results"`
	// List is set when the report holds only the URLs of the results list
	// as filtered and sorted in the UI (added in 1.5.0), see Select.
	List *ListInfo `json:"list,omitempty"`
	// Content is the text and HTML of the analysed pages for the content
	// search after an import (added in 1.5.0; JSON exports only).
	Content []PageContent `json:"content,omitempty"`
}

// NewReport builds a report from the current state of c.
func NewReport(c *Crawler, generator string) Report {
	cfg := c.Config()
	rs, issues := c.Analyzed()
	ua := cfg.Fetch.UserAgent
	if ua == "" {
		ua = fetch.FirefoxUA
	}
	list := c.List()
	ov := NewOverview(rs, list != nil) // a list counts every listed URL, see Select
	return Report{
		Overview:  &ov,
		Generator: generator,
		StartURL:  cfg.StartURL,
		CreatedAt: time.Now().UTC(),
		UserAgent: ua,
		Referer:   cfg.Fetch.Referer,
		Stats:     Summarize(rs),
		Issues:    issues,
		Results:   rs,
		List:      list,
	}
}
