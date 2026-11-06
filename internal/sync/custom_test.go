package sync

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"iptv-proxy/internal/settings"
	"iptv-proxy/internal/store"
)

func newTestSyncer(t *testing.T) *Syncer {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return New(st, settings.New(st))
}

func names(t *testing.T, s *Syncer, id int64) string {
	t.Helper()
	var n string
	if err := s.st.DB.QueryRow(`SELECT COALESCE(group_concat(title),'') FROM (SELECT title FROM streams WHERE src=? ORDER BY title)`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestAutoRefreshAndEdit(t *testing.T) {
	var version atomic.Int32
	version.Store(1)
	var fail atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			http.Error(w, "boom", 500)
			return
		}
		fmt.Fprintf(w, "#EXTM3U\n#EXTINF:-1,Chan v%d\nhttps://ex.com/%d.m3u8\n", version.Load(), version.Load())
	}))
	defer srv.Close()

	s := newTestSyncer(t)
	id, err := s.AddCustom("src", srv.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := names(t, s, id); got != "Chan v1" {
		t.Fatalf("initial: %q", got)
	}

	// not due yet: the list must not change
	version.Store(2)
	s.RefreshAllCustom()
	if got := names(t, s, id); got != "Chan v1" {
		t.Fatalf("refreshed before it was due: %q", got)
	}

	// due (last attempt long ago): refreshed
	s.st.DB.Exec(`UPDATE custom_sources SET last_ok=1, last_try=1 WHERE id=?`, id)
	s.RefreshAllCustom()
	if got := names(t, s, id); got != "Chan v2" {
		t.Fatalf("auto refresh: %q", got)
	}

	// auto-refresh off: never refreshed even when old
	if err := s.UpdateCustom(id, CustomEdit{Name: "renamed", URL: srv.URL, RefreshHours: 0}); err != nil {
		t.Fatal(err)
	}
	version.Store(3)
	s.st.DB.Exec(`UPDATE custom_sources SET last_ok=1, last_try=1 WHERE id=?`, id)
	s.RefreshAllCustom()
	if got := names(t, s, id); got != "Chan v2" {
		t.Fatalf("refreshed although auto-refresh is off: %q", got)
	}
	list, _ := s.ListCustom()
	if list[0].Name != "renamed" || list[0].RefreshHours != 0 || list[0].NextRefresh != 0 {
		t.Fatalf("edit not stored: %+v", list[0])
	}

	// a failing new URL changes nothing
	fail.Store(true)
	if err := s.UpdateCustom(id, CustomEdit{Name: "x", URL: srv.URL + "/other", RefreshHours: 6}); err == nil {
		t.Fatal("expected an error for a failing URL")
	}
	list, _ = s.ListCustom()
	if list[0].Name != "renamed" || list[0].URL != srv.URL || got(t, s, id) != "Chan v2" {
		t.Fatalf("failed edit must not change anything: %+v", list[0])
	}

	// a failing refresh keeps the last good list and backs off
	s.st.DB.Exec(`UPDATE custom_sources SET refresh_hours=1, last_ok=1, last_try=1 WHERE id=?`, id)
	s.RefreshAllCustom()
	list, _ = s.ListCustom()
	if got(t, s, id) != "Chan v2" || list[0].LastError == "" || list[0].LastTry <= 1 {
		t.Fatalf("failure handling: %+v", list[0])
	}
}

func got(t *testing.T, s *Syncer, id int64) string { return names(t, s, id) }

func TestEditPastedSourceReplacesPlaylist(t *testing.T) {
	s := newTestSyncer(t)
	id, err := s.AddCustom("p", "", "#EXTM3U\n#EXTINF:-1,Old\nhttps://ex.com/o.m3u8\n")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateCustom(id, CustomEdit{Name: "p2", M3U: "#EXTM3U\n#EXTINF:-1,New\nhttps://ex.com/n.m3u8\n", RefreshHours: 12}); err != nil {
		t.Fatal(err)
	}
	if got := names(t, s, id); got != "New" {
		t.Fatalf("got %q", got)
	}
	if err := s.UpdateCustom(id, CustomEdit{Name: "p2", M3U: "not a playlist", RefreshHours: 12}); err == nil {
		t.Fatal("expected error for an invalid playlist")
	}
	if got := names(t, s, id); got != "New" {
		t.Fatalf("invalid replacement changed the list: %q", got)
	}
}

func TestEditShowsOriginalPlaylist(t *testing.T) {
	s := newTestSyncer(t)
	orig := "#EXTM3U\n# my note\n#EXTINF:-1 tvg-id=\"BTV.lt\" group-title=\"News\",BTV\nhttps://ex.com/btv.m3u8\n"
	id, err := s.AddCustom("p", "", orig)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := s.CustomPlaylist(id); got != orig {
		t.Fatalf("original text not kept: %q", got)
	}
	// replacing it updates the kept text
	repl := "#EXTM3U\n#EXTINF:-1,Two\nhttps://ex.com/2.m3u8\n"
	if err := s.UpdateCustom(id, CustomEdit{Name: "p", M3U: repl, RefreshHours: 12}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.CustomPlaylist(id); got != repl {
		t.Fatalf("replacement not kept: %q", got)
	}
	// switching to a URL clears the kept text
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "#EXTM3U\n#EXTINF:-1,U\nhttps://ex.com/u.m3u8\n")
	}))
	defer srv.Close()
	if err := s.UpdateCustom(id, CustomEdit{Name: "p", URL: srv.URL, RefreshHours: 12}); err != nil {
		t.Fatal(err)
	}
	var kept string
	s.st.DB.QueryRow(`SELECT m3u FROM custom_sources WHERE id=?`, id).Scan(&kept)
	if kept != "" {
		t.Fatalf("stale text kept: %q", kept)
	}
	if got, _ := s.CustomPlaylist(id); got != "" {
		t.Fatalf("URL source has no local text: %q", got)
	}
}

// A refresh rebuilds the source's streams but keeps each stream's health, including when its failures began
// (automatic exclusion counts the days from there).
func TestRefreshKeepsStreamHealth(t *testing.T) {
	s := newTestSyncer(t)
	text := "#EXTM3U\n#EXTINF:-1,Alpha\nhttps://ex.com/a.m3u8\n"
	id, err := s.AddCustom("src", "", text)
	if err != nil {
		t.Fatal(err)
	}
	s.st.DB.Exec(`UPDATE streams SET status=2, checked_at=500, fail_count=3, fail_since=100, fail_reason='Timed out'`)
	if err := s.UpdateCustom(id, CustomEdit{Name: "src", M3U: text + "#EXTINF:-1,Beta\nhttps://ex.com/b.m3u8\n", RefreshHours: 12}); err != nil {
		t.Fatal(err)
	}
	var st, fails int
	var at, since int64
	var reason string
	s.st.DB.QueryRow(`SELECT status, checked_at, fail_count, fail_since, fail_reason FROM streams WHERE url='https://ex.com/a.m3u8'`).Scan(&st, &at, &fails, &since, &reason)
	if st != 2 || at != 500 || fails != 3 || since != 100 || reason != "Timed out" {
		t.Fatalf("health after refresh: %d %d %d %d %q", st, at, fails, since, reason)
	}
}

// A new URL source takes its auto-refresh from the setting (72 hours by default); a change applies to the next source.
func TestNewSourceUsesDefaultRefresh(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "#EXTM3U\n#EXTINF:-1,Chan\nhttps://ex.com/1.m3u8\n")
	}))
	defer srv.Close()
	s := newTestSyncer(t)
	hours := func(id int64) (h int) {
		s.st.DB.QueryRow(`SELECT refresh_hours FROM custom_sources WHERE id=?`, id).Scan(&h)
		return
	}
	id, err := s.AddCustom("a", srv.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	if h := hours(id); h != 72 {
		t.Fatalf("default auto-refresh: %d h", h)
	}
	s.set.Apply(map[string]any{"custom_refresh_hours": 6.0})
	id, err = s.AddCustom("b", srv.URL+"/b", "")
	if err != nil {
		t.Fatal(err)
	}
	if h := hours(id); h != 6 {
		t.Fatalf("changed setting applies without a restart: %d h", h)
	}
}

func TestRemovingChannelsFromSourceLeavesRestUntouched(t *testing.T) {
	s := newTestSyncer(t)
	dropped := 0
	s.OnProfilesChanged = func() { dropped++ }
	text := "#EXTM3U\n" +
		"#EXTINF:-1 tvg-id=\"A.lt\",Alpha\nhttps://ex.com/a.m3u8\n" +
		"#EXTINF:-1,Beta\nhttps://ex.com/b.m3u8\n" +
		"#EXTINF:-1,Gamma\nhttps://ex.com/g.m3u8\n"
	id, err := s.AddCustom("src", "", text)
	if err != nil {
		t.Fatal(err)
	}
	cks := s.sourceCKs(id)
	if len(cks) != 3 {
		t.Fatalf("cks: %v", cks)
	}
	byName := map[string]string{}
	for _, ck := range cks {
		var n string
		s.st.DB.QueryRow(`SELECT title FROM streams WHERE ck=?`, ck).Scan(&n)
		byName[n] = ck
	}
	u, _ := s.st.CreateUser("owner", "x", "user")
	p, _ := s.st.CreateProfile(u.ID, "P")
	// profile order: Gamma, Alpha, Beta with custom settings on Alpha
	if err := s.st.ReplaceChannels(p.ID, []store.ProfileChannel{
		{CK: byName["Gamma"]}, {CK: byName["Alpha"], Name: "My Alpha", Mode: "proxy", EPGID: "X.lt"}, {CK: byName["Beta"]},
	}); err != nil {
		t.Fatal(err)
	}

	// remove Beta, retitle Alpha (same URL, same tvg-id), keep Gamma
	next := "#EXTM3U\n" +
		"#EXTINF:-1 tvg-id=\"A.lt\",Alpha renamed\nhttps://ex.com/a.m3u8\n" +
		"#EXTINF:-1,Gamma\nhttps://ex.com/g.m3u8\n"
	if err := s.UpdateCustom(id, CustomEdit{Name: "src", M3U: next, RefreshHours: 12}); err != nil {
		t.Fatal(err)
	}
	pcs, _ := s.st.ProfileChannels(p.ID)
	if len(pcs) != 2 || pcs[0].CK != byName["Gamma"] || pcs[1].CK != byName["Alpha"] {
		t.Fatalf("profile after removal: %+v", pcs)
	}
	if pcs[1].Name != "My Alpha" || pcs[1].Mode != "proxy" || pcs[1].EPGID != "X.lt" {
		t.Fatalf("kept channel's settings changed: %+v", pcs[1])
	}
	if dropped != 1 {
		t.Fatalf("OnProfilesChanged calls: %d", dropped)
	}

	// retitle a channel WITHOUT tvg-id: identity follows its stream URL
	next2 := "#EXTM3U\n" +
		"#EXTINF:-1 tvg-id=\"A.lt\",Alpha renamed\nhttps://ex.com/a.m3u8\n" +
		"#EXTINF:-1,Gamma Prime\nhttps://ex.com/g.m3u8\n"
	if err := s.UpdateCustom(id, CustomEdit{Name: "src", M3U: next2, RefreshHours: 12}); err != nil {
		t.Fatal(err)
	}
	pcs, _ = s.st.ProfileChannels(p.ID)
	if len(pcs) != 2 || pcs[0].CK != byName["Gamma"] {
		t.Fatalf("retitled channel lost its profile slot: %+v", pcs)
	}

	// deleting the whole source empties it from profiles
	if err := s.DeleteCustom(id); err != nil {
		t.Fatal(err)
	}
	pcs, _ = s.st.ProfileChannels(p.ID)
	if len(pcs) != 0 {
		t.Fatalf("deleted source's channels remain in a profile: %+v", pcs)
	}
}
