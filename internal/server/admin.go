package server

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/skip2/go-qrcode"

	"iptv-proxy/internal/health"
	"iptv-proxy/internal/store"
	isync "iptv-proxy/internal/sync"
)

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<20)).Decode(v); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return false
	}
	return true
}

// ownProfile reads the profile id from the path and checks the signed-in user may use that profile:
// their own, or any when their role may see all. Someone else's profile is reported as not found.
func (s *Server) ownProfile(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, ok := pathID(w, r)
	if !ok {
		return 0, false
	}
	p, err := s.st.GetProfile(id)
	if err != nil || (owner(r) != 0 && p.OwnerID != owner(r)) {
		httpErr(w, 404, "profile not found")
		return 0, false
	}
	return id, true
}

func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		httpErr(w, http.StatusBadRequest, "invalid id")
		return 0, false
	}
	return id, true
}

func (s *Server) statusData() map[string]any {
	var channels, streams, working, dead, skipped int
	q := s.st.DB
	q.QueryRow(`SELECT COUNT(*) FROM entries`).Scan(&channels)
	q.QueryRow(`SELECT COUNT(*), COALESCE(SUM(status=1),0), COALESCE(SUM(status=2),0) FROM streams`).Scan(&streams, &working, &dead)
	q.QueryRow(`SELECT COUNT(*) FROM skipped`).Scan(&skipped)
	running, done, total := s.health.Progress()
	return map[string]any{
		"ready":    s.st.Ready(),
		"channels": channels, "streams": streams, "working": working, "dead": dead, "skipped": skipped,
		"sync":      map[string]any{"running": s.sync.Running(), "last": s.st.MetaInt("synced_at"), "error": s.st.Meta("sync_error")},
		"epg":       s.epg.Status(),
		"health":    map[string]any{"running": running, "done": done, "total": total, "scope": s.set.String("health_scope")},
		"intervals": map[string]int{"sync_hours": s.set.Int("sync_interval_hours"), "epg_hours": s.set.Int("epg_interval_hours")},
	}
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) { writeJSON(w, s.statusData()) }

func (s *Server) healthDetails(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.health.Details())
}

func (s *Server) healthRun(w http.ResponseWriter, r *http.Request) {
	var in health.Target
	if !decode(w, r, &in) {
		return
	}
	n, err := s.health.CheckNow(in)
	switch {
	case errors.Is(err, health.ErrBusy), errors.Is(err, health.ErrNothing):
		httpErr(w, http.StatusConflict, err.Error())
	case errors.Is(err, health.ErrTarget):
		httpErr(w, http.StatusBadRequest, err.Error())
	case err != nil:
		httpErr(w, 500, err.Error())
	default:
		w.WriteHeader(http.StatusAccepted)
		writeJSON(w, map[string]int{"streams": n})
	}
}

func (s *Server) healthStop(w http.ResponseWriter, r *http.Request) {
	if !s.health.Stop() {
		httpErr(w, http.StatusConflict, "no check is running")
		return
	}
	writeJSON(w, map[string]bool{"stopping": true})
}

func (s *Server) healthPause(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Paused bool `json:"paused"`
	}
	if !decode(w, r, &in) {
		return
	}
	s.health.SetPaused(in.Paused)
	writeJSON(w, map[string]bool{"paused": in.Paused})
}

func (s *Server) healthDown(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	num := func(k string) int { n, _ := strconv.Atoi(q.Get(k)); return n }
	writeJSON(w, s.health.Down(health.DownQuery{Q: q.Get("q"), Reason: q.Get("reason"), Offset: num("offset"), Limit: num("limit")}))
}

func (s *Server) channelDetail(w http.ResponseWriter, r *http.Request) {
	d, ok := s.health.Channel(r.URL.Query().Get("ck"), owner(r))
	if !ok {
		httpErr(w, 404, "channel not found")
		return
	}
	writeJSON(w, d)
}

func (s *Server) healthRounds(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	offset, _ := strconv.Atoi(q.Get("offset"))
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 500 {
		limit = 10
	}
	items, total := s.st.Rounds(offset, limit)
	writeJSON(w, map[string]any{"total": total, "items": items, "trend": s.st.Trend(60)})
}

func (s *Server) healthRound(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	num := func(k string) int { n, _ := strconv.Atoi(q.Get(k)); return n }
	id, _ := strconv.ParseInt(q.Get("id"), 10, 64)
	d, ok := s.health.Round(health.RoundQuery{ID: id, Result: q.Get("result"), Q: q.Get("q"), Offset: num("offset"), Limit: num("limit")})
	if !ok {
		httpErr(w, 404, "round not found")
		return
	}
	writeJSON(w, d)
}

// healthExcluded lists the streams left out of health checks.
func (s *Server) healthExcluded(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	offset, _ := strconv.Atoi(q.Get("offset"))
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 500 {
		limit = 10
	}
	items, total := s.st.Excluded(q.Get("q"), max(offset, 0), limit)
	writeJSON(w, map[string]any{"total": total, "items": items})
}

// healthExclude excludes a stream from health checks or includes it again.
func (s *Server) healthExclude(w http.ResponseWriter, r *http.Request) {
	var in struct {
		URL      string `json:"url"`
		Excluded bool   `json:"excluded"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !s.st.SetExcluded(in.URL, in.Excluded) {
		httpErr(w, 404, "stream not found")
		return
	}
	writeJSON(w, map[string]bool{"excluded": in.Excluded})
}

func (s *Server) triggerSync(w http.ResponseWriter, r *http.Request) {
	if s.sync.Running() {
		httpErr(w, http.StatusConflict, "sync already running")
		return
	}
	go func() { _ = s.sync.SyncNow() }()
	w.WriteHeader(http.StatusAccepted)
	writeJSON(w, map[string]bool{"started": true})
}

func (s *Server) triggerEPG(w http.ResponseWriter, r *http.Request) {
	if !s.epg.Enabled() {
		httpErr(w, http.StatusConflict, "EPG grabber is not installed in this image")
		return
	}
	s.epg.RunNow()
	w.WriteHeader(http.StatusAccepted)
	writeJSON(w, map[string]bool{"started": true})
}

func (s *Server) filters(w http.ResponseWriter, r *http.Request) {
	cats, countries, langs, err := s.st.Filters()
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	srcs, _ := s.sync.ListCustom()
	type src struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	}
	sources := []src{}
	for _, c := range srcs {
		sources = append(sources, src{c.ID, c.Name})
	}
	writeJSON(w, map[string]any{"categories": cats, "countries": countries, "languages": langs, "sources": sources})
}

// entryQuery reads the channel filters shared by the list and "select all matching".
func entryQuery(r *http.Request) store.EntryQuery {
	q := r.URL.Query()
	num := func(k string) int { n, _ := strconv.Atoi(q.Get(k)); return n }
	return store.EntryQuery{
		Q: strings.TrimSpace(q.Get("q")), Country: q.Get("country"), Category: q.Get("category"), Language: q.Get("language"), Source: q.Get("source"),
		WorkingOnly: q.Get("working") == "1", NSFW: q.Get("nsfw") == "1", InProfile: q.Get("profile") == "1", Owner: owner(r),
		Sort: q.Get("sort"), Offset: num("offset"), Limit: num("limit"),
	}
}

func (s *Server) channels(w http.ResponseWriter, r *http.Request) {
	items, total, err := s.st.QueryEntries(entryQuery(r))
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]any{"total": total, "items": items})
}

// channelKeys lists every channel matching the filters (key, name and profiles only), so the page can
// select all of them, not just the visible page.
func (s *Server) channelKeys(w http.ResponseWriter, r *http.Request) {
	q := entryQuery(r)
	q.All = true
	items, _, err := s.st.QueryEntries(q)
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	type key struct {
		CK       string  `json:"ck"`
		Name     string  `json:"name"`
		Profiles []int64 `json:"profiles"`
	}
	out := make([]key, len(items))
	for i, e := range items {
		out[i] = key{e.CK, e.Name, e.Profiles}
	}
	writeJSON(w, out)
}

func (s *Server) entryStreams(w http.ResponseWriter, r *http.Request) {
	st, err := s.st.Streams(r.PathValue("ck"))
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	writeJSON(w, st)
}

// ---- profiles ----

type profileView struct {
	store.Profile
	Channels []store.ProfileChannel `json:"channels"`
	URLs     map[string]string      `json:"urls"`
}

func (s *Server) profileURLs(r *http.Request, p *store.Profile) map[string]string {
	b := s.pub(r) + "/p/" + p.Token
	return map[string]string{"playlist": b + "/playlist.m3u", "epg": b + "/epg.xml", "epg_gz": b + "/epg.xml.gz"}
}

func (s *Server) listProfiles(w http.ResponseWriter, r *http.Request) {
	ps, err := s.st.ListProfiles(owner(r))
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	writeJSON(w, ps)
}

func (s *Server) createProfile(w http.ResponseWriter, r *http.Request) {
	var in struct{ Name string }
	if !decode(w, r, &in) {
		return
	}
	p, err := s.st.CreateProfile(userFrom(r).ID, in.Name)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	writeJSON(w, p)
}

func (s *Server) getProfile(w http.ResponseWriter, r *http.Request) {
	id, ok := s.ownProfile(w, r)
	if !ok {
		return
	}
	p, err := s.st.GetProfile(id)
	if errors.Is(err, sql.ErrNoRows) {
		httpErr(w, 404, "profile not found")
		return
	} else if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	chs, err := s.st.ProfileChannels(id)
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	p.Count = len(chs)
	writeJSON(w, profileView{Profile: *p, Channels: chs, URLs: s.profileURLs(r, p)})
}

func (s *Server) patchProfile(w http.ResponseWriter, r *http.Request) {
	id, ok := s.ownProfile(w, r)
	if !ok {
		return
	}
	p, err := s.st.GetProfile(id)
	if err != nil {
		httpErr(w, 404, "profile not found")
		return
	}
	in := struct{ Name, Mode, Compat *string }{}
	if !decode(w, r, &in) {
		return
	}
	if in.Name != nil {
		p.Name = *in.Name
	}
	if in.Mode != nil {
		p.Mode = *in.Mode
	}
	if in.Compat != nil {
		p.Compat = *in.Compat
	}
	if err := s.st.UpdateProfile(id, p.Name, p.Mode, p.Compat); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) deleteProfile(w http.ResponseWriter, r *http.Request) {
	id, ok := s.ownProfile(w, r)
	if !ok {
		return
	}
	if err := s.st.DeleteProfile(id); err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	s.epg.MarkDirty()
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) rotateToken(w http.ResponseWriter, r *http.Request) {
	id, ok := s.ownProfile(w, r)
	if !ok {
		return
	}
	if err := s.st.RotateToken(id); err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

type cksIn struct {
	CKs []string `json:"cks"`
}

func (s *Server) addChannels(w http.ResponseWriter, r *http.Request) {
	id, ok := s.ownProfile(w, r)
	var in cksIn
	if !ok || !decode(w, r, &in) {
		return
	}
	if err := s.st.AddChannels(id, in.CKs); err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	s.epg.MarkDirty()
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) removeChannels(w http.ResponseWriter, r *http.Request) {
	id, ok := s.ownProfile(w, r)
	var in cksIn
	if !ok || !decode(w, r, &in) {
		return
	}
	if err := s.st.RemoveChannels(id, in.CKs); err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	s.epg.MarkDirty()
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) replaceChannels(w http.ResponseWriter, r *http.Request) {
	id, ok := s.ownProfile(w, r)
	var in struct {
		Channels []store.ProfileChannel `json:"channels"`
	}
	if !ok || !decode(w, r, &in) {
		return
	}
	if err := s.st.ReplaceChannels(id, in.Channels); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	s.epg.MarkDirty()
	writeJSON(w, map[string]bool{"ok": true})
}

// ---- custom sources ----

func (s *Server) listCustom(w http.ResponseWriter, r *http.Request) {
	c, err := s.sync.ListCustom()
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	writeJSON(w, c)
}

func (s *Server) addCustom(w http.ResponseWriter, r *http.Request) {
	var in struct{ Name, URL, M3U string }
	if !decode(w, r, &in) {
		return
	}
	id, err := s.sync.AddCustom(in.Name, in.URL, in.M3U)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	writeJSON(w, map[string]int64{"id": id})
}

func (s *Server) getCustom(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	list, err := s.sync.ListCustom()
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	for _, c := range list {
		if c.ID != id {
			continue
		}
		text, err := s.sync.CustomPlaylist(id)
		if err != nil {
			httpErr(w, 500, err.Error())
			return
		}
		writeJSON(w, struct {
			isync.CustomSource
			M3U string `json:"m3u"`
		}{c, text})
		return
	}
	httpErr(w, 404, "source not found")
}

func (s *Server) updateCustom(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		Name         string `json:"name"`
		URL          string `json:"url"`
		M3U          string `json:"m3u"`
		RefreshHours int    `json:"refresh_hours"`
	}
	if !decode(w, r, &in) {
		return
	}
	err := s.sync.UpdateCustom(id, isync.CustomEdit{Name: in.Name, URL: in.URL, M3U: in.M3U, RefreshHours: in.RefreshHours})
	if errors.Is(err, sql.ErrNoRows) {
		httpErr(w, 404, "source not found")
		return
	} else if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) refreshCustom(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.sync.RefreshCustom(id); err != nil {
		httpErr(w, 502, err.Error())
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) deleteCustom(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.sync.DeleteCustom(id); err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// reasonGroup collapses "unknown channel ID Foo.us" into one filterable reason.
const reasonGroup = `CASE WHEN reason LIKE 'unknown channel ID%' THEN 'unknown channel ID' ELSE reason END`

func (s *Server) skipped(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	if limit <= 0 || limit > 1000 {
		limit = 10
	}
	where, args := []string{"1=1"}, []any{}
	if t := strings.TrimSpace(q.Get("q")); t != "" {
		like := "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(t) + "%"
		where = append(where, `(ref LIKE ? ESCAPE '\' OR url LIKE ? ESCAPE '\' OR reason LIKE ? ESCAPE '\')`)
		args = append(args, like, like, like)
	}
	if v := q.Get("source"); v != "" {
		where = append(where, "source=?")
		args = append(args, v)
	}
	if v := q.Get("reason"); v != "" {
		where = append(where, reasonGroup+"=?")
		args = append(args, v)
	}
	cond := strings.Join(where, " AND ")
	var total int
	s.st.DB.QueryRow(`SELECT COUNT(*) FROM skipped WHERE `+cond, args...).Scan(&total)
	rows, err := s.st.DB.Query(`SELECT source,ref,url,reason FROM skipped WHERE `+cond+` ORDER BY id LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	defer rows.Close()
	items := []map[string]string{}
	for rows.Next() {
		var a, b, u, c string
		rows.Scan(&a, &b, &u, &c)
		items = append(items, map[string]string{"source": a, "ref": b, "url": u, "reason": c})
	}
	type facet struct {
		Value string `json:"value"`
		Count int    `json:"count"`
	}
	facets := func(expr string) []facet {
		out := []facet{}
		fr, err := s.st.DB.Query(`SELECT ` + expr + ` v, COUNT(*) FROM skipped GROUP BY v ORDER BY 2 DESC`)
		if err != nil {
			return out
		}
		defer fr.Close()
		for fr.Next() {
			var f facet
			fr.Scan(&f.Value, &f.Count)
			out = append(out, f)
		}
		return out
	}
	srcFacets := facets("source")
	// list every source, also those with nothing skipped, so none seems to be missing
	have := map[string]bool{}
	for _, f := range srcFacets {
		have[f.Value] = true
	}
	if cs, err := s.sync.ListCustom(); err == nil {
		for _, c := range cs {
			if v := "custom:" + strconv.FormatInt(c.ID, 10); !have[v] {
				srcFacets = append(srcFacets, facet{Value: v, Count: 0})
			}
		}
	}
	writeJSON(w, map[string]any{"total": total, "items": items, "reasons": facets(reasonGroup), "sources": srcFacets})
}

// ---- settings / QR ----

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"fields": s.set.All(), "detected_address": s.detectedAddress(r)})
}

func (s *Server) putSettings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Values map[string]any `json:"values"`
	}
	if !decode(w, r, &in) {
		return
	}
	if errs := s.set.Apply(in.Values); errs != nil {
		httpFieldErrs(w, errs)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) qr(w http.ResponseWriter, r *http.Request) {
	text := r.URL.Query().Get("text")
	if text == "" || len(text) > 1000 {
		httpErr(w, 400, "text required")
		return
	}
	png, err := qrcode.Encode(text, qrcode.Medium, 280)
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.Write(png)
}
