package mirror

import (
	"bytes"
	"regexp"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"

	"github.com/dlehmann/linkscout/internal/links"
)

// URLs in JavaScript are found heuristically, like HTTrack does it: string
// literals that look like a URL or a path to a file with a known extension
// ("/img/slide-2.jpg", 'https://example.com/data.json', "/team.html"). They are
// downloaded so pages that load images or data from scripts keep working
// when the copy is served from a web server, but the scripts themselves are
// not rewritten – changing code is too risky. Errors of such guessed URLs
// are not reported.

// jsString matches a quoted string literal without white space, quotes or
// template expressions inside.
var jsString = regexp.MustCompile("([\"'`])([^\"'`\\s<>{}\\\\]{3,1000})[\"'`]")

// scriptURLs returns the candidate URLs of a script. Relative paths
// ("img/a.png") are only taken from inline scripts (relative=true): in a
// script file they would resolve against the page that runs it, which is
// unknown.
func scriptURLs(src string, relative bool) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range jsString.FindAllStringSubmatch(src, -1) {
		s := m[2]
		if m[1] != m[0][len(m[0])-1:] || seen[s] {
			continue // different quotes at both ends
		}
		abs := strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") || strings.HasPrefix(s, "//")
		if !abs && !strings.HasPrefix(s, "/") && !(relative && strings.Contains(s, "/")) {
			continue
		}
		if strings.Contains(s, "*") {
			continue // a pattern, not a URL
		}
		// Only URLs with a known file extension: otherwise every "/" string
		// and API endpoint in the code would be fetched.
		if ext := links.Extension(s); ext == "" || links.Category(ext) == links.CatOther {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// inlineScripts returns the text of the <script> elements of a page
// (except JSON data blocks, which are no code).
func inlineScripts(body []byte) []string {
	var out []string
	z := html.NewTokenizer(bytes.NewReader(body))
	in := false
	for {
		switch z.Next() {
		case html.ErrorToken:
			return out
		case html.StartTagToken:
			name, hasAttr := z.TagName()
			in = atom.Lookup(name) == atom.Script
			for in && hasAttr {
				var k, v []byte
				k, v, hasAttr = z.TagAttr()
				if string(k) == "type" && strings.Contains(strings.ToLower(string(v)), "json") {
					in = false
				}
			}
		case html.TextToken:
			if in {
				out = append(out, string(z.Text()))
			}
		case html.EndTagToken:
			in = false
		}
	}
}
