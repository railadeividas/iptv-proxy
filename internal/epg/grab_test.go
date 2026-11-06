package epg

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"iptv-proxy/internal/settings"
	"iptv-proxy/internal/store"
)

// A fake `npm` stands in for the iptv-org/epg grabber: it records how it was called and writes whatever FAKE_MODE says.
// The real grabber inside the image is covered by scripts/epg-smoke.sh (run by CI).
const fakeNPM = `#!/bin/sh
echo "$PWD|$*" >> "$FAKE_LOG"
out=""
for a in "$@"; do case "$a" in --output=*) out="${a#--output=}";; esac; done
case "$FAKE_MODE" in
ok) printf '<?xml version="1.0" encoding="UTF-8"?>\n<tv generator-info-name="fake"><channel id="Smoke.us@SD"><display-name>Smoke</display-name></channel><programme start="20260101000000 +0000" stop="20260101010000 +0000" channel="Smoke.us@SD"><title>Show %s</title></programme></tv>\n' "$FAKE_TAG" > "$out";;
fail) echo "boom: site unreachable" >&2; exit 1;;
notxml) printf 'this is definitely not xml, but it is longer than one hundred bytes so the size check passes first and the parser decides' > "$out";;
wrongroot) printf '<?xml version="1.0" encoding="UTF-8"?><html><body>an error page that is a well-formed document but not XMLTV, padded to pass the size check</body></html>' > "$out";;
tiny) printf x > "$out";;
none) ;;
esac
`

type grabEnv struct {
	m    *Manager
	st   *store.Store
	set  *settings.Settings
	log  string // what the fake npm recorded
	data string
}

func newGrabEnv(t *testing.T) *grabEnv {
	t.Helper()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "npm"), []byte(fakeNPM), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	log := filepath.Join(t.TempDir(), "calls.log")
	t.Setenv("FAKE_LOG", log)
	t.Setenv("FAKE_TAG", "1")
	t.Setenv("FAKE_MODE", "ok")

	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	set := settings.New(st)
	u, err := st.CreateUser("owner", "x", "user")
	if err != nil {
		t.Fatal(err)
	}
	p, err := st.CreateProfile(u.ID, "p")
	if err != nil {
		t.Fatal(err)
	}
	// one selected channel that has a guide mapping (channel Smoke.us, feed SD) and a name with characters that need escaping
	if _, err := st.DB.Exec(`INSERT INTO guides(channel,feed,site,site_id,site_name,lang) VALUES('Smoke.us','SD','smoke.test','7','x','en')`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.Exec(`INSERT INTO channels(id,name) VALUES('Smoke.us','Smoke & "Mirrors"')`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.Exec(`INSERT INTO profile_channels(profile_id,ck,position) VALUES(?,?,0)`, p.ID, "Smoke.us@SD"); err != nil {
		t.Fatal(err)
	}
	data, grabber := t.TempDir(), t.TempDir()
	return &grabEnv{m: New(st, set, grabber, data), st: st, set: set, log: log, data: data}
}

func (e *grabEnv) calls(t *testing.T) []string {
	t.Helper()
	b, _ := os.ReadFile(e.log)
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func (e *grabEnv) guide(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(e.m.guidePath())
	if err != nil {
		return ""
	}
	return string(b)
}

func TestGrabRunsTheGrabberAndInstallsTheGuide(t *testing.T) {
	e := newGrabEnv(t)
	e.m.grab(context.Background())

	if got := e.m.Status().Error; got != "" {
		t.Fatalf("grab failed: %s", got)
	}
	calls := e.calls(t)
	if len(calls) != 1 {
		t.Fatalf("grabber started %d times, want 1: %v", len(calls), calls)
	}
	dir, args, _ := strings.Cut(calls[0], "|")
	work := filepath.Join(e.data, "epg", "work")
	want := "run grab --- --channels=" + filepath.Join(work, "channels.xml") + " --output=" + filepath.Join(work, "guide.xml") + " --maxConnections=5"
	if args != want {
		t.Fatalf("grabber command line changed:\n got  %s\n want %s", args, want)
	}
	if real, _ := filepath.EvalSymlinks(e.m.dir); dir != real {
		t.Fatalf("grabber ran in %s, want its checkout %s", dir, real)
	}
	// the channel file the grabber reads: xmltv_id is the channel key (it becomes the tvg-id), values are escaped
	ch, err := os.ReadFile(filepath.Join(work, "channels.xml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{`xmltv_id="Smoke.us@SD"`, `site="smoke.test"`, `site_id="7"`, `lang="en"`, `Smoke &amp; &#34;Mirrors&#34;</channel>`} {
		if !strings.Contains(string(ch), s) {
			t.Fatalf("channels.xml lacks %s:\n%s", s, ch)
		}
	}
	if g := e.guide(t); !strings.Contains(g, "Show 1") {
		t.Fatalf("guide not installed: %q", g)
	}
	if e.st.MetaInt("epg_ok_at") == 0 || e.st.MetaInt("epg_channels") != 1 || e.st.MetaInt("epg_unmapped") != 0 {
		t.Fatalf("status not recorded: ok_at=%d channels=%d unmapped=%d", e.st.MetaInt("epg_ok_at"), e.st.MetaInt("epg_channels"), e.st.MetaInt("epg_unmapped"))
	}
	if _, err := os.Stat(filepath.Join(work, "guide.xml")); err == nil {
		t.Fatal("work file left behind after the atomic swap")
	}
}

// Settings are read when the grabber starts, so a change applies to the next run without a restart.
func TestGrabSettingsApplyWithoutRestart(t *testing.T) {
	e := newGrabEnv(t)
	e.m.grab(context.Background())
	if res := e.set.Apply(map[string]any{"epg_max_connections": 9, "epg_days": 3}); len(res) != 0 {
		t.Fatalf("settings rejected: %v", res)
	}
	t.Setenv("FAKE_TAG", "2")
	e.m.grab(context.Background())
	calls := e.calls(t)
	if len(calls) != 2 {
		t.Fatalf("calls: %v", calls)
	}
	if first := calls[0]; strings.Contains(first, "--days") || !strings.Contains(first, "--maxConnections=5") {
		t.Fatalf("first run (defaults): %s", first)
	}
	if second := calls[1]; !strings.Contains(second, "--maxConnections=9") || !strings.HasSuffix(second, "--days=3") {
		t.Fatalf("second run did not pick up the changed settings: %s", second)
	}
}

// A failed or broken grab must never replace the last good guide, and must say why.
func TestGrabFailureKeepsLastGoodGuide(t *testing.T) {
	cases := []struct {
		mode, wantErr string
	}{
		{"fail", "boom: site unreachable"},
		{"none", "grabber produced no guide"},
		{"tiny", "grabber produced no guide"},
		{"notxml", "invalid XMLTV"},
		{"wrongroot", "root is not <tv>"},
	}
	for _, c := range cases {
		t.Run(c.mode, func(t *testing.T) {
			e := newGrabEnv(t)
			e.m.grab(context.Background())
			good, okAt := e.guide(t), e.st.MetaInt("epg_ok_at")
			if !strings.Contains(good, "Show 1") {
				t.Fatalf("setup: no first guide")
			}
			t.Setenv("FAKE_MODE", c.mode)
			t.Setenv("FAKE_TAG", "2")
			e.m.grab(context.Background())

			if got := e.m.Status().Error; !strings.Contains(got, c.wantErr) {
				t.Fatalf("error %q does not mention %q", got, c.wantErr)
			}
			if e.guide(t) != good {
				t.Fatal("the last good guide was replaced")
			}
			if e.st.MetaInt("epg_ok_at") != okAt {
				t.Fatal("a failed grab was recorded as a success")
			}
			t.Setenv("FAKE_MODE", "ok") // and the next good run clears the error
			e.m.grab(context.Background())
			if e.m.Status().Error != "" || !strings.Contains(e.guide(t), "Show 2") {
				t.Fatalf("did not recover: err=%q", e.m.Status().Error)
			}
		})
	}
}

func TestGrabNeedsSelectedChannelsWithAGuide(t *testing.T) {
	e := newGrabEnv(t)
	if _, err := e.st.DB.Exec(`DELETE FROM guides`); err != nil {
		t.Fatal(err)
	}
	e.m.grab(context.Background())
	if got := e.m.Status().Error; !strings.Contains(got, "no selected channels have an EPG source") {
		t.Fatalf("error: %q", got)
	}
	if e.m.Status().Unmapped != 1 {
		t.Fatalf("unmapped = %d, want 1", e.m.Status().Unmapped)
	}
	if b, _ := os.ReadFile(e.log); len(b) != 0 {
		t.Fatalf("the grabber must not start without channels: %s", b)
	}
}
