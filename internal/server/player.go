package server

import (
	"database/sql"
	"net/http"
	"net/url"
	"strings"

	"iptv-proxy/internal/playlist"
	"iptv-proxy/internal/proxy"
	"iptv-proxy/internal/store"
)

func (s *Server) profileFromToken(w http.ResponseWriter, r *http.Request) (*store.Profile, bool) {
	p, err := s.st.ProfileByToken(r.PathValue("token"))
	if err == sql.ErrNoRows {
		http.NotFound(w, r)
		return nil, false
	} else if err != nil {
		http.Error(w, "internal error", 500)
		return nil, false
	}
	return p, true
}

// playlist serves /p/{token}/playlist.m3u. Dead-only channels are hidden and
// no upstream headers or credentials ever appear in it.
func (s *Server) playlist(w http.ResponseWriter, r *http.Request) {
	p, ok := s.profileFromToken(w, r)
	if !ok {
		return
	}
	if !s.st.Ready() {
		w.Header().Set("Retry-After", "60")
		http.Error(w, "channel data is still loading, try again shortly", http.StatusServiceUnavailable)
		return
	}
	chs, err := s.st.ProfileChannels(p.ID)
	if err != nil {
		http.Error(w, "internal error", 500)
		return
	}
	pub := s.pub(r)
	var items []playlist.Out
	for _, c := range chs {
		// every stream counts here, also ones excluded from health checks: a channel is left out only when it has
		// no stream or all of them are marked not working
		var all, dead int
		s.st.DB.QueryRow(`SELECT COUNT(*), COALESCE(SUM(status=2),0) FROM streams WHERE ck=?`, c.CK).Scan(&all, &dead)
		if all == 0 || dead == all {
			continue
		}
		info, ok := s.st.EntryInfo(c.CK)
		if !ok {
			continue
		}
		name := info.Name
		if c.Name != "" {
			name = c.Name
		}
		items = append(items, playlist.Out{
			TvgID: c.CK, Name: name, Logo: info.Logo, Group: info.Group,
			URL: pub + "/p/" + p.Token + "/channel/" + url.PathEscape(c.CK) + ".m3u8",
		})
	}
	epgURL := ""
	if s.epg.Enabled() {
		epgURL = pub + "/p/" + p.Token + "/epg.xml"
	}
	w.Header().Set("Content-Type", "audio/x-mpegurl; charset=utf-8")
	w.Header().Set("Content-Disposition", `inline; filename="playlist.m3u"`)
	w.Header().Set("Access-Control-Allow-Origin", "*")
	playlist.Write(w, epgURL, items)
}

func (s *Server) guide(gz bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := s.profileFromToken(w, r)
		if !ok {
			return
		}
		if !s.epg.Enabled() || !s.epg.HasGuide() {
			w.Header().Set("Retry-After", "300")
			http.Error(w, "guide is not available yet", http.StatusServiceUnavailable)
			return
		}
		chs, err := s.st.ProfileChannels(p.ID)
		if err != nil {
			http.Error(w, "internal error", 500)
			return
		}
		keep := map[string]bool{}
		for _, c := range chs {
			keep[c.CK] = true
		}
		plain, z, err := s.epg.Guide(p.ID, p.Rev, keep)
		if err != nil {
			http.Error(w, "guide unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Access-Control-Allow-Origin", "*")
		if gz {
			w.Header().Set("Content-Type", "application/gzip")
			w.Write(z)
			return
		}
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		w.Write(plain)
	}
}

// channel serves the stable URL /p/{token}/channel/{ck}.m3u8. The stream URL
// and headers are resolved at play time, so upstream changes never break a
// player's channel list.
func (s *Server) channel(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Range")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	p, ok := s.profileFromToken(w, r)
	if !ok {
		return
	}
	ck := r.PathValue("file")
	if i := strings.LastIndexByte(ck, '.'); i >= 0 && (ck[i:] == ".m3u8" || ck[i:] == ".ts") {
		ck = ck[:i]
	}
	pc, err := s.st.ProfileChannel(p.ID, ck)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	streams, err := s.st.Streams(ck)
	if err != nil || len(streams) == 0 {
		http.NotFound(w, r)
		return
	}
	if pc.StreamURL != "" { // preferred stream first, the others stay as backups
		for i, st := range streams {
			if st.URL == pc.StreamURL {
				streams[0], streams[i] = streams[i], streams[0]
				break
			}
		}
	}
	mode := pc.Mode
	if mode == "" || mode == "auto" {
		mode = p.Mode
	}

	if mode != "proxy" {
		first := streams[0]
		for _, st := range streams {
			if st.Status != store.StatusDead {
				first = st
				break
			}
		}
		if mode == "redirect" || (first.UserAgent == "" && first.Referrer == "") {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			http.Redirect(w, r, first.URL, http.StatusFound)
			return
		}
	}

	release, ok := s.px.Acquire()
	if !ok {
		http.Error(w, "too many connections", http.StatusServiceUnavailable)
		return
	}
	defer release()
	tried := 0
	for _, st := range streams {
		if tried >= 4 || r.Context().Err() != nil {
			break
		}
		tried++
		ua := st.UserAgent
		if ua == "" {
			ua = proxy.CompatUA(s.set, p.Compat)
		}
		up, err := s.px.Open(r.Context(), r, proxy.Payload{U: st.URL, R: st.Referrer, A: ua})
		if err != nil {
			if r.Context().Err() == nil {
				s.st.MarkPlayback(st.URL, false)
			}
			continue
		}
		s.st.MarkPlayback(st.URL, true)
		s.px.Respond(w, r, up, s.pub(r))
		return
	}
	http.Error(w, "no working stream for this channel", http.StatusBadGateway)
}
