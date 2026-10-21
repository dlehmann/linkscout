// Package i18n provides the (tiny) translation layer of LinkScout.
//
// The UI language is derived from the usual POSIX/GNU locale environment
// variables. English is the reference language and the fallback for every
// unsupported locale and for missing keys; German is the second language.
//
// All user-facing strings – CLI output as well as the web UI – live in the
// message tables lang_<code>.go. The web UI receives the complete table for
// the active language via the initial page state, so there is exactly one
// source of truth.
//
// Adding a language means adding one file, e.g. lang_fr.go:
//
//	package i18n
//
//	const FR Lang = "fr"
//
//	func init() { register(FR, map[string]string{ /* every key of lang_en.go */ }) }
//
// The unit tests then insist that the table has exactly the English keys and
// the same placeholders; Parse, Detect, the -lang flag and the web UI pick the
// new language up automatically.
package i18n

import (
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
)

// Lang is a supported UI language code.
type Lang string

// EN is English, the reference and fallback language.
const EN Lang = "en"

// Supported lists all languages that have a message table (English first,
// then alphabetically). It is filled by register.
var Supported []Lang

// messages holds the table of every registered language.
var messages = map[Lang]map[string]string{}

// register adds the message table of a language. It is called from the init
// function of each lang_<code>.go file.
func register(l Lang, table map[string]string) {
	messages[l] = table
	Supported = append(Supported, l)
	sort.Slice(Supported, func(i, j int) bool {
		if (Supported[i] == EN) != (Supported[j] == EN) {
			return Supported[i] == EN
		}
		return Supported[i] < Supported[j]
	})
}

// Codes returns the supported language codes, e.g. "en, de" (for help texts).
func Codes() string {
	codes := make([]string, len(Supported))
	for i, l := range Supported {
		codes[i] = string(l)
	}
	return strings.Join(codes, ", ")
}

// AcceptLanguage returns the Accept-Language header a browser with this UI
// language would send.
func AcceptLanguage(l Lang) string {
	if l == EN || l == "" {
		return "en-US,en;q=0.5"
	}
	return string(l) + ",en-US;q=0.7,en;q=0.3"
}

// Detect determines the UI language from the process environment. Programs
// started from the macOS Finder or Dock get no locale variables, so there the
// first preferred language of the system settings is used instead.
func Detect() Lang {
	if runtime.GOOS == "darwin" && !hasLocaleEnv(os.Getenv) {
		if l, ok := Parse(firstAppleLanguage(macLanguages())); ok {
			return l
		}
	}
	return DetectFrom(os.Getenv)
}

// hasLocaleEnv reports whether any of the locale variables is set.
func hasLocaleEnv(getenv func(string) string) bool {
	return firstNonEmpty(getenv("LANGUAGE"), getenv("LC_ALL"), getenv("LC_MESSAGES"), getenv("LANG")) != ""
}

// macLanguages returns the output of `defaults read -g AppleLanguages`
// (replaceable in tests).
var macLanguages = func() string {
	out, err := exec.Command("defaults", "read", "-g", "AppleLanguages").Output()
	if err != nil {
		return ""
	}
	return string(out)
}

// firstAppleLanguage extracts the first entry of the property list printed
// by defaults, e.g. `(\n    "de-DE",\n    "en-US"\n)` → "de-DE".
func firstAppleLanguage(plist string) string {
	for _, f := range strings.FieldsFunc(plist, func(r rune) bool { return strings.ContainsRune("(),\"\n\t ", r) }) {
		return f
	}
	return ""
}

// DetectFrom determines the UI language using the given getenv function.
//
// Precedence follows GNU gettext: LANGUAGE (a colon separated priority list)
// wins, then LC_ALL, LC_MESSAGES and finally LANG. The "C" and "POSIX" locales
// mean "no localisation" and therefore resolve to English.
func DetectFrom(getenv func(string) string) Lang {
	// LANGUAGE is ignored by gettext when the locale itself is "C"; we mirror that.
	effective := firstNonEmpty(getenv("LC_ALL"), getenv("LC_MESSAGES"), getenv("LANG"))
	if lst := getenv("LANGUAGE"); lst != "" && !isCLocale(effective) {
		for _, item := range strings.Split(lst, ":") {
			if l, ok := Parse(item); ok {
				return l
			}
		}
	}
	if l, ok := Parse(effective); ok {
		return l
	}
	return EN
}

// Parse maps a locale string such as "de_DE.UTF-8", "de-AT" or "en" to a
// supported language. The boolean is false for unsupported or empty locales.
func Parse(locale string) (Lang, bool) {
	locale = strings.TrimSpace(strings.ToLower(locale))
	if locale == "" || isCLocale(locale) {
		return "", false
	}
	// Strip encoding (".UTF-8") and modifier ("@euro"), then the territory.
	if i := strings.IndexAny(locale, ".@"); i >= 0 {
		locale = locale[:i]
	}
	if i := strings.IndexAny(locale, "_-"); i >= 0 {
		locale = locale[:i]
	}
	for _, l := range Supported {
		if string(l) == locale {
			return l, true
		}
	}
	return "", false
}

// isCLocale reports whether s is the "C" / "POSIX" locale (no localisation).
func isCLocale(s string) bool {
	s = strings.ToLower(s)
	return s == "c" || s == "posix" || strings.HasPrefix(s, "c.")
}

// firstNonEmpty returns the first non-empty value.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// T returns the translation of key for lang, falling back to English and
// finally to the key itself so a missing translation never breaks the UI.
func T(lang Lang, key string) string {
	if m, ok := messages[lang]; ok {
		if s, ok := m[key]; ok {
			return s
		}
	}
	if s, ok := messages[EN][key]; ok {
		return s
	}
	return key
}

// Messages returns the complete message table for lang. Keys missing in the
// language are filled from English. The returned map is a copy.
func Messages(lang Lang) map[string]string {
	out := make(map[string]string, len(messages[EN]))
	for k, v := range messages[EN] {
		out[k] = v
	}
	for k, v := range messages[lang] {
		out[k] = v
	}
	return out
}

// Keys returns all keys of the English (reference) table.
func Keys() []string {
	keys := make([]string, 0, len(messages[EN]))
	for k := range messages[EN] {
		keys = append(keys, k)
	}
	return keys
}
