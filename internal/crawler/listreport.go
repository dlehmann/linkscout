package crawler

// ListInfo describes a report that holds only part of the results: the
// results list of the UI as it was filtered and sorted.
type ListInfo struct {
	Total   int    `json:"total"`             // URLs of the complete results
	Filters string `json:"filters,omitempty"` // the active filters, as the UI shows them
	Sort    string `json:"sort,omitempty"`    // the sort order, as the UI shows it
}

// Select returns a report with the results of urls in this order (unknown
// and repeated URLs are skipped). The SEO issues stay as Analyze found them
// on the whole site – a duplicate title is still a duplicate when the other
// page is filtered out – so readers must not analyse a selection again (see
// Report.List). Figures, search matches and page content cover the
// selection; the overview counts every listed URL, external links too,
// because the filters already decided what is in the list.
func (rep Report) Select(urls []string, info ListInfo) Report {
	byURL := make(map[string]int, len(rep.Results))
	for i, r := range rep.Results {
		byURL[r.URL] = i
	}
	info.Total = len(rep.Results)
	sel := make([]Result, 0, len(urls))
	in := map[string]bool{}
	for _, u := range urls {
		i, ok := byURL[u]
		if !ok || in[u] {
			continue
		}
		in[u] = true
		sel = append(sel, rep.Results[i])
	}
	out := rep
	out.Results = sel
	out.List = &info
	out.Stats = Summarize(sel)
	out.Issues = CountIssues(sel)
	ov := NewOverview(sel, true)
	out.Overview = &ov
	if rep.Search != nil {
		s := *rep.Search
		s.Total, s.Matches = 0, []SearchMatch{}
		for _, m := range rep.Search.Matches {
			if in[m.URL] {
				s.Matches = append(s.Matches, m)
				s.Total += m.Count
			}
		}
		out.Search = &s
	}
	out.Content = nil
	for _, p := range rep.Content {
		if in[p.URL] {
			out.Content = append(out.Content, p)
		}
	}
	return out
}

// LoadReport loads an exported JSON report – a complete one or a list
// export – with its page content (Load + LoadContent). The SEO issues of a
// list export are kept as they are (see Analyzed) until the next check.
func (c *Crawler) LoadReport(cfg Config, rep Report) error {
	if err := c.Load(cfg, rep.Results); err != nil {
		return err
	}
	c.LoadContent(rep.Content)
	if rep.List != nil {
		l := *rep.List
		c.mu.Lock()
		c.list = &l
		c.mu.Unlock()
	}
	return nil
}

// List describes the loaded results when they are an imported list export,
// nil otherwise.
func (c *Crawler) List() *ListInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.list == nil {
		return nil
	}
	l := *c.list
	return &l
}

// Analyzed returns the results with their SEO issues. Normally Analyze
// evaluates them; the results of an imported list export keep the issues
// found on the whole site, which a part of it cannot reproduce (a duplicate
// title whose twin was filtered out).
func (c *Crawler) Analyzed() ([]Result, []IssueCount) {
	rs := c.Results()
	if c.List() != nil {
		return rs, CountIssues(rs)
	}
	return rs, Analyze(rs)
}
