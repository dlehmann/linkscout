package crawler

import (
	"net/url"
	"reflect"
	"testing"
)

func TestTPCategory(t *testing.T) {
	site, _ := url.Parse("https://www.example.com/page")
	for raw, want := range map[string]string{
		"https://fonts.googleapis.com/css2?family=Roboto": TPFonts,
		"https://www.googletagmanager.com/gtag/js?id=G-1": TPAnalytics,
		"https://www.youtube-nocookie.com/embed/abc":      TPVideo,
		"https://www.google.com/maps/embed?pb=1":          TPMaps,
		"https://www.google.com/recaptcha/api.js":         TPCaptcha,
		"https://www.google.com/other":                    TPOther,
		"https://cdn.jsdelivr.net/npm/x@1/x.js":           TPCDN,
		"https://consent.cookiebot.com/uc.js":             TPConsent,
		"https://cdn.example.com/logo.png":                TPOwn,
		"https://notyoutube.com/x.js":                     TPOther, // no sub-domain of youtube.com
		"https://connect.facebook.net/en_US/sdk.js":       TPSocial,
		"https://pagead2.googlesyndication.com/pagead/x":  TPAds,
		"https://images.example-cdn.de/a.jpg":             TPOther,
	} {
		if got := TPCategory(raw, site); got != want {
			t.Errorf("TPCategory(%s) = %s, want %s", raw, got, want)
		}
	}
}

// NewThirdParty counts pages per host; own sub-domains come last and do not
// count as external services.
func TestNewThirdParty(t *testing.T) {
	page := func(u string, tp ...string) Result {
		return Result{URL: u, Status: 200, Kind: KindInternal, SEO: &PageSEO{ThirdParty: tp}}
	}
	rs := []Result{
		page("https://e.com/", "https://fonts.gstatic.com/a.woff2", "https://fonts.gstatic.com/b.woff2", "https://img.e.com/x.png"),
		page("https://e.com/a", "https://fonts.gstatic.com/a.woff2", "https://www.youtube.com/embed/1"),
		page("https://e.com/b", "https://img.e.com/y.png"),
		page("https://e.com/c"),
		{URL: "https://e.com/x.pdf", Status: 200, Kind: KindFile}, // not analysed
	}
	o := NewThirdParty(rs)
	want := []ThirdPartyHost{
		{Host: "fonts.gstatic.com", Category: TPFonts, Pages: 2, Resources: 2},
		{Host: "www.youtube.com", Category: TPVideo, Pages: 1, Resources: 1},
		{Host: "img.e.com", Category: TPOwn, Pages: 2, Resources: 2},
	}
	if o.Pages != 4 || o.PagesWith != 2 || o.Services != 2 || !reflect.DeepEqual(o.Hosts, want) {
		t.Errorf("third party = %+v", o)
	}
	if NewThirdParty(rs[4:]) != nil {
		t.Error("no analysed page → nil")
	}
	if got := o.HostCategories()["www.youtube.com"]; got != TPVideo {
		t.Errorf("category = %s", got)
	}
}
