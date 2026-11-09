package proxy

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

const liveMPD = `<?xml version="1.0"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="dynamic" minimumUpdatePeriod="PT2S" availabilityStartTime="2020-01-01T00:00:00Z">
 <Period id="1" start="PT0S">
  <AdaptationSet id="1" contentType="audio" mimeType="audio/mp4">
   <SegmentTemplate timescale="1000" initialization="audio/$RepresentationID$/init.mp4" media="audio/$RepresentationID$/$Number%03d$.m4s" startNumber="100">
    <SegmentTimeline><S t="0" d="2000" r="5"/></SegmentTimeline>
   </SegmentTemplate>
   <Representation id="a1" bandwidth="64000"/>
  </AdaptationSet>
  <AdaptationSet id="2" contentType="video" mimeType="video/mp4">
   <SegmentTemplate timescale="1000" media="video/$RepresentationID$/$Time$.m4s">
    <SegmentTimeline><S t="1000" d="2000" r="5"/></SegmentTimeline>
   </SegmentTemplate>
   <Representation id="v1" bandwidth="500000"/>
   <Representation id="v2" bandwidth="2000000"/>
  </AdaptationSet>
 </Period>
</MPD>`

func TestParseDASH(t *testing.T) {
	live, sets := parseDASH([]byte(liveMPD), 3)
	if !live || len(sets) != 2 {
		t.Fatalf("live %v, sets %+v", live, sets)
	}
	if sets[0].id != "1" || len(sets[0].reps) != 1 || !reflect.DeepEqual(sets[0].reps[0].media, []string{"audio/a1/103.m4s", "audio/a1/104.m4s", "audio/a1/105.m4s"}) {
		t.Fatalf("audio: %+v", sets[0])
	}
	if len(sets[1].reps) != 2 || sets[1].reps[1].id != "v2" || !reflect.DeepEqual(sets[1].reps[1].media, []string{"video/v2/7000.m4s", "video/v2/9000.m4s", "video/v2/11000.m4s"}) {
		t.Fatalf("video: %+v", sets[1])
	}

	// gaps and repeats: a segment without t continues where the last one ended
	gappy := `<MPD type="dynamic"><Period><AdaptationSet><SegmentTemplate media="s_$Time$_$Number$.m4s"><SegmentTimeline>
		<S t="0" d="10"/><S d="10" r="2"/><S t="100" d="5"/></SegmentTimeline></SegmentTemplate><Representation id="r"/></AdaptationSet></Period></MPD>`
	_, sets = parseDASH([]byte(gappy), 10)
	if len(sets) != 1 || sets[0].id != "#0" || !reflect.DeepEqual(sets[0].reps[0].media, []string{"s_0_1.m4s", "s_10_2.m4s", "s_20_3.m4s", "s_30_4.m4s", "s_100_5.m4s"}) {
		t.Fatalf("timeline: %+v", sets)
	}

	for name, mpd := range map[string]string{
		"static":      `<MPD type="static"><Period><AdaptationSet><SegmentTemplate media="a_$Number$.m4s"><SegmentTimeline><S t="0" d="1"/></SegmentTimeline></SegmentTemplate><Representation id="r"/></AdaptationSet></Period></MPD>`,
		"open repeat": `<MPD type="dynamic"><Period><AdaptationSet><SegmentTemplate media="a_$Number$.m4s"><SegmentTimeline><S t="0" d="1" r="-1"/></SegmentTimeline></SegmentTemplate><Representation id="r"/></AdaptationSet></Period></MPD>`,
		"no timeline": `<MPD type="dynamic"><Period><AdaptationSet><SegmentTemplate media="a_$Number$.m4s" duration="2"/><Representation id="r"/></AdaptationSet></Period></MPD>`,
		"period base": `<MPD type="dynamic"><Period><BaseURL>x/</BaseURL><AdaptationSet><SegmentTemplate media="a_$Number$.m4s"><SegmentTimeline><S t="0" d="1"/></SegmentTimeline></SegmentTemplate><Representation id="r"/></AdaptationSet></Period></MPD>`,
		"rep base":    `<MPD type="dynamic"><Period><AdaptationSet><SegmentTemplate media="a_$Number$.m4s"><SegmentTimeline><S t="0" d="1"/></SegmentTimeline></SegmentTemplate><Representation id="r"><BaseURL>y/</BaseURL></Representation></AdaptationSet></Period></MPD>`,
		"not xml":     `#EXTM3U`,
	} {
		if _, sets := parseDASH([]byte(mpd), 3); len(sets) != 0 {
			t.Errorf("%s: nothing to prefetch, got %+v", name, sets)
		}
	}
	if got := expandTemplate("a/$$/$RepresentationID$_$Bandwidth$_$Number%05d$_$Time$", "v", "99", 7, 123); got != "a/$/v_99_00007_123" {
		t.Fatalf("template: %q", got)
	}
}

func TestDASHTopBase(t *testing.T) {
	base, _ := url.Parse("http://cdn.example/live/a/manifest.mpd?token=1")
	for mpd, want := range map[string]string{
		`<MPD><Period></Period></MPD>`:                                   "http://cdn.example/live/a/manifest.mpd?token=1",
		`<MPD><BaseURL>../media/</BaseURL><Period></Period></MPD>`:       "http://cdn.example/live/media/",
		`<MPD><BaseURL>http://other.example/x/</BaseURL><Period/></MPD>`: "http://other.example/x/",
		`<MPD><Period><BaseURL>inner/</BaseURL></Period></MPD>`:          "http://cdn.example/live/a/manifest.mpd?token=1",
	} {
		if got := dashTopBase(base, mpd).String(); got != want {
			t.Errorf("%s: %s, want %s", mpd, got, want)
		}
	}
	if dirString(dashTopBase(base, `<MPD><Period></Period></MPD>`)) != "http://cdn.example/live/a/" {
		t.Fatal("the directory of the manifest, as a signed directory link has it")
	}
}

type countingServer struct {
	*httptest.Server
	mu     sync.Mutex
	counts map[string]int
}

func (c *countingServer) count(path string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.counts[path]
}

func (c *countingServer) any(prefix string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for p := range c.counts {
		if strings.HasPrefix(p, prefix) {
			return true
		}
	}
	return false
}

func newDASHServer(t *testing.T) *countingServer {
	t.Helper()
	c := &countingServer{counts: map[string]int{}}
	c.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		c.counts[r.URL.Path]++
		c.mu.Unlock()
		if strings.HasSuffix(r.URL.Path, ".mpd") {
			w.Header().Set("Content-Type", "application/dash+xml")
			w.Write([]byte(liveMPD))
			return
		}
		w.Header().Set("Content-Type", "video/mp4")
		w.Write([]byte(strings.Repeat("m", 100)))
	}))
	t.Cleanup(c.Close)
	return c
}

func xdGet(t *testing.T, px *Proxy, tok, rel string) int {
	t.Helper()
	rec := httptest.NewRecorder()
	px.ServeSigned(rec, httptest.NewRequest("GET", "/xd/"+tok+"/"+rel, nil), "/xd/", "http://proxy")
	return rec.Code
}

func TestDASHPrefetchFollowsTheQualityViewersPlay(t *testing.T) {
	srv := newDASHServer(t)
	px := newSharingProxy(t, map[string]any{"playlist_cache_seconds": 0.0, "segment_prefetch_count": 3.0})
	mpd := func() string {
		t.Helper()
		code, body := signedGet(t, px, Payload{U: srv.URL + "/live/manifest.mpd"})
		if code != 200 {
			t.Fatalf("manifest: %d", code)
		}
		return body
	}
	m := regexp.MustCompile(`/xd/([^/]+)/`).FindStringSubmatch(mpd())
	if m == nil {
		t.Fatal("the manifest has a directory link")
	}
	tok := m[1]

	// first poll: audio has one quality, so its newest three segments come ahead; the video has two, nothing is guessed
	waitFor(t, "the audio segments", func() bool {
		return srv.count("/live/audio/a1/103.m4s") == 1 && srv.count("/live/audio/a1/104.m4s") == 1 && srv.count("/live/audio/a1/105.m4s") == 1
	})
	if srv.any("/live/video/") || srv.count("/live/audio/a1/102.m4s") != 0 {
		t.Fatal("only the newest audio segments, and no video quality until one is known")
	}

	// a viewer plays the second video quality ...
	if xdGet(t, px, tok, "video/v2/11000.m4s") != 200 {
		t.Fatal("segment")
	}
	// ... the next poll fetches ahead for that quality, and only that one
	mpd()
	waitFor(t, "video v2 ahead", func() bool {
		return srv.count("/live/video/v2/7000.m4s") == 1 && srv.count("/live/video/v2/9000.m4s") == 1
	})
	time.Sleep(100 * time.Millisecond)
	if srv.any("/live/video/v1/") {
		t.Fatal("the quality nobody plays is never fetched")
	}
	if srv.count("/live/video/v2/11000.m4s") != 1 {
		t.Fatal("the segment the viewer already got is not fetched again")
	}

	// the viewer's next segment is served from the cache
	before := px.Stats().CacheHits
	if xdGet(t, px, tok, "video/v2/9000.m4s") != 200 || srv.count("/live/video/v2/9000.m4s") != 1 || px.Stats().CacheHits != before+1 {
		t.Fatalf("a prefetched segment comes from the cache: server saw %d, hits %d -> %d", srv.count("/live/video/v2/9000.m4s"), before, px.Stats().CacheHits)
	}
	// the audio segment, too
	if xdGet(t, px, tok, "audio/a1/104.m4s") != 200 || srv.count("/live/audio/a1/104.m4s") != 1 {
		t.Fatal("audio from the cache")
	}
	if px.Stats().Prefetched < 5 {
		t.Fatalf("prefetch is counted: %+v", px.Stats())
	}
}

func TestDASHPrefetchFollowsTheSetting(t *testing.T) {
	srv := newDASHServer(t)
	px := newSharingProxy(t, map[string]any{"playlist_cache_seconds": 0.0, "segment_prefetch_count": 0.0})
	signedGet(t, px, Payload{U: srv.URL + "/live/manifest.mpd"})
	time.Sleep(150 * time.Millisecond)
	if srv.any("/live/audio/") {
		t.Fatal("off: nothing is fetched ahead")
	}
	if errs := px.set.Apply(map[string]any{"segment_prefetch_count": 2.0}); errs != nil {
		t.Fatal(errs)
	}
	signedGet(t, px, Payload{U: srv.URL + "/live/manifest.mpd"})
	waitFor(t, "audio ahead after switching on", func() bool {
		return srv.count("/live/audio/a1/105.m4s") == 1 && srv.count("/live/audio/a1/104.m4s") == 1
	})
	if srv.count("/live/audio/a1/103.m4s") != 0 {
		t.Fatal("two segments ahead, as set")
	}
}
