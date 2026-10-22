package crawler

import (
	"encoding/csv"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Comparison lists what changed between an earlier report and the current
// results.
type Comparison struct {
	OldCreatedAt time.Time      `json:"oldCreatedAt"`
	Added        []string       `json:"added"`   // URLs that are new
	Removed      []string       `json:"removed"` // URLs that are gone
	Status       []StatusChange `json:"status"`  // URLs whose status changed
	NewIssues    []IssueChange  `json:"newIssues"`
	FixedIssues  []IssueChange  `json:"fixedIssues"`
}

// StatusChange is a URL that answered differently than before.
type StatusChange struct {
	URL string `json:"url"`
	Old int    `json:"old"`
	New int    `json:"new"`
}

// IssueChange is a URL with SEO issues that appeared or disappeared.
type IssueChange struct {
	URL    string   `json:"url"`
	Issues []string `json:"issues"`
}

// Compare compares the results of an earlier report with the current ones.
// The SEO checks of both sides are evaluated freshly, so reports of older
// LinkScout versions (without issues) can be compared too.
func Compare(old Report, current []Result) Comparison {
	oldRs := append([]Result(nil), old.Results...)
	curRs := append([]Result(nil), current...)
	Analyze(oldRs)
	Analyze(curRs)
	before := make(map[string]*Result, len(oldRs))
	for i := range oldRs {
		before[oldRs[i].URL] = &oldRs[i]
	}
	cmp := Comparison{OldCreatedAt: old.CreatedAt}
	seen := map[string]bool{}
	for i := range curRs {
		r := &curRs[i]
		if r.Pending {
			continue
		}
		seen[r.URL] = true
		o, ok := before[r.URL]
		if !ok {
			cmp.Added = append(cmp.Added, r.URL)
			continue
		}
		if o.Status != r.Status {
			cmp.Status = append(cmp.Status, StatusChange{URL: r.URL, Old: o.Status, New: r.Status})
		}
		if added := minus(r.Issues, o.Issues); len(added) > 0 {
			cmp.NewIssues = append(cmp.NewIssues, IssueChange{URL: r.URL, Issues: added})
		}
		if fixed := minus(o.Issues, r.Issues); len(fixed) > 0 {
			cmp.FixedIssues = append(cmp.FixedIssues, IssueChange{URL: r.URL, Issues: fixed})
		}
	}
	for _, o := range oldRs {
		if !seen[o.URL] {
			cmp.Removed = append(cmp.Removed, o.URL)
		}
	}
	sort.Strings(cmp.Added)
	sort.Strings(cmp.Removed)
	return cmp
}

// minus returns the elements of a that are not in b.
func minus(a, b []string) []string {
	var out []string
	for _, v := range a {
		found := false
		for _, w := range b {
			if v == w {
				found = true
				break
			}
		}
		if !found {
			out = append(out, v)
		}
	}
	return out
}

// CSVHeader are the columns of WriteCSV: everything a result holds, the
// SEO facts of a page included (only the element ids of a page are left
// out, they are just the targets of the anchor check).
var CSVHeader = []string{
	"url", "final_url", "redirects", "status", "error", "kind", "internal", "mime_type",
	"type", "category", "content_type", "title", "clicks", "size", "duration_ms",
	"checked_at", "found_on", "in_sitemap", "robots_blocked",
	"worst_issue", "issues",
	"seo_titles", "seo_descriptions", "seo_robots", "seo_canonicals", "seo_h1",
	"seo_headings", "seo_lang", "seo_viewport", "seo_hreflang", "seo_jsonld",
	"seo_jsonld_errors", "seo_social", "seo_images_no_alt", "seo_mixed_content",
	"seo_fragment_links", "seo_security_missing", "seo_cert_not_after", "seo_content_hash",
}

// WriteCSV writes the results as CSV table, one row per URL. Columns with
// several values (redirects, linking pages, issues, titles …) hold one value
// per line. Run Analyze first to fill the issue columns.
func WriteCSV(w io.Writer, rs []Result) error {
	cw := csv.NewWriter(w)
	if err := cw.Write(CSVHeader); err != nil {
		return err
	}
	lines := func(list []string) string { return strings.Join(list, "\n") }
	stamp := func(t time.Time) string {
		if t.IsZero() {
			return ""
		}
		return t.UTC().Format(time.RFC3339)
	}
	for _, r := range rs {
		worst := ""
		if len(r.Issues) > 0 {
			worst = WorstSeverity(r)
		}
		row := []string{
			r.URL, r.FinalURL, lines(r.Redirects), strconv.Itoa(r.Status), r.Error, r.Kind,
			strconv.FormatBool(r.Internal), ResultMime(r), r.Type, r.Category, r.ContentType, r.Title,
			strconv.Itoa(r.Depth), strconv.FormatInt(r.Size, 10), strconv.FormatInt(r.DurationMs, 10),
			stamp(r.CheckedAt), lines(r.FoundOn),
			strconv.FormatBool(r.InSitemap), strconv.FormatBool(r.RobotsBlocked),
			worst, lines(r.Issues),
		}
		if p := r.SEO; p != nil {
			headings := make([]string, len(p.Headings))
			for i, h := range p.Headings {
				headings[i] = "h" + strconv.Itoa(h)
			}
			hreflang := make([]string, len(p.Hreflang))
			for i, a := range p.Hreflang {
				hreflang[i] = a.Lang + " " + a.URL
			}
			row = append(row, lines(p.Titles), lines(p.Descriptions), lines(p.Robots), lines(p.Canonicals),
				lines(p.H1), strings.Join(headings, " "), p.Lang, strconv.FormatBool(p.Viewport),
				lines(hreflang), strconv.Itoa(p.JSONLD), lines(p.JSONLDErrors), lines(p.Social),
				lines(p.ImagesNoAlt), lines(p.Mixed), lines(p.Fragments), lines(p.Security),
				stamp(p.CertNotAfter), p.ContentHash)
		} else {
			row = append(row, make([]string, len(CSVHeader)-len(row))...)
		}
		if err := cw.Write(row); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}
