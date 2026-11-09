package proxy

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"
)

// Shared live streams. A continuous MPEG-TS stream (one response without a length, which never ends) is opened once
// per stream and its bytes go into a ring buffer; every player that asks for the same stream reads from the buffer
// instead of opening a connection of its own. Nothing is decoded or changed: the bytes are copied.
//
//   - The first viewer opens the stream (with a context of its own, so it can leave without ending the stream for the
//     others). Only MPEG-TS is shared: anything else (a playlist, a file with a length, another container) goes to that
//     viewer unchanged.
//   - Later viewers join at the live point (a little behind it, at a packet boundary). Bytes served to them count as
//     served from the cache.
//   - A viewer that falls further behind than the buffer is disconnected; it reconnects at the live point.
//   - When the server ends or drops the stream while viewers watch, the proxy connects again by itself.
//   - The stream closes shared_stream_linger_seconds after the last viewer left.
//
// Only the channel endpoint asks for sharing (OpenLive). Segments and playlists never go through here.

const (
	tsPktSize  = 188
	tsSyncByte = 0x47
	joinBack   = 32 * tsPktSize // a joining viewer starts this far behind the live point: it gets data at once
	pumpChunk  = 32 << 10
)

var errLagged = errors.New("viewer fell too far behind the shared stream")

// minLiveRun is how long a connection must have run before its end counts as a drop worth connecting again for. A
// response that ends sooner is a finished file (a server may send those without a length too): it is not repeated.
var minLiveRun = 3 * time.Second

// shareSet holds the shared streams that are open and the counters about them.
type shareSet struct {
	mu   sync.Mutex
	hubs map[string]*hub

	joins   atomic.Uint64 // viewers that joined a stream somebody else had opened
	bytes   atomic.Uint64 // bytes those viewers were served from the buffer
	streams atomic.Int64  // shared streams open now
	viewers atomic.Int64  // viewers on them
	full    atomic.Uint64 // streams left unshared because the total buffer memory was used
	memory  atomic.Int64  // bytes of buffer held by the shared streams open now (against shared_stream_total_mb)
}

func (s *shareSet) getOrStart(p *Proxy, key string, pl Payload) (*hub, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if h := s.hubs[key]; h != nil {
		return h, false
	}
	h := &hub{set: s, p: p, key: key, pl: pl, ready: make(chan struct{})}
	h.cond = sync.NewCond(&h.mu)
	if s.hubs == nil {
		s.hubs = map[string]*hub{}
	}
	s.hubs[key] = h
	return h, true
}

// reserve takes size bytes of the total the shared streams may hold and says whether there was room.
func (s *shareSet) reserve(h *hub, size, limit int64) bool {
	for {
		cur := s.memory.Load()
		if cur+size > limit {
			return false
		}
		if s.memory.CompareAndSwap(cur, cur+size) {
			h.reserved.Store(size)
			return true
		}
	}
}

func (s *shareSet) remove(h *hub) {
	s.memory.Add(-h.reserved.Swap(0)) // gives the buffer back, once, however many times the hub is removed
	s.mu.Lock()
	if s.hubs[h.key] == h {
		delete(s.hubs, h.key)
	}
	s.mu.Unlock()
}

// hub is one shared stream. Lock order: hub.mu, then shareSet.mu (never the other way round).
type hub struct {
	set *shareSet
	p   *Proxy
	key string
	pl  Payload

	reserved atomic.Int64  // bytes of the total this hub holds
	ready    chan struct{} // closed once it is known whether the stream is shared
	shared   bool          // valid after ready is closed
	hdr      http.Header   // what players are told about the stream
	url      *url.URL
	cancel   context.CancelFunc // ends the connection to the server

	mu      sync.Mutex
	cond    *sync.Cond
	ring    []byte
	head    int64 // bytes written in all
	subs    int
	stopped bool // nobody can join any more
	done    bool // the server side is over for good: viewers drain the buffer, then see the end
	linger  *time.Timer
}

// subscriber is one viewer's reading position in a hub. It is the body of the Upstream a viewer is given.
type subscriber struct {
	h        *hub
	pos      int64
	follower bool // joined a stream somebody else opened: its bytes count as served from the cache
	closed   bool
}

func (h *hub) at(off int64) byte { return h.ring[off%int64(len(h.ring))] }

// joinPos is where a viewer who joins now starts: shortly behind the live point, at the next MPEG-TS packet boundary.
func (h *hub) joinPos() int64 {
	oldest := h.head - int64(len(h.ring))
	if oldest < 0 {
		oldest = 0
	}
	pos := h.head - joinBack
	if pos < oldest {
		pos = oldest
	}
	for i := int64(0); i < 2*tsPktSize && pos+i < h.head; i++ {
		at := pos + i
		if h.at(at) == tsSyncByte && (at+tsPktSize >= h.head || h.at(at+tsPktSize) == tsSyncByte) {
			return at
		}
	}
	return pos
}

// join adds a viewer to a running stream; nil when it is not running any more.
func (h *hub) join() *subscriber {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.stopped || h.done {
		return nil
	}
	if h.linger != nil {
		h.linger.Stop()
		h.linger = nil
	}
	h.subs++
	h.set.viewers.Add(1)
	h.set.joins.Add(1)
	return &subscriber{h: h, pos: h.joinPos(), follower: true}
}

func (h *hub) write(b []byte) {
	h.mu.Lock()
	size := int64(len(h.ring))
	n := int64(len(b))
	var skip int64
	if n > size {
		skip = n - size // only the newest bytes fit
	}
	data := b[skip:]
	c := copy(h.ring[(h.head+skip)%size:], data)
	copy(h.ring, data[c:])
	h.head += n
	h.mu.Unlock()
	h.cond.Broadcast()
}

func (h *hub) copyOut(dst []byte, pos int64) int {
	n := h.head - pos
	if int64(len(dst)) < n {
		n = int64(len(dst))
	}
	at := pos % int64(len(h.ring))
	c := copy(dst[:n], h.ring[at:])
	if int64(c) < n {
		copy(dst[c:n], h.ring)
	}
	return int(n)
}

func (s *subscriber) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	h := s.h
	h.mu.Lock()
	defer h.mu.Unlock()
	for {
		if s.closed {
			return 0, io.EOF
		}
		if s.pos < h.head-int64(len(h.ring)) {
			return 0, errLagged
		}
		if s.pos < h.head {
			n := h.copyOut(p, s.pos)
			s.pos += int64(n)
			if s.follower {
				h.set.bytes.Add(uint64(n))
			}
			return n, nil
		}
		if h.done {
			return 0, io.EOF
		}
		h.cond.Wait()
	}
}

func (s *subscriber) Close() error {
	h := s.h
	h.mu.Lock()
	if s.closed {
		h.mu.Unlock()
		return nil
	}
	s.closed = true
	h.subs--
	h.set.viewers.Add(-1)
	if h.subs == 0 && !h.stopped && !h.done {
		if d := time.Duration(h.p.set.Int("shared_stream_linger_seconds")) * time.Second; d > 0 {
			h.linger = time.AfterFunc(d, func() {
				h.mu.Lock()
				if h.subs == 0 && !h.stopped {
					h.stopLocked()
				}
				h.mu.Unlock()
			})
		} else {
			h.stopLocked()
		}
	}
	h.mu.Unlock()
	h.cond.Broadcast()
	return nil
}

// stopLocked ends the stream: no more viewers can join and the connection to the server is closed.
func (h *hub) stopLocked() {
	h.stopped = true
	h.set.remove(h)
	if h.cancel != nil {
		h.cancel()
	}
}

func (h *hub) isStopped() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.stopped
}

// decide tells everybody waiting whether the stream is shared.
func (h *hub) decide(shared bool) {
	h.shared = shared
	if !shared {
		h.set.remove(h)
	}
	close(h.ready)
}

// pump copies the stream from the server into the buffer, and connects again when the server ends or drops it while
// somebody watches.
func (h *hub) pump(ctx context.Context, up *Upstream) {
	defer func() {
		h.mu.Lock()
		h.done, h.stopped = true, true
		if h.linger != nil {
			h.linger.Stop()
		}
		h.set.remove(h)
		h.set.streams.Add(-1)
		h.mu.Unlock()
		h.cond.Broadcast()
		h.cancel()
	}()
	for {
		started := time.Now()
		h.copyFrom(up.body)
		up.Close()
		if h.isStopped() || ctx.Err() != nil || time.Since(started) < minLiveRun {
			return
		}
		failures := 0 // the stream ran for a while: it dropped, so connect again
		for {
			if failures >= h.p.set.Int("shared_stream_reconnects") {
				return
			}
			failures++
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Duration(h.p.set.Int("upstream_retry_delay_ms")) * time.Millisecond):
			}
			if h.isStopped() {
				return
			}
			next, err := h.p.open(ctx, nil, h.pl, false)
			if err != nil {
				if ctx.Err() == nil {
					h.p.upstreamErr.Add(1)
				}
				continue
			}
			if next.Kind != "" || next.resp == nil || next.resp.StatusCode != http.StatusOK || next.resp.ContentLength >= 0 {
				next.Close()
				h.p.upstreamErr.Add(1)
				continue
			}
			h.p.upstreamOK.Add(1)
			up = next
			break
		}
	}
}

func (h *hub) copyFrom(r io.Reader) {
	buf := make([]byte, pumpChunk)
	for {
		c, err := r.Read(buf)
		if c > 0 {
			h.write(buf[:c])
		}
		if err != nil {
			return
		}
	}
}

func isTS(b []byte) bool {
	return len(b) >= 3*tsPktSize && b[0] == tsSyncByte && b[tsPktSize] == tsSyncByte && b[2*tsPktSize] == tsSyncByte
}

// shareable tells whether a request may be served from a shared stream: a plain GET of something that is not a playlist.
func (p *Proxy) shareable(in *http.Request, pl Payload) bool {
	if in == nil || in.Method != http.MethodGet || in.Header.Get("Range") != "" || p.set.String("shared_streams") != "on" {
		return false
	}
	u, err := url.Parse(pl.U)
	return err == nil && !isManifestPath(u.Path)
}

// OpenLive is Open for a channel link: a continuous MPEG-TS stream is shared with the other viewers of it (see above);
// everything else is opened as Open does.
func (p *Proxy) OpenLive(ctx context.Context, in *http.Request, pl Payload) (*Upstream, error) {
	if !p.shareable(in, pl) {
		return p.Open(ctx, in, pl)
	}
	key := cacheKey(pl)
	for try := 0; try < 3; try++ {
		h, leader := p.shares.getOrStart(p, key, pl)
		if leader {
			return p.leadShared(ctx, in, pl, h)
		}
		select {
		case <-h.ready:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		if !h.shared {
			return p.Open(ctx, in, pl) // not something that can be shared: a connection of its own
		}
		if sub := h.join(); sub != nil {
			return p.sharedUpstream(ctx, h, sub, pl), nil
		}
		// the stream ended while we looked: try again (the next round may start a new one)
	}
	return p.Open(ctx, in, pl)
}

// leadShared opens the stream for the first viewer and, when it is a continuous MPEG-TS stream, shares it.
func (p *Proxy) leadShared(ctx context.Context, in *http.Request, pl Payload, h *hub) (*Upstream, error) {
	// the connection is the stream's, not this viewer's: it must outlive the viewer. Until it is adopted by the
	// shared stream, the viewer's own context still ends it.
	dctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	stop := context.AfterFunc(ctx, cancel)
	up, err := p.Open(dctx, in, pl)
	if err != nil {
		stop()
		cancel()
		h.decide(false)
		return nil, err
	}
	keep := func() *Upstream { // this viewer keeps the connection for itself, as before
		h.decide(false)
		prev := up.done
		up.done = func() {
			if prev != nil {
				prev()
			}
			stop()
			cancel()
		}
		return up
	}
	if up.Kind != "" || up.resp == nil || up.resp.StatusCode != http.StatusOK || up.resp.ContentLength >= 0 {
		return keep(), nil
	}
	first := make([]byte, 3*tsPktSize)
	n, _ := io.ReadFull(up.body, first)
	first = first[:n]
	if !isTS(first) {
		up.body = io.MultiReader(bytes.NewReader(first), up.body)
		return keep(), nil
	}
	size := p.set.MB("shared_stream_buffer_mb")
	if !p.shares.reserve(h, size, p.set.MB("shared_stream_total_mb")) {
		up.body = io.MultiReader(bytes.NewReader(first), up.body)
		p.shares.full.Add(1)
		return keep(), nil // the shared streams already hold the most memory they may: this one is not shared
	}
	stop() // from here on the shared stream owns the connection
	h.ring = make([]byte, size)
	h.hdr = http.Header{}
	for _, k := range []string{"Content-Type", "Cache-Control"} {
		if v := up.resp.Header.Get(k); v != "" {
			h.hdr.Set(k, v)
		}
	}
	h.url = up.URL
	h.cancel = cancel
	h.write(first)
	sub := &subscriber{h: h}
	h.mu.Lock()
	h.subs = 1
	h.mu.Unlock()
	p.shares.viewers.Add(1)
	p.shares.streams.Add(1)
	h.decide(true)
	go h.pump(dctx, up)
	return p.sharedUpstream(ctx, h, sub, pl), nil
}

// sharedUpstream dresses a viewer's place in a shared stream as an upstream response, so it is relayed like any other.
func (p *Proxy) sharedUpstream(ctx context.Context, h *hub, sub *subscriber, pl Payload) *Upstream {
	resp := &http.Response{StatusCode: http.StatusOK, Header: h.hdr.Clone(), ContentLength: -1, Body: sub}
	stop := context.AfterFunc(ctx, func() { sub.Close() }) // the viewer left while waiting for data
	return &Upstream{resp: resp, body: sub, URL: h.url, Payload: pl, done: func() { stop() }}
}
