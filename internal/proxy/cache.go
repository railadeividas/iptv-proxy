package proxy

import (
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"iptv-proxy/internal/settings"
)

type cached struct {
	status  int
	ct      string
	data    []byte
	expires time.Time
}

// segCache is a tiny TTL cache for media segments.
type segCache struct {
	mu   sync.Mutex
	m    map[string]cached
	size int
	set  *settings.Settings // size and lifetime are read live

	hits, misses atomic.Uint64
	hitBytes     atomic.Uint64 // bytes served from the cache
	stores       atomic.Uint64 // segments put in the cache
	rejected     atomic.Uint64 // segments not cached because the cache was full

	inflight map[string]chan struct{} // segments being prefetched
	held     map[string]heldManifest  // last good playlists
}

// heldManifest is the last good copy of a playlist, served when the upstream fails.
type heldManifest struct {
	kind     string
	manifest []byte
	url      *url.URL
	at       time.Time
}

// holdoverMaxAge bounds how long a copy is kept; it matches the largest playlist_holdover_seconds.
const holdoverMaxAge = 300 * time.Second

func newSegCache(set *settings.Settings) *segCache {
	return &segCache{m: map[string]cached{}, set: set, inflight: map[string]chan struct{}{}, held: map[string]heldManifest{}}
}

func cacheKey(pl Payload) string { return pl.U + "\x00" + pl.R + "\x00" + pl.A }

// get returns a fresh copy and counts the hit. A miss is not counted here: most lookups are for things that were never going
// to be in the cache (playlists), so the caller counts a miss only for a response that really was a segment fetched from the
// server (see ServeSigned).
func (c *segCache) get(k string) (cached, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.m[k]
	if !ok || time.Now().After(v.expires) {
		return cached{}, false
	}
	c.hits.Add(1)
	c.hitBytes.Add(uint64(len(v.data)))
	return v, true
}

// on says whether the segment cache is switched on (the master switch).
func (c *segCache) on() bool {
	return c.set.String("segment_cache") == "on" && c.set.MB("segment_cache_mb") > 0
}

// put keeps a segment a player asked for, for the other viewers: only when sharing segments is switched on.
func (c *segCache) put(k string, v cached) {
	if c.set.String("segment_share") == "on" {
		c.putTTL(k, v, c.set.Seconds("segment_cache_ttl_seconds"))
	}
}

// putTTL keeps a copy for ttl and says whether it was kept: not when the cache is switched off or full of fresh copies.
// Every feature that uses the cache decides its own ttl.
func (c *segCache) putTTL(k string, v cached, ttl time.Duration) bool {
	max := int(c.set.MB("segment_cache_mb"))
	if !c.on() || ttl <= 0 {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	if c.size+len(v.data) > max {
		for key, e := range c.m {
			if now.After(e.expires) {
				c.size -= len(e.data)
				delete(c.m, key)
			}
		}
		if c.size+len(v.data) > max {
			c.rejected.Add(1)
			return false
		}
	}
	if old, ok := c.m[k]; ok {
		c.size -= len(old.data)
	}
	v.expires = now.Add(ttl)
	c.m[k] = v
	c.size += len(v.data)
	c.stores.Add(1)
	return true
}

// peek returns a fresh copy without counting a hit (the caller counts it with noteHit when it really serves it).
func (c *segCache) peek(k string) (cached, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.m[k]
	if !ok || time.Now().After(v.expires) {
		return cached{}, false
	}
	return v, true
}

func (c *segCache) noteHit(bytes int) {
	c.hits.Add(1)
	c.hitBytes.Add(uint64(bytes))
}

// usage is what the cache holds now.
func (c *segCache) usage() (entries, bytes int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.m), c.size
}

// has reports whether a fresh copy is cached, without counting a hit or a miss.
func (c *segCache) has(k string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.m[k]
	return ok && !time.Now().After(v.expires)
}

// startFetch claims k for a prefetch; false when it is cached or already being fetched.
func (c *segCache) startFetch(k string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if v, ok := c.m[k]; ok && !time.Now().After(v.expires) {
		return false
	}
	if _, ok := c.inflight[k]; ok {
		return false
	}
	c.inflight[k] = make(chan struct{})
	return true
}

func (c *segCache) endFetch(k string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if ch, ok := c.inflight[k]; ok {
		close(ch)
		delete(c.inflight, k)
	}
}

// pending is closed when the prefetch of k ends; nil when none is running.
func (c *segCache) pending(k string) <-chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.inflight[k]
}

func (c *segCache) hold(k string, h heldManifest) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for key, e := range c.held {
		if h.at.Sub(e.at) > holdoverMaxAge {
			delete(c.held, key)
		}
	}
	c.held[k] = h
}

// holdover returns the last good playlist when it is younger than grace.
func (c *segCache) holdover(k string, grace time.Duration) (heldManifest, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	h, ok := c.held[k]
	if !ok || grace <= 0 || time.Since(h.at) > grace {
		return heldManifest{}, false
	}
	return h, true
}
