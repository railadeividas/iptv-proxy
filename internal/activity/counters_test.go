package activity

import "testing"

// Counters only grow: sessions, bytes, playlist and guide fetches, per profile and overall, and they stay when
// a session ends (unlike what is listed as watching now).
func TestCounters(t *testing.T) {
	tr, _, _ := setup(t)
	r := req("10.0.0.5", "VLC")
	tr.Client(r, 1, "playlist")
	tr.Client(r, 1, "playlist")
	tr.Client(r, 2, "guide")
	l := tr.Begin(r, 1, nil, "A", Proxy, "")
	l.Add(700)
	l.Add(300)
	l.End()
	tr.Begin(r, 1, nil, "A", Proxy, "").End() // the same session again: not a new one
	tr.Begin(r, 1, nil, "B", Redirect, "").End()
	tr.Begin(req("10.0.0.6", "Kodi"), 2, nil, "A", Proxy, "").End()

	all, per := tr.Counters()
	if all.Playlists != 2 || all.Guides != 1 || all.Proxy != 2 || all.Redirect != 1 || all.Bytes != 1000 {
		t.Fatalf("all: %+v", all)
	}
	if a := per[1]; a.Playlists != 2 || a.Proxy != 1 || a.Redirect != 1 || a.Bytes != 1000 {
		t.Fatalf("profile 1: %+v", a)
	}
	if b := per[2]; b.Guides != 1 || b.Proxy != 1 || b.Bytes != 0 {
		t.Fatalf("profile 2: %+v", b)
	}

	tr.Flush(true) // every session ends
	if len(tr.Now(0)) != 0 {
		t.Fatal("nobody is watching")
	}
	if all2, _ := tr.Counters(); all2 != all {
		t.Fatalf("counters survive the end of sessions: %+v", all2)
	}
}

func TestCountersStopWhenRecordingIsOff(t *testing.T) {
	tr, _, set := setup(t)
	apply(t, set, map[string]any{"stats_enabled": "off"})
	tr.Client(req("10.0.0.5", "x"), 1, "playlist")
	tr.Begin(req("10.0.0.5", "x"), 1, nil, "A", Proxy, "").End()
	if all, _ := tr.Counters(); all != (Counters{}) {
		t.Fatalf("nothing counted while off: %+v", all)
	}
}
