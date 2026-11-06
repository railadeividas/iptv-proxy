package activity

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"iptv-proxy/internal/settings"
	"iptv-proxy/internal/store"
)

func setup(t *testing.T) (*Tracker, *store.Store, *settings.Settings) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	st.DB.Exec(`INSERT INTO users(id,username,password,role,created_at) VALUES(1,'ann','x','user',1),(2,'bob','x','user',1)`)
	st.DB.Exec(`INSERT INTO profiles(id,owner_id,name,token,created_at) VALUES(1,1,'Ann TV','t1',1),(2,2,'Bob TV','t2',1)`)
	set := settings.New(st)
	return New(st, set), st, set
}

func req(ip, ua string) *http.Request {
	r := httptest.NewRequest("GET", "/x", nil)
	r.RemoteAddr = ip + ":5555"
	if strings.Contains(ip, ":") {
		r.RemoteAddr = "[" + ip + "]:5555"
	}
	r.Header.Set("User-Agent", ua)
	return r
}

func apply(t *testing.T, set *settings.Settings, kv map[string]any) {
	t.Helper()
	if errs := set.Apply(kv); errs != nil {
		t.Fatal(errs)
	}
}

// Addresses are masked by default; every privacy setting applies to the next request without a restart.
func TestAddressFollowsPrivacySettings(t *testing.T) {
	tr, _, set := setup(t)
	r := req("192.168.1.77", "VLC/3")
	if got := tr.Addr(r); got != "192.168.1.0/24" {
		t.Fatalf("masked by default: %q", got)
	}
	apply(t, set, map[string]any{"stats_mask_ipv4_bits": 16.0})
	if got := tr.Addr(r); got != "192.168.0.0/16" {
		t.Fatalf("mask bits are a setting: %q", got)
	}
	if got := tr.Addr(req("2001:db8:1:2:3:4:5:6", "")); got != "2001:db8:1::/48" {
		t.Fatalf("IPv6: %q", got)
	}
	apply(t, set, map[string]any{"stats_ip_mode": "full"})
	if got := tr.Addr(r); got != "192.168.1.77" {
		t.Fatalf("full: %q", got)
	}
	apply(t, set, map[string]any{"stats_ip_mode": "none"})
	if got := tr.Addr(r); got != "" {
		t.Fatalf("none: %q", got)
	}
	apply(t, set, map[string]any{"stats_ip_mode": "full", "stats_forwarded_header": "X-Forwarded-For"})
	r.Header.Set("X-Forwarded-For", "203.0.113.9, 10.0.0.1")
	if got := tr.Addr(r); got != "203.0.113.9" {
		t.Fatalf("behind a reverse proxy: %q", got)
	}
	r.Header.Set("X-Forwarded-For", "not an address")
	if got := tr.Addr(r); got != "192.168.1.77" {
		t.Fatalf("a bad header falls back to the connection: %q", got)
	}
	if tr.Agent(r) != "VLC/3" {
		t.Fatal("player name")
	}
	apply(t, set, map[string]any{"stats_store_user_agent": "off"})
	if tr.Agent(r) != "" {
		t.Fatal("the player name can be switched off")
	}
}

// One device watching one channel is one session, however many requests it makes; it is listed while it is
// active, saved to the database, and ends after the idle time.
func TestSessionLifecycle(t *testing.T) {
	tr, st, set := setup(t)
	r := req("10.0.0.5", "VLC")
	for i := 0; i < 3; i++ { // the channel link, then segments
		l := tr.Begin(r, 1, nil, "A@SD", Proxy, "cdn.example")
		l.Add(1000)
		l.End()
	}
	tr.Begin(req("10.0.0.5", "VLC"), 1, nil, "B", Redirect, "other.example").End()
	tr.Begin(req("10.0.9.9", "Kodi"), 2, nil, "A@SD", Proxy, "cdn.example").End()

	if n := len(tr.Now(0)); n != 3 {
		t.Fatalf("three sessions: %d", n)
	}
	mine := tr.Now(1)
	if len(mine) != 2 {
		t.Fatalf("a user only sees their own profiles' sessions: %+v", mine)
	}
	for _, w := range mine {
		if w.Mode == Proxy && (w.Requests != 3 || w.Bytes != 3000 || w.Host != "cdn.example" || w.IP != "10.0.0.0/24" || w.Profile != "Ann TV") {
			t.Fatalf("session: %+v", w)
		}
	}

	tr.Flush(false)
	ss, total, _ := st.ActivitySessions(store.ActivityFilter{}, 10, 0)
	if total != 3 {
		t.Fatalf("saved: %d", total)
	}
	cs, _, _ := st.ActivityClients(store.ActivityFilter{Owner: 1}, 10, 0)
	if len(cs) != 1 || cs[0].Plays != 2 {
		t.Fatalf("a device that watched two channels: %+v", cs)
	}
	_ = ss

	// more bytes arrive: the saved row is updated, not duplicated
	l := tr.Begin(r, 1, nil, "A@SD", Proxy, "")
	l.Add(500)
	l.End()
	tr.Flush(false)
	ss, total, _ = st.ActivitySessions(store.ActivityFilter{Owner: 1, Mode: "proxy"}, 10, 0)
	if total != 1 || ss[0].Bytes != 3500 || ss[0].Requests != 4 {
		t.Fatalf("updated: %+v", ss)
	}

	// silence ends the session (the idle time is a setting, read now)
	apply(t, set, map[string]any{"stats_session_idle_seconds": 30.0})
	for _, s := range tr.live {
		s.last.Store(time.Now().Add(-time.Minute).UnixNano())
	}
	tr.Flush(false)
	if len(tr.live) != 0 || len(tr.Now(0)) != 0 {
		t.Fatal("idle sessions end")
	}
	l = tr.Begin(r, 1, nil, "A@SD", Proxy, "")
	l.End()
	tr.Flush(false)
	if _, total, _ := st.ActivitySessions(store.ActivityFilter{Owner: 1, Mode: "proxy"}, 10, 0); total != 2 {
		t.Fatalf("the same device coming back starts a new session: %d", total)
	}
}

// A running request keeps its session live and open however long the stream is silent between writes.
func TestActiveRequestIsNeverIdle(t *testing.T) {
	tr, _, _ := setup(t)
	l := tr.Begin(req("10.0.0.5", "VLC"), 1, nil, "A", Proxy, "")
	for _, s := range tr.live {
		s.last.Store(time.Now().Add(-time.Hour).UnixNano())
	}
	tr.Flush(false)
	if len(tr.live) != 1 || len(tr.Now(0)) != 1 {
		t.Fatal("a request still being served is watching")
	}
	l.End()
}

func TestRecordingCanBeOffAndHistoryCanBeZero(t *testing.T) {
	tr, st, set := setup(t)
	apply(t, set, map[string]any{"stats_enabled": "off"})
	if l := tr.Begin(req("10.0.0.5", "x"), 1, nil, "A", Proxy, ""); l != nil || len(tr.Now(0)) != 0 {
		t.Fatal("nothing is recorded while off")
	}
	tr.Client(req("10.0.0.5", "x"), 1, "playlist")
	if _, total, _ := st.ActivityClients(store.ActivityFilter{}, 10, 0); total != 0 {
		t.Fatal("no devices while off")
	}
	var nilLive *Live
	nilLive.Add(5)
	nilLive.End() // callers need no checks

	apply(t, set, map[string]any{"stats_enabled": "on", "stats_history_sessions": 0.0})
	tr.Begin(req("10.0.0.5", "x"), 1, nil, "A", Proxy, "").End()
	tr.Flush(false)
	if len(tr.Now(0)) != 1 {
		t.Fatal("still shown live")
	}
	if _, total, _ := st.ActivitySessions(store.ActivityFilter{}, 10, 0); total != 0 {
		t.Fatal("0 sessions to keep stores none")
	}
	tr.Client(req("10.0.0.5", "x"), 1, "playlist")
	if _, total, _ := st.ActivityClients(store.ActivityFilter{}, 10, 0); total != 1 {
		t.Fatal("playlist fetches are recorded")
	}
	apply(t, set, map[string]any{"stats_history_clients": 0.0})
	tr.Client(req("10.0.0.6", "x"), 1, "playlist")
	if _, total, _ := st.ActivityClients(store.ActivityFilter{}, 10, 0); total != 1 {
		t.Fatal("0 devices to keep records no more")
	}
}

// Running sessions reach the database when the app stops.
func TestFinalFlushSavesRunningSessions(t *testing.T) {
	tr, st, _ := setup(t)
	l := tr.Begin(req("10.0.0.5", "x"), 1, nil, "A", Proxy, "")
	l.Add(10)
	tr.Flush(true)
	if _, total, _ := st.ActivitySessions(store.ActivityFilter{}, 10, 0); total != 1 {
		t.Fatal("saved at shutdown")
	}
	l.End()
}
