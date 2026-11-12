package proxy

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestSignRoundTrip(t *testing.T) {
	s := NewSigner([]byte("0123456789abcdef0123456789abcdef"))
	tok := s.Encode(Payload{U: "http://a/b", R: "http://r/", E: time.Now().Add(time.Minute).Unix()})
	p, err := s.Decode(tok)
	if err != nil || p.U != "http://a/b" || p.R != "http://r/" {
		t.Fatalf("decode: %v %+v", err, p)
	}
	if _, err := s.Decode(tok[:len(tok)-2] + "AA"); err == nil {
		t.Fatal("tampered token accepted")
	}
	if _, err := s.Decode(s.Encode(Payload{U: "x", E: 1})); err == nil {
		t.Fatal("expired token accepted")
	}
}

func TestSealHidesUpstream(t *testing.T) {
	s := NewSigner([]byte("0123456789abcdef0123456789abcdef"))
	in := Payload{U: "https://cdn.example.com/secret/path.m3u8", R: "https://ref.example/", A: "UA-secret", E: time.Now().Add(time.Minute).Unix(), P: 2, C: "ch@SD"}
	tok := s.Seal(in)
	for _, leak := range []string{"example", "secret", "UA-"} {
		if strings.Contains(tok, leak) {
			t.Fatalf("token leaks %q: %s", leak, tok)
		}
		if raw, err := base64.RawURLEncoding.DecodeString(strings.Split(tok, ".")[0]); err == nil && strings.Contains(string(raw), leak) {
			t.Fatalf("decoded token leaks %q", leak)
		}
	}
	out, err := s.Open(tok)
	if err != nil || out != in {
		t.Fatalf("open: %v %+v", err, out)
	}
	if tok == s.Seal(in) {
		t.Fatal("tokens must differ (random nonce)")
	}
	if _, err := s.Open(tok[:len(tok)-2] + "AA"); err == nil {
		t.Fatal("tampered token accepted")
	}
	if _, err := s.Open(s.Seal(Payload{U: "x", E: 1})); err == nil {
		t.Fatal("expired token accepted")
	}
	if _, err := NewSigner([]byte("another-key-another-key-another!")).Open(tok); err == nil {
		t.Fatal("token opened with a different key")
	}
	if _, err := s.Open(s.Encode(in)); err == nil {
		t.Fatal("old readable token accepted")
	}
}

func newRW(base string) *rewriter {
	u, _ := url.Parse(base)
	return &rewriter{signer: NewSigner([]byte("0123456789abcdef0123456789abcdef")), pub: "http://proxy", base: u, ref: "http://ref/", ua: "UA", exp: time.Now().Add(time.Hour).Unix()}
}

func TestHLSRewrite(t *testing.T) {
	rw := newRW("https://cdn.example.com/live/master.m3u8?token=1")
	in := "#EXTM3U\n#EXT-X-KEY:METHOD=AES-128,URI=\"key.bin\"\n#EXT-X-MAP:URI=\"init.mp4\"\n#EXTINF:4,\nseg1.ts?x=1\n#EXTINF:4,\nhttps://other.example.com/a/seg2.ts\n"
	out := string(rw.HLS([]byte(in)))
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "#EXTINF") || l == "" || l == "#EXTM3U" {
			continue
		}
		if strings.Contains(l, "cdn.example.com") || strings.Contains(l, "other.example.com") {
			t.Fatalf("upstream URL leaked: %s", l)
		}
	}
	if strings.Count(out, "http://proxy/x/") != 4 {
		t.Fatalf("want 4 rewritten refs:\n%s", out)
	}
	// decode the first segment link and check it resolved correctly
	i := strings.Index(out, "seg1")
	line := out[strings.LastIndex(out[:i], "\n")+1:]
	tok := strings.Split(strings.TrimPrefix(strings.Split(line, "\n")[0], "http://proxy/x/"), "/")[0]
	p, err := rw.signer.Open(tok)
	if err != nil || p.U != "https://cdn.example.com/live/seg1.ts?x=1" || p.R != "http://ref/" || p.A != "UA" {
		t.Fatalf("payload: %v %+v", err, p)
	}
}

func TestDASHRewrite(t *testing.T) {
	rw := newRW("https://cdn.example.com/dash/manifest.mpd")
	in := `<MPD xmlns="urn:mpeg:dash:schema:mpd:2011"><Period><AdaptationSet><SegmentTemplate media="v-$Number$.m4s" initialization="init.mp4"/></AdaptationSet></Period></MPD>`
	out := string(rw.DASH([]byte(in)))
	if !strings.Contains(out, "<BaseURL>http://proxy/xd/") || !strings.Contains(out, `media="v-$Number$.m4s"`) {
		t.Fatalf("unexpected: %s", out)
	}
	in2 := `<MPD><BaseURL>https://a.example.com/x/</BaseURL><BaseURL>https://b.example.com/y/</BaseURL><Period><BaseURL>https://c.example.com/z/</BaseURL></Period></MPD>`
	out = string(rw.DASH([]byte(in2)))
	if strings.Contains(out, "example.com") || strings.Count(out, "<BaseURL>") != 2 {
		t.Fatalf("unexpected: %s", out)
	}
}

// End-to-end: fake upstream that requires Referer + UA, served via the proxy.
func TestProxyEndToEnd(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Referer") != "http://ref.example/" || r.Header.Get("User-Agent") != "TestUA" {
			http.Error(w, "forbidden", 403)
			return
		}
		switch r.URL.Path {
		case "/live/index.m3u8":
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			fmt.Fprint(w, "#EXTM3U\n#EXTINF:4,\nseg1.ts\n")
		case "/live/seg1.ts":
			w.Header().Set("Content-Type", "application/octet-stream")
			http.ServeContent(w, r, "seg1.ts", time.Time{}, strings.NewReader("0123456789"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer up.Close()

	px := New(NewSigner([]byte("0123456789abcdef0123456789abcdef")), testSettings(t))
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/x/") {
			px.ServeSigned(w, r, "/x/", "http://"+r.Host)
			return
		}
		up, err := px.Open(context.Background(), r, Payload{U: upURL(r), R: "http://ref.example/", A: "TestUA"})
		if err != nil {
			http.Error(w, err.Error(), 502)
			return
		}
		px.Respond(w, r, up, "http://"+r.Host)
	}))
	defer front.Close()
	_ = up
	upBase = up.URL

	resp, err := http.Get(front.URL + "/channel")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !strings.Contains(string(body), front.URL+"/x/") {
		t.Fatalf("manifest: %d %s", resp.StatusCode, body)
	}
	seg := strings.Split(strings.TrimSpace(string(body)), "\n")[2]
	if !strings.HasSuffix(seg, "/seg1.ts") {
		t.Fatalf("cosmetic name lost: %s", seg)
	}
	resp, _ = http.Get(seg)
	body, _ = io.ReadAll(resp.Body)
	if string(body) != "0123456789" || resp.Header.Get("Content-Type") != "video/mp2t" {
		t.Fatalf("segment: %q %s", body, resp.Header.Get("Content-Type"))
	}
	req, _ := http.NewRequest("GET", seg, nil)
	req.Header.Set("Range", "bytes=2-4")
	resp, _ = http.DefaultClient.Do(req)
	body, _ = io.ReadAll(resp.Body)
	if resp.StatusCode != 206 || string(body) != "234" {
		t.Fatalf("range: %d %q", resp.StatusCode, body)
	}
	resp, _ = http.Head(seg)
	if resp.StatusCode != 200 {
		t.Fatalf("head: %d", resp.StatusCode)
	}
	resp, _ = http.Get(front.URL + "/x/bogus.sig/seg.ts")
	if resp.StatusCode != 403 {
		t.Fatalf("forged link: %d", resp.StatusCode)
	}
}

var upBase string

func upURL(*http.Request) string { return upBase + "/live/index.m3u8" }

// A segment must reach the player while the upstream is still sending it,
// and a complete one must still land in the cache.
func TestSegmentStreamedBeforeComplete(t *testing.T) {
	release := make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp2t")
		w.Header().Set("Content-Length", "10")
		w.Write([]byte("01234"))
		w.(http.Flusher).Flush()
		<-release
		w.Write([]byte("56789"))
	}))
	defer up.Close()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()

	set, signer := testSettings(t), NewSigner([]byte("0123456789abcdef0123456789abcdef"))
	px := New(signer, set)
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pl := Payload{U: up.URL + "/seg.ts"}
		o, err := px.Open(r.Context(), r, pl)
		if err != nil {
			http.Error(w, err.Error(), 502)
			return
		}
		px.Respond(w, r, o, "http://proxy")
	}))
	defer front.Close()

	resp, err := http.Get(front.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	first := make([]byte, 5)
	if _, err := io.ReadFull(resp.Body, first); err != nil || string(first) != "01234" {
		t.Fatalf("first bytes not streamed before upstream finished: %v %q", err, first)
	}
	close(release)
	rest, _ := io.ReadAll(resp.Body)
	if string(rest) != "56789" {
		t.Fatalf("rest = %q", rest)
	}
	// the player has every byte as soon as the last chunk is flushed; the proxy stores the copy right after that
	// (when it reads the end of the upstream body), so give it a moment
	var cached bool
	for i := 0; i < 200 && !cached; i++ {
		if _, cached = px.cache.get(cacheKey(Payload{U: up.URL + "/seg.ts"})); !cached {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if !cached {
		t.Fatal("complete segment was not cached")
	}
}
