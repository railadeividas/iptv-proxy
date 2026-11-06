package health

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"iptv-proxy/internal/store"
)

// An excluded stream is left out of automatic and manual checks, the due count and pending channels;
// including it again brings it back.
func TestExcludedStreamsAreNotChecked(t *testing.T) {
	c, st, set := newTest(t)
	set.Apply(map[string]any{"health_scope": "all"})
	addStream(t, st, 0, "A", "http://a/1", store.StatusUnknown, 0)
	addStream(t, st, 0, "B", "http://b/1", store.StatusUnknown, 0)
	st.DB.Exec(`INSERT INTO entries(ck,name) VALUES('A','A'),('B','B')`)
	now := time.Now().Unix()
	if n := len(c.autoJobs(now)); n != 2 {
		t.Fatalf("both due, got %d", n)
	}
	if st.SetExcluded("http://nope", true) {
		t.Fatal("an unknown address cannot be excluded")
	}
	st.SetExcluded("http://a/1", true)
	if jobs := c.autoJobs(now); len(jobs) != 1 || jobs[0].url != "http://b/1" {
		t.Fatalf("automatic checks skip the excluded stream: %+v", jobs)
	}
	for _, kind := range []string{"all", "unchecked", "source"} {
		jobs, _, err := c.JobsFor(Target{Kind: kind, ID: "0"})
		if err != nil || len(jobs) != 1 {
			t.Fatalf("manual %s check skips it: %+v %v", kind, jobs, err)
		}
	}
	d := c.Details()
	if d.Excluded != 1 || d.Due != 1 || d.ChannelStates.Pending != 1 || d.ChannelStates.NotChecked != 1 {
		t.Fatalf("details: excluded %d due %d states %+v", d.Excluded, d.Due, d.ChannelStates)
	}
	ch, _ := c.Channel("A", 0)
	if !ch.Streams[0].Excluded || ch.Streams[0].ExclAuto || ch.Streams[0].ExclSince == 0 {
		t.Fatalf("the channel shows the stream as excluded by hand: %+v", ch.Streams[0])
	}
	st.SetExcluded("http://a/1", false)
	if n := len(c.autoJobs(now)); n != 2 || st.ExcludedCount() != 0 {
		t.Fatal("included again, it is checked again")
	}
}

// A stream whose failures in a row began the set number of days ago is excluded by the checker; the setting applies
// without a restart, 0 switches it off, and a working check or including it again restarts the count.
func TestAutoExcludeAfterDaysOfFailing(t *testing.T) {
	c, st, set := newTest(t)
	fail := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail {
			http.Error(w, "gone", 404)
			return
		}
		w.Write([]byte("#EXTM3U\n#EXTINF:6,\nseg1.ts\n"))
	}))
	defer srv.Close()
	set.Apply(map[string]any{"health_scope": "off", "health_auto_exclude_days": 0.0})
	addStream(t, st, 0, "A", srv.URL+"/a.m3u8", store.StatusOK, 0)
	run := func() {
		jobs, label, err := c.JobsFor(Target{Kind: "all"})
		if err != nil {
			t.Fatal(err)
		}
		c.pass(context.Background(), &request{target: Target{Kind: "all"}, label: label, jobs: jobs})
	}
	since := func() (v int64) { st.DB.QueryRow(`SELECT fail_since FROM streams`).Scan(&v); return }
	daysAgo := func(d int) { st.DB.Exec(`UPDATE streams SET fail_since=?`, time.Now().AddDate(0, 0, -d).Unix()) }

	run()
	first := since()
	if first == 0 {
		t.Fatal("the first failure starts the run")
	}
	run()
	if since() != first {
		t.Fatal("later failures keep the start of the run")
	}
	daysAgo(30)
	run()
	if st.ExcludedCount() != 0 {
		t.Fatal("0 never excludes")
	}
	set.Apply(map[string]any{"health_auto_exclude_days": 7.0})
	daysAgo(6)
	run()
	if st.ExcludedCount() != 0 {
		t.Fatal("failing for 6 days is not 7")
	}
	daysAgo(7)
	run()
	items, _ := st.Excluded("", 0, 25)
	if len(items) != 1 || !items[0].Auto || items[0].Reason != "HTTP 404" || items[0].FailingSince == 0 {
		t.Fatalf("excluded after failing for 7 days: %+v", items)
	}
	if jobs, _, _ := c.JobsFor(Target{Kind: "all"}); len(jobs) != 0 {
		t.Fatal("an auto-excluded stream is not checked any more")
	}
	// including it again restarts the count, so the next failure does not exclude it at once
	st.SetExcluded(items[0].URL, false)
	if since() != 0 {
		t.Fatal("including a stream clears its run of failures")
	}
	run()
	if st.ExcludedCount() != 0 {
		t.Fatal("a fresh run of failures is not excluded")
	}
	// a working check ends the run
	fail = false
	run()
	if since() != 0 {
		t.Fatal("a working check clears the start of the run")
	}
}
