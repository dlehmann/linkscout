package crawler

// External services: the resources a page makes the browser load from other
// hosts (fonts, statistics, videos, maps …). They are only information, not
// SEO issues. What a script loads at run time is not visible without a
// browser – the UI says so.

import (
	"net/url"
	"sort"
	"strings"

	"github.com/dlehmann/linkscout/internal/links"
)

// Categories of external services (texts: tp.cat.<id>).
const (
	TPFonts     = "fonts"
	TPAnalytics = "analytics"
	TPVideo     = "video"
	TPMaps      = "maps"
	TPAds       = "ads"
	TPSocial    = "social"
	TPConsent   = "consent"
	TPCaptcha   = "captcha"
	TPCDN       = "cdn"
	TPOther     = "other"
	TPOwn       = "own" // sub-domain of the checked site (cdn.example.com)
)

// TPCategories lists the categories in display order.
var TPCategories = []string{TPFonts, TPAnalytics, TPVideo, TPMaps, TPAds, TPSocial, TPConsent, TPCaptcha, TPCDN, TPOther, TPOwn}

// maxThirdParty limits the external resources stored per page.
const maxThirdParty = 200

// tpRule assigns a category to a host (and its sub-domains), optionally
// only below a path prefix. The first matching rule wins, so the ones with
// a path come first.
type tpRule struct{ host, path, cat string }

var tpRules = []tpRule{
	{"google.com", "/maps", TPMaps},
	{"google.com", "/recaptcha", TPCaptcha},
	{"gstatic.com", "/recaptcha", TPCaptcha},
	{"fonts.googleapis.com", "", TPFonts},
	{"fonts.gstatic.com", "", TPFonts},
	{"typekit.net", "", TPFonts},
	{"fonts.bunny.net", "", TPFonts},
	{"use.fontawesome.com", "", TPFonts},
	{"kit.fontawesome.com", "", TPFonts},
	{"fonts.net", "", TPFonts},
	{"google-analytics.com", "", TPAnalytics},
	{"googletagmanager.com", "", TPAnalytics},
	{"analytics.google.com", "", TPAnalytics},
	{"matomo.cloud", "", TPAnalytics},
	{"plausible.io", "", TPAnalytics},
	{"stats.wp.com", "", TPAnalytics},
	{"hotjar.com", "", TPAnalytics},
	{"clarity.ms", "", TPAnalytics},
	{"etracker.com", "", TPAnalytics},
	{"etracker.de", "", TPAnalytics},
	{"statcounter.com", "", TPAnalytics},
	{"cloudflareinsights.com", "", TPAnalytics},
	{"segment.com", "", TPAnalytics},
	{"mc.yandex.ru", "", TPAnalytics},
	{"youtube.com", "", TPVideo},
	{"youtube-nocookie.com", "", TPVideo},
	{"ytimg.com", "", TPVideo},
	{"vimeo.com", "", TPVideo},
	{"vimeocdn.com", "", TPVideo},
	{"dailymotion.com", "", TPVideo},
	{"wistia.com", "", TPVideo},
	{"maps.googleapis.com", "", TPMaps},
	{"maps.gstatic.com", "", TPMaps},
	{"maps.google.com", "", TPMaps},
	{"openstreetmap.org", "", TPMaps},
	{"api.mapbox.com", "", TPMaps},
	{"doubleclick.net", "", TPAds},
	{"googlesyndication.com", "", TPAds},
	{"googleadservices.com", "", TPAds},
	{"adservice.google.com", "", TPAds},
	{"amazon-adsystem.com", "", TPAds},
	{"criteo.com", "", TPAds},
	{"taboola.com", "", TPAds},
	{"outbrain.com", "", TPAds},
	{"facebook.net", "", TPSocial},
	{"facebook.com", "", TPSocial},
	{"fbcdn.net", "", TPSocial},
	{"twitter.com", "", TPSocial},
	{"twimg.com", "", TPSocial},
	{"x.com", "", TPSocial},
	{"instagram.com", "", TPSocial},
	{"cdninstagram.com", "", TPSocial},
	{"linkedin.com", "", TPSocial},
	{"licdn.com", "", TPSocial},
	{"pinterest.com", "", TPSocial},
	{"pinimg.com", "", TPSocial},
	{"tiktok.com", "", TPSocial},
	{"cookiebot.com", "", TPConsent},
	{"usercentrics.eu", "", TPConsent},
	{"cookielaw.org", "", TPConsent},
	{"onetrust.com", "", TPConsent},
	{"consentmanager.net", "", TPConsent},
	{"borlabs.io", "", TPConsent},
	{"recaptcha.net", "", TPCaptcha},
	{"hcaptcha.com", "", TPCaptcha},
	{"challenges.cloudflare.com", "", TPCaptcha},
	{"cdnjs.cloudflare.com", "", TPCDN},
	{"jsdelivr.net", "", TPCDN},
	{"unpkg.com", "", TPCDN},
	{"ajax.googleapis.com", "", TPCDN},
	{"code.jquery.com", "", TPCDN},
	{"bootstrapcdn.com", "", TPCDN},
	{"cloudfront.net", "", TPCDN},
	{"akamaihd.net", "", TPCDN},
	{"fastly.net", "", TPCDN},
	{"googleusercontent.com", "", TPCDN},
}

// matchesHost reports whether host is domain or one of its sub-domains.
func matchesHost(host, domain string) bool {
	return host == domain || strings.HasSuffix(host, "."+domain)
}

// TPCategory returns the category of an external resource URL; site is the
// URL of the page that loads it (for own sub-domains).
func TPCategory(raw string, site *url.URL) string {
	u, err := url.Parse(raw)
	if err != nil {
		return TPOther
	}
	host := strings.ToLower(u.Hostname())
	if site != nil {
		own := strings.TrimPrefix(strings.ToLower(site.Hostname()), "www.")
		if own != "" && matchesHost(host, own) {
			return TPOwn
		}
	}
	for _, r := range tpRules {
		if matchesHost(host, r.host) && (r.path == "" || strings.HasPrefix(u.Path, r.path)) {
			return r.cat
		}
	}
	return TPOther
}

// thirdParty returns the embedded resources of a page that live on other
// hosts (absolute URLs, at most maxThirdParty).
func thirdParty(doc links.Document, page *url.URL) []string {
	var out []string
	for _, l := range doc.Links {
		if !l.Embedded || len(out) >= maxThirdParty {
			continue
		}
		if u, err := url.Parse(l.URL); err == nil && !links.SameSite(page, u) {
			out = append(out, l.URL)
		}
	}
	return out
}

// ThirdPartyOverview summarises the external services of the analysed pages.
type ThirdPartyOverview struct {
	Pages     int              `json:"pages"`     // analysed pages
	PagesWith int              `json:"pagesWith"` // pages with at least one external service (own sub-domains do not count)
	Services  int              `json:"services"`  // hosts except own sub-domains
	Hosts     []ThirdPartyHost `json:"hosts"`     // by pages, own sub-domains last
}

// ThirdPartyHost is one host that pages load resources from.
type ThirdPartyHost struct {
	Host      string `json:"host"`
	Category  string `json:"category"`
	Pages     int    `json:"pages"`     // pages that load something from it
	Resources int    `json:"resources"` // distinct URLs
}

// NewThirdParty computes the external services over all analysed pages;
// nil when no page was analysed.
func NewThirdParty(rs []Result) *ThirdPartyOverview {
	o := &ThirdPartyOverview{}
	type acc struct {
		ThirdPartyHost
		urls map[string]bool
	}
	hosts := map[string]*acc{}
	for _, r := range rs {
		if r.SEO == nil || r.Status != 200 || r.Pending {
			continue
		}
		o.Pages++
		page, _ := url.Parse(r.URL)
		seen := map[string]bool{}
		foreign := false
		for _, raw := range r.SEO.ThirdParty {
			u, err := url.Parse(raw)
			if err != nil || u.Hostname() == "" {
				continue
			}
			host := strings.ToLower(u.Hostname())
			cat := TPCategory(raw, page)
			h := hosts[host]
			if h == nil {
				h = &acc{ThirdPartyHost{Host: host, Category: cat}, map[string]bool{}}
				hosts[host] = h
			} else if h.Category == TPOther && cat != TPOther {
				h.Category = cat // a path rule may classify a later URL of the host
			}
			h.urls[raw] = true
			if !seen[host] {
				seen[host] = true
				h.Pages++
			}
			if cat != TPOwn {
				foreign = true
			}
		}
		if foreign {
			o.PagesWith++
		}
	}
	if o.Pages == 0 {
		return nil
	}
	for _, h := range hosts {
		h.Resources = len(h.urls)
		if h.Category != TPOwn {
			o.Services++
		}
		o.Hosts = append(o.Hosts, h.ThirdPartyHost)
	}
	sort.Slice(o.Hosts, func(i, j int) bool {
		a, b := o.Hosts[i], o.Hosts[j]
		if (a.Category == TPOwn) != (b.Category == TPOwn) {
			return b.Category == TPOwn
		}
		if a.Pages != b.Pages {
			return a.Pages > b.Pages
		}
		return a.Host < b.Host
	})
	return o
}

// HostCategories maps every host of o to its category (nil-safe).
func (o *ThirdPartyOverview) HostCategories() map[string]string {
	m := map[string]string{}
	if o != nil {
		for _, h := range o.Hosts {
			m[h.Host] = h.Category
		}
	}
	return m
}
