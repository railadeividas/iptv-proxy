// Package health probes streams in the background and stores their status.
package health

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"iptv-proxy/internal/settings"
	"iptv-proxy/internal/store"
)

// Target says what a manual check covers.
type Target struct {
	Kind string `json:"kind"` // profiles, all, bad, unchecked, profile, source, channel
	ID   string `json:"id"`   // profile id, source id ("0" = iptv-org) or channel key
}

type job struct {
	url, ua, ref string
}

type request struct {
	target Target
	label  string
	jobs   []job
}

type Checker struct {
	st  *store.Store
	set *settings.Settings

	mu      sync.Mutex
	Running bool
	Done    int
	Total   int
	passOK  int // results of the pass in progress
	passBad int
	started int64
	kind    string // "auto" or "manual"
	label   string
	cancel  context.CancelFunc

	trigger chan request
	slots   slots
}

func New(st *store.Store, set *settings.Settings) *Checker {
	c := &Checker{st: st, set: set, trigger: make(chan request, 1)}
	// lowering a history limit deletes the surplus right away
	set.OnChange(func(keys []string) {
		for _, k := range keys {
			if strings.HasPrefix(k, "health_history_") {
				c.prune()
				st.Notify2("health")
				return
			}
		}
	})
	return c
}

func (c *Checker) prune() {
	c.st.PruneHealth(store.HistoryLimits{
		Rounds: c.set.Int("health_history_rounds"), PerChannel: c.set.Int("health_history_per_channel"),
		Total: c.set.Int("health_history_total"), MaxAgeDays: c.set.Int("health_history_days"),
	})
}

// newClient builds the probe client from the current timeouts, once per pass.
func (c *Checker) newClient() *http.Client {
	tr := &http.Transport{
		DialContext:           (&net.Dialer{Timeout: c.set.Seconds("health_connect_timeout_seconds")}).DialContext,
		TLSHandshakeTimeout:   c.set.Seconds("health_connect_timeout_seconds"),
		ResponseHeaderTimeout: c.set.Seconds("health_response_timeout_seconds"),
		MaxIdleConnsPerHost:   2,
	}
	return &http.Client{Transport: tr, Timeout: c.set.Seconds("health_total_timeout_seconds")}
}

// ---- pause, quiet hours, what may run automatically ----

func (c *Checker) Paused() bool { return c.st.MetaInt("health_paused") == 1 }

func (c *Checker) SetPaused(p bool) {
	v := int64(0)
	if p {
		v = 1
	}
	c.st.SetMetaInt("health_paused", v)
	c.st.Notify2("health")
}

// location is the configured time zone, else the server's.
func (c *Checker) location() *time.Location {
	if name := c.set.String("time_zone"); name != "" {
		if loc, err := time.LoadLocation(name); err == nil {
			return loc
		}
	}
	return time.Local
}

// inQuiet reports whether t falls into the quiet hours start..end ("HH:MM"),
// which may wrap past midnight. Empty or equal times mean no quiet hours.
func inQuiet(t time.Time, start, end string) bool {
	s, err1 := time.Parse("15:04", start)
	e, err2 := time.Parse("15:04", end)
	if err1 != nil || err2 != nil {
		return false
	}
	mins := func(x time.Time) int { return x.Hour()*60 + x.Minute() }
	now, a, b := mins(t), mins(s), mins(e)
	switch {
	case a == b:
		return false
	case a < b:
		return now >= a && now < b
	default:
		return now >= a || now < b
	}
}

func (c *Checker) Quiet() bool {
	return inQuiet(time.Now().In(c.location()), c.set.String("health_quiet_start"), c.set.String("health_quiet_end"))
}

// autoAllowed: automatic checks run unless switched off, paused or in quiet hours.
func (c *Checker) autoAllowed() bool {
	return c.set.String("health_scope") != "off" && !c.Paused() && !c.Quiet()
}

// ---- run control ----

// Stop cancels the running check. It reports false when nothing is running.
func (c *Checker) Stop() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.Running || c.cancel == nil {
		return false
	}
	c.cancel()
	return true
}

// CheckNow starts a manual check of the target and returns how many streams it covers.
// It fails when a check is already running, the target is unknown or nothing matches.
func (c *Checker) CheckNow(t Target) (int, error) {
	c.mu.Lock()
	busy := c.Running
	c.mu.Unlock()
	if busy {
		return 0, ErrBusy
	}
	jobs, label, err := c.JobsFor(t)
	if err != nil {
		return 0, err
	}
	if len(jobs) == 0 {
		return 0, ErrNothing
	}
	select {
	case c.trigger <- request{target: t, label: label, jobs: jobs}:
	default:
		return 0, ErrBusy
	}
	return len(jobs), nil
}

var (
	ErrBusy    = errors.New("a check is already running")
	ErrNothing = errors.New("no streams to check there")
	ErrTarget  = errors.New("unknown thing to check")
)

func (c *Checker) Progress() (running bool, done, total int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.Running, c.Done, c.Total
}

func (c *Checker) Run(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		if c.st.Ready() && c.autoAllowed() {
			c.pass(ctx, nil)
		}
		select {
		case <-ctx.Done():
			return
		case req := <-c.trigger:
			c.pass(ctx, &req)
		case <-t.C:
		}
	}
}

// autoCond is the WHERE condition (and its arguments) for streams that are due under the current scope.
// Streams of selected channels are re-checked on the short interval, the rest on the long one;
// a stream that failed fewer times in a row than the threshold is retried soon.
func (c *Checker) autoCond(now int64) (string, []any) {
	cond := `(s.checked_at < ? AND s.ck IN (SELECT ck FROM profile_channels))`
	args := []any{now - int64(c.set.Hours("health_selected_interval_hours").Seconds())}
	if c.set.String("health_scope") == "all" {
		cond += ` OR s.checked_at < ?`
		args = append(args, now-int64(c.set.Hours("health_interval_hours").Seconds()))
	}
	cond += ` OR (s.fail_count>0 AND s.fail_count<? AND s.checked_at < ?)`
	args = append(args, c.set.Int("health_failures_before_down"), now-int64(c.set.Minutes("health_retry_minutes").Seconds()))
	return `(` + cond + `) AND ` + notExcluded, args
}

// notExcluded leaves out the streams excluded from health checks (by hand or after too many failures).
const notExcluded = `s.url NOT IN (SELECT url FROM health_excluded)`

// autoJobs lists the streams that are due under the current scope.
func (c *Checker) autoJobs(now int64) []job {
	cond, args := c.autoCond(now)
	q := `SELECT url, COALESCE(user_agent,''), COALESCE(referrer,'') FROM streams s WHERE (` + cond + `)` +
		` GROUP BY url ORDER BY (s.ck IN (SELECT ck FROM profile_channels)) DESC, s.checked_at LIMIT ?`
	args = append(args, c.set.Int("health_max_per_pass"))
	return c.queryJobs(q, args...)
}

func (c *Checker) queryJobs(q string, args ...any) []job {
	rows, err := c.st.DB.Query(q, args...)
	if err != nil {
		slog.Error("health query", "err", err)
		return nil
	}
	defer rows.Close()
	var jobs []job
	for rows.Next() {
		var j job
		_ = rows.Scan(&j.url, &j.ua, &j.ref)
		jobs = append(jobs, j)
	}
	return jobs
}

// JobsFor lists the streams a manual check of the target covers and a label for the history.
func (c *Checker) JobsFor(t Target) ([]job, string, error) {
	var cond, label string
	var args []any
	switch t.Kind {
	case "profiles":
		cond, label = `s.ck IN (SELECT ck FROM profile_channels)`, "Channels in profiles"
	case "all":
		cond, label = `1=1`, "Every stream"
	case "bad":
		cond, label = `s.status=2`, "Streams marked not working"
	case "unchecked":
		cond, label = `s.status=0`, "Streams not checked yet"
	case "profile":
		id, err := strconv.ParseInt(t.ID, 10, 64)
		if err != nil {
			return nil, "", ErrTarget
		}
		p, err := c.st.GetProfile(id)
		if err != nil {
			return nil, "", ErrTarget
		}
		cond, args, label = `s.ck IN (SELECT ck FROM profile_channels WHERE profile_id=?)`, []any{id}, "Profile “"+p.Name+"”"
	case "source":
		id, err := strconv.ParseInt(t.ID, 10, 64)
		if err != nil || id < 0 {
			return nil, "", ErrTarget
		}
		label = "iptv-org"
		if id > 0 {
			if err := c.st.DB.QueryRow(`SELECT name FROM custom_sources WHERE id=?`, id).Scan(&label); err != nil {
				return nil, "", ErrTarget
			}
		}
		cond, args, label = `s.src=?`, []any{id}, "Source “"+label+"”"
	case "channel":
		var name string
		if err := c.st.DB.QueryRow(`SELECT name FROM entries WHERE ck=?`, t.ID).Scan(&name); err != nil {
			return nil, "", ErrTarget
		}
		cond, args, label = `s.ck=?`, []any{t.ID}, "Channel “"+name+"”"
	default:
		return nil, "", ErrTarget
	}
	q := `SELECT url, COALESCE(user_agent,''), COALESCE(referrer,'') FROM streams s WHERE (` + cond + `) AND ` + notExcluded + ` GROUP BY url ORDER BY s.checked_at`
	return c.queryJobs(q, args...), label, nil
}

// pass runs one round. req is nil for an automatic round.
func (c *Checker) pass(ctx context.Context, req *request) {
	manual := req != nil
	var jobs []job
	kind, label := "auto", "Automatic"
	if manual {
		jobs, kind, label = req.jobs, "manual", req.label
	} else {
		jobs = c.autoJobs(time.Now().Unix())
	}
	if len(jobs) == 0 {
		return
	}
	pctx, cancel := context.WithCancel(ctx)
	defer cancel()
	now := time.Now().Unix()
	c.mu.Lock()
	c.Running, c.Done, c.Total = true, 0, len(jobs)
	c.passOK, c.passBad, c.started, c.kind, c.label, c.cancel = 0, 0, now, kind, label, cancel
	c.mu.Unlock()
	c.st.Notify2("health")
	var roundID int64
	if c.set.Int("health_history_rounds") > 0 {
		roundID = c.st.StartRound(kind, label)
	}
	slog.Info("health pass started", "streams", len(jobs), "kind", kind, "what", label)

	client, ua := c.newClient(), c.set.String("health_user_agent")
	threshold, keep := c.set.Int("health_failures_before_down"), c.set.Int("health_history_per_channel")
	if c.set.Int("health_history_total") == 0 {
		keep = 0
	}
	workers := min(len(jobs), 200)
	ch := make(chan job)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range ch {
				// the concurrency setting is read live, so changing it speeds up or slows down a running pass
				host := hostOf(j.url)
				if !c.slots.acquire(pctx, host, func() int { return c.set.Int("health_concurrency") }, func() int { return c.set.Int("health_per_host_concurrency") }) {
					continue
				}
				// an automatic pass stops when checking is switched off, paused or in quiet hours,
				// also for streams a worker is already holding
				if pctx.Err() != nil || (!manual && !c.autoAllowed()) {
					c.slots.release(host)
					continue
				}
				// one deadline for the whole probe, also when it follows a master playlist to a variant
				probeCtx, probeCancel := context.WithTimeout(pctx, c.set.Seconds("health_total_timeout_seconds"))
				res := probe(probeCtx, client, ua, j)
				probeCancel()
				c.slots.release(host)
				if pctx.Err() != nil {
					continue // stopped: an aborted request is not a failed stream
				}
				c.st.RecordProbe(store.Probe{URL: j.url, OK: res.ok, Reason: res.reason, MS: res.ms}, threshold, keep)
				if !res.ok && c.st.AutoExclude(j.url, c.set.Int("health_auto_exclude_days")) {
					slog.Info("health: stream excluded after failing too often", "url", j.url, "reason", res.reason)
				}
				c.mu.Lock()
				c.Done++
				if res.ok {
					c.passOK++
				} else {
					c.passBad++
				}
				c.mu.Unlock()
			}
		}()
	}
loop:
	for _, j := range jobs {
		// an automatic pass also stops when checking is switched off, paused or in quiet hours
		if pctx.Err() != nil || (!manual && !c.autoAllowed()) {
			break loop
		}
		select {
		case ch <- j:
		case <-pctx.Done():
			break loop
		}
	}
	close(ch)
	wg.Wait()
	c.mu.Lock()
	c.Running, c.cancel = false, nil
	ok, bad, done, started := c.passOK, c.passBad, c.Done, c.started
	c.mu.Unlock()
	stopped := done < len(jobs) // some streams were left unchecked
	c.st.FinishRound(roundID, done, ok, bad, stopped)
	c.st.SetMetaInt("health_last_start", started)
	c.st.SetMetaInt("health_last_end", time.Now().Unix())
	c.st.SetMetaInt("health_last_count", int64(done))
	c.st.SetMetaInt("health_last_ok", int64(ok))
	c.st.SetMetaInt("health_last_bad", int64(bad))
	c.prune()
	c.st.Notify2("health")
	slog.Info("health pass finished", "probed", done, "working", ok, "not_working", bad, "stopped", stopped)
}

// slots limits how many probes run at once, in total and per host; both limits are read at every acquire.
// A worker only holds a slot while it can really probe, so a busy host never blocks checks of other hosts.
type slots struct {
	mu    sync.Mutex
	n     int
	hosts map[string]int
}

func (s *slots) acquire(ctx context.Context, host string, limit, hostLimit func() int) bool {
	for {
		s.mu.Lock()
		if s.n < limit() && s.hosts[host] < hostLimit() {
			s.n++
			if s.hosts == nil {
				s.hosts = map[string]int{}
			}
			s.hosts[host]++
			s.mu.Unlock()
			return true
		}
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return false
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (s *slots) release(host string) {
	s.mu.Lock()
	s.n--
	if s.hosts[host]--; s.hosts[host] <= 0 {
		delete(s.hosts, host)
	}
	s.mu.Unlock()
}

// ---- probing ----

// result of one probe. reason is why it failed, or for a working stream the HTTP status it answered with.
type result struct {
	ok     bool
	reason string
	ms     int
}

const (
	// sniffBytes is how much of a stream is read to judge it. A live stream never ends, so the probe hangs up after
	// this much; that is what a player that stops watching does too.
	sniffBytes = 4096
	// maxManifest caps how much of a playlist or manifest is read; real ones are far smaller.
	maxManifest = 1 << 20
	// maxVariantHops is how many playlists deep a master playlist is followed to find one with segments.
	maxVariantHops = 3
)

// get requests a stream address with the stream's own headers (or the default User-Agent).
func get(ctx context.Context, client *http.Client, defaultUA string, j job, addr string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", addr, nil)
	if err != nil {
		return nil, err
	}
	ua := j.ua
	if ua == "" {
		ua = defaultUA
	}
	req.Header.Set("User-Agent", ua)
	if j.ref != "" {
		req.Header.Set("Referer", j.ref)
		if u, err := url.Parse(j.ref); err == nil && u.Host != "" {
			req.Header.Set("Origin", u.Scheme+"://"+u.Host)
		}
	}
	return client.Do(req)
}

// probe reports whether the stream responds with plausible media/manifest data. An HLS master playlist is followed to its
// first variant, which must list segments. A working stream's reason is the HTTP status and the format it serves.
func probe(ctx context.Context, client *http.Client, defaultUA string, j job) result {
	start := time.Now()
	fail := func(reason string) result { return result{reason: reason, ms: int(time.Since(start).Milliseconds())} }
	if _, err := http.NewRequest("GET", j.url, nil); err != nil {
		return fail("Invalid address")
	}
	resp, err := get(ctx, client, defaultUA, j, j.url)
	if err != nil {
		return fail(classify(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fail(fmt.Sprintf("HTTP %d", resp.StatusCode))
	}
	code := resp.StatusCode
	buf := make([]byte, sniffBytes)
	n, _ := io.ReadFull(resp.Body, buf)
	ms := int(time.Since(start).Milliseconds()) // time to the first data
	if n == 0 {
		return fail("Empty response")
	}
	head := buf[:n]
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	lp := strings.ToLower(resp.Request.URL.Path)
	ok := func(format string) result {
		return result{ok: true, reason: fmt.Sprintf("HTTP %d · %s", code, format), ms: ms} // kept in the history
	}
	switch {
	case strings.Contains(ct, "mpegurl") || strings.HasSuffix(lp, ".m3u8") || strings.HasPrefix(strings.TrimSpace(string(head)), "#EXTM3U"):
		body := readRest(resp.Body, head)
		if !strings.Contains(body, "#EXTM3U") {
			return fail("Not a valid playlist")
		}
		if reason := checkHLS(ctx, client, defaultUA, j, resp.Request.URL, body); reason != "" {
			return fail(reason)
		}
		return ok("HLS")
	case strings.Contains(ct, "dash+xml") || strings.HasSuffix(lp, ".mpd"):
		body := readRest(resp.Body, head)
		if !strings.Contains(body, "<MPD") && !strings.Contains(body, ":MPD") {
			return fail("Not a valid manifest")
		}
		if !strings.Contains(body, "<Period") && !strings.Contains(body, ":Period") {
			return fail("Manifest has no periods")
		}
		return ok("DASH")
	case strings.HasPrefix(ct, "text/html"):
		return fail("Returned a web page")
	}
	format := sniffFormat(head)
	if format == "" && looksLikeText(head) {
		return fail("Returned text instead of video")
	}
	if format == "" {
		format = "unknown format"
	}
	return ok(format)
}

// readRest reads the rest of a playlist or manifest after its first bytes, up to maxManifest in all.
func readRest(r io.Reader, head []byte) string {
	if len(head) < sniffBytes {
		return string(head) // the body ended already
	}
	rest, _ := io.ReadAll(io.LimitReader(r, maxManifest-int64(len(head))))
	return string(head) + string(rest)
}

// checkHLS follows a master playlist to its first variant until it reaches a media playlist, which must list segments.
// It returns why the stream fails, or "" when it looks fine.
func checkHLS(ctx context.Context, client *http.Client, defaultUA string, j job, base *url.URL, body string) string {
	for hop := 0; ; hop++ {
		variant, master := firstVariant(body)
		if !master {
			if !hasSegment(body) {
				return "Playlist has no segments"
			}
			return ""
		}
		if variant == "" {
			return "Playlist has no streams"
		}
		if hop >= maxVariantHops {
			return "Not a valid playlist"
		}
		u, err := base.Parse(variant)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
			return "Variant playlist: Invalid address"
		}
		resp, err := get(ctx, client, defaultUA, j, u.String())
		if err != nil {
			return "Variant playlist: " + classify(err)
		}
		if resp.StatusCode >= 400 {
			resp.Body.Close()
			return fmt.Sprintf("Variant playlist: HTTP %d", resp.StatusCode)
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, maxManifest))
		resp.Body.Close()
		if body = string(b); !strings.Contains(body, "#EXTM3U") {
			return "Variant playlist: Not a valid playlist"
		}
		base = resp.Request.URL
	}
}

// firstVariant reports whether an HLS playlist is a master playlist and the address of its first variant.
func firstVariant(body string) (variant string, master bool) {
	next := false
	for _, line := range strings.Split(body, "\n") {
		t := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(t, "#EXT-X-STREAM-INF"):
			master, next = true, true
		case t == "" || strings.HasPrefix(t, "#"):
		case next:
			return t, true
		}
	}
	return "", master
}

// hasSegment reports whether a media playlist lists at least one segment.
func hasSegment(body string) bool {
	for _, line := range strings.Split(body, "\n") {
		if t := strings.TrimSpace(line); t != "" && !strings.HasPrefix(t, "#") {
			return true
		}
	}
	return false
}

// sniffFormat names the media format the first bytes of a stream look like, or "" when it is not recognised.
// It only labels the result: a stream in a format that is not listed still counts as working.
func sniffFormat(b []byte) string {
	const ts = 188
	// MPEG-TS: a sync byte every 188 bytes; a live stream may start in the middle of a packet
	for i := 0; i < ts && i < len(b); i++ {
		if b[i] == 0x47 && i+ts < len(b) && b[i+ts] == 0x47 && (i+2*ts >= len(b) || b[i+2*ts] == 0x47) {
			return "MPEG-TS"
		}
	}
	has := func(off int, sig string) bool { return len(b) >= off+len(sig) && string(b[off:off+len(sig)]) == sig }
	switch {
	case has(4, "ftyp") || has(4, "styp") || has(4, "moof") || has(4, "moov"):
		return "MP4"
	case has(0, "FLV"):
		return "FLV"
	case has(0, "\x1a\x45\xdf\xa3"):
		return "Matroska"
	case has(0, "OggS"):
		return "Ogg"
	case has(0, "ID3"):
		return "MP3 or AAC"
	case len(b) >= 2 && b[0] == 0xFF && b[1]&0xF6 == 0xF0:
		return "AAC"
	case len(b) >= 2 && b[0] == 0xFF && b[1]&0xE0 == 0xE0 && b[1]&0x06 != 0:
		return "MP3"
	}
	return ""
}

// looksLikeText reports whether the bytes are readable text (an error message, JSON, a login page without a content type),
// which a video stream never is.
func looksLikeText(b []byte) bool {
	if !utf8.Valid(b) {
		// the read may have cut a character in two: drop up to 3 trailing bytes and try again
		for cut := 1; cut <= 3 && cut < len(b); cut++ {
			if utf8.Valid(b[:len(b)-cut]) {
				b = b[:len(b)-cut]
				break
			}
		}
		if !utf8.Valid(b) {
			return false
		}
	}
	for _, c := range b {
		if c < 0x20 && c != '\t' && c != '\n' && c != '\r' {
			return false
		}
	}
	return true
}

// classify turns a network error into a short reason for the Health page.
func classify(err error) string {
	var dns *net.DNSError
	var ne net.Error
	msg := strings.ToLower(err.Error())
	switch {
	case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()) || strings.Contains(msg, "timeout"):
		return "Timed out"
	case errors.As(err, &dns):
		return "DNS lookup failed"
	case strings.Contains(msg, "x509") || strings.Contains(msg, "tls:") || strings.Contains(msg, "certificate"):
		return "TLS or certificate error"
	case strings.Contains(msg, "refused"):
		return "Connection refused"
	case strings.Contains(msg, "unreachable") || strings.Contains(msg, "no route"):
		return "Host unreachable"
	case strings.Contains(msg, "too many redirects"):
		return "Too many redirects"
	case strings.Contains(msg, "eof") || strings.Contains(msg, "reset"):
		return "Connection dropped"
	}
	return "Network error"
}
