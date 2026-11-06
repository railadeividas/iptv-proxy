package server

import (
	"bytes"
	"context"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/coder/websocket"

	"iptv-proxy/internal/events"
)

const csp = "default-src 'self'; img-src * data:; style-src 'self' 'unsafe-inline'; connect-src 'self' ws: wss:; frame-ancestors 'none'"

// spa serves the built Vue app. Unknown paths fall back to index.html so
// client-side routes (history mode) survive reloads and deep links.
func (s *Server) spa() http.Handler {
	files := http.FileServer(http.FS(s.ui))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Content-Security-Policy", csp)
		h.Set("Referrer-Policy", "no-referrer")
		p := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if p != "" {
			if st, err := fs.Stat(s.ui, p); err == nil && !st.IsDir() {
				if strings.HasPrefix(p, "assets/") {
					h.Set("Cache-Control", "public, max-age=31536000, immutable") // hashed file names
				} else {
					h.Set("Cache-Control", "no-cache")
				}
				files.ServeHTTP(w, r)
				return
			}
			if path.Ext(p) != "" { // missing asset: real 404, not the app shell
				http.NotFound(w, r)
				return
			}
		}
		index, err := fs.ReadFile(s.ui, "index.html")
		if err != nil {
			http.Error(w, "UI not built: run `npm ci && npm run build` in web/ (the Docker image does this)", http.StatusServiceUnavailable)
			return
		}
		h.Set("Content-Type", "text/html; charset=utf-8")
		h.Set("Cache-Control", "no-cache")
		http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(index))
	})
}

// Start runs background publishers until ctx ends: it pushes the status
// object to WebSocket clients whenever it changes (sync/EPG/health progress).
func (s *Server) Start(ctx context.Context) {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	var last []byte
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if s.hub.Clients() == 0 {
				last = nil
				continue
			}
			msg := events.Message("status", s.statusData())
			if !bytes.Equal(msg, last) {
				last = msg
				s.hub.Send("status", s.statusData())
			}
		}
	}
}

// ws streams change events to the admin UI.
func (s *Server) ws(w http.ResponseWriter, r *http.Request) {
	// The session cookie is SameSite=Strict, so cross-site pages cannot open this
	// socket with credentials; skipping the Origin check keeps reverse proxies working.
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer c.CloseNow()
	ctx := c.CloseRead(r.Context())
	ch, cancel := s.hub.Subscribe()
	defer cancel()
	write := func(msg []byte) bool {
		wctx, done := context.WithTimeout(ctx, 10*time.Second)
		defer done()
		return c.Write(wctx, websocket.MessageText, msg) == nil
	}
	if !write(events.Message("status", s.statusData())) {
		return
	}
	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-ch:
			if !write(msg) {
				return
			}
		case <-ping.C:
			pctx, done := context.WithTimeout(ctx, 10*time.Second)
			err := c.Ping(pctx)
			done()
			if err != nil {
				return
			}
		}
	}
}
