package activity

import (
	"testing"
	"time"
)

func sampleRow(t *testing.T, tr *Tracker, hour int64) (hits, misses, retries int64) {
	t.Helper()
	tr.st.DB.QueryRow(`SELECT COALESCE(SUM(cache_hits),0), COALESCE(SUM(cache_misses),0), COALESCE(SUM(retries),0) FROM stats_samples WHERE hour=?`, hour).Scan(&hits, &misses, &retries)
	return
}

func fetchRow(t *testing.T, tr *Tracker, hour, profile int64) (playlists, guides int64) {
	t.Helper()
	tr.st.DB.QueryRow(`SELECT COALESCE(SUM(playlists),0), COALESCE(SUM(guides),0) FROM stats_profile_samples WHERE hour=? AND profile_id=?`, hour, profile).Scan(&playlists, &guides)
	return
}

// Each flush adds the increase of the counters to the row of the hour; a counter that starts over (a restart) counts
// from zero again; the counters themselves are never touched.
func TestSamplesAddTheIncreasePerHour(t *testing.T) {
	tr, _, _ := setup(t)
	var srv ServerCounters
	tr.SetServerSource(func() ServerCounters { return srv })
	h1 := time.Date(2030, 1, 1, 10, 0, 0, 0, time.UTC)
	h2 := h1.Add(time.Hour)

	r := req("10.0.0.5", "VLC")
	tr.Client(r, 1, "playlist")
	tr.Client(r, 1, "playlist")
	tr.Client(r, 2, "guide")
	srv = ServerCounters{CacheHits: 10, CacheMisses: 4, Retries: 1}
	tr.sample(h1.Add(5 * time.Minute))
	if p, g := fetchRow(t, tr, h1.Unix(), 1); p != 2 || g != 0 {
		t.Fatalf("profile 1: %d %d", p, g)
	}
	if p, g := fetchRow(t, tr, h1.Unix(), 2); p != 0 || g != 1 {
		t.Fatalf("profile 2 is counted apart: %d %d", p, g)
	}
	if hits, misses, retries := sampleRow(t, tr, h1.Unix()); hits != 10 || misses != 4 || retries != 1 {
		t.Fatalf("server: %d %d %d", hits, misses, retries)
	}

	// the same hour again: only what is new is added
	tr.Client(r, 1, "guide")
	srv = ServerCounters{CacheHits: 15, CacheMisses: 4, Retries: 1}
	tr.sample(h1.Add(50 * time.Minute))
	if p, g := fetchRow(t, tr, h1.Unix(), 1); p != 2 || g != 1 {
		t.Fatalf("profile 1 after the second flush: %d %d", p, g)
	}
	if hits, misses, _ := sampleRow(t, tr, h1.Unix()); hits != 15 || misses != 4 {
		t.Fatalf("server after the second flush: %d %d", hits, misses)
	}

	// next hour, and the proxy restarted: its counters are lower than before
	tr.Client(r, 1, "playlist")
	srv = ServerCounters{CacheHits: 3}
	tr.sample(h2.Add(time.Minute))
	if hits, _, _ := sampleRow(t, tr, h2.Unix()); hits != 3 {
		t.Fatalf("a counter that started over counts its value: %d", hits)
	}
	if p, _ := fetchRow(t, tr, h2.Unix(), 1); p != 1 {
		t.Fatalf("second hour: %d", p)
	}
	if hits, _, _ := sampleRow(t, tr, h1.Unix()); hits != 15 {
		t.Fatal("the first hour is left alone")
	}

	// nothing new: no rows are made
	tr.sample(h2.Add(2 * time.Hour))
	var n int
	tr.st.DB.QueryRow(`SELECT COUNT(*) FROM stats_samples`).Scan(&n)
	if n != 2 {
		t.Fatalf("an hour without increase has no row: %d", n)
	}
	// the in-memory counters (what /metrics reads) are never reset
	if all, per := tr.Counters(); all.Playlists != 3 || all.Guides != 2 || per[1].Playlists != 3 {
		t.Fatalf("counters: %+v %+v", all, per)
	}
}

func TestSamplesFollowRecordingSwitch(t *testing.T) {
	tr, _, set := setup(t)
	var srv ServerCounters
	tr.SetServerSource(func() ServerCounters { return srv })
	h := time.Date(2030, 1, 1, 10, 0, 0, 0, time.UTC)
	apply(t, set, map[string]any{"stats_enabled": "off"})
	srv = ServerCounters{CacheHits: 50}
	tr.sample(h)
	if hits, _, _ := sampleRow(t, tr, h.Unix()); hits != 0 {
		t.Fatal("nothing is stored while recording is off")
	}
	apply(t, set, map[string]any{"stats_enabled": "on"})
	srv = ServerCounters{CacheHits: 52}
	tr.sample(h.Add(time.Minute))
	if hits, _, _ := sampleRow(t, tr, h.Unix()); hits != 2 {
		t.Fatalf("the time it was off is not counted afterwards: %d", hits)
	}
}

func TestSamplesWorkWithoutServerSource(t *testing.T) {
	tr, _, _ := setup(t)
	tr.Client(req("10.0.0.5", "VLC"), 1, "playlist")
	tr.Flush(false)
	var p int64
	tr.st.DB.QueryRow(`SELECT COALESCE(SUM(playlists),0) FROM stats_profile_samples WHERE profile_id=1`).Scan(&p)
	if p != 1 {
		t.Fatalf("fetches are sampled by Flush: %d", p)
	}
}

// Flush adds a point to the live ring: the proxied bytes so far and the viewers, per profile so that a user sees
// their own profiles only. Old points leave, and the ring is bounded.
func TestLiveRing(t *testing.T) {
	tr, _, _ := setup(t)
	l := tr.Begin(req("10.0.0.5", "VLC"), 1, nil, "A", Proxy, "h")
	l.Add(1000)
	l.End()
	l = tr.Begin(req("10.0.0.6", "Kodi"), 2, nil, "A", Proxy, "h")
	l.Add(300)
	l.End()
	tr.Begin(req("10.0.0.7", "Kodi"), 2, nil, "B", Redirect, "h").End()
	tr.Flush(false)

	all := tr.LiveSamples(nil)
	if len(all) != 1 || all[0].Bytes != 1300 || all[0].Watching != 2 || all[0].T == 0 {
		t.Fatalf("everybody: %+v", all)
	}
	mine := tr.LiveSamples(func(id int64) bool { return id == 1 })
	if len(mine) != 1 || mine[0].Bytes != 1000 || mine[0].Watching != 1 {
		t.Fatalf("one profile: %+v", mine)
	}
	if got := tr.LiveSamples(func(int64) bool { return false }); len(got) != 1 || got[0].Bytes != 0 || got[0].Watching != 0 {
		t.Fatalf("nobody in scope: %+v", got)
	}

	// the viewers drop when sessions go quiet; the bytes only grow
	for _, s := range tr.live {
		s.last.Store(time.Now().Add(-time.Hour).UnixNano())
	}
	tr.Flush(false)
	got := tr.LiveSamples(nil)
	if len(got) != 2 || got[1].Watching != 0 || got[1].Bytes != 1300 {
		t.Fatalf("second point: %+v", got)
	}

	// points older than an hour are dropped
	tr.ring[0].t = time.Now().Add(-2 * time.Hour).Unix()
	tr.Flush(false)
	if got := tr.LiveSamples(nil); len(got) != 2 {
		t.Fatalf("old points leave: %+v", got)
	}
	for i := 0; i < ringMax+50; i++ {
		tr.sample(time.Now())
	}
	if got := tr.LiveSamples(nil); len(got) != ringMax {
		t.Fatalf("the ring is bounded: %d", len(got))
	}
}
