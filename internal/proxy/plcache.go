package proxy

import (
	"bytes"
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// A fetched playlist is shared for a moment. When ten players ask for the same live playlist (an HLS media playlist or a
// DASH manifest) within a few hundred milliseconds, the server is asked once. What is kept is the playlist exactly as the
// server sent it: every viewer still gets it rewritten with links of their own (profile and channel stay right).
//
// How long a copy lives is the setting playlist_cache_seconds, but never more than half of how often the playlist changes
// (its target duration, or minimumUpdatePeriod), so a player never gets a list that is older than it would be anyway.

type plEntry struct {
	kind string
	data []byte
	url  *url.URL
	exp  time.Time
}

type plCache struct {
	mu     sync.Mutex
	m      map[string]plEntry
	flight map[string]chan struct{} // a playlist being fetched: others wait for it

	hits atomic.Uint64 // playlists handed out from a copy (including those that waited for a fetch already running)
}

const plCacheMax = 512 // entries kept at most; expired ones are dropped first

func (c *plCache) get(k string) (plEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[k]
	if !ok || time.Now().After(e.exp) {
		return plEntry{}, false
	}
	return e, true
}

func (c *plCache) put(k string, e plEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]plEntry{}
	}
	if len(c.m) >= plCacheMax {
		now := time.Now()
		for key, old := range c.m {
			if now.After(old.exp) {
				delete(c.m, key)
			}
		}
		if len(c.m) >= plCacheMax {
			return // full of fresh copies: not worth the trouble
		}
	}
	c.m[k] = e
}

// begin claims the fetch of k: the first caller leads, the others get a channel that closes when the leader is done.
func (c *plCache) begin(k string) (leader bool, wait <-chan struct{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if ch, ok := c.flight[k]; ok {
		return false, ch
	}
	if c.flight == nil {
		c.flight = map[string]chan struct{}{}
	}
	c.flight[k] = make(chan struct{})
	return true, nil
}

func (c *plCache) end(k string) {
	c.mu.Lock()
	if ch, ok := c.flight[k]; ok {
		delete(c.flight, k)
		close(ch)
	}
	c.mu.Unlock()
}

var (
	targetDurRe = regexp.MustCompile(`#EXT-X-TARGETDURATION:\s*(\d+(?:\.\d+)?)`)
	minUpdateRe = regexp.MustCompile(`minimumUpdatePeriod="PT(?:(\d+(?:\.\d+)?)H)?(?:(\d+(?:\.\d+)?)M)?(?:(\d+(?:\.\d+)?)S)?"`)
)

// playlistTTL is how long a copy may be shared: the setting, but at most half of the playlist's own update interval.
func playlistTTL(setting time.Duration, kind string, data []byte) time.Duration {
	ttl := setting
	var every float64 // seconds between updates of a live playlist, 0 = unknown / not live
	switch kind {
	case "hls":
		if m := targetDurRe.FindSubmatch(data); m != nil {
			every, _ = strconv.ParseFloat(string(m[1]), 64)
		}
	case "dash":
		if m := minUpdateRe.FindSubmatch(data); m != nil {
			for i, mul := range []float64{3600, 60, 1} {
				if v, err := strconv.ParseFloat(string(bytes.TrimSpace(m[i+1])), 64); err == nil {
					every += v * mul
				}
			}
		}
	}
	if every > 0 {
		if half := time.Duration(every / 2 * float64(time.Second)); half < ttl {
			ttl = half
		}
	}
	return ttl
}

// cachedPlaylist answers from a shared copy, as an Upstream with nothing to renew (no connection, no hold-over, no prefetch:
// the viewer who fetched it did all that).
func (p *Proxy) cachedPlaylist(key string, pl Payload) (*Upstream, bool) {
	e, ok := p.pl.get(key)
	if !ok {
		return nil, false
	}
	p.pl.hits.Add(1)
	return &Upstream{URL: e.url, Payload: pl, Kind: e.kind, Manifest: e.data}, true
}

// Open requests the upstream URL with the payload's headers. Errors (including HTTP >= 400 and HTML responses) let callers
// fall back to another stream. A playlist fetched a moment ago for somebody else is handed out again (see above).
func (p *Proxy) Open(ctx context.Context, in *http.Request, pl Payload) (*Upstream, error) {
	plain := in != nil && (in.Method == http.MethodGet || in.Method == http.MethodHead) && in.Header.Get("Range") == ""
	if plain { // a live playlist the proxy follows: players get it a head start behind the server's live point
		if up, ok := p.headStartServe(pl); ok {
			return up, nil
		}
	}
	up, err := p.openCachedPlaylist(ctx, in, pl)
	if err == nil && plain && up.Kind == "hls" {
		if hs, ok := p.headStartBegin(ctx, pl, up); ok {
			up.Close()
			return hs, nil
		}
	}
	return up, err
}

func (p *Proxy) openCachedPlaylist(ctx context.Context, in *http.Request, pl Payload) (*Upstream, error) {
	setting := p.set.Seconds("playlist_cache_seconds")
	if setting <= 0 || in == nil || (in.Method != http.MethodGet && in.Method != http.MethodHead) || in.Header.Get("Range") != "" {
		return p.openFresh(ctx, in, pl)
	}
	key := cacheKey(pl)
	if up, ok := p.cachedPlaylist(key, pl); ok {
		return up, nil
	}
	if u, err := url.Parse(pl.U); err == nil && isManifestPath(u.Path) {
		if leader, wait := p.pl.begin(key); leader {
			defer p.pl.end(key)
			// somebody may have finished between our look at the copy and our claim of the fetch
			if up, ok := p.cachedPlaylist(key, pl); ok {
				return up, nil
			}
		} else {
			select {
			case <-wait:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			if up, ok := p.cachedPlaylist(key, pl); ok {
				return up, nil
			}
			// the one we waited for failed, or was not a playlist: fetch for ourselves
		}
	}
	up, err := p.openFresh(ctx, in, pl)
	if err == nil && up.Kind != "" && up.URL != nil {
		if ttl := playlistTTL(setting, up.Kind, up.Manifest); ttl > 0 {
			p.pl.put(key, plEntry{kind: up.Kind, data: up.Manifest, url: up.URL, exp: time.Now().Add(ttl)})
		}
	}
	return up, err
}
