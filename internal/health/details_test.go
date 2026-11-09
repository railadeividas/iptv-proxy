package health

import (
	"fmt"
	"testing"
	"time"

	"iptv-proxy/internal/settings"
	"iptv-proxy/internal/store"
)

func newTest(t *testing.T) (*Checker, *store.Store, *settings.Settings) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	set := settings.New(st)
	// no quiet hours by default in tests, so they do not depend on the time of day they run at
	set.Apply(map[string]any{"health_quiet_start": "", "health_quiet_end": ""})
	return New(st, set), st, set
}

func addStream(t *testing.T, st *store.Store, src int, ck, url string, status int, at int64) {
	t.Helper()
	if _, err := st.DB.Exec(`INSERT INTO streams(src,ck,url,status,checked_at) VALUES(?,?,?,?,?)`, src, ck, url, status, at); err != nil {
		t.Fatal(err)
	}
}

func TestDetails(t *testing.T) {
	c, st, set := newTest(t)
	now := time.Now().Unix()
	addStream(t, st, 0, "A", "http://a/1", store.StatusOK, now-60)
	addStream(t, st, 0, "B", "http://b/1", store.StatusDead, now-30)
	addStream(t, st, 0, "B", "http://b/2", store.StatusDead, now-30)
	addStream(t, st, 0, "C", "http://c/1", store.StatusUnknown, 0)
	st.DB.Exec(`INSERT INTO custom_sources(id,name) VALUES(7,'Mine')`)
	addStream(t, st, 7, "custom:7:x", "http://m/1", store.StatusOK, now-7200)
	for _, ck := range []string{"A", "B", "C"} {
		st.DB.Exec(`INSERT INTO entries(ck,name) VALUES(?,?)`, ck, "Channel "+ck)
	}
	st.DB.Exec(`UPDATE streams SET fail_reason='HTTP 404' WHERE ck='B'`)
	u, _ := st.CreateUser("owner", "x", "user")
	p, _ := st.CreateProfile(u.ID, "Living room")
	if err := st.AddChannels(p.ID, []string{"A", "B", "C"}); err != nil {
		t.Fatal(err)
	}

	d := c.Details()
	if d.Totals != (Counts{Streams: 5, OK: 2, Bad: 2, Unchecked: 1}) {
		t.Fatalf("totals %+v", d.Totals)
	}
	if d.Fresh.Hour != 3 || d.Fresh.Day != 1 || d.Fresh.Never != 1 {
		t.Fatalf("freshness %+v", d.Fresh)
	}
	if d.Profiles.Channels != 3 || d.Profiles.ChannelsDown != 1 {
		t.Fatalf("profile health %+v", d.Profiles)
	}
	page := c.Down(DownQuery{})
	if page.Total != 1 || page.Items[0].CK != "B" || page.Items[0].Reason != "HTTP 404" || page.Items[0].Profiles[0].Name != "Living room" ||
		len(page.Reasons) != 1 || page.Reasons[0].Reason != "HTTP 404" {
		t.Fatalf("down %+v", page)
	}
	if c.Down(DownQuery{Reason: "Timed out"}).Total != 0 || c.Down(DownQuery{Q: "channel b"}).Total != 1 || c.Down(DownQuery{Q: "zzz"}).Total != 0 {
		t.Fatal("down filters")
	}
	// the never-checked stream of C is waiting; the 2h-old custom one is not in a profile and within 12h
	if d.Due != 1 {
		t.Fatalf("due %d", d.Due)
	}
	if len(d.Sources) != 2 || d.Sources[0].Name != "iptv-org" || d.Sources[1].Name != "Mine" || d.Sources[1].ID != 7 || d.Sources[1].OK != 1 {
		t.Fatalf("sources %+v", d.Sources)
	}

	// settings apply to the very next look
	set.Apply(map[string]any{"health_scope": "selected"})
	if sel := c.Details(); sel.Scope != "selected" || sel.Due != 1 {
		t.Fatalf("scope selected: %+v", sel)
	}
	set.Apply(map[string]any{"health_scope": "off", "health_concurrency": 7.0})
	if off := c.Details(); off.Due != 0 || off.Concurrency != 7 {
		t.Fatalf("scope off must not queue anything: %+v", off)
	}

	// channel view: streams with reasons, and the stored history
	st.RecordProbe(store.Probe{URL: "http://b/1", Reason: "HTTP 404", MS: 12}, 1, 50)
	cd, ok := c.Channel("B", 0)
	if !ok || cd.Name != "Channel B" || len(cd.Streams) != 2 || cd.Streams[0].Reason != "HTTP 404" || cd.Streams[0].Host != "b" || len(cd.History) != 1 || cd.History[0].Host != "b" || cd.History[0].OK {
		t.Fatalf("channel %+v", cd)
	}
	if _, ok := c.Channel("nope", 0); ok {
		t.Fatal("unknown channel")
	}
}

// One failed check does not flip a working stream (the history shows it, the status stays working) until the failures in a row reach the setting.
func TestFailedCheckKeepsAWorkingStreamWorkingUntilTheThreshold(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	st.DB.Exec(`INSERT INTO streams(url,ck,src,status) VALUES('http://x/1','A@SD','iptv-org',1)`)
	status := func() (int, int) {
		var s, f int
		st.DB.QueryRow(`SELECT status, fail_count FROM streams WHERE url='http://x/1'`).Scan(&s, &f)
		return s, f
	}
	st.RecordProbe(store.Probe{URL: "http://x/1", Reason: "timeout"}, 2, 50)
	if s, f := status(); s != store.StatusOK || f != 1 {
		t.Fatalf("first failure: status %d, %d failed", s, f)
	}
	st.RecordProbe(store.Probe{URL: "http://x/1", OK: true}, 2, 50)
	st.RecordProbe(store.Probe{URL: "http://x/1", Reason: "timeout"}, 2, 50)
	if s, f := status(); s != store.StatusOK || f != 1 {
		t.Fatalf("a success in between starts the count again: status %d, %d failed", s, f)
	}
	st.RecordProbe(store.Probe{URL: "http://x/1", Reason: "timeout"}, 2, 50)
	if s, _ := status(); s != store.StatusDead {
		t.Fatalf("two in a row: not working, got %d", s)
	}
}

func TestNextCheck(t *testing.T) {
	c, st, set := newTest(t)
	now := time.Now().Unix()
	if c.Details().Next != 0 && c.Details().Scope == "off" {
		t.Fatal("no check planned while off")
	}
	set.Apply(map[string]any{"health_scope": "selected", "health_selected_interval_hours": 1.0})
	// a profile channel last checked 20 minutes ago is due in about 40 minutes
	u, _ := st.CreateUser("o", "x", "user")
	p, _ := st.CreateProfile(u.ID, "P")
	addStream(t, st, 0, "A", "http://a/1", store.StatusOK, 0)
	st.DB.Exec(`INSERT INTO entries(ck,name) VALUES('A','A')`)
	st.AddChannels(p.ID, []string{"A"})
	st.DB.Exec(`UPDATE streams SET checked_at=?`, now-20*60)
	if n := c.Details().Next; n < now+39*60 || n > now+41*60 {
		t.Fatalf("next check in %d s, want about 2400", n-now)
	}
	// something already due means "now"
	st.DB.Exec(`UPDATE streams SET checked_at=?`, now-2*3600)
	if n := c.Details().Next; n < now || n > now+2 {
		t.Fatalf("due now, got %d s from now", n-now)
	}
	set.Apply(map[string]any{"health_scope": "off"})
	if c.Details().Next != 0 {
		t.Fatal("off plans nothing")
	}
	set.Apply(map[string]any{"health_scope": "selected"})
	c.SetPaused(true)
	if c.Details().Next != 0 {
		t.Fatal("paused plans nothing")
	}
}

func TestChannelStatesAddUp(t *testing.T) {
	c, st, set := newTest(t)
	add := func(ck string, statuses ...int) {
		st.DB.Exec(`INSERT INTO entries(ck,name) VALUES(?,?)`, ck, ck)
		for i, s := range statuses {
			addStream(t, st, 0, ck, fmt.Sprintf("http://%s/%d", ck, i), s, 0)
		}
	}
	add("G", store.StatusOK, store.StatusUnknown)   // working: a working stream and none failing
	add("W", store.StatusOK, store.StatusDead)      // partly working: one works, one fails
	add("D", store.StatusDead, store.StatusDead)    // not working: all streams fail
	add("M", store.StatusDead, store.StatusUnknown) // a failed and an unchecked stream: no verdict yet
	add("P", store.StatusUnknown)                   // in a profile, never checked
	add("O", store.StatusUnknown)                   // outside every profile
	u, _ := st.CreateUser("o", "x", "user")
	p, _ := st.CreateProfile(u.ID, "P")
	st.AddChannels(p.ID, []string{"P"})

	check := func(scope string, working, partly, down, pending, notChecked int) {
		t.Helper()
		set.Apply(map[string]any{"health_scope": scope})
		s := c.Details().ChannelStates
		if s.Total != 6 || s.Working != working || s.Partly != partly || s.NotWorking != down || s.Pending != pending || s.NotChecked != notChecked {
			t.Fatalf("scope %s: %+v, want %d/%d/%d/%d/%d of 6", scope, s, working, partly, down, pending, notChecked)
		}
		if s.Working+s.Partly+s.NotWorking+s.Pending+s.NotChecked != s.Total {
			t.Fatalf("the states must add up to the total: %+v", s)
		}
	}
	check("all", 1, 1, 1, 3, 0)      // everything is going to be checked
	check("selected", 1, 1, 1, 1, 2) // only P is; M and O are outside the checks
	check("off", 1, 1, 1, 0, 3)      // nothing is checked automatically

	// an excluded failing stream does not make a channel partly working ...
	st.SetExcluded("http://W/1", true)
	check("off", 2, 0, 1, 0, 3)
	// ... but a channel whose streams are all excluded keeps their last results
	st.SetExcluded("http://D/0", true)
	st.SetExcluded("http://D/1", true)
	check("off", 2, 0, 1, 0, 3)
	// and the stream bars count excluded streams apart from their last result
	d := c.Details()
	if d.Totals.Excluded != 3 || d.Totals.Bad != 1 || d.Totals.OK != 2 || d.Totals.Streams != 10 {
		t.Fatalf("stream totals: %+v", d.Totals)
	}
}

// A viewer's playback changes the status too, so it must show in the history (it used to flip a stream back to working with no trace),
// but only when the status changes: a viewer retrying a dead stream or playing a working one does not fill the history.
func TestPlaybackShowsInTheHistoryWhenItChangesTheStatus(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	st.DB.Exec(`INSERT INTO streams(url,ck,src,status) VALUES('http://x/1','A@SD','iptv-org',1)`)
	rows := func() (n int, last string) {
		st.DB.QueryRow(`SELECT COUNT(*), COALESCE((SELECT reason FROM health_checks ORDER BY at DESC, id DESC LIMIT 1),'') FROM health_checks`).Scan(&n, &last)
		return
	}
	st.MarkPlayback("http://x/1", true, 50) // working and clean: nothing to explain
	if n, _ := rows(); n != 0 {
		t.Fatalf("a normal playback is not history: %d rows", n)
	}
	st.RecordProbe(store.Probe{URL: "http://x/1", Reason: "Connection refused"}, 3, 50) // still working, one failure
	st.MarkPlayback("http://x/1", true, 50)                                             // the status stays working: not a change
	if n, _ := rows(); n != 1 {
		t.Fatalf("working stays working: %d rows (the probe only)", n)
	}
	for i := 0; i < 5; i++ {
		st.MarkPlayback("http://x/1", false, 50) // the stream goes down, and the viewer retries
	}
	if n, last := rows(); n != 2 || last != "Playback failed" {
		t.Fatalf("one row when it went down, not one per retry: %d rows, last %q", n, last)
	}
	st.MarkPlayback("http://x/1", true, 50) // not working -> working
	if n, last := rows(); n != 3 || last != "Played by a viewer" {
		t.Fatalf("the status changed back: %d rows, last %q", n, last)
	}
	st.MarkPlayback("http://x/1", false, 0) // history switched off: nothing is written
	if n, _ := rows(); n != 3 {
		t.Fatalf("history off: %d rows", n)
	}
}
