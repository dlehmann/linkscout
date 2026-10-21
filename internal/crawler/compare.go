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

// CSVHeader are the columns of WriteCSV.
var CSVHeader = []string{
	"url", "final_url", "status", "error", "kind", "mime_type", "title", "clicks", "size",
	"duration_ms", "in_sitemap", "robots_blocked", "issues", "found_on",
}

// WriteCSV writes the results as CSV table (one row per URL; issues and the
// linking pages are separated by spaces). Run Analyze first to fill the
// issues column.
func WriteCSV(w io.Writer, rs []Result) error {
	cw := csv.NewWriter(w)
	if err := cw.Write(CSVHeader); err != nil {
		return err
	}
	for _, r := range rs {
		mime := r.MimeType
		if mime == "" {
			mime = r.Type
		}
		row := []string{
			r.URL, r.FinalURL, strconv.Itoa(r.Status), r.Error, r.Kind, mime, r.Title,
			strconv.Itoa(r.Depth), strconv.FormatInt(r.Size, 10), strconv.FormatInt(r.DurationMs, 10),
			strconv.FormatBool(r.InSitemap), strconv.FormatBool(r.RobotsBlocked),
			strings.Join(r.Issues, " "), strings.Join(r.FoundOn, " "),
		}
		if err := cw.Write(row); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}
