// Package activity records who uses the playlist links: which devices fetch playlists and guides, who is
// watching through the proxy right now, and which players were sent straight to a stream (redirect).
//
// Running sessions live in memory and are written to the database every few seconds (setting
// stats_save_interval_seconds), so a request never waits for the database. A session is one device watching one
// channel; it ends after stats_session_idle_seconds of silence. What is stored, for how long and how much of an
// address is kept are all settings (the Stats tab).
package activity

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"iptv-proxy/internal/settings"
	"iptv-proxy/internal/store"
)

const (
	Proxy    = "proxy"
	Redirect = "redirect"

	maxUA        = 200
	pruneEvery   = 10 * time.Minute // besides after a change of the history settings
	liveCapacity = 4096             // sessions kept in memory at most; a flood of addresses cannot grow it without bound
)

// counters only grow (until the app restarts); the metrics endpoint reports them.
type counters struct {
	playlists, guides, proxy, redirect, bytes atomic.Int64
}

// Counters is a copy of the counters of one profile, or of all of them.
type Counters struct {
	Playlists, Guides, Proxy, Redirect, Bytes int64
}

func (c *counters) snapshot() Counters {
	return Counters{c.playlists.Load(), c.guides.Load(), c.proxy.Load(), c.redirect.Load(), c.bytes.Load()}
}

type session struct {
	tot, all           *counters // the profile's and the overall counters
	profileID, ownerID int64
	profile            string
	ck, mode, host     string
	ip, ua             string
	started            int64 // unix seconds

	last     atomic.Int64 // unix nanoseconds
	bytes    atomic.Int64
	requests atomic.Int64
	active   atomic.Int32 // requests being served right now

	// owned by Flush
	id         int64
	savedLast  int64
	savedBytes int64
	savedReqs  int64
}

// Tracker keeps the running sessions.
type Tracker struct {
	st  *store.Store
	set *settings.Settings

	mu   sync.Mutex
	live map[string]*session
	per  map[int64]*counters
	all  counters

	flushMu   sync.Mutex
	lastPrune time.Time
	pruneNow  atomic.Bool
}

func New(st *store.Store, set *settings.Settings) *Tracker {
	t := &Tracker{st: st, set: set, live: map[string]*session{}, per: map[int64]*counters{}}
	set.OnChange(func(keys []string) {
		for _, k := range keys {
			if strings.HasPrefix(k, "stats_history_") {
				t.pruneNow.Store(true)
				return
			}
		}
	})
	return t
}

func (t *Tracker) enabled() bool { return t.set.String("stats_enabled") == "on" }

// ---- who is asking ----

// Addr returns the address of the client as it is recorded: empty, masked or full, per the privacy settings.
func (t *Tracker) Addr(r *http.Request) string {
	mode := t.set.String("stats_ip_mode")
	if mode == "none" {
		return ""
	}
	raw := ""
	if h := strings.TrimSpace(t.set.String("stats_forwarded_header")); h != "" {
		v, _, _ := strings.Cut(r.Header.Get(h), ",")
		raw = strings.TrimSpace(v)
	}
	ip := net.ParseIP(raw)
	if ip == nil {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		ip = net.ParseIP(host)
	}
	if ip == nil {
		return ""
	}
	if mode != "masked" {
		return ip.String()
	}
	if v4 := ip.To4(); v4 != nil {
		bits := t.set.Int("stats_mask_ipv4_bits")
		if bits >= 32 {
			return v4.String()
		}
		return (&net.IPNet{IP: v4.Mask(net.CIDRMask(bits, 32)), Mask: net.CIDRMask(bits, 32)}).String()
	}
	bits := t.set.Int("stats_mask_ipv6_bits")
	if bits >= 128 {
		return ip.String()
	}
	return (&net.IPNet{IP: ip.Mask(net.CIDRMask(bits, 128)), Mask: net.CIDRMask(bits, 128)}).String()
}

// Agent returns the player name as it is recorded.
func (t *Tracker) Agent(r *http.Request) string {
	if t.set.String("stats_store_user_agent") != "on" {
		return ""
	}
	ua := strings.TrimSpace(r.UserAgent())
	if len(ua) > maxUA {
		ua = ua[:maxUA]
	}
	return strings.ToValidUTF8(ua, "")
}

// profile returns the counters of a profile; the caller holds t.mu.
func (t *Tracker) profile(id int64) *counters {
	c := t.per[id]
	if c == nil {
		c = &counters{}
		t.per[id] = c
	}
	return c
}

// Counters returns the overall counters and those of each profile that has any.
func (t *Tracker) Counters() (Counters, map[int64]Counters) {
	t.mu.Lock()
	defer t.mu.Unlock()
	per := make(map[int64]Counters, len(t.per))
	for id, c := range t.per {
		per[id] = c.snapshot()
	}
	return t.all.snapshot(), per
}

// Client counts a playlist or guide request ("playlist" or "guide") of a profile.
func (t *Tracker) Client(r *http.Request, profileID int64, kind string) {
	if !t.enabled() {
		return
	}
	t.mu.Lock()
	pc := t.profile(profileID)
	t.mu.Unlock()
	if kind == "guide" {
		pc.guides.Add(1)
		t.all.guides.Add(1)
	} else {
		pc.playlists.Add(1)
		t.all.playlists.Add(1)
	}
	if t.set.Int("stats_history_clients") == 0 {
		return
	}
	t.st.TouchClient(profileID, t.Addr(r), t.Agent(r), kind, time.Now().Unix())
}

// ---- sessions ----

// Live is a running request of a session. All methods accept nil (recording is off).
type Live struct{ s *session }

// Add counts bytes sent to the player.
func (l *Live) Add(n int) {
	if l == nil {
		return
	}
	l.s.bytes.Add(int64(n))
	l.s.tot.bytes.Add(int64(n))
	l.s.all.bytes.Add(int64(n))
	l.s.last.Store(time.Now().UnixNano())
}

// End marks the request finished.
func (l *Live) End() {
	if l == nil {
		return
	}
	l.s.last.Store(time.Now().UnixNano())
	l.s.active.Add(-1)
}

// HostOf is the host of a stream address, empty when it cannot be read.
func HostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Host
}

// Begin starts (or continues) the session of the client watching ck from a profile and counts one request.
// mode is Proxy or Redirect, host the stream server. p may be nil when only its id is known: it is loaded when a
// new session starts.
func (t *Tracker) Begin(r *http.Request, profileID int64, p *store.Profile, ck, mode, host string) *Live {
	if !t.enabled() || profileID == 0 {
		return nil
	}
	ip, ua := t.Addr(r), t.Agent(r)
	key := strings.Join([]string{strconv.FormatInt(profileID, 10), ck, mode, ip, ua}, "\x00")
	now := time.Now()

	t.mu.Lock()
	s := t.live[key]
	if s == nil && p == nil {
		t.mu.Unlock() // the lookup must not hold the lock
		var err error
		if p, err = t.st.GetProfile(profileID); err != nil {
			return nil
		}
		t.mu.Lock()
		s = t.live[key]
	}
	if s == nil {
		if len(t.live) >= liveCapacity {
			t.mu.Unlock()
			return nil
		}
		s = &session{tot: t.profile(profileID), all: &t.all, profileID: profileID, ownerID: p.OwnerID, profile: p.Name, ck: ck, mode: mode, host: host, ip: ip, ua: ua, started: now.Unix()}
		t.live[key] = s
		if mode == Redirect {
			s.tot.redirect.Add(1)
			s.all.redirect.Add(1)
		} else {
			s.tot.proxy.Add(1)
			s.all.proxy.Add(1)
		}
	}
	if s.host == "" {
		s.host = host
	}
	s.active.Add(1)
	s.requests.Add(1)
	s.last.Store(now.UnixNano())
	t.mu.Unlock()
	return &Live{s}
}

// Observe is the hook for proxy.Proxy.Observe: it follows a proxied request (a segment or key of a stream
// whose link carries a profile and a channel).
func (t *Tracker) Observe(r *http.Request, profileID int64, ck, upstream string) (add func(int), done func()) {
	l := t.Begin(r, profileID, nil, ck, Proxy, HostOf(upstream))
	return l.Add, l.End
}

// Watching is a session seen from outside.
type Watching struct {
	ProfileID int64  `json:"profile_id"`
	Profile   string `json:"profile"`
	OwnerID   int64  `json:"-"`
	CK        string `json:"ck"`
	Name      string `json:"name"`
	Mode      string `json:"mode"`
	Host      string `json:"host"`
	IP        string `json:"ip"`
	UA        string `json:"ua"`
	Started   int64  `json:"started"`
	Last      int64  `json:"last"`
	Bytes     int64  `json:"bytes"`
	Requests  int64  `json:"requests"`
}

// Now lists the sessions that were active within the "still watching" window, newest first.
// owner 0 means everybody's.
func (t *Tracker) Now(owner int64) []Watching {
	cut := time.Now().Add(-t.set.Seconds("stats_live_seconds")).UnixNano()
	t.mu.Lock()
	defer t.mu.Unlock()
	out := []Watching{}
	for _, s := range t.live {
		last := s.last.Load()
		if (owner != 0 && s.ownerID != owner) || (s.active.Load() == 0 && last < cut) {
			continue
		}
		out = append(out, Watching{ProfileID: s.profileID, Profile: s.profile, OwnerID: s.ownerID, CK: s.ck, Mode: s.mode, Host: s.host,
			IP: s.ip, UA: s.ua, Started: s.started, Last: last / int64(time.Second), Bytes: s.bytes.Load(), Requests: s.requests.Load()})
	}
	for i := 1; i < len(out); i++ { // newest first; the list is short
		for j := i; j > 0 && out[j].Started > out[j-1].Started; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// ---- saving ----

// Run saves running sessions until ctx ends, then saves them once more.
func (t *Tracker) Run(ctx context.Context) {
	t.prune()
	for {
		select {
		case <-ctx.Done():
			t.Flush(true)
			return
		case <-time.After(t.set.Seconds("stats_save_interval_seconds")):
			t.Flush(false)
			if t.pruneNow.Swap(false) || time.Since(t.lastPrune) > pruneEvery {
				t.prune()
			}
		}
	}
}

func (t *Tracker) prune() {
	t.lastPrune = time.Now()
	t.st.PruneActivity(t.set.Int("stats_history_days"), t.set.Int("stats_history_sessions"), t.set.Int("stats_history_clients"))
}

// Flush writes new and changed sessions to the database and forgets the ones that ended (silent for
// stats_session_idle_seconds, or all of them when final).
func (t *Tracker) Flush(final bool) {
	t.flushMu.Lock()
	defer t.flushMu.Unlock()
	idle := t.set.Seconds("stats_session_idle_seconds")
	keep := t.set.Int("stats_history_sessions") > 0
	now := time.Now()

	t.mu.Lock()
	work := make([]*session, 0, len(t.live))
	ended := false
	for k, s := range t.live {
		work = append(work, s)
		if final || (s.active.Load() == 0 && now.Sub(time.Unix(0, s.last.Load())) > idle) {
			delete(t.live, k)
			ended = true
		}
	}
	t.mu.Unlock()

	for _, s := range work {
		if !keep {
			continue
		}
		last, bytes, reqs := s.last.Load()/int64(time.Second), s.bytes.Load(), s.requests.Load()
		if s.id == 0 {
			s.id = t.st.AddSession(store.Session{ProfileID: s.profileID, CK: s.ck, Mode: s.mode, Host: s.host, IP: s.ip, UA: s.ua,
				Started: s.started, Last: last, Bytes: bytes, Requests: reqs})
			if s.id != 0 && t.set.Int("stats_history_clients") > 0 {
				t.st.TouchClient(s.profileID, s.ip, s.ua, "play", last)
			}
		} else if last != s.savedLast || bytes != s.savedBytes || reqs != s.savedReqs {
			t.st.UpdateSession(s.id, last, bytes, reqs)
		}
		s.savedLast, s.savedBytes, s.savedReqs = last, bytes, reqs
	}
	if ended {
		t.st.Notify2("activity")
	}
}
