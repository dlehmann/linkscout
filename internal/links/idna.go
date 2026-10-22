package links

import (
	"net"
	"net/url"
	"strings"
	"unicode/utf8"
)

// ToASCII returns raw with an internationalised host name (müller.de) in its
// ASCII form (xn--mller-kva.de). Everything else is left as it is; strings
// that are no URL with a host are returned unchanged.
func ToASCII(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || isASCII(u.Host) {
		return raw
	}
	u.Host = ASCIIHost(u.Host)
	return u.String()
}

// ASCIIHost converts every label of a host name (optionally with port) that
// contains non-ASCII characters to Punycode with the "xn--" prefix. Labels
// are lower-cased first; full IDNA mapping (Unicode normalisation) is not
// done – input typed into a form is NFC already.
func ASCIIHost(host string) string {
	if isASCII(host) {
		return host
	}
	name, port := host, ""
	if h, p, err := net.SplitHostPort(host); err == nil {
		name, port = h, p
	}
	// Ideographic and full-width full stops separate labels too (UTS #46).
	name = strings.NewReplacer("。", ".", "．", ".", "｡", ".").Replace(name)
	labels := strings.Split(name, ".")
	for i, l := range labels {
		if isASCII(l) {
			continue
		}
		if enc, ok := punycode(strings.ToLower(l)); ok {
			labels[i] = "xn--" + enc
		}
	}
	name = strings.Join(labels, ".")
	if port != "" {
		return net.JoinHostPort(name, port)
	}
	return name
}

// isASCII reports whether s contains only ASCII characters.
func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

// Punycode parameters (RFC 3492, section 5).
const (
	pcBase        = 36
	pcTMin        = 1
	pcTMax        = 26
	pcSkew        = 38
	pcDamp        = 700
	pcInitialBias = 72
	pcInitialN    = 128
)

// punycode encodes one label (RFC 3492, section 6.3).
func punycode(s string) (string, bool) {
	runes := []rune(s)
	var out []byte
	for _, r := range runes {
		if r < 0x80 {
			out = append(out, byte(r))
		}
	}
	b := len(out)
	h := b
	if b > 0 {
		out = append(out, '-')
	}
	n, delta, bias := rune(pcInitialN), 0, pcInitialBias
	for h < len(runes) {
		m := rune(0x7fffffff)
		for _, r := range runes {
			if r >= n && r < m {
				m = r
			}
		}
		if int64(m-n)*int64(h+1) > int64(0x7fffffff-delta) {
			return "", false // overflow
		}
		delta += int(m-n) * (h + 1)
		n = m
		for _, r := range runes {
			if r < n {
				delta++
			}
			if r != n {
				continue
			}
			q := delta
			for k := pcBase; ; k += pcBase {
				t := k - bias
				if t < pcTMin {
					t = pcTMin
				} else if t > pcTMax {
					t = pcTMax
				}
				if q < t {
					break
				}
				out = append(out, pcDigit(t+(q-t)%(pcBase-t)))
				q = (q - t) / (pcBase - t)
			}
			out = append(out, pcDigit(q))
			bias = pcAdapt(delta, h+1, h == b)
			delta = 0
			h++
		}
		delta++
		n++
	}
	return string(out), true
}

// pcDigit returns the basic code point of a digit value (0–35).
func pcDigit(d int) byte {
	if d < 26 {
		return byte('a' + d)
	}
	return byte('0' + d - 26)
}

// pcAdapt is the bias adaptation function (RFC 3492, section 6.1).
func pcAdapt(delta, numPoints int, first bool) int {
	if first {
		delta /= pcDamp
	} else {
		delta /= 2
	}
	delta += delta / numPoints
	k := 0
	for delta > ((pcBase-pcTMin)*pcTMax)/2 {
		delta /= pcBase - pcTMin
		k += pcBase
	}
	return k + (pcBase-pcTMin+1)*delta/(delta+pcSkew)
}
