package links

import "testing"

func TestASCIIHost(t *testing.T) {
	cases := map[string]string{
		"example.com":          "example.com",
		"müller.de":            "xn--mller-kva.de",
		"MÜLLER.de":            "xn--mller-kva.de",
		"www.bücher.de:8080":   "www.xn--bcher-kva.de:8080",
		"straße.de":            "xn--strae-oqa.de",
		"例え.テスト":               "xn--r8jz45g.xn--zckzah",
		"münchen。de":           "xn--mnchen-3ya.de",
		"xn--mller-kva.de":     "xn--mller-kva.de",
		"bücher.example.com":   "xn--bcher-kva.example.com",
		"ελληνικά.gr":          "xn--hxargifdar.gr",
		"правительство.рф":     "xn--80aealotwbjpid2k.xn--p1ai",
		"żółć.pl":              "xn--kda4b0koi.pl",
		"[::1]:8080":           "[::1]:8080",
		"localhost":            "localhost",
		"ümlaut-only-ascii.de": "xn--mlaut-only-ascii-izb.de",
	}
	for in, want := range cases {
		if got := ASCIIHost(in); got != want {
			t.Errorf("ASCIIHost(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestToASCII(t *testing.T) {
	cases := map[string]string{
		"https://müller.de/pfad?q=ä": "https://xn--mller-kva.de/pfad?q=ä",
		"https://M%C3%BCller.de/":    "https://xn--mller-kva.de/",
		"https://example.com/ä":      "https://example.com/ä",
		"müller.de":                  "müller.de", // no scheme, no host: unchanged
		"":                           "",
	}
	for in, want := range cases {
		if got := ToASCII(in); got != want {
			t.Errorf("ToASCII(%q) = %q, want %q", in, got, want)
		}
	}
}
