package proxy

import (
	"context"
	"io"
	"net/url"
	"strings"
)

// prefetch downloads the newest segments of a live HLS media playlist into the segment cache, so a slow or
// stalling upstream is absorbed before the player asks. It only runs for playlists a player is fetching right
// now, never for idle channels, and needs a free connection slot for every download.
func (p *Proxy) prefetch(up *Upstream) {
	n := p.set.Int("segment_prefetch_count")
	if n <= 0 || p.set.MB("segment_cache_mb") == 0 || p.set.Seconds("segment_cache_ttl_seconds") == 0 {
		return
	}
	text := string(up.Manifest)
	if strings.Contains(text, "#EXT-X-STREAM-INF") || strings.Contains(text, "#EXT-X-BYTERANGE") {
		return // a master playlist, or segments that are ranges of one file (never cached)
	}
	var segs []*url.URL
	for _, line := range strings.Split(text, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		u, err := up.URL.Parse(t)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || isManifestPath(u.Path) {
			continue
		}
		u.Fragment = ""
		segs = append(segs, u)
	}
	if len(segs) > n {
		segs = segs[len(segs)-n:]
	}
	for _, u := range segs {
		pl := Payload{U: u.String(), R: up.Payload.R, A: up.Payload.A}
		k := cacheKey(pl)
		if !p.cache.startFetch(k) {
			continue
		}
		go p.fetchSegment(k, pl)
	}
}

func (p *Proxy) fetchSegment(k string, pl Payload) {
	defer p.cache.endFetch(k)
	release, ok := p.Acquire()
	if !ok {
		return
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), p.set.Seconds("upstream_response_timeout_seconds"))
	defer cancel()
	up, err := p.open(ctx, nil, pl)
	if err != nil {
		return
	}
	defer up.Close()
	resp := up.resp
	limit := p.set.MB("segment_cache_item_max_mb")
	if up.Kind != "" || resp.StatusCode != 200 || resp.ContentLength > limit {
		return
	}
	data, err := io.ReadAll(io.LimitReader(up.body, limit+1))
	if err != nil || int64(len(data)) > limit || (resp.ContentLength > 0 && int64(len(data)) != resp.ContentLength) {
		return
	}
	p.cache.put(k, cached{status: 200, ct: fixMIME(resp.Header.Get("Content-Type"), up.URL.Path), data: data})
}
