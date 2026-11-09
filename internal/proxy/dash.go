package proxy

import (
	"encoding/xml"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DASH prefetch. For a live DASH manifest that lists its segments in a SegmentTimeline the newest segments are downloaded
// into the segment cache before the player asks, as is done for HLS. A manifest usually offers several qualities, and
// fetching all of them would waste bandwidth: the quality to fetch is learned from the segments players really ask for.
// Sets with a single quality (audio, a single bitrate) are fetched from the first poll on.
//
// Only what can be done exactly is done: manifests with a SegmentTemplate and SegmentTimeline, no BaseURL below the
// Period, and live (type="dynamic"). Templates that count segments by a fixed duration would need the clock of the
// stream and are left alone: a wrong guess is a request for a segment that does not exist.

// dashMedia is the media path of every segment of one representation, oldest first.
type dashRep struct {
	id    string
	media []string
}

type dashSet struct {
	id   string
	reps []dashRep
}

type xmlTimeline struct {
	S []struct {
		T *int64 `xml:"t,attr"`
		D int64  `xml:"d,attr"`
		R int    `xml:"r,attr"`
	} `xml:"S"`
}

type xmlTemplate struct {
	Media       string       `xml:"media,attr"`
	StartNumber *int64       `xml:"startNumber,attr"`
	Timeline    *xmlTimeline `xml:"SegmentTimeline"`
}

type xmlRep struct {
	ID        string       `xml:"id,attr"`
	Bandwidth string       `xml:"bandwidth,attr"`
	BaseURL   []string     `xml:"BaseURL"`
	Template  *xmlTemplate `xml:"SegmentTemplate"`
}

type xmlSet struct {
	ID       string       `xml:"id,attr"`
	BaseURL  []string     `xml:"BaseURL"`
	Template *xmlTemplate `xml:"SegmentTemplate"`
	Reps     []xmlRep     `xml:"Representation"`
}

type xmlPeriod struct {
	BaseURL  []string     `xml:"BaseURL"`
	Template *xmlTemplate `xml:"SegmentTemplate"`
	Sets     []xmlSet     `xml:"AdaptationSet"`
}

type xmlMPD struct {
	Type    string      `xml:"type,attr"`
	Periods []xmlPeriod `xml:"Period"`
}

var tmplVarRe = regexp.MustCompile(`\$(RepresentationID|Number|Bandwidth|Time)(%0(\d+)d)?\$|\$\$`)

// expand fills in a segment template.
func expandTemplate(t, repID, bandwidth string, number, tm int64) string {
	return tmplVarRe.ReplaceAllStringFunc(t, func(m string) string {
		if m == "$$" {
			return "$"
		}
		sub := tmplVarRe.FindStringSubmatch(m)
		pad := func(n int64) string {
			if sub[3] == "" {
				return strconv.FormatInt(n, 10)
			}
			w, _ := strconv.Atoi(sub[3])
			return fmt.Sprintf("%0*d", w, n)
		}
		switch sub[1] {
		case "RepresentationID":
			return repID
		case "Bandwidth":
			return bandwidth
		case "Number":
			return pad(number)
		default:
			return pad(tm)
		}
	})
}

// timelineSegments lists (number, time) of the segments of a timeline, oldest first; nil when it is too long or unusable.
func timelineSegments(t *xmlTimeline, startNumber int64) (numbers, times []int64) {
	var total int64
	for _, s := range t.S {
		if s.D <= 0 || s.R < 0 {
			return nil, nil // open ended repeats (r=-1) need the clock
		}
		total += int64(s.R) + 1
		if total > 20000 {
			return nil, nil
		}
	}
	tm, n := int64(0), startNumber
	for _, s := range t.S {
		if s.T != nil {
			tm = *s.T
		}
		for i := 0; i <= s.R; i++ {
			numbers, times = append(numbers, n), append(times, tm)
			tm += s.D
			n++
		}
	}
	return numbers, times
}

// parseDASH reads the usable part of a live manifest; keep is how many of the newest segments of each representation to list.
func parseDASH(body []byte, keep int) (live bool, sets []dashSet) {
	var m xmlMPD
	if xml.Unmarshal(body, &m) != nil || m.Type != "dynamic" || len(m.Periods) == 0 {
		return false, nil
	}
	per := m.Periods[len(m.Periods)-1] // the current one
	if len(per.BaseURL) > 0 {
		return true, nil
	}
	for si, as := range per.Sets {
		if len(as.BaseURL) > 0 {
			continue
		}
		set := dashSet{id: as.ID}
		if set.id == "" {
			set.id = "#" + strconv.Itoa(si)
		}
		for _, r := range as.Reps {
			t := r.Template
			if t == nil {
				t = as.Template
			}
			if t == nil {
				t = per.Template
			}
			if len(r.BaseURL) > 0 || t == nil || t.Media == "" || t.Timeline == nil || r.ID == "" {
				continue
			}
			start := int64(1)
			if t.StartNumber != nil {
				start = *t.StartNumber
			}
			numbers, times := timelineSegments(t.Timeline, start)
			if len(numbers) == 0 {
				continue
			}
			from := 0
			if len(numbers) > keep {
				from = len(numbers) - keep
			}
			rep := dashRep{id: r.ID}
			for i := from; i < len(numbers); i++ {
				rep.media = append(rep.media, expandTemplate(t.Media, r.ID, r.Bandwidth, numbers[i], times[i]))
			}
			set.reps = append(set.reps, rep)
		}
		if len(set.reps) > 0 {
			sets = append(sets, set)
		}
	}
	return true, sets
}

var (
	dashBaseRe = regexp.MustCompile(`(?s)<BaseURL[^>]*>(.*?)</BaseURL>`)
)

// dashTopBase is the directory relative references of the manifest resolve against: the first BaseURL before the Period,
// else the manifest's own address. It is the rule rewriter.DASH follows when it makes the signed directory link.
func dashTopBase(base *url.URL, manifest string) *url.URL {
	period := strings.Index(manifest, "<Period")
	if period < 0 {
		period = len(manifest)
	}
	for _, m := range dashBaseRe.FindAllStringSubmatchIndex(manifest, -1) {
		if m[0] >= period {
			break
		}
		if u, err := base.Parse(html.UnescapeString(strings.TrimSpace(manifest[m[2]:m[3]]))); err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" {
			return u
		}
		break
	}
	return base
}

// dashIndex remembers which representation each recent segment belongs to, and which representation viewers use.
type dashIndex struct {
	mu   sync.Mutex
	refs map[string]dashRef  // segment cache key -> its representation
	pick map[string]dashPick // manifest + set -> the representation viewers ask for
}

type dashRef struct {
	mpd, set, rep string
	exp           time.Time
}

type dashPick struct {
	rep string
	at  time.Time
}

const (
	dashRefTTL  = 3 * time.Minute
	dashPickTTL = 5 * time.Minute
	dashRefMax  = 4096
)

func pickKey(mpd, set string) string { return mpd + "\x00" + set }

// note records the recent segments of a representation, so a request for one of them tells which quality a viewer plays.
func (d *dashIndex) note(mpd, set, rep string, keys []string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.refs == nil {
		d.refs = map[string]dashRef{}
	}
	now := time.Now()
	if len(d.refs) >= dashRefMax {
		for k, r := range d.refs {
			if now.After(r.exp) {
				delete(d.refs, k)
			}
		}
		if len(d.refs) >= dashRefMax {
			return
		}
	}
	for _, k := range keys {
		d.refs[k] = dashRef{mpd: mpd, set: set, rep: rep, exp: now.Add(dashRefTTL)}
	}
}

// learn is told about a segment a viewer asked for.
func (d *dashIndex) learn(key string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	r, ok := d.refs[key]
	if !ok || time.Now().After(r.exp) {
		return
	}
	if d.pick == nil {
		d.pick = map[string]dashPick{}
	}
	d.pick[pickKey(r.mpd, r.set)] = dashPick{rep: r.rep, at: time.Now()}
}

func (d *dashIndex) chosen(mpd, set string) (string, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	c, ok := d.pick[pickKey(mpd, set)]
	if !ok || time.Since(c.at) > dashPickTTL {
		delete(d.pick, pickKey(mpd, set))
		return "", false
	}
	return c.rep, true
}

// prefetchDASH downloads the newest segments of the representations viewers use (see above).
func (p *Proxy) prefetchDASH(up *Upstream) {
	n := p.set.Int("segment_prefetch_count")
	if !p.prefetchOn() || up.URL == nil {
		return
	}
	live, sets := parseDASH(up.Manifest, 2*n)
	if !live || len(sets) == 0 {
		return
	}
	dir := dirString(dashTopBase(up.URL, string(up.Manifest)))
	mpd := cacheKey(up.Payload)
	keyOf := func(media string) (Payload, string) {
		pl := Payload{U: dir + media, R: up.Payload.R, A: up.Payload.A}
		return pl, cacheKey(pl)
	}
	for _, set := range sets {
		for _, r := range set.reps {
			keys := make([]string, len(r.media))
			for i, m := range r.media {
				_, keys[i] = keyOf(m)
			}
			p.dash.note(mpd, set.id, r.id, keys)
		}
		var use *dashRep
		if len(set.reps) == 1 {
			use = &set.reps[0]
		} else if id, ok := p.dash.chosen(mpd, set.id); ok {
			for i := range set.reps {
				if set.reps[i].id == id {
					use = &set.reps[i]
				}
			}
		}
		if use == nil {
			continue
		}
		media := use.media
		if len(media) > n {
			media = media[len(media)-n:]
		}
		for _, m := range media {
			pl, k := keyOf(m)
			if !p.cache.startFetch(k) {
				continue
			}
			go p.fetchSegment(k, pl, p.set.Seconds("segment_prefetch_keep_seconds"))
		}
	}
}
