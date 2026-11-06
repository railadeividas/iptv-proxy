package proxy

import (
	"context"
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
	p, err := rw.signer.Decode(tok)
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
