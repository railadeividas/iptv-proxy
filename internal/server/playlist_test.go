package server

import (
	"strings"
	"testing"
	"testing/fstest"

	"iptv-proxy/internal/config"
	"iptv-proxy/internal/epg"
	"iptv-proxy/internal/events"
	"iptv-proxy/internal/health"
	"iptv-proxy/internal/proxy"
	"iptv-proxy/internal/settings"
	"iptv-proxy/internal/store"
	isync "iptv-proxy/internal/sync"
)

// The playlist ignores health-check exclusion: a channel is left out only when every stream is marked not working,
// whether or not some of its streams are excluded (player links must not change because of it).
func TestPlaylistIgnoresExclusion(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	set := settings.New(st)
	st.SetMetaInt("synced_at", 1) // the catalogue is loaded
	signer := proxy.NewSigner([]byte("0123456789abcdef0123456789abcdef"))
	h := New(config.Config{DataDir: t.TempDir()}, st, set, isync.New(st, set), proxy.New(signer, set),
		epg.New(st, set, "/nonexistent", t.TempDir()), health.New(st, set), signer, events.NewHub(),
		fstest.MapFS{"index.html": {Data: []byte("<html></html>")}}).Handler()

	add := func(ck string, statuses ...int) {
		st.DB.Exec(`INSERT INTO entries(ck,name,nsfw,closed,src) VALUES(?,?,0,0,0)`, ck, ck)
		for i, s := range statuses {
			st.DB.Exec(`INSERT INTO streams(ck,url,status) VALUES(?,?,?)`, ck, "http://"+ck+"/"+string(rune('a'+i)), s)
		}
	}
	add("Keep", store.StatusOK, store.StatusDead)   // the working stream is excluded, the counted one fails
	add("Drop", store.StatusDead, store.StatusDead) // every stream fails
	st.SetExcluded("http://Keep/a", true)
	u, _ := st.CreateUser("o", "x", "user")
	p, _ := st.CreateProfile(u.ID, "P")
	st.AddChannels(p.ID, []string{"Keep", "Drop"})

	rec := call(h, "GET", "/p/"+p.Token+"/playlist.m3u", nil)
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, `tvg-id="Keep"`) || strings.Contains(body, `tvg-id="Drop"`) {
		t.Fatalf("playlist %d:\n%s", rec.Code, body)
	}
}
