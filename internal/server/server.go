// Package server wires the admin API, player endpoints and the embedded UI.
package server

import (
	"bufio"
	"encoding/json"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"iptv-proxy/internal/activity"
	"iptv-proxy/internal/auth"
	"iptv-proxy/internal/config"
	"iptv-proxy/internal/epg"
	"iptv-proxy/internal/events"
	"iptv-proxy/internal/health"
	"iptv-proxy/internal/proxy"
	"iptv-proxy/internal/settings"
	"iptv-proxy/internal/store"
	isync "iptv-proxy/internal/sync"
)

type Server struct {
	cfg    config.Config
	set    *settings.Settings
	st     *store.Store
	sync   *isync.Syncer
	px     *proxy.Proxy
	epg    *epg.Manager
	health *health.Checker
	signer *proxy.Signer
	hub    *events.Hub
	act    *activity.Tracker
	ui     fs.FS

	loginMu  sync.Mutex
	failures map[string][]time.Time

	reqMu sync.Mutex
	reqs  map[reqKey]uint64 // requests by kind and status, for /metrics
}

func New(cfg config.Config, st *store.Store, set *settings.Settings, sy *isync.Syncer, px *proxy.Proxy, ep *epg.Manager, hc *health.Checker, signer *proxy.Signer, hub *events.Hub, ui fs.FS) *Server {
	act := activity.New(st, set)
	px.Observe = func(r *http.Request, pl proxy.Payload) (func(int), func()) { return act.Observe(r, pl.P, pl.C, pl.U) }
	return &Server{cfg: cfg, st: st, set: set, sync: sy, px: px, epg: ep, health: hc, signer: signer, hub: hub, act: act, ui: ui, failures: map[string][]time.Time{}, reqs: map[reqKey]uint64{}}
}

// Activity is the tracker of who uses the player links; main runs it (it saves running sessions).
func (s *Server) Activity() *activity.Tracker { return s.act }

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// open: health and player endpoints (token / signature protected)
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /metrics", s.metrics) // off, or behind a token, per the Metrics settings
	mux.HandleFunc("GET /p/{token}/playlist.m3u", s.playlist)
	mux.HandleFunc("GET /p/{token}/epg.xml", s.guide(false))
	mux.HandleFunc("GET /p/{token}/epg.xml.gz", s.guide(true))
	mux.HandleFunc("/p/{token}/channel/{file}", s.channel)
	mux.HandleFunc("/x/", func(w http.ResponseWriter, r *http.Request) { s.px.ServeSigned(w, r, "/x/", s.pub(r)) })
	mux.HandleFunc("/xd/", func(w http.ResponseWriter, r *http.Request) { s.px.ServeSigned(w, r, "/xd/", s.pub(r)) })

	// accounts
	mux.HandleFunc("POST /api/setup", s.setup)
	mux.HandleFunc("POST /api/login", s.login)
	mux.HandleFunc("POST /api/logout", s.logout)
	mux.HandleFunc("GET /api/session", s.session)

	// what a role may do is decided in package auth: everyone signed in browses channels and manages their
	// own profiles, the rest needs a permission
	api := func(pattern string, h http.HandlerFunc) { mux.HandleFunc(pattern, s.requireAuth(h)) }
	perm := func(p auth.Perm, pattern string, h http.HandlerFunc) { mux.HandleFunc(pattern, s.requirePerm(p, h)) }

	api("POST /api/account/password", s.changeOwnPassword)
	api("GET /api/status", s.status)
	api("GET /api/filters", s.filters)
	api("GET /api/channels", s.channels)
	api("GET /api/channels/keys", s.channelKeys)
	api("GET /api/channel", s.channelDetail)
	api("GET /api/entry/{ck}/streams", s.entryStreams)
	api("GET /api/qr", s.qr)
	api("GET /api/profiles", s.listProfiles)
	api("POST /api/profiles", s.createProfile)
	api("GET /api/profiles/{id}", s.getProfile)
	api("PATCH /api/profiles/{id}", s.patchProfile)
	api("DELETE /api/profiles/{id}", s.deleteProfile)
	api("POST /api/profiles/{id}/rotate-token", s.rotateToken)
	api("POST /api/profiles/{id}/add", s.addChannels)
	api("POST /api/profiles/{id}/remove", s.removeChannels)
	api("PUT /api/profiles/{id}/channels", s.replaceChannels)

	api("GET /api/stats", s.statsOverview)
	api("GET /api/stats/channels", s.statsChannels)
	api("GET /api/stats/clients", s.statsClients)
	api("GET /api/stats/sessions", s.statsSessions)

	perm(auth.Health, "GET /api/health", s.healthDetails)
	perm(auth.Health, "POST /api/health/run", s.healthRun)
	perm(auth.Health, "POST /api/health/stop", s.healthStop)
	perm(auth.Health, "POST /api/health/pause", s.healthPause)
	perm(auth.Health, "GET /api/health/down", s.healthDown)
	perm(auth.Health, "GET /api/health/rounds", s.healthRounds)
	perm(auth.Health, "GET /api/health/round", s.healthRound)
	perm(auth.Health, "GET /api/health/excluded", s.healthExcluded)
	perm(auth.Health, "POST /api/health/exclude", s.healthExclude)

	perm(auth.Sources, "GET /api/custom", s.listCustom)
	perm(auth.Sources, "POST /api/custom", s.addCustom)
	perm(auth.Sources, "GET /api/custom/{id}", s.getCustom)
	perm(auth.Sources, "PATCH /api/custom/{id}", s.updateCustom)
	perm(auth.Sources, "POST /api/custom/{id}/refresh", s.refreshCustom)
	perm(auth.Sources, "DELETE /api/custom/{id}", s.deleteCustom)
	perm(auth.Sources, "GET /api/skipped", s.skipped)

	perm(auth.Settings, "POST /api/sync", s.triggerSync)
	perm(auth.Settings, "POST /api/epg/run", s.triggerEPG)
	perm(auth.Settings, "GET /api/settings", s.getSettings)
	perm(auth.Settings, "PUT /api/settings", s.putSettings)

	perm(auth.Users, "GET /api/users", s.listUsers)
	perm(auth.Users, "POST /api/users", s.createUser)
	perm(auth.Users, "PATCH /api/users/{id}", s.patchUser)
	perm(auth.Users, "DELETE /api/users/{id}", s.deleteUser)

	mux.HandleFunc("GET /ws", s.requireAuth(s.ws))
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { httpErr(w, http.StatusNotFound, "not found") })

	mux.Handle("/", s.spa())
	return s.logging(mux)
}

type statusWriter struct {
	http.ResponseWriter
	code int
}

func (w *statusWriter) WriteHeader(c int) { w.code = c; w.ResponseWriter.WriteHeader(c) }
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return http.NewResponseController(w.ResponseWriter).Hijack()
}

func redact(p string) string {
	parts := strings.SplitN(p, "/", 4)
	if len(parts) >= 3 && (parts[1] == "p" || parts[1] == "x" || parts[1] == "xd") {
		parts[2] = "***"
		if parts[1] != "p" && len(parts) == 4 {
			return "/" + parts[1] + "/***/" + lastSeg(parts[3])
		}
		return strings.Join(parts, "/")
	}
	return p
}

func lastSeg(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

func (s *Server) logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, code: 200}
		next.ServeHTTP(sw, r)
		s.countRequest(r.URL.Path, sw.code)
		if r.URL.Path == "/healthz" {
			return
		}
		slog.Info("request", "method", r.Method, "path", redact(r.URL.Path), "status", sw.code, "ms", time.Since(start).Milliseconds())
	})
}

// pub returns the public base URL for generated links: the "address for your
// player" setting, else the host the client connected to.
func (s *Server) pub(r *http.Request) string {
	if v := s.set.String("player_address"); v != "" {
		return v
	}
	return s.detectedAddress(r)
}

// detectedAddress is the address the client used to reach the server.
func (s *Server) detectedAddress(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if p := r.Header.Get("X-Forwarded-Proto"); p != "" {
		scheme = strings.ToLower(strings.TrimSpace(strings.Split(p, ",")[0]))
		if scheme != "https" {
			scheme = "http"
		}
	}
	return scheme + "://" + r.Host
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// httpFieldErrs answers a rejected form: one message per bad field.
func httpFieldErrs(w http.ResponseWriter, fields map[string]string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnprocessableEntity)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": "Some values are not valid", "fields": fields})
}

func httpErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	if err := s.st.DB.Ping(); err != nil {
		http.Error(w, "db error", http.StatusServiceUnavailable)
		return
	}
	w.Write([]byte("ok\n"))
}

// ---- sign-in limits ----

func clientIP(r *http.Request) string {
	h, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return h
}

// rateLimited: too many failed attempts within the window (both settings) from one address.
func (s *Server) rateLimited(ip string) bool {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	cut := time.Now().Add(-s.set.Seconds("login_window_seconds"))
	var keep []time.Time
	for _, t := range s.failures[ip] {
		if t.After(cut) {
			keep = append(keep, t)
		}
	}
	s.failures[ip] = keep
	return len(keep) >= s.set.Int("login_max_failures")
}

func (s *Server) recordFailure(ip string) {
	s.loginMu.Lock()
	s.failures[ip] = append(s.failures[ip], time.Now())
	s.loginMu.Unlock()
}
