// Package epg runs the iptv-org/epg grabber for the selected channels and
// serves the resulting XMLTV, filtered per profile.
package epg

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"iptv-proxy/internal/settings"
	"iptv-proxy/internal/store"
)

type Manager struct {
	st      *store.Store
	set     *settings.Settings
	dir     string // grabber checkout
	dataDir string
	trigger chan struct{}

	mu       sync.Mutex
	running  bool
	lastErr  string
	cacheMu  sync.Mutex
	filtered map[int64]filteredGuide
}

type filteredGuide struct {
	key     string
	xml, gz []byte
}

func New(st *store.Store, set *settings.Settings, grabberDir, dataDir string) *Manager {
	return &Manager{st: st, set: set, dir: grabberDir, dataDir: dataDir,
		trigger: make(chan struct{}, 1), filtered: map[int64]filteredGuide{}}
}

func (m *Manager) Enabled() bool {
	_, err := os.Stat(filepath.Join(m.dir, "package.json"))
	return err == nil
}

func (m *Manager) guidePath() string { return filepath.Join(m.dataDir, "epg", "guide.xml") }

// HasGuide reports whether a grab has succeeded at least once.
func (m *Manager) HasGuide() bool {
	_, err := os.Stat(m.guidePath())
	return err == nil
}

type Status struct {
	Enabled  bool   `json:"enabled"`
	Running  bool   `json:"running"`
	LastOK   int64  `json:"last_ok"`
	Error    string `json:"error"`
	Channels int    `json:"channels"`
	Unmapped int    `json:"unmapped"`
}

func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	return Status{Enabled: m.Enabled(), Running: m.running, LastOK: m.st.MetaInt("epg_ok_at"), Error: m.lastErr,
		Channels: int(m.st.MetaInt("epg_channels")), Unmapped: int(m.st.MetaInt("epg_unmapped"))}
}

// MarkDirty schedules a grab soon (selection changed).
func (m *Manager) MarkDirty() { m.st.SetMetaInt("epg_dirty", 1) }

// RunNow requests an immediate grab.
func (m *Manager) RunNow() {
	select {
	case m.trigger <- struct{}{}:
	default:
	}
}

func (m *Manager) Run(ctx context.Context) {
	if !m.Enabled() {
		slog.Warn("EPG grabber not found, guide generation disabled", "dir", m.dir)
		return
	}
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	dirtySince := time.Time{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-m.trigger:
			m.grab(ctx)
		case <-t.C:
			if !m.st.Ready() {
				continue
			}
			age := time.Since(time.Unix(m.st.MetaInt("epg_attempt_at"), 0))
			if m.st.MetaInt("epg_dirty") == 1 {
				if dirtySince.IsZero() {
					dirtySince = time.Now()
				}
				// debounce edits so a burst of changes triggers one grab
				delay := m.set.Minutes("epg_change_delay_minutes")
				if time.Since(dirtySince) >= delay && age >= 2*delay {
					dirtySince = time.Time{}
					m.grab(ctx)
				}
			} else if age >= m.set.Hours("epg_interval_hours") {
				m.grab(ctx)
			}
		}
	}
}

type guideRow struct{ ck, site, siteID, lang, name string }

// mapChannels maps each selected channel key to a guide: the exact feed if one
// exists, else a feed-less guide, else any guide of the same channel (SD/HD
// variants share programming). xmltv_id is always the channel key.
func (m *Manager) mapChannels() (rows []guideRow, unmapped int, err error) {
	links, err := m.st.SelectedEPG()
	if err != nil {
		return nil, 0, err
	}
	for ck, link := range links {
		g := guideRow{ck: ck}
		id := link
		if id == "" {
			id = ck
		}
		channel, feed, _ := strings.Cut(id, "@")
		var name string
		err := m.st.DB.QueryRow(`SELECT g.site,g.site_id,g.lang,COALESCE((SELECT name FROM channels WHERE id=g.channel),g.channel)
			FROM guides g WHERE g.channel=?
			ORDER BY (g.feed=?) DESC, (g.feed='') DESC LIMIT 1`, channel, feed).Scan(&g.site, &g.siteID, &g.lang, &name)
		if err != nil {
			unmapped++
			continue
		}
		g.name = name
		rows = append(rows, g)
	}
	return rows, unmapped, nil
}

func (m *Manager) grab(ctx context.Context) {
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return
	}
	m.running = true
	m.mu.Unlock()
	m.st.SetMetaInt("epg_attempt_at", time.Now().Unix())
	m.st.SetMetaInt("epg_dirty", 0)
	err := m.doGrab(ctx)
	m.mu.Lock()
	m.running = false
	if err != nil {
		m.lastErr = err.Error()
		slog.Error("epg grab failed; keeping last good guide", "err", err)
	} else {
		m.lastErr = ""
	}
	m.mu.Unlock()
}

func (m *Manager) doGrab(ctx context.Context) error {
	rows, unmapped, err := m.mapChannels()
	if err != nil {
		return err
	}
	m.st.SetMetaInt("epg_channels", int64(len(rows)))
	m.st.SetMetaInt("epg_unmapped", int64(unmapped))
	if len(rows) == 0 {
		return errors.New("no selected channels have an EPG source")
	}
	work := filepath.Join(m.dataDir, "epg", "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		return err
	}
	var b strings.Builder
	b.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<channels>\n")
	for _, r := range rows {
		fmt.Fprintf(&b, "  <channel site=\"%s\" lang=\"%s\" xmltv_id=\"%s\" site_id=\"%s\">%s</channel>\n",
			html.EscapeString(r.site), html.EscapeString(r.lang), html.EscapeString(r.ck), html.EscapeString(r.siteID), html.EscapeString(r.name))
	}
	b.WriteString("</channels>\n")
	chFile := filepath.Join(work, "channels.xml")
	out := filepath.Join(work, "guide.xml")
	os.Remove(out)
	if err := os.WriteFile(chFile, []byte(b.String()), 0o644); err != nil {
		return err
	}
	args := []string{"run", "grab", "---", "--channels=" + chFile, "--output=" + out, "--maxConnections=" + strconv.Itoa(m.set.Int("epg_max_connections"))}
	if days := m.set.Int("epg_days"); days > 0 {
		args = append(args, "--days="+strconv.Itoa(days))
	}
	gctx, cancel := context.WithTimeout(ctx, m.set.Minutes("epg_timeout_minutes"))
	defer cancel()
	cmd := exec.CommandContext(gctx, "npm", args...)
	cmd.Dir = m.dir
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	slog.Info("epg grab started", "channels", len(rows))
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("grabber: %w: %s", err, tail(buf.String(), 400))
	}
	fi, err := os.Stat(out)
	if err != nil || fi.Size() < 100 {
		return errors.New("grabber produced no guide")
	}
	if err := validXMLTV(out); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(m.guidePath()), 0o755); err != nil {
		return err
	}
	if err := os.Rename(out, m.guidePath()); err != nil { // atomic swap: last good copy survives failures
		return err
	}
	m.st.SetMetaInt("epg_ok_at", time.Now().Unix())
	m.cacheMu.Lock()
	m.filtered = map[int64]filteredGuide{}
	m.cacheMu.Unlock()
	slog.Info("epg grab finished", "size", fi.Size())
	return nil
}

func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

func validXMLTV(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	d := xml.NewDecoder(f)
	for {
		t, err := d.Token()
		if err != nil {
			return fmt.Errorf("invalid XMLTV: %w", err)
		}
		if se, ok := t.(xml.StartElement); ok {
			if se.Name.Local != "tv" {
				return errors.New("invalid XMLTV: root is not <tv>")
			}
			return nil
		}
	}
}

// Guide returns the guide filtered to a profile's channels (plain and gzipped).
func (m *Manager) Guide(profileID, rev int64, cks map[string]bool) (plain, gz []byte, err error) {
	key := fmt.Sprintf("%d:%d", rev, m.st.MetaInt("epg_ok_at"))
	m.cacheMu.Lock()
	if g, ok := m.filtered[profileID]; ok && g.key == key {
		m.cacheMu.Unlock()
		return g.xml, g.gz, nil
	}
	m.cacheMu.Unlock()
	f, err := os.Open(m.guidePath())
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	var out bytes.Buffer
	if err := filterXMLTV(f, &out, cks); err != nil {
		return nil, nil, err
	}
	var zb bytes.Buffer
	zw := gzip.NewWriter(&zb)
	zw.Write(out.Bytes())
	zw.Close()
	m.cacheMu.Lock()
	m.filtered[profileID] = filteredGuide{key: key, xml: out.Bytes(), gz: zb.Bytes()}
	m.cacheMu.Unlock()
	return out.Bytes(), zb.Bytes(), nil
}

// filterXMLTV copies only the <channel>/<programme> elements of wanted IDs.
func filterXMLTV(r io.Reader, w io.Writer, keep map[string]bool) error {
	d := xml.NewDecoder(r)
	d.Strict = false
	io.WriteString(w, xml.Header)
	e := xml.NewEncoder(w)
	depth := 0
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if depth == 1 {
				attr := "id"
				if t.Name.Local == "programme" {
					attr = "channel"
				}
				if t.Name.Local != "channel" && t.Name.Local != "programme" {
					_ = d.Skip()
					continue
				}
				id := ""
				for _, a := range t.Attr {
					if a.Name.Local == attr {
						id = a.Value
					}
				}
				if !keep[id] {
					_ = d.Skip()
					continue
				}
			}
			depth++
			if err := e.EncodeToken(xml.CopyToken(t)); err != nil {
				return err
			}
		case xml.EndElement:
			depth--
			if err := e.EncodeToken(t); err != nil {
				return err
			}
			if depth == 1 {
				e.Flush()
				io.WriteString(w, "\n")
			}
		case xml.CharData:
			if depth >= 2 {
				if err := e.EncodeToken(xml.CopyToken(t)); err != nil {
					return err
				}
			}
		}
	}
	e.Flush()
	return nil
}
