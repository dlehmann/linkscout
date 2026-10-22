package crawler

import (
	"reflect"
	"testing"
)

// The overview counts like the overview page: pending URLs never, external
// pages only on request, files always; SEO health and lengths per page.
func TestNewOverview(t *testing.T) {
	seo := func(title, desc string) *PageSEO {
		return &PageSEO{Titles: []string{title}, Descriptions: []string{desc}}
	}
	rs := []Result{
		{URL: "https://e.com/", Kind: KindInternal, Status: 200, MimeType: "text/html", Size: 1000, DurationMs: 50,
			SEO: seo("A good title for this page", "")}, // 26 characters, description missing
		{URL: "https://e.com/b", Kind: KindInternal, Status: 200, MimeType: "text/html", Size: 500, DurationMs: 300, Depth: 1,
			SEO: seo("Short", "x"), Issues: []string{"title_short", "mixed_content"}},
		{URL: "https://e.com/x.pdf", Kind: KindFile, Status: 404, Type: "pdf", DurationMs: 1500, Depth: 12},
		{URL: "https://e.com/orphan", Kind: KindInternal, Status: 200, MimeType: "text/html", Depth: NotLinked},
		{URL: "https://other.org/", Kind: KindExternal, Status: 200, MimeType: "text/html", Size: 99999},
		{URL: "https://e.com/p", Kind: KindInternal, Pending: true},
	}
	o := NewOverview(rs, false)
	if o.Total != 4 || o.OK != 2+1 || o.Broken != 1 || o.ExternalCount != 1 || o.TotalBytes != 1500 || o.InternalPages != 3 {
		t.Errorf("figures = %+v", o)
	}
	if !reflect.DeepEqual(o.MimeTypes, []Count{{"text/html", 3}, {"pdf", 1}}) {
		t.Errorf("mime types = %v", o.MimeTypes)
	}
	if !reflect.DeepEqual(o.Clicks, []Count{{"0", 1}, {"1", 1}, {"2", 0}, {"3", 0}, {"4", 0}, {"5", 0}, {"6", 0},
		{"7", 0}, {"8", 0}, {"9", 0}, {"10+", 1}, {"-1", 1}}) {
		t.Errorf("clicks = %v", o.Clicks)
	}
	// buckets up to 1 s always, then up to the slowest one (1–2 s)
	if len(o.ResponseTimes) != 5 || o.ResponseTimes[0].Count != 2 || o.ResponseTimes[2].Count != 1 || o.ResponseTimes[4] != (TimeBucket{1000, 2000, 1}) {
		t.Errorf("times = %v", o.ResponseTimes)
	}
	if s := o.SEO; s == nil || s.Pages != 2 || s.PagesWithoutErrors != 1 ||
		!reflect.DeepEqual(s.Health, []Count{{SevError, 1}, {"none", 1}}) ||
		s.TitleLength[1].Count != 1 || s.TitleLength[3].Count != 1 || !s.TitleLength[3].Recommended ||
		s.DescriptionLength[0].Count != 1 || s.DescriptionLength[1].Count != 1 {
		t.Errorf("seo = %+v", o.SEO)
	}
	if all := NewOverview(rs, true); all.Total != 5 || all.TotalBytes != 101499 || !all.ExternalIncluded {
		t.Errorf("with external = %+v", all)
	}
}
