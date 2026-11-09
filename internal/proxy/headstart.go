package proxy

import (
	"context"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Head start for live HLS. A live HLS player sits at the live point of the server: whenever a segment is late, the server
// stalls for a moment, or its playlist window has moved on, the player runs dry, stops or skips. The proxy therefore follows
// the live media playlists it serves itself:
//
//   - one tracker per playlist asks the server for the playlist on its own (about every half target duration) while somebody
//     watches, and downloads every segment into the cache the moment it appears, keeping them for as long as players may still
//     ask for them (longer than the server's own window);
//   - players are not given the server's playlist but one that ends hls_head_start_seconds behind the live point and lists only
//     segments the proxy holds, with the server's own sequence numbers, so a late segment, a short stall or a reconnect of the
//     server happens inside that head start and never reaches the player;
//   - every viewer still gets the playlist rewritten with links of their own.
//
// It is for live media playlists only. Master playlists, playlists that end (VOD), byte ranges and low latency HLS are left as
// they are, as is everything when the setting is 0 or the segment cache is off.

// liveMinPoll is the fastest a followed playlist is asked for (tests make it shorter).
var liveMinPoll = time.Second

const (
	liveMaxPoll = 5 * time.Second
	hsMaxTries  = 5   // downloads of one segment tried before it is given up
	hsMaxWindow = 8   // segments in a playlist given to players at most
	hsMaxKept   = 400 // segments a tracker keeps at most
)

// hsRaw is a segment as a playlist lists it.
type hsRaw struct {
	uri  string
	dur  float64
	tags []string // EXTINF and the other tags of this segment (not KEY and MAP)
	key  string   // the EXT-X-KEY and EXT-X-MAP lines in effect for it
	mp   string
	disc bool // an EXT-X-DISCONTINUITY comes before it
}

type hsParsed struct {
	version int
	target  float64
	seq0    int64
	dsn0    int64
	hasDSN  bool
	header  []string // other tags before the first segment, kept as they are
	segs    []hsRaw
}

func segScopedTag(l string) bool {
	for _, p := range []string{"#EXTINF", "#EXT-X-PROGRAM-DATE-TIME", "#EXT-X-GAP", "#EXT-X-DATERANGE", "#EXT-X-BITRATE"} {
		if strings.HasPrefix(l, p) {
			return true
		}
	}
	return l == "#EXT-X-DISCONTINUITY"
}

// parseMedia reads a live media playlist; ok is false for everything head start leaves alone.
func parseMedia(text string) (*hsParsed, bool) {
	out := &hsParsed{version: 3}
	var pending []string
	var curKey, curMap string
	var dur float64
	for _, raw := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		l := strings.TrimSpace(raw)
		switch {
		case l == "" || l == "#EXTM3U":
		case l == "#EXT-X-ENDLIST", strings.HasPrefix(l, "#EXT-X-PLAYLIST-TYPE:VOD"), strings.HasPrefix(l, "#EXT-X-STREAM-INF"),
			strings.HasPrefix(l, "#EXT-X-I-FRAME-STREAM-INF"), strings.HasPrefix(l, "#EXT-X-I-FRAMES-ONLY"), strings.HasPrefix(l, "#EXT-X-BYTERANGE"),
			strings.HasPrefix(l, "#EXT-X-PART"), strings.HasPrefix(l, "#EXT-X-PRELOAD-HINT"), strings.HasPrefix(l, "#EXT-X-SERVER-CONTROL"),
			strings.HasPrefix(l, "#EXT-X-SKIP"), strings.HasPrefix(l, "#EXT-X-RENDITION-REPORT"):
			return nil, false
		case strings.HasPrefix(l, "#EXT-X-VERSION:"):
			out.version, _ = strconv.Atoi(strings.TrimSpace(l[len("#EXT-X-VERSION:"):]))
		case strings.HasPrefix(l, "#EXT-X-TARGETDURATION:"):
			out.target, _ = strconv.ParseFloat(strings.TrimSpace(l[len("#EXT-X-TARGETDURATION:"):]), 64)
		case strings.HasPrefix(l, "#EXT-X-MEDIA-SEQUENCE:"):
			out.seq0, _ = strconv.ParseInt(strings.TrimSpace(l[len("#EXT-X-MEDIA-SEQUENCE:"):]), 10, 64)
		case strings.HasPrefix(l, "#EXT-X-DISCONTINUITY-SEQUENCE:"):
			out.dsn0, _ = strconv.ParseInt(strings.TrimSpace(l[len("#EXT-X-DISCONTINUITY-SEQUENCE:"):]), 10, 64)
			out.hasDSN = true
		case strings.HasPrefix(l, "#EXT-X-KEY"):
			curKey = l
		case strings.HasPrefix(l, "#EXT-X-MAP"):
			curMap = l
		case strings.HasPrefix(l, "#EXTINF:"):
			v := strings.TrimPrefix(l, "#EXTINF:")
			if i := strings.IndexByte(v, ','); i >= 0 {
				v = v[:i]
			}
			dur, _ = strconv.ParseFloat(strings.TrimSpace(v), 64)
			pending = append(pending, l)
		case strings.HasPrefix(l, "#"):
			if len(out.segs) > 0 || len(pending) > 0 || segScopedTag(l) {
				pending = append(pending, l)
			} else {
				out.header = append(out.header, l)
			}
		default:
			disc := false
			for _, t := range pending {
				disc = disc || t == "#EXT-X-DISCONTINUITY"
			}
			out.segs = append(out.segs, hsRaw{uri: l, dur: dur, tags: pending, key: curKey, mp: curMap, disc: disc})
			pending, dur = nil, 0
		}
	}
	if len(out.segs) == 0 {
		return nil, false
	}
	if out.target <= 0 {
		for _, s := range out.segs {
			out.target = math.Max(out.target, math.Ceil(s.dur))
		}
	}
	if out.target <= 0 {
		out.target = 6
	}
	return out, true
}

// hsSeg is a segment a tracker knows.
type hsSeg struct {
	hsRaw
	seq   int64
	abs   *url.URL
	dsn   int64 // discontinuity sequence number in effect for it
	first time.Time
	ck    string // cache key of its bytes
	tries int
}

type hsTracker struct {
	p   *Proxy
	key string
	pl  Payload  // the playlist: address, Referer and User-Agent
	url *url.URL // its final address (after redirects)

	mu       sync.Mutex
	segs     []*hsSeg
	header   []string
	version  int
	target   float64
	hasDSN   bool
	windowN  int // segments the server's playlist lists
	stopped  bool
	lastUsed atomic.Int64 // unix nanoseconds
}

// hsSet holds the trackers and the counters about them.
type hsSet struct {
	mu        sync.Mutex
	m         map[string]*hsTracker
	cooling   map[string]time.Time // playlists the head start could not be started for: left alone until then
	streams   atomic.Int64         // playlists followed now
	playlists atomic.Uint64        // playlists handed to players from a tracker
}

func (s *hsSet) get(key string) *hsTracker {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.m[key]
}

func (s *hsSet) getOrCreate(key string, mk func() *hsTracker) (*hsTracker, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t := s.m[key]; t != nil {
		return t, false
	}
	if s.m == nil {
		s.m = map[string]*hsTracker{}
	}
	t := mk()
	s.m[key] = t
	s.streams.Add(1)
	return t, true
}

func (s *hsSet) coolDown(key string, d time.Duration) {
	s.mu.Lock()
	if s.cooling == nil {
		s.cooling = map[string]time.Time{}
	}
	s.cooling[key] = time.Now().Add(d)
	s.mu.Unlock()
}

func (s *hsSet) isCooling(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	until, ok := s.cooling[key]
	if ok && time.Now().After(until) {
		delete(s.cooling, key)
		return false
	}
	return ok
}

func (s *hsSet) remove(t *hsTracker) {
	s.mu.Lock()
	if s.m[t.key] == t {
		delete(s.m, t.key)
		s.streams.Add(-1)
	}
	s.mu.Unlock()
}

func (p *Proxy) headStartDelay() time.Duration { return p.set.Seconds("hls_head_start_seconds") }

// headStartOn: the setting is above 0 and there is a segment cache to keep the segments in.
func (p *Proxy) headStartOn() bool { return p.set.String("hls_head_start") == "on" && p.cache.on() }

func (t *hsTracker) touch() { t.lastUsed.Store(time.Now().UnixNano()) }

// span is how long the server's own playlist window is.
func (t *hsTracker) spanLocked() time.Duration {
	n := t.windowN
	if n < 3 {
		n = 3
	}
	return time.Duration(float64(n) * t.target * float64(time.Second))
}

// keepLocked is how long a segment is kept: until no player can still be given it.
func (t *hsTracker) keepLocked() time.Duration {
	return t.p.headStartDelay() + 3*t.spanLocked() + t.p.set.Seconds("hls_head_start_keep_extra_seconds")
}

// merge takes in a playlist fetched from the server and says whether the tracker can go on following it.
func (t *hsTracker) merge(pp *hsParsed, now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.header, t.version, t.target, t.hasDSN, t.windowN = pp.header, pp.version, pp.target, pp.hasDSN, len(pp.segs)
	build := func(i int, r hsRaw, first time.Time) (*hsSeg, bool) {
		abs, err := t.url.Parse(r.uri)
		if err != nil || (abs.Scheme != "http" && abs.Scheme != "https") || abs.Host == "" {
			return nil, false
		}
		abs.Fragment = ""
		dsn := pp.dsn0
		for j := 1; j <= i; j++ {
			if pp.segs[j].disc {
				dsn++
			}
		}
		return &hsSeg{hsRaw: r, seq: pp.seq0 + int64(i), abs: abs, dsn: dsn, first: first, ck: cacheKey(Payload{U: abs.String(), R: t.pl.R, A: t.pl.A})}, true
	}
	reset := func() bool {
		t.segs = nil
		var after time.Duration // how long ago a segment was published, guessed from the segments that follow it
		ages := make([]time.Duration, len(pp.segs))
		for i := len(pp.segs) - 1; i >= 0; i-- {
			ages[i] = after
			after += time.Duration(pp.segs[i].dur * float64(time.Second))
		}
		for i, r := range pp.segs {
			s, ok := build(i, r, now.Add(-ages[i]))
			if !ok {
				return false
			}
			t.segs = append(t.segs, s)
		}
		return true
	}
	if len(t.segs) == 0 {
		return reset()
	}
	known := map[int64]*hsSeg{}
	for _, s := range t.segs {
		known[s.seq] = s
	}
	newest := t.segs[len(t.segs)-1].seq
	last := pp.seq0 + int64(len(pp.segs)) - 1
	if last < t.segs[0].seq || pp.seq0 > newest+1 {
		return reset() // the server started its numbering again, or we missed segments and a list with a hole is not allowed
	}
	for i, r := range pp.segs {
		seq := pp.seq0 + int64(i)
		if old, ok := known[seq]; ok {
			if old.uri != r.uri {
				return reset() // same number, another segment: a restart
			}
			continue
		}
		if seq < t.segs[0].seq {
			continue // older than anything we keep
		}
		s, ok := build(i, r, now)
		if !ok {
			return false
		}
		if seq > newest {
			t.segs = append(t.segs, s)
			newest = seq
		} else { // a late one inside what we have: keep the order
			at := len(t.segs)
			for k, e := range t.segs {
				if e.seq > seq {
					at = k
					break
				}
			}
			t.segs = append(t.segs, nil)
			copy(t.segs[at+1:], t.segs[at:])
			t.segs[at] = s
		}
	}
	// forget what no player can be given any more
	keep := t.keepLocked()
	cut := 0
	for cut < len(t.segs)-1 && (now.Sub(t.segs[cut].first) > keep || len(t.segs)-cut > hsMaxKept) {
		cut++
	}
	t.segs = t.segs[cut:]
	return true
}

// fetchMissing starts the download of every segment that is not in the cache yet.
func (t *hsTracker) fetchMissing() {
	t.mu.Lock()
	ttl := t.keepLocked() + 15*time.Second
	var todo []*hsSeg
	for _, s := range t.segs {
		if s.tries < hsMaxTries && t.p.cache.startFetch(s.ck) {
			s.tries++
			todo = append(todo, s)
		}
	}
	t.mu.Unlock()
	for _, s := range todo {
		go func(s *hsSeg) {
			t.p.fetchSegment(s.ck, Payload{U: s.abs.String(), R: t.pl.R, A: t.pl.A}, ttl)
		}(s)
	}
}

// present are the segments whose bytes the cache holds, oldest first.
func (t *hsTracker) presentLocked() []*hsSeg {
	var out []*hsSeg
	for _, s := range t.segs {
		if t.p.cache.has(s.ck) {
			out = append(out, s)
		}
	}
	return out
}

func (t *hsTracker) readyCount() (have, want int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	want = len(t.segs)
	if want > 3 {
		want = 3
	}
	return len(t.presentLocked()), want
}

// render is the playlist players are given at time now, delay behind the live point; ok is false when there is nothing to list.
func (t *hsTracker) render(now time.Time, delay time.Duration) (string, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	present := t.presentLocked()
	if len(present) == 0 {
		return "", false
	}
	var vis []*hsSeg
	for _, s := range present {
		if now.Sub(s.first) >= delay {
			vis = append(vis, s)
		}
	}
	if need := minInt(3, len(present)); len(vis) < need {
		vis = present[:need] // just started: the oldest ones, so a player has something to start with
	}
	w := t.windowN
	if w < 3 {
		w = 3
	}
	if w > hsMaxWindow {
		w = hsMaxWindow
	}
	if len(vis) > w {
		vis = vis[len(vis)-w:]
	}
	var b strings.Builder
	b.WriteString("#EXTM3U\n")
	fmt.Fprintf(&b, "#EXT-X-VERSION:%d\n", maxInt(t.version, 3))
	fmt.Fprintf(&b, "#EXT-X-TARGETDURATION:%d\n", int(math.Ceil(t.target)))
	fmt.Fprintf(&b, "#EXT-X-MEDIA-SEQUENCE:%d\n", vis[0].seq)
	if t.hasDSN || vis[0].dsn > 0 {
		fmt.Fprintf(&b, "#EXT-X-DISCONTINUITY-SEQUENCE:%d\n", vis[0].dsn)
	}
	for _, h := range t.header {
		b.WriteString(h + "\n")
	}
	var prevKey, prevMap string
	for i, s := range vis {
		if s.key != prevKey || i == 0 {
			if s.key != "" {
				b.WriteString(s.key + "\n")
			} else if prevKey != "" {
				b.WriteString("#EXT-X-KEY:METHOD=NONE\n")
			}
			prevKey = s.key
		}
		if s.mp != prevMap && s.mp != "" {
			b.WriteString(s.mp + "\n")
		}
		prevMap = s.mp
		for _, tag := range s.tags {
			if i == 0 && tag == "#EXT-X-DISCONTINUITY" {
				continue // the first segment of a playlist needs none: the sequence number says it
			}
			b.WriteString(tag + "\n")
		}
		b.WriteString(s.uri + "\n")
	}
	return b.String(), true
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// pollEvery is how often the server is asked for the playlist: half a target duration.
func (t *hsTracker) pollEvery() time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	d := time.Duration(t.target / 2 * float64(time.Second))
	if d < liveMinPoll {
		d = liveMinPoll
	}
	if d > liveMaxPoll {
		d = liveMaxPoll
	}
	return d
}

func (t *hsTracker) isStopped() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.stopped
}

func (t *hsTracker) finish() {
	t.mu.Lock()
	t.stopped = true
	t.mu.Unlock()
	t.p.hs.remove(t)
}

// run follows the playlist while somebody watches (and for hls_head_start_linger_seconds after).
func (t *hsTracker) run() {
	defer t.finish()
	for {
		time.Sleep(t.pollEvery())
		linger := t.p.set.Seconds("hls_head_start_linger_seconds")
		if t.isStopped() || !t.p.headStartOn() || time.Since(time.Unix(0, t.lastUsed.Load())) > linger {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), t.p.set.Seconds("upstream_response_timeout_seconds"))
		up, err := t.p.openFresh(ctx, nil, t.pl)
		cancel()
		if err != nil {
			continue // the head start covers it; try again at the next poll
		}
		kind, manifest := up.Kind, up.Manifest
		up.Close()
		pp, ok := parseMedia(string(manifest))
		if kind != "hls" || !ok {
			return // it is not a live media playlist any more (it ended, or changed): players get it as it is
		}
		if !t.merge(pp, time.Now()) {
			return
		}
		t.fetchMissing()
	}
}

// headStartServe answers a playlist request from the tracker that follows it.
func (p *Proxy) headStartServe(pl Payload) (*Upstream, bool) {
	if !p.headStartOn() {
		return nil, false
	}
	t := p.hs.get(cacheKey(pl))
	if t == nil {
		return nil, false
	}
	text, ok := t.render(time.Now(), p.headStartDelay())
	if !ok {
		return nil, false
	}
	t.touch()
	p.hs.playlists.Add(1)
	return &Upstream{URL: t.url, Payload: pl, Kind: "hls", Manifest: []byte(text)}, true
}

// headStartBegin starts following a live playlist that was just fetched and answers with the delayed one; false leaves the
// playlist as the server sent it.
func (p *Proxy) headStartBegin(ctx context.Context, pl Payload, up *Upstream) (*Upstream, bool) {
	if !p.headStartOn() || up.Kind != "hls" || up.URL == nil {
		return nil, false
	}
	key := cacheKey(pl)
	if p.hs.isCooling(key) {
		return nil, false
	}
	pp, ok := parseMedia(string(up.Manifest))
	if !ok {
		return nil, false
	}
	t, created := p.hs.getOrCreate(key, func() *hsTracker {
		t := &hsTracker{p: p, key: key, pl: Payload{U: pl.U, R: pl.R, A: pl.A}, url: up.URL}
		t.touch()
		return t
	})
	if created {
		if !t.merge(pp, time.Now()) {
			t.finish()
			return nil, false
		}
		t.fetchMissing()
		go t.run()
	}
	// a player needs a few segments to start with: wait for the first downloads
	limit := p.set.Seconds("upstream_response_timeout_seconds")
	if limit > 8*time.Second {
		limit = 8 * time.Second
	}
	deadline := time.Now().Add(limit)
	for {
		have, want := t.readyCount()
		if have >= want && have > 0 {
			break
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			if have == 0 {
				// the segments cannot be kept (cache full, server refuses them, ...): do not make every poll wait for it
				t.finish()
				p.hs.coolDown(key, time.Minute)
				return nil, false
			}
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	return p.headStartServe(pl)
}
