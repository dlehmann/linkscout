package crawler

import (
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Overview holds every figure of the overview page of the UI: key figures,
// status share, SEO health, title and description lengths, response times,
// clicks from the start page, status codes, MIME types and size by MIME
// type. The page computes the same figures in app.js (summarize,
// renderStats, seoCharts); keep both in step. The reports (JSON, HTML, PDF)
// use this one.
type Overview struct {
	// ExternalIncluded tells whether links to pages on other websites (kind
	// external) are counted; files count wherever they are hosted.
	ExternalIncluded bool `json:"externalIncluded"`
	ExternalCount    int  `json:"externalCount"` // checked URLs of kind external

	Total         int   `json:"total"`
	OK            int   `json:"ok"`
	Redirected    int   `json:"redirected"`
	Broken        int   `json:"broken"`
	Errors        int   `json:"errors"`
	InternalPages int   `json:"internalPages"`
	AvgMs         int64 `json:"avgMs"`
	TotalBytes    int64 `json:"totalBytes"`

	StatusClasses []Count      `json:"statusClasses"` // 2xx, 3xx, 4xx, 5xx, error
	StatusCodes   []Count      `json:"statusCodes"`   // sorted by code
	MimeTypes     []Count      `json:"mimeTypes"`     // by count; "" = unknown
	SizeByMime    []ByteCount  `json:"sizeByMime"`    // by size; adds up to TotalBytes
	ResponseTimes []TimeBucket `json:"responseTimes"`
	// Clicks from the start page: keys "0" … "9", "10+" and "-1" (only
	// listed in the sitemap, see NotLinked).
	Clicks []Count      `json:"clicks"`
	SEO    *SEOOverview `json:"seo,omitempty"` // nil when no page was analysed
}

// ByteCount is a labelled size in bytes.
type ByteCount struct {
	Key   string `json:"key"`
	Bytes int64  `json:"bytes"`
}

// TimeBucket counts the URLs with MinMs <= response time < MaxMs (MaxMs 0 =
// no upper limit).
type TimeBucket struct {
	MinMs int64 `json:"minMs"`
	MaxMs int64 `json:"maxMs,omitempty"`
	Count int   `json:"count"`
}

// SEOOverview describes the analysed pages (internal HTML pages with
// status 200).
type SEOOverview struct {
	Pages              int `json:"pages"`
	PagesWithoutErrors int `json:"pagesWithoutErrors"`
	// Health counts the pages by their most severe issue: error, warning,
	// notice or none (only keys with pages).
	Health            []Count        `json:"health"`
	TitleLength       []LengthBucket `json:"titleLength"`
	DescriptionLength []LengthBucket `json:"descriptionLength"`
}

// LengthBucket counts pages whose first title / description has Min … Max
// characters. Min = Max = 0 means missing, Max = 0 with Min > 0 means no
// upper limit. Recommended marks the range the SEO checks accept.
type LengthBucket struct {
	Min         int  `json:"min"`
	Max         int  `json:"max,omitempty"`
	Count       int  `json:"count"`
	Recommended bool `json:"recommended,omitempty"`
}

// Limits of the overview charts (the same as in app.js).
var (
	timeBounds = []int64{100, 250, 500, 1000, 2000, 5000} // ms, the last bucket is open
	// upper bounds in characters; bucket 0 = missing, the last one is open
	titleBounds = []int{9, 25, 40, 60, 80}
	descBounds  = []int{49, 100, 160, 200}
)

// MaxClicks is the click depth from which the overview folds URLs into one
// "10+" column.
const MaxClicks = 10

// NewOverview computes the overview over the checked results; external
// pages are only counted with includeExternal (the default of the UI is
// without them). Run Analyze first, the SEO health needs the issues.
func NewOverview(rs []Result, includeExternal bool) Overview {
	o := Overview{ExternalIncluded: includeExternal}
	var list []Result
	for _, r := range rs {
		if r.Pending {
			continue
		}
		if r.Kind == KindExternal {
			o.ExternalCount++
			if !includeExternal {
				continue
			}
		}
		list = append(list, r)
	}

	classes := map[string]int{}
	codes := map[int]int{}
	types := map[string]int{}
	sizes := map[string]int64{}
	times := make([]int, len(timeBounds)+1)
	var timed int
	var sumMs int64
	maxDepth := 0
	notLinked := 0
	for _, r := range list {
		o.Total++
		classes[StatusClass(r)]++
		if r.Status > 0 {
			codes[r.Status]++
			timed++
			sumMs += r.DurationMs
		}
		if r.Size > 0 {
			o.TotalBytes += r.Size
		}
		if len(r.Redirects) > 0 {
			o.Redirected++
		}
		switch {
		case r.Status == 200:
			o.OK++
		case r.Status >= 400:
			o.Broken++
		case r.Status == 0:
			o.Errors++
		}
		if r.Kind == KindInternal && r.Status == 200 {
			o.InternalPages++
		}
		mime := ResultMime(r)
		types[mime]++
		if r.Size > 0 {
			sizes[mime] += r.Size
		}
		i := sort.Search(len(timeBounds), func(i int) bool { return r.DurationMs < timeBounds[i] })
		times[i]++
		if r.Depth == NotLinked {
			notLinked++
		} else if r.Depth > maxDepth {
			maxDepth = r.Depth
		}
	}
	if timed > 0 {
		o.AvgMs = sumMs / int64(timed)
	}

	for _, k := range []string{"2xx", "3xx", "4xx", "5xx", "1xx", "error"} {
		if classes[k] > 0 {
			o.StatusClasses = append(o.StatusClasses, Count{k, classes[k]})
		}
	}
	codeKeys := make([]int, 0, len(codes))
	for c := range codes {
		codeKeys = append(codeKeys, c)
	}
	sort.Ints(codeKeys)
	for _, c := range codeKeys {
		o.StatusCodes = append(o.StatusCodes, Count{strconv.Itoa(c), codes[c]})
	}
	for k, n := range types {
		o.MimeTypes = append(o.MimeTypes, Count{k, n})
	}
	sort.Slice(o.MimeTypes, func(i, j int) bool {
		a, b := o.MimeTypes[i], o.MimeTypes[j]
		if a.Count != b.Count {
			return a.Count > b.Count
		}
		return a.Key < b.Key
	})
	for k, n := range sizes {
		o.SizeByMime = append(o.SizeByMime, ByteCount{k, n})
	}
	sort.Slice(o.SizeByMime, func(i, j int) bool {
		a, b := o.SizeByMime[i], o.SizeByMime[j]
		if a.Bytes != b.Bytes {
			return a.Bytes > b.Bytes
		}
		return a.Key < b.Key
	})

	// Response times: drop the empty slow tail, but always keep the buckets up to 1 s.
	for len(times) > 4 && times[len(times)-1] == 0 {
		times = times[:len(times)-1]
	}
	for i, n := range times {
		b := TimeBucket{Count: n}
		if i > 0 {
			b.MinMs = timeBounds[i-1]
		}
		if i < len(timeBounds) {
			b.MaxMs = timeBounds[i]
		}
		o.ResponseTimes = append(o.ResponseTimes, b)
	}

	// Clicks: one column per depth up to the deepest URL, "10+" for the rest,
	// a column for the URLs only known from the sitemap at the end.
	if maxDepth > MaxClicks {
		maxDepth = MaxClicks
	}
	depth := make([]int, maxDepth+1)
	for _, r := range list {
		if r.Depth != NotLinked {
			depth[min(max(r.Depth, 0), maxDepth)]++
		}
	}
	if len(list) > 0 {
		for d, n := range depth {
			key := strconv.Itoa(d)
			if d >= MaxClicks {
				key += "+"
			}
			o.Clicks = append(o.Clicks, Count{key, n})
		}
	}
	if notLinked > 0 {
		o.Clicks = append(o.Clicks, Count{strconv.Itoa(NotLinked), notLinked})
	}

	o.SEO = seoOverview(rs)
	return o
}

// seoOverview covers every analysed page, external links do not matter here.
func seoOverview(rs []Result) *SEOOverview {
	s := &SEOOverview{
		TitleLength:       lengthBuckets(titleBounds, titleMin, titleMax),
		DescriptionLength: lengthBuckets(descBounds, descMin, descMax),
	}
	health := map[string]int{}
	for _, r := range rs {
		if r.SEO == nil || r.Status != 200 || r.Pending {
			continue
		}
		s.Pages++
		sev := WorstSeverity(r)
		health[sev]++
		if sev != SevError {
			s.PagesWithoutErrors++
		}
		s.TitleLength[lengthIndex(first(r.SEO.Titles), titleBounds)].Count++
		s.DescriptionLength[lengthIndex(first(r.SEO.Descriptions), descBounds)].Count++
	}
	if s.Pages == 0 {
		return nil
	}
	for _, k := range []string{SevError, SevWarning, SevNotice, "none"} {
		if health[k] > 0 {
			s.Health = append(s.Health, Count{k, health[k]})
		}
	}
	return s
}

// lengthBuckets builds the empty buckets: missing, ≤ bounds[0], …, > last bound.
func lengthBuckets(bounds []int, recMin, recMax int) []LengthBucket {
	bs := []LengthBucket{{}}
	lo := 1
	for _, hi := range bounds {
		bs = append(bs, LengthBucket{Min: lo, Max: hi, Recommended: lo >= recMin && hi <= recMax})
		lo = hi + 1
	}
	return append(bs, LengthBucket{Min: lo})
}

// lengthIndex returns the bucket of a text by its length in characters.
func lengthIndex(s string, bounds []int) int {
	n := utf8.RuneCountInString(strings.TrimSpace(s))
	if n == 0 {
		return 0
	}
	for i, hi := range bounds {
		if n <= hi {
			return i + 1
		}
	}
	return len(bounds) + 1
}

func first(list []string) string {
	if len(list) == 0 {
		return ""
	}
	return list[0]
}

// ResultMime is the type the UI shows: the MIME type, for old reports the
// file extension.
func ResultMime(r Result) string {
	if r.MimeType != "" {
		return r.MimeType
	}
	return r.Type
}

// severities maps every issue ID to its severity.
var severities = func() map[string]string {
	m := make(map[string]string, len(Issues))
	for _, d := range Issues {
		m[d.ID] = d.Severity
	}
	return m
}()

// IssueSeverity returns the severity of an issue ID (notice when unknown).
func IssueSeverity(id string) string {
	if s, ok := severities[id]; ok {
		return s
	}
	return SevNotice
}

// WorstSeverity returns the most severe issue of a result: error, warning,
// notice or "none".
func WorstSeverity(r Result) string {
	worst := "none"
	rank := map[string]int{"none": 0, SevNotice: 1, SevWarning: 2, SevError: 3}
	for _, id := range r.Issues {
		if s := IssueSeverity(id); rank[s] > rank[worst] {
			worst = s
		}
	}
	return worst
}
