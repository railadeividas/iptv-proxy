package proxy

import (
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
}

func newSegCache(set *settings.Settings) *segCache {
	return &segCache{m: map[string]cached{}, set: set}
}

func cacheKey(pl Payload) string { return pl.U + "\x00" + pl.R + "\x00" + pl.A }

func (c *segCache) get(k string) (cached, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.m[k]
	if !ok || time.Now().After(v.expires) {
		c.misses.Add(1)
		return cached{}, false
	}
	c.hits.Add(1)
	return v, true
}

func (c *segCache) put(k string, v cached) {
	max, ttl := int(c.set.MB("segment_cache_mb")), c.set.Seconds("segment_cache_ttl_seconds")
	if max == 0 || ttl == 0 {
		return // cache switched off
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
			return
		}
	}
	if old, ok := c.m[k]; ok {
		c.size -= len(old.data)
	}
	v.expires = now.Add(ttl)
	c.m[k] = v
	c.size += len(v.data)
}

// usage is what the cache holds now.
func (c *segCache) usage() (entries, bytes int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.m), c.size
}
