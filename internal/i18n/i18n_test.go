package i18n

import (
	"regexp"
	"testing"
)

func envFunc(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestDetectFrom(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want Lang
	}{
		{"empty environment falls back to English", map[string]string{}, EN},
		{"LANG German", map[string]string{"LANG": "de_DE.UTF-8"}, DE},
		{"LANG Austrian German", map[string]string{"LANG": "de_AT.UTF-8"}, DE},
		{"LANG English", map[string]string{"LANG": "en_GB.UTF-8"}, EN},
		{"unsupported language falls back", map[string]string{"LANG": "fr_FR.UTF-8"}, EN},
		{"LC_ALL beats LANG", map[string]string{"LANG": "en_US.UTF-8", "LC_ALL": "de_DE.UTF-8"}, DE},
		{"LC_MESSAGES beats LANG", map[string]string{"LANG": "en_US.UTF-8", "LC_MESSAGES": "de_CH.UTF-8"}, DE},
		{"LC_ALL beats LC_MESSAGES", map[string]string{"LC_MESSAGES": "de_DE", "LC_ALL": "en_US"}, EN},
		{"LANGUAGE list wins", map[string]string{"LANG": "en_US.UTF-8", "LANGUAGE": "de:en"}, DE},
		{"LANGUAGE skips unsupported", map[string]string{"LANG": "en_US.UTF-8", "LANGUAGE": "fr:de"}, DE},
		{"LANGUAGE ignored for C locale", map[string]string{"LANG": "C", "LANGUAGE": "de"}, EN},
		{"C.UTF-8 is English", map[string]string{"LANG": "C.UTF-8"}, EN},
		{"POSIX is English", map[string]string{"LC_ALL": "POSIX"}, EN},
		{"modifier is stripped", map[string]string{"LANG": "de_DE@euro"}, DE},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DetectFrom(envFunc(tt.env)); got != tt.want {
				t.Errorf("DetectFrom(%v) = %q, want %q", tt.env, got, tt.want)
			}
		})
	}
}

func TestParse(t *testing.T) {
	for in, want := range map[string]Lang{"de": DE, "DE-de": DE, "en-US": EN, " en ": EN} {
		if got, ok := Parse(in); !ok || got != want {
			t.Errorf("Parse(%q) = %q,%v want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "fr", "C", "posix", "xx_YY"} {
		if _, ok := Parse(in); ok {
			t.Errorf("Parse(%q) should fail", in)
		}
	}
}

func TestTFallbacks(t *testing.T) {
	if got := T(DE, "tab.stats"); got != "Übersicht" {
		t.Errorf("German translation = %q", got)
	}
	if got := T(EN, "tab.stats"); got != "Overview" {
		t.Errorf("English translation = %q", got)
	}
	if got := T(Lang("fr"), "tab.stats"); got != "Overview" {
		t.Errorf("unknown language must fall back to English, got %q", got)
	}
	if got := T(DE, "does.not.exist"); got != "does.not.exist" {
		t.Errorf("missing key must return the key, got %q", got)
	}
}

// Every language must have exactly the keys of the English reference table,
// otherwise the UI would silently show English fragments.
func TestTablesComplete(t *testing.T) {
	for _, l := range Supported {
		for k := range messages[EN] {
			if _, ok := messages[l][k]; !ok {
				t.Errorf("%s: translation missing for %q", l, k)
			}
		}
		for k := range messages[l] {
			if _, ok := messages[EN][k]; !ok {
				t.Errorf("%s: key %q does not exist in the English table", l, k)
			}
		}
	}
}

// Placeholders (printf verbs and %name% markers) must match between
// languages, otherwise formatting breaks at runtime.
func TestPlaceholdersMatch(t *testing.T) {
	re := regexp.MustCompile(`%[a-z]+%|%[dsv]`)
	for _, l := range Supported {
		for k, en := range messages[EN] {
			pe, pl := re.FindAllString(en, -1), re.FindAllString(messages[l][k], -1)
			if !sameMultiset(pe, pl) {
				t.Errorf("%s %q: placeholders differ: en=%v %s=%v", l, k, pe, l, pl)
			}
		}
	}
}

func TestSupportedAndHelpers(t *testing.T) {
	if len(Supported) < 2 || Supported[0] != EN {
		t.Fatalf("Supported = %v, English must come first", Supported)
	}
	if Codes() != "en, de" {
		t.Errorf("Codes() = %q", Codes())
	}
	if AcceptLanguage(EN) != "en-US,en;q=0.5" || AcceptLanguage(DE) != "de,en-US;q=0.7,en;q=0.3" {
		t.Errorf("AcceptLanguage: %q / %q", AcceptLanguage(EN), AcceptLanguage(DE))
	}
}

func sameMultiset(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	m := map[string]int{}
	for _, s := range a {
		m[s]++
	}
	for _, s := range b {
		m[s]--
	}
	for _, v := range m {
		if v != 0 {
			return false
		}
	}
	return true
}

func TestMessagesIsCopyWithFallback(t *testing.T) {
	m := Messages(DE)
	if len(m) != len(messages[EN]) {
		t.Fatalf("Messages(DE) has %d keys, want %d", len(m), len(messages[EN]))
	}
	m["tab.results"] = "changed"
	if T(DE, "tab.results") == "changed" {
		t.Error("Messages must return a copy")
	}
	if len(Keys()) != len(messages[EN]) {
		t.Error("Keys() incomplete")
	}
}

func TestFirstAppleLanguage(t *testing.T) {
	for in, want := range map[string]string{
		"(\n    \"de-DE\",\n    \"en-US\"\n)\n": "de-DE",
		"(\n    en\n)":                          "en",
		"":                                      "",
	} {
		if got := firstAppleLanguage(in); got != want {
			t.Errorf("firstAppleLanguage(%q) = %q want %q", in, got, want)
		}
	}
	if l, ok := Parse(firstAppleLanguage("(\n \"de-AT\"\n)")); !ok || l != DE {
		t.Errorf("de-AT should map to German, got %q %v", l, ok)
	}
	if hasLocaleEnv(envFunc(map[string]string{})) || !hasLocaleEnv(envFunc(map[string]string{"LANG": "de_DE.UTF-8"})) {
		t.Error("hasLocaleEnv")
	}
}
