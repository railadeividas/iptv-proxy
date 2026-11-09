package proxy

import (
	"html"
	"net/url"
	"path"
	"regexp"
	"strings"
)

// rewriter turns upstream manifest references into signed proxy URLs.
type rewriter struct {
	signer *Signer
	pub    string   // public base URL of this proxy, no trailing slash
	base   *url.URL // URL of the manifest being rewritten
	ref    string   // Referer to propagate
	ua     string   // User-Agent to propagate
	exp    int64
	prof   int64 // profile and channel to carry into the links (statistics)
	ck     string
}

func (rw *rewriter) resolve(ref string) (*url.URL, bool) {
	ref = strings.TrimSpace(ref)
	if ref == "" || strings.HasPrefix(ref, "data:") {
		return nil, false
	}
	u, err := rw.base.Parse(ref)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, false
	}
	return u, true
}

func cosmeticName(u *url.URL) string {
	n := path.Base(u.Path)
	if n == "." || n == "/" || n == "" {
		return "index"
	}
	return url.PathEscape(n)
}

func (rw *rewriter) fileURL(u *url.URL) string {
	u.Fragment = ""
	tok := rw.signer.Encode(Payload{U: u.String(), R: rw.ref, A: rw.ua, E: rw.exp, P: rw.prof, C: rw.ck})
	return rw.pub + "/x/" + tok + "/" + cosmeticName(u)
}

// dirString is the directory of u as it goes into a signed directory link: no query, no fragment, a trailing slash.
func dirString(u *url.URL) string {
	d := *u
	d.RawQuery, d.Fragment = "", ""
	if d.Path == "" {
		d.Path = "/"
	}
	if !strings.HasSuffix(d.Path, "/") {
		d.Path = d.Path[:strings.LastIndex(d.Path, "/")+1]
		d.RawPath = ""
	}
	return d.String()
}

func (rw *rewriter) dirURL(u *url.URL) string {
	tok := rw.signer.Encode(Payload{U: dirString(u), R: rw.ref, A: rw.ua, E: rw.exp, D: true, P: rw.prof, C: rw.ck})
	return rw.pub + "/xd/" + tok + "/"
}

var uriAttr = regexp.MustCompile(`URI="([^"]*)"`)

// HLS rewrites master and media playlists.
func (rw *rewriter) HLS(body []byte) []byte {
	text := strings.ReplaceAll(string(body), "\r\n", "\n")
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		t := strings.TrimSpace(line)
		switch {
		case t == "":
		case strings.HasPrefix(t, "#"):
			if strings.Contains(t, "URI=\"") {
				lines[i] = uriAttr.ReplaceAllStringFunc(t, func(m string) string {
					sub := uriAttr.FindStringSubmatch(m)
					if u, ok := rw.resolve(sub[1]); ok {
						return `URI="` + rw.fileURL(u) + `"`
					}
					return m
				})
			}
		default:
			if u, ok := rw.resolve(t); ok {
				lines[i] = rw.fileURL(u)
			}
		}
	}
	return []byte(strings.Join(lines, "\n"))
}

var (
	baseURLRe = regexp.MustCompile(`(?s)<BaseURL([^>]*)>(.*?)</BaseURL>`)
	mpdOpenRe = regexp.MustCompile(`<MPD[^>]*>`)
)

// DASH rewrites BaseURL so templates and segment lists keep working: the
// MPD-level BaseURL becomes a signed directory URL, so relative
// SegmentTemplate/SegmentList references resolve through the proxy.
func (rw *rewriter) DASH(body []byte) []byte {
	s := string(body)
	period := strings.Index(s, "<Period")
	if period < 0 {
		period = len(s)
	}
	matches := baseURLRe.FindAllStringSubmatchIndex(s, -1)

	// Effective top-level base: first MPD-level BaseURL, else the manifest dir.
	top := rw.base
	topIdx := -1
	for i, m := range matches {
		if m[0] < period {
			topIdx = i
			break
		}
	}
	if topIdx >= 0 {
		m := matches[topIdx]
		if u, ok := rw.resolve(html.UnescapeString(s[m[4]:m[5]])); ok {
			top = u
		}
	}
	topProxy := rw.dirURL(top)

	var b strings.Builder
	last := 0
	for i, m := range matches {
		b.WriteString(s[last:m[0]])
		last = m[1]
		attrs := s[m[2]:m[3]]
		text := html.UnescapeString(strings.TrimSpace(s[m[4]:m[5]]))
		switch {
		case m[0] < period && i == topIdx:
			b.WriteString("<BaseURL" + attrs + ">" + topProxy + "</BaseURL>")
		case m[0] < period:
			// alternate MPD-level base URLs would bypass the proxy; drop them
		default:
			u, ok := rw.resolveAbsoluteOnly(text)
			switch {
			case !ok:
				b.WriteString(s[m[0]:m[1]]) // relative: resolves against our proxy dir
			case strings.HasSuffix(u.Path, "/"):
				b.WriteString("<BaseURL" + attrs + ">" + rw.dirURL(u) + "</BaseURL>")
			default:
				b.WriteString("<BaseURL" + attrs + ">" + rw.fileURL(u) + "</BaseURL>")
			}
		}
	}
	b.WriteString(s[last:])
	out := b.String()
	if topIdx < 0 {
		loc := mpdOpenRe.FindStringIndex(out)
		if loc != nil {
			out = out[:loc[1]] + "<BaseURL>" + topProxy + "</BaseURL>" + out[loc[1]:]
		}
	}
	return []byte(out)
}

// resolveAbsoluteOnly accepts only absolute http(s) references.
func (rw *rewriter) resolveAbsoluteOnly(ref string) (*url.URL, bool) {
	u, err := url.Parse(ref)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, false
	}
	return u, true
}
