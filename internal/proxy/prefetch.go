package proxy

import (
	"context"
	"io"
	"net/url"
	"strings"
	"time"
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
		go p.fetchSegment(k, pl, 0)
	}
}

// fetchSegment downloads one segment into the cache for ttl (0 = the setting) and says whether it was kept. What the server
// sent must look like a segment: some servers answer 200 with "not found" for a segment that is not there (yet, or any more),
// and that must never be kept and handed to a player.
func (p *Proxy) fetchSegment(k string, pl Payload, ttl time.Duration) bool {
	defer p.cache.endFetch(k)
	release, ok := p.Acquire()
	if !ok {
		return false
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), p.set.Seconds("upstream_response_timeout_seconds"))
	defer cancel()
	up, err := p.open(ctx, nil, pl, true)
	if err != nil {
		if err != errHostBusy { // a refusal made here is no request to the server
			p.upstreamErr.Add(1)
		}
		return false
	}
	p.upstreamOK.Add(1)
	defer up.Close()
	resp := up.resp
	limit := p.set.MB("segment_cache_item_max_mb")
	if up.Kind != "" || resp.StatusCode != 200 || resp.ContentLength > limit {
		return false
	}
	data, err := io.ReadAll(io.LimitReader(up.body, limit+1))
	if err != nil || int64(len(data)) > limit || (resp.ContentLength > 0 && int64(len(data)) != resp.ContentLength) {
		return false
	}
	ct := fixMIME(resp.Header.Get("Content-Type"), up.URL.Path)
	if !plausibleSegment(up.URL.Path, ct, data) {
		return false
	}
	if !p.cache.putTTL(k, cached{status: 200, ct: ct, data: data}, ttl) {
		return false
	}
	p.prefetched.Add(1)
	return true
}

// plausibleSegment tells whether bytes can be a media segment: not text (an error page, "not found") and, for MPEG-TS, at
// least one packet. It does not look inside: a segment may be encrypted, and then nothing in it looks like MPEG-TS.
func plausibleSegment(path, ct string, data []byte) bool {
	if len(data) == 0 || strings.HasPrefix(strings.ToLower(ct), "text/") {
		return false
	}
	if (strings.HasSuffix(strings.ToLower(path), ".ts") || strings.Contains(strings.ToLower(ct), "mp2t")) && len(data) < tsPktSize {
		return false // "not found" with a 200, served as video/MP2T by a real provider
	}
	return true
}
