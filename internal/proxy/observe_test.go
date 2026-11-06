package proxy

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// A link made before statistics existed has no profile or channel and still decodes; one made now carries both
// (they are signed, so a viewer cannot claim another profile).
func TestPayloadProfileIsOptionalAndSigned(t *testing.T) {
	sg := NewSigner([]byte("0123456789abcdef0123456789abcdef"))
	old := base64.RawURLEncoding.EncodeToString([]byte(`{"u":"http://a/b.ts","e":0}`))
	tok := old + "." + base64.RawURLEncoding.EncodeToString(sg.mac(old))
	pl, err := sg.Decode(tok)
	if err != nil || pl.U != "http://a/b.ts" || pl.P != 0 || pl.C != "" {
		t.Fatalf("old link: %+v %v", pl, err)
	}
	pl, err = sg.Decode(sg.Encode(Payload{U: "http://a/b.ts", P: 7, C: "News@SD"}))
	if err != nil || pl.P != 7 || pl.C != "News@SD" {
		t.Fatalf("new link: %+v %v", pl, err)
	}
	forged := strings.Replace(sg.Encode(Payload{U: "http://a/b.ts", P: 7}), ".", "x.", 1)
	if _, err := sg.Decode(forged); err == nil {
		t.Fatal("a changed payload is refused")
	}
}

// ServeSigned tells the observer about links that carry a profile and counts the bytes written to the player.
func TestServeSignedReportsToObserver(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp2t")
		w.Write([]byte(strings.Repeat("x", 3000)))
	}))
	defer up.Close()
	set := testSettings(t)
	sg := NewSigner([]byte("0123456789abcdef0123456789abcdef"))
	px := New(sg, set)
	var bytes, started, ended atomic.Int64
	var seen Payload
	px.Observe = func(r *http.Request, pl Payload) (func(int), func()) {
		seen = pl
		started.Add(1)
		return func(n int) { bytes.Add(int64(n)) }, func() { ended.Add(1) }
	}
	serve := func(pl Payload) {
		r := httptest.NewRequest("GET", "/x/"+sg.Encode(pl)+"/seg.ts", nil)
		px.ServeSigned(httptest.NewRecorder(), r, "/x/", "http://p")
	}
	serve(Payload{U: up.URL + "/seg.ts"}) // no profile: not reported
	if started.Load() != 0 {
		t.Fatal("links without a profile are not observed")
	}
	serve(Payload{U: up.URL + "/seg.ts", P: 3, C: "News"})
	if started.Load() != 1 || ended.Load() != 1 || bytes.Load() != 3000 || seen.P != 3 || seen.C != "News" {
		t.Fatalf("observer: started %d ended %d bytes %d %+v", started.Load(), ended.Load(), bytes.Load(), seen)
	}
}
