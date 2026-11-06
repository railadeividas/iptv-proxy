package health

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"iptv-proxy/internal/store"
)

func TestFailureThresholdAndRetry(t *testing.T) {
	c, st, set := newTest(t)
	set.Apply(map[string]any{"health_failures_before_down": 3.0, "health_retry_minutes": 5.0})
	addStream(t, st, 0, "A", "http://a/1", store.StatusOK, 0)
	st.DB.Exec(`UPDATE streams SET checked_at=1`)
	fail := func() { st.RecordProbe(store.Probe{URL: "http://a/1", Reason: "Timed out"}, 3, 10) }
	status := func() (int, int) {
		var s, n int
		st.DB.QueryRow(`SELECT status, fail_count FROM streams`).Scan(&s, &n)
		return s, n
	}
	fail()
	fail()
	if s, n := status(); s != store.StatusOK || n != 2 {
		t.Fatalf("two failures must not flip a working stream: status %d fails %d", s, n)
	}
	// a stream with failures that is not marked yet is retried soon, long before the 12 h interval
	st.DB.Exec(`UPDATE streams SET checked_at=?`, time.Now().Unix()-10*60)
	if jobs := c.autoJobs(time.Now().Unix()); len(jobs) != 1 {
		t.Fatalf("retry after %d min expected, jobs %d", set.Int("health_retry_minutes"), len(jobs))
	}
	st.DB.Exec(`UPDATE streams SET checked_at=?`, time.Now().Unix()-60)
	if jobs := c.autoJobs(time.Now().Unix()); len(jobs) != 0 {
		t.Fatal("not yet time to retry")
	}
	fail()
	if s, n := status(); s != store.StatusDead || n != 3 {
		t.Fatalf("third failure marks it: status %d fails %d", s, n)
	}
	st.RecordProbe(store.Probe{URL: "http://a/1", OK: true}, 3, 10)
	var reason string
	st.DB.QueryRow(`SELECT fail_reason FROM streams`).Scan(&reason)
	if s, n := status(); s != store.StatusOK || n != 0 || reason != "" {
		t.Fatal("a success clears the failures")
	}
	// the first check of a stream that was never checked has nothing to protect: one failure is enough
	addStream(t, st, 0, "B", "http://b/1", store.StatusUnknown, 0)
	st.RecordProbe(store.Probe{URL: "http://b/1", Reason: "Timed out"}, 3, 10)
	var bs, bn int
	st.DB.QueryRow(`SELECT status, fail_count FROM streams WHERE url='http://b/1'`).Scan(&bs, &bn)
	if bs != store.StatusDead || bn != 1 {
		t.Fatalf("a failed first check marks it not working: status %d fails %d", bs, bn)
	}
	// and it is still retried soon, until it has failed `threshold` times in a row
	st.DB.Exec(`UPDATE streams SET checked_at=?`, time.Now().Unix()-10*60)
	if jobs := c.autoJobs(time.Now().Unix()); len(jobs) != 1 || jobs[0].url != "http://b/1" {
		t.Fatalf("the failed stream is due for a retry, got %+v", jobs)
	}
	st.RecordProbe(store.Probe{URL: "http://b/1", Reason: "Timed out"}, 3, 10)
	st.RecordProbe(store.Probe{URL: "http://b/1", Reason: "Timed out"}, 3, 10)
	st.DB.Exec(`UPDATE streams SET checked_at=?`, time.Now().Unix()-10*60)
	for _, j := range c.autoJobs(time.Now().Unix()) {
		if j.url == "http://b/1" {
			t.Fatal("after the threshold it waits for the normal interval")
		}
	}
	// a viewer's failed playback marks it at once
	addStream(t, st, 0, "D", "http://d/1", store.StatusOK, 0)
	st.MarkPlayback("http://d/1", false)
	st.DB.QueryRow(`SELECT status FROM streams WHERE url='http://d/1'`).Scan(&bs)
	if bs != store.StatusDead {
		t.Fatal("playback failure is immediate")
	}
}

func TestProbeReasons(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/ok.m3u8", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("#EXTM3U\n#EXTINF:1,x\na.ts\n")) })
	mux.HandleFunc("/404", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	mux.HandleFunc("/403", func(w http.ResponseWriter, r *http.Request) { http.Error(w, "no", 403) })
	mux.HandleFunc("/html", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("<html>login</html>"))
	})
	mux.HandleFunc("/bad.m3u8", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("hello")) })
	mux.HandleFunc("/bad.mpd", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("hello")) })
	mux.HandleFunc("/empty", func(w http.ResponseWriter, r *http.Request) {})
	// a master playlist is followed to its first variant (relative to the master's address)
	master := func(variant string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte("#EXTM3U\n#EXT-X-I-FRAME-STREAM-INF:URI=\"iframes.m3u8\"\n#EXT-X-STREAM-INF:BANDWIDTH=1\n" + variant + "\n#EXT-X-STREAM-INF:BANDWIDTH=2\n/ok.m3u8\n"))
		}
	}
	mux.HandleFunc("/live/master.m3u8", master("media.m3u8"))
	mux.HandleFunc("/live/media.m3u8", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("#EXTM3U\n#EXTINF:6,\nseg1.ts\n")) })
	mux.HandleFunc("/dead/master.m3u8", master("gone.m3u8"))
	mux.HandleFunc("/nested/master.m3u8", master("/live/master.m3u8"))
	mux.HandleFunc("/noseg/master.m3u8", master("empty.m3u8"))
	mux.HandleFunc("/noseg/empty.m3u8", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:6\n")) })
	mux.HandleFunc("/novariant.m3u8", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\n"))
	})
	mux.HandleFunc("/loop.m3u8", master("/loop.m3u8"))
	// a long media playlist is read past the first 4 KB
	mux.HandleFunc("/long.m3u8", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("#EXTM3U\n" + strings.Repeat("#EXT-X-PROGRAM-DATE-TIME:2026-01-01T00:00:00Z\n", 200) + "#EXTINF:6,\nseg.ts\n"))
	})
	mux.HandleFunc("/ok.mpd", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<?xml version="1.0"?><MPD><Period id="1"></Period></MPD>`))
	})
	mux.HandleFunc("/noperiod.mpd", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`<MPD></MPD>`)) })
	// direct streams: the format is named; text instead of video fails, unknown binary still works
	mux.HandleFunc("/s.ts", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp2t")
		w.Write(append([]byte{1, 2, 3}, tsData()...)) // starts in the middle of a packet
	})
	mux.HandleFunc("/s.mp4", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("\x00\x00\x00\x18ftypisom\x00\x00\x02\x00"))
	})
	mux.HandleFunc("/s.flv", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("FLV\x01\x05\x00\x00\x00\x09")) })
	mux.HandleFunc("/radio", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ID3\x04\x00\x00\x00\x00\x00\x00")) })
	mux.HandleFunc("/binary", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte{0x00, 0x01, 0xfe, 0x9c, 0x10}) })
	mux.HandleFunc("/text", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp2t")
		w.Write([]byte("Stream not found or token expired"))
	})
	mux.HandleFunc("/json", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"error":"Ünauthorized"}`)) })
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) { time.Sleep(2 * time.Second) })
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c, _, set := newTest(t)
	set.Apply(map[string]any{"health_total_timeout_seconds": 2.0, "health_response_timeout_seconds": 1.0})
	client := c.newClient()

	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	closed := ln.Addr().String()
	ln.Close()

	for path, want := range map[string]string{
		"/ok.m3u8": "HTTP 200 · HLS", "/404": "HTTP 404", "/403": "HTTP 403", "/html": "Returned a web page", "/bad.m3u8": "Not a valid playlist",
		"/bad.mpd": "Not a valid manifest", "/empty": "Empty response", "/slow": "Timed out",
		"/live/master.m3u8": "HTTP 200 · HLS", "/dead/master.m3u8": "Variant playlist: HTTP 404", "/nested/master.m3u8": "HTTP 200 · HLS",
		"/noseg/master.m3u8": "Playlist has no segments", "/novariant.m3u8": "Playlist has no streams", "/loop.m3u8": "Not a valid playlist",
		"/long.m3u8": "HTTP 200 · HLS", "/ok.mpd": "HTTP 200 · DASH", "/noperiod.mpd": "Manifest has no periods",
		"/s.ts": "HTTP 200 · MPEG-TS", "/s.mp4": "HTTP 200 · MP4", "/s.flv": "HTTP 200 · FLV", "/radio": "HTTP 200 · MP3 or AAC",
		"/binary": "HTTP 200 · unknown format", "/text": "Returned text instead of video", "/json": "Returned text instead of video",
	} {
		r := probe(context.Background(), client, "UA", job{url: srv.URL + path})
		if r.reason != want || r.ok != strings.HasPrefix(want, "HTTP 200") {
			t.Errorf("%s: got ok=%v reason=%q, want %q", path, r.ok, r.reason, want)
		}
	}
	if r := probe(context.Background(), client, "UA", job{url: "http://" + closed + "/x"}); r.reason != "Connection refused" {
		t.Errorf("refused: %q", r.reason)
	}
	if r := probe(context.Background(), client, "UA", job{url: "http://no-such-host.invalid/x"}); r.reason != "DNS lookup failed" {
		t.Errorf("dns: %q", r.reason)
	}
}

func TestTargets(t *testing.T) {
	c, st, _ := newTest(t)
	now := time.Now().Unix()
	addStream(t, st, 0, "A", "http://a/1", store.StatusOK, now)
	addStream(t, st, 0, "B", "http://b/1", store.StatusDead, now)
	addStream(t, st, 0, "C", "http://c/1", store.StatusUnknown, 0)
	st.DB.Exec(`INSERT INTO custom_sources(id,name) VALUES(7,'Mine')`)
	addStream(t, st, 7, "custom:7:x", "http://m/1", store.StatusOK, now)
	for _, ck := range []string{"A", "B", "C", "custom:7:x"} {
		st.DB.Exec(`INSERT INTO entries(ck,name) VALUES(?,?)`, ck, "Channel "+ck)
	}
	u, _ := st.CreateUser("owner", "x", "user")
	p, _ := st.CreateProfile(u.ID, "Living room")
	st.AddChannels(p.ID, []string{"A", "B"})

	count := func(t Target) int {
		jobs, _, err := c.JobsFor(t)
		if err != nil {
			return -1
		}
		return len(jobs)
	}
	for tg, want := range map[Target]int{
		{Kind: "profiles"}: 2, {Kind: "all"}: 4, {Kind: "bad"}: 1, {Kind: "unchecked"}: 1,
		{Kind: "profile", ID: fmt.Sprint(p.ID)}: 2, {Kind: "source", ID: "7"}: 1, {Kind: "source", ID: "0"}: 3, {Kind: "channel", ID: "C"}: 1,
		{Kind: "profile", ID: "999"}: -1, {Kind: "source", ID: "42"}: -1, {Kind: "channel", ID: "nope"}: -1, {Kind: "weird"}: -1, {Kind: "profile", ID: "x"}: -1,
	} {
		if got := count(tg); got != want {
			t.Errorf("%+v: %d streams, want %d", tg, got, want)
		}
	}
	// labels name what is checked
	if _, label, _ := c.JobsFor(Target{Kind: "source", ID: "7"}); label != "Source “Mine”" {
		t.Fatalf("label %q", label)
	}
}

func TestQuietHours(t *testing.T) {
	at := func(h, m int) time.Time { return time.Date(2026, 1, 1, h, m, 0, 0, time.UTC) }
	cases := []struct {
		now        time.Time
		start, end string
		want       bool
	}{
		{at(23, 30), "23:00", "06:00", true}, {at(3, 0), "23:00", "06:00", true}, {at(6, 0), "23:00", "06:00", false}, {at(12, 0), "23:00", "06:00", false},
		{at(10, 0), "09:00", "17:00", true}, {at(17, 0), "09:00", "17:00", false}, {at(8, 59), "09:00", "17:00", false},
		{at(12, 0), "", "06:00", false}, {at(12, 0), "09:00", "", false}, {at(12, 0), "09:00", "09:00", false}, {at(12, 0), "nope", "10:00", false},
	}
	for _, c := range cases {
		if got := inQuiet(c.now, c.start, c.end); got != c.want {
			t.Errorf("%v in %s-%s: got %v", c.now.Format("15:04"), c.start, c.end, got)
		}
	}
	c, _, set := newTest(t)
	if c.Quiet() {
		t.Fatal("no quiet hours by default")
	}
	now := time.Now()
	set.Apply(map[string]any{"health_quiet_start": now.Add(-time.Hour).Format("15:04"), "health_quiet_end": now.Add(time.Hour).Format("15:04"), "time_zone": time.Local.String()})
	if !c.Quiet() || c.autoAllowed() {
		t.Fatal("inside quiet hours automatic checks are not allowed")
	}
	if !c.Details().Quiet {
		t.Fatal("details must say it is quiet")
	}
}

func TestPauseAndScopeGateAutomaticChecks(t *testing.T) {
	c, _, set := newTest(t)
	if !c.autoAllowed() {
		t.Fatal("allowed by default")
	}
	c.SetPaused(true)
	if c.autoAllowed() || !c.Details().Paused {
		t.Fatal("paused")
	}
	c.SetPaused(false)
	set.Apply(map[string]any{"health_scope": "off"})
	if c.autoAllowed() {
		t.Fatal("scope off")
	}
}

// slowServer answers every stream after d and records how many requests were in flight at once.
type slowServer struct {
	*httptest.Server
	hits, inflight, maxInflight atomic.Int32
}

func newSlowServer(d time.Duration, onHit func(n int32)) *slowServer {
	s := &slowServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := s.hits.Add(1)
		cur := s.inflight.Add(1)
		for {
			m := s.maxInflight.Load()
			if cur <= m || s.maxInflight.CompareAndSwap(m, cur) {
				break
			}
		}
		if onHit != nil {
			onHit(n)
		}
		time.Sleep(d)
		s.inflight.Add(-1)
		w.Header().Set("Content-Type", "video/mp2t")
		w.Write(tsData())
	}))
	return s
}

// tsData is the start of an MPEG-TS stream: sync bytes every 188 bytes.
func tsData() []byte {
	b := make([]byte, 3*188)
	for i := 0; i < len(b); i += 188 {
		b[i] = 0x47
	}
	return b
}

func addMany(t *testing.T, st *store.Store, base string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		addStream(t, st, 0, fmt.Sprintf("C%d", i), fmt.Sprintf("%s/s%d.ts", base, i), store.StatusUnknown, 0)
	}
}

func TestSwitchingScopeOffStopsARunningPass(t *testing.T) {
	c, st, set := newTest(t)
	srv := newSlowServer(0, func(n int32) {
		if n == 3 {
			set.Apply(map[string]any{"health_scope": "off"})
		}
	})
	defer srv.Close()
	addMany(t, st, srv.URL, 40)
	set.Apply(map[string]any{"health_scope": "all", "health_concurrency": 1.0})
	c.pass(t.Context(), nil)
	if h := srv.hits.Load(); h < 3 || h >= 40 {
		t.Fatalf("expected the pass to stop soon after the switch, probed %d of 40", h)
	}
	rounds, _ := st.Rounds(0, 10)
	if len(rounds) != 1 || !rounds[0].Stopped || rounds[0].Kind != "auto" {
		t.Fatalf("the round is recorded as stopped: %+v", rounds)
	}
}

func TestStopCancelsAndDoesNotCountAbortedProbesAsFailures(t *testing.T) {
	c, st, set := newTest(t)
	srv := newSlowServer(400*time.Millisecond, nil)
	defer srv.Close()
	addMany(t, st, srv.URL, 20)
	set.Apply(map[string]any{"health_concurrency": 4.0})
	if c.Stop() {
		t.Fatal("nothing to stop yet")
	}
	n, err := c.CheckNow(Target{Kind: "all"})
	if err != nil || n != 20 {
		t.Fatalf("CheckNow: %d %v", n, err)
	}
	if _, err := c.CheckNow(Target{Kind: "all"}); err != nil && err != ErrBusy {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); req := <-c.trigger; c.pass(ctx, &req) }()
	time.Sleep(250 * time.Millisecond) // first probes are in flight
	if !c.Stop() {
		t.Fatal("a running check can be stopped")
	}
	wg.Wait()
	var bad int
	st.DB.QueryRow(`SELECT COUNT(*) FROM streams WHERE status=2 OR fail_count>0`).Scan(&bad)
	if bad != 0 {
		t.Fatalf("%d streams were blamed for a request that was aborted by Stop", bad)
	}
	if running, _, _ := c.Progress(); running {
		t.Fatal("still running")
	}
	rounds, _ := st.Rounds(0, 10)
	if len(rounds) != 1 || !rounds[0].Stopped || rounds[0].Kind != "manual" || rounds[0].Label != "Every stream" {
		t.Fatalf("round: %+v", rounds)
	}
	if srv.hits.Load() >= 20 {
		t.Fatal("stop must end the round early")
	}
}

func TestParallelChecksChangeWhileRunning(t *testing.T) {
	c, st, set := newTest(t)
	srv := newSlowServer(80*time.Millisecond, nil)
	defer srv.Close()
	addMany(t, st, srv.URL, 40)
	set.Apply(map[string]any{"health_scope": "all", "health_concurrency": 1.0, "health_per_host_concurrency": 10.0})
	go func() {
		time.Sleep(400 * time.Millisecond)
		set.Apply(map[string]any{"health_concurrency": 10.0}) // sped up from the page while running
	}()
	req := &request{label: "x", jobs: c.queryJobs(`SELECT url,'','' FROM streams s GROUP BY url`)}
	c.pass(t.Context(), req)
	if m := srv.maxInflight.Load(); m < 5 {
		t.Fatalf("raising the setting during a pass must add parallel checks, peak was %d", m)
	}
	if srv.hits.Load() != 40 {
		t.Fatalf("all streams checked: %d", srv.hits.Load())
	}
}

func TestHistoryRetention(t *testing.T) {
	c, st, set := newTest(t)
	srv := newSlowServer(0, nil)
	defer srv.Close()
	addMany(t, st, srv.URL, 3)
	set.Apply(map[string]any{"health_history_rounds": 2.0, "health_history_per_channel": 3.0})
	for i := 0; i < 5; i++ {
		jobs, label, _ := c.JobsFor(Target{Kind: "all"})
		c.pass(t.Context(), &request{label: label, jobs: jobs})
	}
	if _, total := st.Rounds(0, 10); total != 2 {
		t.Fatalf("rounds kept: %d, limit 2", total)
	}
	cd, _ := func() (ChannelDetail, bool) {
		st.DB.Exec(`INSERT INTO entries(ck,name) VALUES('C0','C0')`)
		return c.Channel("C0", 0)
	}()
	if len(cd.History) != 3 {
		t.Fatalf("per-channel results kept: %d, limit 3", len(cd.History))
	}
	// lowering a limit deletes the surplus immediately, 0 keeps nothing
	set.Apply(map[string]any{"health_history_per_channel": 1.0, "health_history_rounds": 1.0})
	cd, _ = c.Channel("C0", 0)
	if _, total := st.Rounds(0, 10); total != 1 || len(cd.History) != 1 {
		t.Fatalf("after lowering: rounds %d, channel results %d", total, len(cd.History))
	}
	set.Apply(map[string]any{"health_history_per_channel": 0.0, "health_history_rounds": 0.0})
	cd, _ = c.Channel("C0", 0)
	if _, total := st.Rounds(0, 10); total != 0 || len(cd.History) != 0 {
		t.Fatal("0 keeps no history")
	}
	jobs, label, _ := c.JobsFor(Target{Kind: "all"})
	c.pass(t.Context(), &request{label: label, jobs: jobs})
	cd, _ = c.Channel("C0", 0)
	if _, total := st.Rounds(0, 10); total != 0 || len(cd.History) != 0 {
		t.Fatal("with 0 nothing is recorded any more")
	}
	// the check itself still works without history
	var ok int
	st.DB.QueryRow(`SELECT COUNT(*) FROM streams WHERE status=1`).Scan(&ok)
	if ok != 3 {
		t.Fatalf("working streams: %d", ok)
	}
}

func TestTrendHasTotalsPerRound(t *testing.T) {
	c, st, _ := newTest(t)
	srv := newSlowServer(0, nil)
	defer srv.Close()
	addMany(t, st, srv.URL, 4)
	jobs, label, _ := c.JobsFor(Target{Kind: "all"})
	c.pass(t.Context(), &request{label: label, jobs: jobs})
	tr := st.Trend(10)
	if len(tr) != 1 || tr[0].OK != 4 || tr[0].TotalOK != 4 || tr[0].TotalBad != 0 || tr[0].Probed != 4 || tr[0].Finished == 0 {
		t.Fatalf("trend %+v", tr)
	}
}

func TestHistoryTotalAndAgeLimits(t *testing.T) {
	_, st, set := newTest(t)
	addMany(t, st, "http://x", 30)
	now := time.Now().Unix()
	for i := 0; i < 30; i++ {
		age := int64(0)
		if i < 10 {
			age = 40 * 86400 // ten results from 40 days ago
		}
		st.DB.Exec(`INSERT INTO health_checks(ck,url,at,ok,reason,ms) VALUES(?,?,?,1,'',0)`, fmt.Sprintf("C%d", i), "http://x", now-age, 0)
	}
	count := func() (n int) { st.DB.QueryRow(`SELECT COUNT(*) FROM health_checks`).Scan(&n); return }
	st.PruneHealth(store.HistoryLimits{Rounds: 10, PerChannel: 50, Total: 1000, MaxAgeDays: 30})
	if n := count(); n != 20 {
		t.Fatalf("results older than 30 days are deleted: %d left", n)
	}
	st.PruneHealth(store.HistoryLimits{Rounds: 10, PerChannel: 50, Total: 5, MaxAgeDays: 30})
	if n := count(); n != 5 {
		t.Fatalf("overall cap: %d left", n)
	}
	_ = set
}

func TestRoundResults(t *testing.T) {
	c, st, set := newTest(t)
	set.Apply(map[string]any{"health_scope": "off"})
	mux := http.NewServeMux()
	mux.HandleFunc("/s0.ts", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp2t")
		w.Write(tsData())
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	srv := httptest.NewServer(mux)
	defer srv.Close()
	addMany(t, st, srv.URL, 3)
	for i := 0; i < 3; i++ {
		st.DB.Exec(`INSERT INTO entries(ck,name) VALUES(?,?)`, fmt.Sprintf("C%d", i), fmt.Sprintf("Channel %d", i))
	}
	jobs, label, _ := c.JobsFor(Target{Kind: "all"})
	c.pass(t.Context(), &request{label: label, jobs: jobs})

	d, ok := c.Round(RoundQuery{ID: 1})
	if !ok || d.Stored != 3 || d.OK != 1 || d.Bad != 2 || d.Total != 3 || len(d.Items) != 3 {
		t.Fatalf("round: %+v", d)
	}
	if d.Items[0].OK || d.Items[0].Reason != "HTTP 404" || d.Items[0].Name != "Channel 1" {
		t.Fatalf("failures come first with the channel name and why: %+v", d.Items)
	}
	if d.Items[2].Reason != "HTTP 200 · MPEG-TS" {
		t.Fatalf("a working stream keeps its status code and format: %+v", d.Items[2])
	}
	if d, _ = c.Round(RoundQuery{ID: 1, Result: "bad"}); d.Total != 2 {
		t.Fatalf("bad only: %d", d.Total)
	}
	if d, _ = c.Round(RoundQuery{ID: 1, Q: "channel 2"}); d.Total != 1 || d.Items[0].CK != "C2" {
		t.Fatalf("search: %+v", d)
	}
	if _, ok := c.Round(RoundQuery{ID: 99}); ok {
		t.Fatal("unknown round")
	}
}

func TestPerHostConcurrencyCapsOneServerAndChangesLive(t *testing.T) {
	c, st, set := newTest(t)
	srv := newSlowServer(60*time.Millisecond, nil)
	defer srv.Close()
	addMany(t, st, srv.URL, 24)
	set.Apply(map[string]any{"health_concurrency": 20.0, "health_per_host_concurrency": 2.0})
	run := func() {
		c.pass(t.Context(), &request{label: "x", jobs: c.queryJobs(`SELECT url,'','' FROM streams s GROUP BY url`)})
	}
	run()
	if m := srv.maxInflight.Load(); m > 2 {
		t.Fatalf("one server must never see more than 2 parallel checks, peak was %d", m)
	}
	if srv.hits.Load() != 24 {
		t.Fatalf("all streams checked: %d", srv.hits.Load())
	}
	// raised without a restart: the next round uses the new limit
	srv.maxInflight.Store(0)
	set.Apply(map[string]any{"health_per_host_concurrency": 8.0})
	run()
	if m := srv.maxInflight.Load(); m < 4 || m > 8 {
		t.Fatalf("the raised limit must apply to the next round, peak was %d", m)
	}
}

func TestProbeSendsStreamHeadersToVariant(t *testing.T) {
	var seen []string
	mux := http.NewServeMux()
	mux.HandleFunc("/m.m3u8", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\nv.m3u8\n"))
	})
	mux.HandleFunc("/v.m3u8", func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.UserAgent()+"|"+r.Referer()+"|"+r.Header.Get("Origin"))
		w.Write([]byte("#EXTM3U\n#EXTINF:6,\ns.ts\n"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c, _, _ := newTest(t)
	r := probe(t.Context(), c.newClient(), "Default", job{url: srv.URL + "/m.m3u8", ua: "MyPlayer", ref: "https://site.example/page"})
	if !r.ok || len(seen) != 1 || seen[0] != "MyPlayer|https://site.example/page|https://site.example" {
		t.Fatalf("the variant is asked with the stream's headers: %+v %q", r, seen)
	}
}

// The total timeout covers the whole probe, not each request: a master and its variant that each answer in time but
// together take too long fail. A changed timeout applies to the next round without a restart.
func TestTotalTimeoutCoversMasterAndVariant(t *testing.T) {
	slow := func(body string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(1200 * time.Millisecond)
			w.Write([]byte(body))
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/m.m3u8", slow("#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\nv.m3u8\n"))
	mux.HandleFunc("/v.m3u8", slow("#EXTM3U\n#EXTINF:6,\ns.ts\n"))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c, st, set := newTest(t)
	set.Apply(map[string]any{"health_scope": "off", "health_total_timeout_seconds": 2.0, "health_response_timeout_seconds": 2.0})
	addStream(t, st, 0, "S", srv.URL+"/m.m3u8", store.StatusUnknown, 0)
	run := func() (int, string) {
		c.pass(t.Context(), &request{label: "x", jobs: c.queryJobs(`SELECT url,'','' FROM streams s`)})
		var status int
		var reason string
		st.DB.QueryRow(`SELECT status, fail_reason FROM streams`).Scan(&status, &reason)
		return status, reason
	}
	if status, reason := run(); status != store.StatusDead || !strings.Contains(reason, "Timed out") {
		t.Fatalf("2.4 s does not fit in 2 s: %d %q", status, reason)
	}
	set.Apply(map[string]any{"health_total_timeout_seconds": 4.0})
	if status, reason := run(); status != store.StatusOK {
		t.Fatalf("2.4 s fits in 4 s: %d %q", status, reason)
	}
}
