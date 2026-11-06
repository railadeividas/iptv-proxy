package store

// Channel status, the same everywhere (Channels page, profiles, Health page):
//   - working: at least one stream works and none fails
//   - partly working: at least one stream works and at least one fails
//   - not working: every stream fails
//   - otherwise no verdict yet
//
// Streams excluded from health checks keep their last result but do not count towards the status, unless every stream
// of the channel is excluded (then the last results are all there is).

// ChannelHealthCTE is a common table expression `ch(ck, t, n, w, d, x)`: per channel all its streams (t), the streams
// that count towards its status (n), how many of those work (w) and fail (d), and how many streams are excluded from
// health checks (x).
// Use it as `WITH ` + ChannelHealthCTE + ` SELECT …` (or after other CTEs, separated by a comma).
const ChannelHealthCTE = `ch AS (
	SELECT ck, COUNT(*) AS t, SUM(counts) AS n, SUM(counts AND status=1) AS w, SUM(counts AND status=2) AS d, SUM(ex) AS x FROM (
		SELECT ck, status, ex, (ex=0 OR MIN(ex) OVER (PARTITION BY ck)=1) AS counts FROM (
			SELECT s.ck AS ck, s.status AS status, (s.url IN (SELECT url FROM health_excluded)) AS ex FROM streams s))
	GROUP BY ck)`

// Channel states as numbers, for SQL (sorting, filtering) and Go.
const (
	ChannelWorking    = 0
	ChannelPartly     = 1
	ChannelNoVerdict  = 2
	ChannelNotWorking = 3
)

// ChannelStateSQL is the state of a channel from the `ch` columns of the channel's row (NULL when it has no streams).
// It also sorts: working, partly working, no verdict yet, not working.
const ChannelStateSQL = `CASE WHEN COALESCE(ch.w,0)>0 AND COALESCE(ch.d,0)=0 THEN 0 WHEN COALESCE(ch.w,0)>0 THEN 1
	WHEN COALESCE(ch.n,0)>0 AND ch.d>=ch.n THEN 3 ELSE 2 END`
