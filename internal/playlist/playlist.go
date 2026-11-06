// Package playlist parses and generates M3U playlists.
package playlist

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"
)

type Item struct {
	Name, URL, TvgID, Logo, Group string
	UserAgent, Referrer           string
}

type Skipped struct{ Ref, URL, Reason string }

var attrRe = regexp.MustCompile(`([\w-]+)="([^"]*)"`)

// Parse reads an M3U playlist. Bad entries are returned as skipped with a
// reason instead of failing the whole import.
func Parse(r io.Reader) ([]Item, []Skipped, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var items []Item
	var skipped []Skipped
	var cur Item
	reset := func() { cur = Item{} }
	sawHeader, sawInf := false, false
	for sc.Scan() {
		line := strings.TrimSpace(strings.TrimPrefix(sc.Text(), "\uFEFF"))
		switch {
		case line == "":
		case strings.HasPrefix(line, "#EXTM3U"):
			sawHeader = true
		case strings.HasPrefix(line, "#EXTINF:"):
			reset()
			sawInf = true
			rest := strings.TrimPrefix(line, "#EXTINF:")
			if i := strings.LastIndex(rest, ","); i >= 0 {
				cur.Name = strings.TrimSpace(rest[i+1:])
			}
			for _, m := range attrRe.FindAllStringSubmatch(rest, -1) {
				switch strings.ToLower(m[1]) {
				case "tvg-id":
					cur.TvgID = m[2]
				case "tvg-logo":
					cur.Logo = m[2]
				case "group-title":
					cur.Group = m[2]
				case "tvg-name":
					if cur.Name == "" {
						cur.Name = m[2]
					}
				case "http-user-agent", "user-agent":
					cur.UserAgent = m[2]
				case "http-referrer", "http-referer", "referrer":
					cur.Referrer = m[2]
				}
			}
		case strings.HasPrefix(line, "#EXTVLCOPT:"):
			kv := strings.SplitN(strings.TrimPrefix(line, "#EXTVLCOPT:"), "=", 2)
			if len(kv) == 2 {
				switch strings.ToLower(kv[0]) {
				case "http-user-agent":
					cur.UserAgent = kv[1]
				case "http-referrer", "http-referer":
					cur.Referrer = kv[1]
				}
			}
		case strings.HasPrefix(line, "#EXTHTTP:"):
			var h map[string]string
			if json.Unmarshal([]byte(strings.TrimPrefix(line, "#EXTHTTP:")), &h) == nil {
				for k, v := range h {
					switch strings.ToLower(k) {
					case "user-agent":
						cur.UserAgent = v
					case "referer", "referrer":
						cur.Referrer = v
					}
				}
			}
		case strings.HasPrefix(line, "#"):
		default:
			raw := line
			// Kodi style: url|User-Agent=x&Referer=y
			if i := strings.Index(raw, "|"); i > 0 {
				if q, err := url.ParseQuery(raw[i+1:]); err == nil {
					for k, v := range q {
						switch strings.ToLower(k) {
						case "user-agent":
							cur.UserAgent = v[0]
						case "referer", "referrer":
							cur.Referrer = v[0]
						}
					}
				}
				raw = raw[:i]
			}
			ref := cur.Name
			if ref == "" {
				ref = raw
			}
			u, err := url.Parse(raw)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				skipped = append(skipped, Skipped{Ref: ref, URL: raw, Reason: "unsupported or invalid stream URL"})
				reset()
				continue
			}
			cur.URL = raw
			if cur.Name == "" {
				cur.Name = u.Host + u.Path
			}
			items = append(items, cur)
			reset()
		}
	}
	if err := sc.Err(); err != nil {
		return items, skipped, err
	}
	if !sawHeader && !sawInf && len(items) == 0 {
		return nil, nil, fmt.Errorf("not an M3U playlist")
	}
	return items, skipped, nil
}

type Out struct {
	TvgID, Name, Logo, Group, URL string
}

func clean(s string) string { return strings.NewReplacer(`"`, `'`, "\n", " ", "\r", " ").Replace(s) }

// Write renders a playlist. No upstream headers or credentials are included.
func Write(w io.Writer, epgURL string, items []Out) {
	if epgURL != "" {
		fmt.Fprintf(w, "#EXTM3U x-tvg-url=\"%s\" url-tvg=\"%s\"\n", clean(epgURL), clean(epgURL))
	} else {
		fmt.Fprint(w, "#EXTM3U\n")
	}
	for _, it := range items {
		fmt.Fprintf(w, "#EXTINF:-1 tvg-id=\"%s\" tvg-name=\"%s\"", clean(it.TvgID), clean(it.Name))
		if it.Logo != "" {
			fmt.Fprintf(w, " tvg-logo=\"%s\"", clean(it.Logo))
		}
		if it.Group != "" {
			fmt.Fprintf(w, " group-title=\"%s\"", clean(it.Group))
		}
		fmt.Fprintf(w, ",%s\n%s\n", clean(it.Name), it.URL)
	}
}
