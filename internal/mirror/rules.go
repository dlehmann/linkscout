package mirror

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// Rules decide which URLs the offline copy downloads, in addition to the
// defaults (all of the site, external embedded resources with
// Config.External, external linked files with Config.ExternalFiles).
//
// A rule is "+pattern" (include) or "-pattern" (exclude). "*" matches any
// characters, including "/". A pattern starting with "/" is compared with
// the path and query of a URL (-/private/*), any other pattern with host,
// path and query without the scheme (-*.zip, +cdn.example.com/*). Patterns
// must match the whole string. When several rules match, the last one wins;
// a URL no rule matches follows the defaults.
type Rules []rule

type rule struct {
	include  bool
	pathOnly bool
	text     string
	re       *regexp.Regexp
}

// RuleError reports a rule that does not start with + or - or has no
// pattern.
type RuleError struct{ Rule string }

func (e *RuleError) Error() string {
	return fmt.Sprintf("invalid rule %q: must be + or - followed by a pattern", e.Rule)
}

// ParseRules parses rules separated by white space (a pattern cannot
// contain spaces, URLs do not either).
func ParseRules(list []string) (Rules, error) {
	var out Rules
	for _, item := range list {
		for _, f := range strings.Fields(item) {
			if len(f) < 2 || (f[0] != '+' && f[0] != '-') {
				return nil, &RuleError{Rule: f}
			}
			pat := f[1:]
			parts := strings.Split(pat, "*")
			for i, p := range parts {
				parts[i] = regexp.QuoteMeta(p)
			}
			out = append(out, rule{
				include:  f[0] == '+',
				pathOnly: strings.HasPrefix(pat, "/"),
				text:     f,
				re:       regexp.MustCompile("^(?s:" + strings.Join(parts, ".*") + ")$"),
			})
		}
	}
	return out, nil
}

// Strings returns the rules in their written form.
func (rs Rules) Strings() []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.text
	}
	return out
}

// Match reports whether a rule matches u (decided) and if so whether the
// last matching rule includes it.
func (rs Rules) Match(u *url.URL) (decided, include bool) {
	if len(rs) == 0 {
		return false, false
	}
	p := u.EscapedPath()
	if u.RawQuery != "" {
		p += "?" + u.RawQuery
	}
	full := strings.ToLower(u.Host) + p
	for _, r := range rs {
		s := full
		if r.pathOnly {
			s = p
		}
		// Patterns are typed by people: compare them with the decoded form
		// too, so -/über/* matches /%C3%BCber/x.
		if r.re.MatchString(s) || r.re.MatchString(unescape(s)) {
			decided, include = true, r.include
		}
	}
	return decided, include
}

// unescape decodes percent escapes, or returns s unchanged when it has
// invalid ones.
func unescape(s string) string {
	if !strings.Contains(s, "%") {
		return s
	}
	if d, err := url.PathUnescape(s); err == nil {
		return d
	}
	return s
}
