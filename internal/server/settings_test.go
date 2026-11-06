package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

func testServer(t *testing.T) (http.Handler, *settings.Settings) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	set := settings.New(st)
	signer := proxy.NewSigner([]byte("0123456789abcdef0123456789abcdef"))
	ui := fstest.MapFS{"index.html": {Data: []byte("<html></html>")}}
	s := New(config.Config{DataDir: t.TempDir()}, st, set, isync.New(st, set), proxy.New(signer, set),
		epg.New(st, set, "/nonexistent", t.TempDir()), health.New(st, set), signer, events.NewHub(), ui)
	return s.Handler(), set
}

func call(h http.Handler, method, path string, body any, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rd)
	req.RemoteAddr = "10.0.0.1:1234"
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func sessionOf(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == "session" && c.MaxAge >= 0 {
			return c
		}
	}
	t.Fatalf("no session cookie: %d %s", rec.Code, rec.Body)
	return nil
}

// signIn makes the superuser "root" on the first call (the first-start setup) and signs in as root afterwards.
func signIn(t *testing.T, h http.Handler) *http.Cookie {
	t.Helper()
	creds := map[string]string{"username": "root", "password": "rootpassword"}
	if rec := call(h, "POST", "/api/setup", creds); rec.Code == 200 {
		return sessionOf(t, rec)
	}
	return sessionOf(t, call(h, "POST", "/api/login", creds))
}

func TestSettingsAPI(t *testing.T) {
	h, set := testServer(t)
	if call(h, "GET", "/api/settings", nil).Code != http.StatusUnauthorized {
		t.Fatal("settings need a session")
	}
	ck := signIn(t, h)

	rec := call(h, "GET", "/api/settings", nil, ck)
	var got struct {
		Fields          []settings.Value `json:"fields"`
		DetectedAddress string           `json:"detected_address"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || len(got.Fields) != len(settings.Defs) || got.DetectedAddress == "" {
		t.Fatalf("GET: %v %s", err, rec.Body)
	}

	// invalid values: 422 with a message per field, nothing stored
	rec = call(h, "PUT", "/api/settings", map[string]any{"values": map[string]any{"max_connections": -1, "api_url": "nope"}}, ck)
	var bad struct {
		Error  string            `json:"error"`
		Fields map[string]string `json:"fields"`
	}
	json.Unmarshal(rec.Body.Bytes(), &bad)
	if rec.Code != http.StatusUnprocessableEntity || bad.Fields["max_connections"] == "" || bad.Fields["api_url"] == "" || set.Int("max_connections") != 20 {
		t.Fatalf("PUT invalid: %d %s", rec.Code, rec.Body)
	}

	// a valid change applies immediately, here to the generated links' base address
	if rec = call(h, "PUT", "/api/settings", map[string]any{"values": map[string]any{"player_address": "http://tv.lan:9000/"}}, ck); rec.Code != 200 {
		t.Fatalf("PUT: %d %s", rec.Code, rec.Body)
	}
	if set.String("player_address") != "http://tv.lan:9000" {
		t.Fatal("not applied")
	}
}

func TestSignInLimitsAreSettings(t *testing.T) {
	h, set := testServer(t)
	ck := signIn(t, h)
	call(h, "PUT", "/api/settings", map[string]any{"values": map[string]any{"login_max_failures": 2}}, ck)
	wrong := func() int {
		return call(h, "POST", "/api/login", map[string]string{"username": "root", "password": "x"}).Code
	}
	if wrong() != 401 || wrong() != 401 {
		t.Fatal("the first two failures are plain 401s")
	}
	if wrong() != http.StatusTooManyRequests {
		t.Fatal("the third failure must be blocked with the limit from settings")
	}
	// the session lifetime follows the setting too
	set.Apply(map[string]any{"login_max_failures": 1000.0, "session_days": 3.0})
	for _, c := range call(h, "POST", "/api/login", map[string]string{"username": "root", "password": "rootpassword"}).Result().Cookies() {
		if c.Name == "session" && c.MaxAge != 3*24*3600 {
			t.Fatalf("session max-age %d", c.MaxAge)
		}
	}
}

func TestHealthControlsAPI(t *testing.T) {
	h, _ := testServer(t)
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/health"}, {"POST", "/api/health/run"}, {"POST", "/api/health/stop"}, {"POST", "/api/health/pause"},
		{"GET", "/api/health/down"}, {"GET", "/api/channel?ck=x"}, {"GET", "/api/health/rounds"},
	} {
		if call(h, c.method, c.path, nil).Code != http.StatusUnauthorized {
			t.Errorf("%s %s needs a session", c.method, c.path)
		}
	}
	ck := signIn(t, h)

	// run: unknown targets are 400, an empty target is 409, a stop with nothing running is 409
	if rec := call(h, "POST", "/api/health/run", map[string]string{"kind": "weird"}, ck); rec.Code != 400 {
		t.Fatalf("unknown target: %d", rec.Code)
	}
	if rec := call(h, "POST", "/api/health/run", map[string]string{"kind": "profile", "id": "99"}, ck); rec.Code != 400 {
		t.Fatalf("unknown profile: %d", rec.Code)
	}
	if rec := call(h, "POST", "/api/health/run", map[string]string{"kind": "all"}, ck); rec.Code != 409 {
		t.Fatalf("nothing to check: %d %s", rec.Code, rec.Body)
	}
	if rec := call(h, "POST", "/api/health/stop", nil, ck); rec.Code != 409 {
		t.Fatalf("stop with nothing running: %d", rec.Code)
	}

	// pause is stored and reported
	if rec := call(h, "POST", "/api/health/pause", map[string]bool{"paused": true}, ck); rec.Code != 200 {
		t.Fatalf("pause: %d", rec.Code)
	}
	var d struct {
		Paused bool `json:"paused"`
	}
	json.Unmarshal(call(h, "GET", "/api/health", nil, ck).Body.Bytes(), &d)
	if !d.Paused {
		t.Fatal("details must report the pause")
	}

	// lists
	var down struct {
		Total int `json:"total"`
	}
	json.Unmarshal(call(h, "GET", "/api/health/down?limit=5", nil, ck).Body.Bytes(), &down)
	var rounds struct {
		Total int           `json:"total"`
		Items []interface{} `json:"items"`
		Trend []interface{} `json:"trend"`
	}
	json.Unmarshal(call(h, "GET", "/api/health/rounds", nil, ck).Body.Bytes(), &rounds)
	if rounds.Items == nil || rounds.Trend == nil {
		t.Fatal("rounds and trend must be arrays, not null")
	}
	if call(h, "GET", "/api/channel?ck=nope", nil, ck).Code != 404 {
		t.Fatal("unknown channel")
	}
}
