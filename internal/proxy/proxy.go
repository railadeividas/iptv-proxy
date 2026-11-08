package proxy

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"iptv-proxy/internal/settings"
)

// CompatUA returns the User-Agent of a profile's compatibility preset
// ("vlc", "browser" or "exoplayer"), or "" for no preset.
func CompatUA(set *settings.Settings, preset string) string {
	switch preset {
	case "vlc":
		return set.String("ua_vlc")
	case "browser":
		return set.String("ua_browser")
	case "exoplayer":
		return set.String("ua_exoplayer")
	}
	return ""
}

type Proxy struct {
	signer *Signer
	set    *settings.Settings
	client atomic.Pointer[http.Client]
	inUse  atomic.Int64 // open proxied connections
	cache  *segCache
	hosts  hostCount

	upstreamOK, upstreamErr atomic.Uint64 // requests to streams that worked / failed (not counting players that left)

	// Observe, when set, is told about every proxied request that carries a profile (see Payload.P). It returns
	// what to call with each number of bytes written to the player and when the request is over. Set it before serving.
	Observe func(r *http.Request, pl Payload) (add func(n int), done func())
}

// InUse is the number of proxied connections open right now.
func (p *Proxy) InUse() int { return int(p.inUse.Load()) }

// CountWriter reports the bytes written to a response.
type CountWriter struct {
	http.ResponseWriter
	OnWrite func(n int)
}

func (c *CountWriter) Write(b []byte) (int, error) {
	n, err := c.ResponseWriter.Write(b)
	if n > 0 && c.OnWrite != nil {
		c.OnWrite(n)
	}
	return n, err
}

// Unwrap lets http.ResponseController reach the real writer (flushing).
func (c *CountWriter) Unwrap() http.ResponseWriter { return c.ResponseWriter }

func New(signer *Signer, set *settings.Settings) *Proxy {
	p := &Proxy{signer: signer, set: set, cache: newSegCache(set)}
	p.client.Store(newClient(set))
	set.OnChange(func(keys []string) {
		for _, k := range keys {
			if k == "upstream_connect_timeout_seconds" || k == "upstream_response_timeout_seconds" {
				p.client.Store(newClient(set)) // running streams keep their connection
				return
			}
		}
	})
	return p
}

func newClient(set *settings.Settings) *http.Client {
	connect := set.Seconds("upstream_connect_timeout_seconds")
	tr := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: connect, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   connect,
		ResponseHeaderTimeout: set.Seconds("upstream_response_timeout_seconds"),
		IdleConnTimeout:       60 * time.Second,
		MaxIdleConnsPerHost:   16,
		ForceAttemptHTTP2:     true,
	}
	c := &http.Client{
		Transport: tr, // no overall Timeout: live streams are unbounded
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("too many redirects")
			}
			if req.URL.Host != via[0].URL.Host {
				// never leak credentials to another host
				req.Header.Del("Cookie")
				req.Header.Del("Authorization")
			}
			return nil
		},
	}
	return c
}

// StatusError is an upstream HTTP error status.
type StatusError struct{ Code int }

func (e StatusError) Error() string { return fmt.Sprintf("upstream returned HTTP %d", e.Code) }

// Upstream is an opened upstream response, ready to be relayed.
type Upstream struct {
	resp     *http.Response
	body     io.Reader
	URL      *url.URL
	Payload  Payload
	Kind     string // "hls", "dash" or "" for raw media
	Manifest []byte
	done     func() // releases the server's request count
}

func (u *Upstream) Close() {
	if u.resp != nil { // a held-over playlist has no response
		u.resp.Body.Close()
	}
	if u.done != nil {
		u.done()
		u.done = nil
	}
}

// hostCount counts the requests open to each server (host and port).
type hostCount struct {
	mu sync.Mutex
	n  map[string]int
}

// enter counts a request to host; with a limit it refuses (false) when the server is already at it.
func (h *hostCount) enter(host string, limit int) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if limit > 0 && h.n[host] >= limit {
		return false
	}
	if h.n == nil {
		h.n = map[string]int{}
	}
	h.n[host]++
	return true
}

func (h *hostCount) leave(host string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.n[host] <= 1 {
		delete(h.n, host)
	} else {
		h.n[host]--
	}
}

var errHostBusy = errors.New("server is at its request limit")

// transient is an upstream failure that may pass by itself: no answer, or a 5xx / 429 status.
func transient(err error) bool {
	var se StatusError
	if errors.As(err, &se) {
		return se.Code >= 500 || se.Code == http.StatusTooManyRequests
	}
	return true
}

func isManifestPath(p string) bool {
	p = strings.ToLower(p)
	return strings.HasSuffix(p, ".m3u8") || strings.HasSuffix(p, ".m3u") || strings.HasSuffix(p, ".mpd")
}

// Stats are the proxy's numbers for the metrics endpoint.
type Stats struct {
	InUse, Limit               int
	CacheEntries               int
	CacheBytes, CacheLimit     int64
	CacheHits, CacheMisses     uint64
	UpstreamOK, UpstreamErrors uint64
}

func (p *Proxy) Stats() Stats {
	n, b := p.cache.usage()
	return Stats{InUse: p.InUse(), Limit: p.set.Int("max_connections"), CacheEntries: n, CacheBytes: int64(b), CacheLimit: p.set.MB("segment_cache_mb"),
		CacheHits: p.cache.hits.Load(), CacheMisses: p.cache.misses.Load(), UpstreamOK: p.upstreamOK.Load(), UpstreamErrors: p.upstreamErr.Load()}
}

// Open requests the upstream URL with the payload's headers. Errors (including
// HTTP >= 400 and HTML responses) let callers fall back to another stream.
func (p *Proxy) Open(ctx context.Context, in *http.Request, pl Payload) (*Upstream, error) {
	var up *Upstream
	var err error
	for try := 0; ; try++ {
		up, err = p.open(ctx, in, pl, false)
		if err == nil || ctx.Err() != nil || try >= p.set.Int("upstream_retries") || !transient(err) {
			break
		}
		select {
		case <-ctx.Done():
		case <-time.After(time.Duration(p.set.Int("upstream_retry_delay_ms")) * time.Millisecond):
		}
	}
	switch {
	case err == nil:
		p.upstreamOK.Add(1)
	case ctx.Err() == nil: // a player that left is not the stream's fault
		p.upstreamErr.Add(1)
	}
	return up, err
}

// open requests the upstream and counts the request against its server until the Upstream is closed.
// capped requests (prefetch) are refused while the server is at upstream_per_host_requests.
func (p *Proxy) open(ctx context.Context, in *http.Request, pl Payload, capped bool) (*Upstream, error) {
	host := ""
	if u, err := url.Parse(pl.U); err == nil {
		host = u.Host
	}
	limit := 0
	if capped {
		limit = p.set.Int("upstream_per_host_requests")
	}
	if !p.hosts.enter(host, limit) {
		return nil, errHostBusy
	}
	up, err := p.openRaw(ctx, in, pl)
	if err != nil {
		p.hosts.leave(host)
		return nil, err
	}
	if up.Kind != "" {
		p.hosts.leave(host) // a playlist is already read in full: its connection is done
		return up, nil
	}
	up.done = func() { p.hosts.leave(host) }
	return up, nil
}

func (p *Proxy) openRaw(ctx context.Context, in *http.Request, pl Payload) (*Upstream, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pl.U, nil) // HEAD is answered via GET
	if err != nil {
		return nil, err
	}
	ua := pl.A
	if ua == "" {
		ua = p.set.String("proxy_default_user_agent")
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept", "*/*")
	if pl.R != "" {
		req.Header.Set("Referer", pl.R)
		if ru, err := url.Parse(pl.R); err == nil && ru.Host != "" {
			req.Header.Set("Origin", ru.Scheme+"://"+ru.Host)
		}
	}
	if in != nil && !isManifestPath(req.URL.Path) {
		for _, h := range []string{"Range", "If-Range"} {
			if v := in.Header.Get(h); v != "" {
				req.Header.Set(h, v)
			}
		}
	}
	resp, err := p.client.Load().Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		resp.Body.Close()
		return nil, StatusError{resp.StatusCode}
	}
	up := &Upstream{resp: resp, URL: resp.Request.URL, Payload: pl}
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	if strings.HasPrefix(ct, "text/html") {
		resp.Body.Close()
		return nil, errors.New("upstream returned an HTML page")
	}
	lp := strings.ToLower(up.URL.Path)
	switch {
	case strings.Contains(ct, "mpegurl") || strings.HasSuffix(lp, ".m3u8") || strings.HasSuffix(lp, ".m3u"):
		up.Kind = "hls"
	case strings.Contains(ct, "dash+xml") || strings.HasSuffix(lp, ".mpd"):
		up.Kind = "dash"
	}
	br := bufio.NewReaderSize(resp.Body, 4096)
	up.body = br
	if up.Kind == "" && resp.StatusCode == 200 && in != nil && in.Header.Get("Range") == "" {
		switch ct {
		case "", "text/plain", "application/octet-stream", "binary/octet-stream", "application/xml", "text/xml", "application/x-mpegurl":
			head, _ := br.Peek(512)
			h := string(head)
			if strings.HasPrefix(strings.TrimSpace(h), "#EXTM3U") {
				up.Kind = "hls"
			} else if strings.Contains(h, "<MPD") {
				up.Kind = "dash"
			}
		}
	}
	if up.Kind != "" {
		limit := p.set.MB("manifest_max_mb")
		data, err := io.ReadAll(io.LimitReader(br, limit+1))
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("reading manifest: %w", err)
		}
		if int64(len(data)) > limit {
			return nil, fmt.Errorf("manifest exceeds %d MiB", limit>>20)
		}
		data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
		if up.Kind == "hls" && !bytes.HasPrefix(bytes.TrimSpace(data), []byte("#EXTM3U")) {
			return nil, errors.New("upstream is not a valid HLS playlist")
		}
		if up.Kind == "dash" && !bytes.Contains(data, []byte("<MPD")) {
			return nil, errors.New("upstream is not a valid DASH manifest")
		}
		up.Manifest = data
	}
	return up, nil
}

func setCORS(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
	h.Set("Access-Control-Allow-Headers", "Range, Content-Type")
	h.Set("Access-Control-Expose-Headers", "Content-Length, Content-Range, Accept-Ranges")
}

// Respond relays an opened upstream, rewriting manifests. pub is the public
// base URL used in rewritten links.
func (p *Proxy) Respond(w http.ResponseWriter, in *http.Request, up *Upstream, pub string) {
	defer up.Close()
	setCORS(w)
	h := w.Header()
	if up.Kind != "" {
		rw := &rewriter{signer: p.signer, pub: pub, base: up.URL, ref: up.Payload.R, ua: up.Payload.A, exp: time.Now().Add(p.set.Hours("link_expiry_hours")).Unix(), prof: up.Payload.P, ck: up.Payload.C}
		if in.Method != http.MethodHead && up.resp != nil { // a held-over copy is not fresh: it must not renew itself
			if p.set.Int("playlist_holdover_seconds") > 0 {
				p.cache.hold(cacheKey(up.Payload), heldManifest{kind: up.Kind, manifest: up.Manifest, url: up.URL, at: time.Now()})
			}
			if up.Kind == "hls" {
				p.prefetch(up)
			}
		}
		var out []byte
		if up.Kind == "hls" {
			out = rw.HLS(up.Manifest)
			h.Set("Content-Type", "application/vnd.apple.mpegurl")
		} else {
			out = rw.DASH(up.Manifest)
			h.Set("Content-Type", "application/dash+xml")
		}
		h.Set("Cache-Control", "no-cache")
		h.Set("Content-Length", strconv.Itoa(len(out)))
		w.WriteHeader(http.StatusOK)
		if in.Method != http.MethodHead {
			w.Write(out)
		}
		return
	}

	resp := up.resp
	for _, k := range []string{"Content-Length", "Content-Range", "Accept-Ranges", "ETag", "Last-Modified", "Cache-Control", "Content-Encoding"} {
		if v := resp.Header.Get(k); v != "" {
			h.Set(k, v)
		}
	}
	h.Set("Content-Type", fixMIME(resp.Header.Get("Content-Type"), up.URL.Path))
	w.WriteHeader(resp.StatusCode)
	if in.Method == http.MethodHead {
		return
	}
	// Small, non-range responses are cached briefly so several viewers of the
	// same channel share segments.
	if in.Header.Get("Range") == "" && resp.StatusCode == 200 && resp.ContentLength > 0 && resp.ContentLength <= p.set.MB("segment_cache_item_max_mb") {
		// Stream to the player as the bytes arrive (a player reading at
		// real-time speed must not wait for the whole segment), keeping a copy.
		data := make([]byte, 0, resp.ContentLength)
		rc := http.NewResponseController(w)
		buf := make([]byte, 32<<10)
		for {
			n, err := up.body.Read(buf)
			if n > 0 {
				data = append(data, buf[:n]...)
				if _, werr := w.Write(buf[:n]); werr != nil {
					return
				}
				_ = rc.Flush()
			}
			if err != nil {
				if err == io.EOF && int64(len(data)) == resp.ContentLength {
					p.cache.put(cacheKey(up.Payload), cached{status: 200, ct: h.Get("Content-Type"), data: data})
				}
				return
			}
		}
	}
	streamCopy(w, up.body, resp.ContentLength < 0)
}

// streamCopy copies a body, flushing as it goes for live (length-less) streams.
func streamCopy(w http.ResponseWriter, r io.Reader, live bool) {
	rc := http.NewResponseController(w)
	buf := make([]byte, 32<<10)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			if live {
				_ = rc.Flush()
			}
		}
		if err != nil {
			return
		}
	}
}

func fixMIME(ct, p string) string {
	l := strings.ToLower(ct)
	generic := l == "" || strings.HasPrefix(l, "application/octet-stream") || strings.HasPrefix(l, "binary/octet-stream") || strings.HasPrefix(l, "text/plain")
	if !generic {
		return ct
	}
	switch strings.ToLower(path.Ext(p)) {
	case ".ts":
		return "video/mp2t"
	case ".m4s", ".mp4", ".cmfv":
		return "video/mp4"
	case ".m4a", ".cmfa", ".aac":
		return "audio/mp4"
	case ".key":
		return "application/octet-stream"
	case ".vtt":
		return "text/vtt"
	}
	if l == "" || strings.HasPrefix(l, "text/plain") {
		return "video/mp2t" // raw live TS endpoints rarely declare a type
	}
	return ct
}

// ---- signed-link handlers ----

// acquire reserves a connection slot; the caller must call the returned func.
func (p *Proxy) Acquire() (release func(), ok bool) {
	limit := int64(p.set.Int("max_connections"))
	for {
		cur := p.inUse.Load()
		if cur >= limit {
			return nil, false
		}
		if p.inUse.CompareAndSwap(cur, cur+1) {
			return func() { p.inUse.Add(-1) }, true
		}
	}
}

// ServeSigned handles /x/{token}/{name} and /xd/{token}/{rest...}.
// pub is the public base URL; prefix is "/x/" or "/xd/".
func (p *Proxy) ServeSigned(w http.ResponseWriter, r *http.Request, prefix, pub string) {
	if r.Method == http.MethodOptions {
		setCORS(w)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, prefix)
	tok, tail, _ := strings.Cut(rest, "/")
	pl, err := p.signer.Decode(tok)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	if prefix == "/xd/" {
		if !pl.D {
			http.Error(w, "invalid link", http.StatusForbidden)
			return
		}
		raw := strings.TrimPrefix(r.URL.EscapedPath(), prefix+tok+"/")
		for _, seg := range strings.Split(raw, "/") {
			if d, _ := url.PathUnescape(seg); d == ".." || d == "." {
				http.Error(w, "invalid path", http.StatusBadRequest)
				return
			}
		}
		target := pl.U + raw
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
		pl.U = target
	} else if pl.D {
		http.Error(w, "invalid link", http.StatusForbidden)
		return
	}
	_ = tail

	if p.Observe != nil && pl.P != 0 && r.Method == http.MethodGet {
		add, done := p.Observe(r, pl)
		defer done()
		w = &CountWriter{ResponseWriter: w, OnWrite: add}
	}

	if r.Method == http.MethodGet && r.Header.Get("Range") == "" {
		if c, ok := p.cache.get(cacheKey(pl)); ok {
			setCORS(w)
			w.Header().Set("Content-Type", c.ct)
			w.Header().Set("Content-Length", strconv.Itoa(len(c.data)))
			w.Write(c.data)
			return
		}
	}
	if r.Method == http.MethodGet && r.Header.Get("Range") == "" {
		if ch := p.cache.pending(cacheKey(pl)); ch != nil {
			// the segment is being prefetched: wait for it instead of asking the server twice
			select {
			case <-ch:
			case <-r.Context().Done():
			case <-time.After(p.set.Seconds("upstream_response_timeout_seconds")):
			}
			if c, ok := p.cache.get(cacheKey(pl)); ok {
				setCORS(w)
				w.Header().Set("Content-Type", c.ct)
				w.Header().Set("Content-Length", strconv.Itoa(len(c.data)))
				w.Write(c.data)
				return
			}
		}
	}
	release, ok := p.Acquire()
	if !ok {
		http.Error(w, "too many connections", http.StatusServiceUnavailable)
		return
	}
	defer release()
	up, err := p.Open(r.Context(), r, pl)
	if err != nil && r.Context().Err() == nil && transient(err) {
		// a playlist the upstream cannot give now: hand out the last good copy for a while
		if h, ok := p.cache.holdover(cacheKey(pl), p.set.Seconds("playlist_holdover_seconds")); ok {
			p.Respond(w, r, &Upstream{URL: h.url, Payload: pl, Kind: h.kind, Manifest: h.manifest}, pub)
			return
		}
	}
	if err != nil {
		var se StatusError
		code := http.StatusBadGateway
		if errors.As(err, &se) {
			code = se.Code
		}
		setCORS(w)
		http.Error(w, err.Error(), code)
		return
	}
	p.Respond(w, r, up, pub)
}
