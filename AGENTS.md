# AGENTS.md

Guidance for anyone (human or AI) changing this repository. Read it before you start.

## What this is

A self-hosted IPTV proxy in one Docker container: browse the iptv-org catalogue, pick channels into profiles, get a
playlist (M3U) and EPG (XMLTV) link per profile; a proxy fixes headers/compatibility so streams play everywhere.
Go backend (single static binary, pure-Go SQLite) + Vue 3 admin SPA embedded into the binary. See `README.md` for features
and configuration, `plan.md` for the original design.

```
cmd/server        main: wiring only
internal/config   command-line flags (the only bootstrap values)
internal/auth     roles, permissions, password hashing
internal/store    SQLite: baseline schema, migrations, entries (browse table), profiles
internal/sync     iptv-org API sync, custom sources (M3U)
internal/proxy    signed links, HLS/DASH rewriting, upstream requests
internal/epg      guide grabber + per-profile XMLTV
internal/health   stream probing, Details() for the Health page
internal/events   WebSocket event hub
internal/activity who uses the player links: running sessions (memory), saved to the database; Stats page data
internal/server   HTTP routes, admin API, player endpoints, SPA hosting
internal/playlist M3U parsing/writing
web/              Vue 3 + vite; `web/dist` is built output and embedded (not committed)
```

## The rule: this is production software

People run this app with real data: users, profiles, custom sources, settings, check history, and playlist links saved in their
players. An upgrade must never lose or break any of it. Assume backward compatibility is required; if a break looks unavoidable, stop
and say so instead of making it.

**What is a contract (changing it needs a migration or a compatible path):**

- **The database.** Every schema or data change is a **migration**. Never edit `baselineSchema` (`internal/store/baseline.go`, frozen at
  version 4) or a migration that has shipped; only append to `migrations` in `internal/store/migrations.go`. Open() applies the
  pending ones at startup, each in its own transaction (foreign keys off during the step and checked before the commit, so rebuilding
  a table is safe), after copying the database to `iptv.db.bak-v<version>`. A database from a newer build is refused. Details below.
- **Player URLs.** `/p/{token}/playlist.m3u`, `/p/{token}/epg.xml(.gz)`, `/p/{token}/channel/{id}.m3u8` and `/x/…`, `/xd/…` links are saved in
  players and apps. They keep working across releases. So do channel keys (`ck`, which are also the `tvg-id`) and the token format.
- **Settings.** Stored as `setting.<key>` rows in `meta`. Renaming a key, removing one or changing what its value means needs a
  migration that converts the stored value; a new setting needs a safe default; narrowing a min/max must not leave stored values
  invalid (clamp them in a migration).
- **Deployment.** The `-listen`, `-data`, `-epg` flags and their defaults, the `/data` layout, `/healthz`, the port, `/metrics` (when on), and "no
  environment variables". The only subcommand is `reset-password` (`cmd/server/resetpw.go`), the recovery path for a lost superuser
  password; keep it working, and don't add other subcommands or flags.
- **Sign-in data.** Password hashes carry their own parameters (`internal/auth`), so you can raise the cost without invalidating
  anyone. Changing the session cookie format signs everybody out: avoid it, and say so in the commit message if you must.

**What may change freely:** the admin UI and the admin API behind sign-in. They ship together in one binary, so routes, JSON fields,
pages and components can be renamed in one change (update every caller, the README and this file). Remove code that becomes unused.

**Removing or replacing something users rely on** (a setting, a URL, a behaviour): add the new way, migrate or keep the old data
working, and only remove the old path in a later commit, once people have had time to upgrade. Note it in the commit message.

### Migrations

- Adding a table or column, changing an index, converting data: write `migration{name, up(tx)}` and append it. `ALTER TABLE ADD COLUMN`
  is fine; for anything else use the rebuild recipe (create new table, copy, drop old, rename). Do not run `PRAGMA` or `VACUUM` in a step.
- A migration runs on a database full of real data and also on a brand-new one (new databases run the baseline, then every
  migration), so write it for both. Destructive steps (dropping a table or column that holds user data) need a reason in the name
  and a mention in the commit message.
- Add a test in `internal/store/migrate_test.go` that migrates a database containing data (the existing ones show how to swap in a
  migration list with `withMigrations`). Store tests use `store.Open(t.TempDir())`.
- Never add `ALTER TABLE` or `CREATE TABLE` anywhere else, and never "fix up" data at startup outside a migration.
- Never ask the user to delete `iptv.db` for a change you made. If you think you need that, you need a migration instead. The only
  databases refused for being too old are the ones from development builds before version 4.

### Builds and versions

- There are no tags and no GitHub Releases. Every push to `main` is tested and published by `.github/workflows/release.yml` as its
  own image, `ghcr.io/<owner>/<repo>:<short commit hash>`, and `latest` follows the newest one (builds never cancel each other; a
  separate job moves `latest` one at a time and only forward). **Every commit on `main` therefore ships**: keep `main`
  working, and never leave a migration or a contract change half done between commits.
- The project's GitHub address lives in `web/src/about.js` (the version badge links to the commit, the footer links use it).
- The version is the short commit hash and lives only in git. The workflow passes it to the Docker build (`VERSION`), which sets
  `internal/version.Version`; the UI shows it from `/api/session`. Never hardcode a version number in the code.
- With no release notes, the **commit message is the release note**: name every migration and every behaviour change users will
  notice in it (and a contract break you could not avoid, see the rule above). The README's Upgrading section describes the
  backup and how to roll back to an older build by its hash; keep it true.

## Build, run, test

```sh
cd web && npx vite build                 # UI -> web/dist (must run before building the binary, it is embedded)
CGO_ENABLED=0 go build -o iptv ./cmd/server
go vet ./... && go test ./...

# throw-away instance for manual / browser testing: ALWAYS use a temp data dir and a spare port
./iptv -data /tmp/x -listen 127.0.0.1:18087 -epg /nonexistent   # open it and create the superuser on the setup page
```

- Port 8810 and the Docker volume belong to the user. Never point tests at them.
- The user picks up UI/backend changes with `docker compose up -d --build` and a hard refresh; say so when you finish.
- Do not claim a UI change works without looking at it: drive the real page in headless Chrome (`playwright-core` with
  `/usr/bin/google-chrome`, `--no-sandbox`) against a temp instance, **measure** layout (bounding boxes) instead of
  eyeballing, and look at a screenshot. Check light and dark themes and a phone width (390px) for layout work.
- Add Go tests for backend behaviour (`internal/*/..._test.go`); store tests use `store.Open(t.TempDir())`. A change that touches the
  database also needs a migration test (see Migrations).

## Configuration: everything is a setting

- There are **no environment variables**. Deployment paths and the port are the flags `-listen`, `-data`, `-epg`
  (see `internal/config`). **Never add an environment variable or a flag for anything else.**
- Every tunable value (timeouts, intervals, limits, user agents, addresses, formats) is a **setting**: add one `Def` to
  `internal/settings/defs.go` (key, group = Settings tab, section, label, help, unit, min/max, default). Validation,
  storage and the Settings pages come from that list; no UI work is needed for a new field.
- Read settings **at the moment of use** (`set.Int("key")`, `set.Hours(...)`) so a change applies immediately. Where something is
  built once (an HTTP client, a limiter), rebuild it from `set.OnChange`. Don't copy a setting into a struct field at startup.
- A new hardcoded number or string that an operator might reasonably want different (a timeout, size, interval, header value,
  URL) is a bug: make it a setting. Protocol facts and internal plumbing stay constants (WebSocket ping, event coalescing,
  read buffers, SQLite pragmas, HTTP server header timeout, request-body size caps).
- Add a Go test that a changed setting takes effect without restarting.

## Accounts and permissions

- The first start has no users: `GET /api/session` says `setup_needed` and the SPA shows `/setup`, where `POST /api/setup` makes the
  one **superuser** (works only while the `users` table is empty). Sign-in is user name + password; sessions are signed cookies
  (`session:<user id>:<password fingerprint>`) re-checked against the database on every request, so deleting a user, changing a
  role or a password takes effect at once.
- **Roles and permissions live in `internal/auth` only** (`grants`). Routes are registered in `server.go` with `api(...)` (any signed-in
  user) or `perm(auth.X, ...)`. A new endpoint must choose one of them; never check a role name anywhere else. The UI hides what a
  role may not do with `can('perm')` (`src/store.js`), but the server is what enforces it.
- Everyone signed in may browse channels and manage their **own** profiles. Profiles have an `owner_id`; use `ownProfile()` for any
  `/api/profiles/{id}` route (someone else's profile is a 404) and `owner(r)` to scope queries (0 = everybody's, for roles with
  `profiles.all`). Profile ids in channel lists are scoped the same way, so one user never learns about another's profiles.
- The superuser role can't be given, changed or deleted. Deleting a user deletes their profiles (foreign key) and calls `epg.MarkDirty()`.

## Health checker

- `internal/health` runs *rounds*: automatic ones (what is due, limited by scope, pause, quiet hours) and manual ones (a `Target`:
  profiles, all, bad, unchecked, profile, source, channel). Both stop cleanly (`Stop`); an aborted request is never counted as a
  failed stream. The number of parallel checks, scope, pause and quiet hours are read live during a round.
- Excluded streams (`health_excluded`, keyed by stream URL like the status) are skipped by every round, automatic or manual, and by
  the due count and next-check time (`notExcluded` in `internal/health`). `Store.SetExcluded` excludes or includes one (including
  clears its failure run); `Store.AutoExclude` runs after each failed probe with the setting `health_auto_exclude_days` (0 = never), counted from
  `streams.fail_since` (set by the first failure of a run, cleared by a success; syncs carry it over like the status).
  The Health page lists them from `/api/health/excluded`; `POST /api/health/exclude` changes one.
- Results go through `Store.RecordProbe` (failure threshold, reason, per-channel history) or `Store.MarkPlayback` (a viewer's
  failed playback marks a stream at once). Never write `streams.status` directly.
- History is bounded by the settings `health_history_rounds`, `health_history_per_channel`, `health_history_total` (0 keeps
  nothing) and `health_history_days` (maximum age); changing one prunes immediately and every round ends with a prune.
- The Health page reads `/api/health` (overview), `/api/health/down` (needs attention), `/api/health/rounds`, `/api/health/round` (one round's stored results: they carry no round id, so a round owns the results whose time falls between its start and end); `/api/channel` returns one channel's catalogue facts, streams and stored check history (the shared channel dialog, also opened from the Channels page).
  The trend chart draws checked streams only (working / not working / excluded) so an unchecked catalogue does not flatten it.
- Channel status (working, partly working, not working, no verdict) has one rule: `store.ChannelHealthCTE` and
  `store.ChannelStateSQL` in SQL, `streamState()` in `src/util.js`. Excluded streams do not count unless all of a channel's
  streams are excluded; stream totals (`Counts`, round totals) count excluded streams apart. The playlist (`player.go`)
  ignores exclusion: it leaves out a channel only when every stream is not working.
- Tests that start real checks must use a local server and a fresh data dir, and must switch the scope to `off` first: the
  default scope (`selected`) probes every channel that is in a profile as soon as it is due.

## Stats

- `internal/activity` records who uses the player links; the Stats page shows it. Playlist and guide fetches call `Tracker.Client`
  (a device = profile + address + player name, with lifetime counters). A watch session is one device on one channel: the channel
  link (`player.go`) and every proxied segment (`proxy.Observe`, fed by the profile id `P` and channel key `C` that signed links carry)
  call `Tracker.Begin`, and the returned `Live` counts bytes and ends the request. A redirect is a session with mode `redirect` and no
  bytes: we never see the player again. Never do database work in the request path for sessions: `Flush` (run every
  `stats_save_interval_seconds`) inserts and updates them and ends the silent ones.
- The payload fields `P` and `C` are optional: links made before they existed still decode and are simply not counted.
- Privacy is settings, not code: `stats_ip_mode` (masked by default), the mask bits, the player name, the forwarded-address header.
  Anything that records an address must go through `Tracker.Addr` so they apply.
- Stats use no permission of their own: everybody signed in gets `/api/stats*`, scoped with `owner(r)` like profiles (someone else's
  profile id is a 404); the connection load is only sent to roles with `profiles.all`.
- History limits are settings (`stats_history_*`) pruned by `Store.PruneActivity`; the tables follow their profile (`ON DELETE CASCADE`).
- **Hourly samples**: what is not in the session tables (the proxy's cache hits and misses, hit bytes, retries, hold-overs; playlist and guide
  fetches per profile) is saved by `Flush` as the *increase* since the last flush into `stats_samples` (server-wide, one row per UTC hour) and
  `stats_profile_samples` (per profile, cascades). The in-memory counters are only read, never reset: `/metrics` shows them. Pruned with the
  other history. `Flush` also fills a one-hour in-memory ring (`Tracker.LiveSamples`) for the Live tab; it is empty after a restart.
- **Stats API** (all `api()`, scoped by `owner(r)`): `/api/stats` (totals, profiles, running sessions, proxy load), `/timeline` (sparse buckets
  per profile and mode, peaks of concurrent streams, active devices), `/patterns` (weekday-by-hour, session lengths, longest session),
  `/breakdown?by=channel|profile|player|network|host` (ranked, paged), `/samples` (per bucket, plus `by_profile` totals), `/live`, `/clients`, `/sessions`. Every one takes `from` / `to`
  (unix seconds, `to` exclusive and optional; default the last 7 days), `step` (bucket size: 300, 900, 3600, 86400 or 604800 seconds; none = the
  server picks about 120 bars, and a step that would give more than 500 bars is raised), `profile` (one id or several separated by commas; somebody else's makes the whole request a 404), `tz` (seconds east of UTC: buckets are cut where
  the viewer's hours, days and Mondays are) and `shift=1` (the range of the same length right before, for comparisons) through `statsFilter`;
  never build a range any other way. Rows are counted in the exact range, only the bucket grid is aligned. `/samples` is never finer than an
  hour and `/live` ignores the range. The `server` part of `/samples` is only for roles that see everybody's activity.
- **Stats page**: tabs **Live** (the default), **Overview, Channels, Profiles, Devices, Sessions, Proxy** (`/stats/:tab`; Proxy only for roles with
  `profiles.all`). The Profiles tab lists one row per profile (columns on a desktop, a small card per row on a phone), searchable, sortable and paged; everything in it follows the Time range: it must stay short with many profiles. The Time range is on every tab. Each card or tile wears a `Scope` badge (period / now / all time / since start): keep one on anything that does not follow the Time range. The time range (a Grafana-style picker: quick ranges that move with the clock, or an absolute from/to), the chart step ("Group by": hour, day or week, auto by default; one control in the top bar for every chart cut by time. The UI always sends an explicit `step`, never finer than an hour), profile, measure (sessions / data / watch time, switched inside the cards that follow it), chart type, split and compare live in the URL. Charts are
  hand-written SVG (`components/stats/StatChart.vue` and friends, shared logic in `src/stats.js`), no chart library; series colours are
  `--s1..--s5`/`--so` in `style.css`, and a profile keeps its colour on every tab (first five by id, the rest are "other").

## Metrics

- `/metrics` (`internal/server/metrics.go`, text format by hand in `internal/metrics`, no dependency) is **off by default**; the settings
  `metrics_enabled`, `metrics_access` (token or open), `metrics_token` (at least 16 characters; empty refuses everyone) and the detail
  switches decide. Disabled is a 404. Never log or expose the token; compare it in constant time.
- **Metric names and labels are a contract** like the player URLs: dashboards and alerts depend on them. Add new ones freely; renaming or
  removing one needs the old name kept for a while, and a note in the commit message.
- Keep cardinality bounded: labels are profile, mode, state, source, host and the channels being watched right now. Never label by
  client address, user agent, token, stream URL or an individual stream. Counters are in memory and reset on restart.
- Numbers are read at scrape time from the components (`Proxy.Stats`, `activity.Tracker.Counters/Now`, `health.Details`, store
  queries in `internal/store/metrics.go`). A new metric needs a line in `TestMetricsEndpoint` (it also checks the format).

## Backend conventions

- Go 1.26, `CGO_ENABLED=0`, `modernc.org/sqlite`. HTTP errors are JSON `{"error": "..."}` via `httpErr`.
- A channel key `ck` is `channel@feed` (or `channel`); custom channels are `custom:<sourceId>:<hash>`. The `tvg-id` in
  playlists equals the XMLTV id.
- Data changes must tell the UI: `store.Notify` kinds are `status`, `streams`, `catalog`, `profiles` (coalesced by the hub).
- Anything that changes what a profile contains must also call `epg.MarkDirty()`.
- Channels that disappear from a custom source (edit, refresh, delete) are removed from every profile
  (`Store.DropFromProfiles`); channels that stay keep their position and settings. A retitled channel keeps its identity by
  stream URL.
- Sync and refresh jobs keep the last good data when they fail.
- Proxy links are HMAC-signed; never add an endpoint that fetches arbitrary URLs.

## Frontend conventions

- Vue 3 SPA with history-mode routing. Pages, in the header's order: **Channels, Profiles, Sources, Health, Stats** (tabs, see Stats), **Settings**, shown according to the user's permissions (plus `/setup` and `/login`); Settings has tabs **General** (`/settings`), **iptv-org** (`/settings/iptv-org`, also shows API sync and EPG grabber status), **Health**, **Proxy**, **Stats**, **Metrics**, **Security** and **Users**. Filters, page and
  page size live in the URL so back/forward and shared links work. Live data arrives over the WebSocket (`src/ws.js`).
- Modals are native `<dialog>`. Confirmations use `ask()` from `src/store.js` (destructive ones are red); never use
  `window.confirm`. Anything that deletes or removes asks first.
- **One control height**: inputs, selects and buttons are `var(--ctl)` (36px). Don't set heights on controls. Don't put
  margins on inputs (use flex/grid `gap`); beware selectors like `.card input` that beat component styles by specificity.
- Side-by-side cards go in `.card-row` (equal height; put the action in `.card-foot`).
- Paging is the shared `Pager` component at the bottom of a list: range on the left, pages in the middle, per-page on the
  right. Page sizes are `5, 10, 25, 50, 100, 200, 500` everywhere and every list opens at 10: use `PAGE_SIZES` and `PAGE_SIZE`
  from `src/util.js`, never a list of your own.
- Colours come from the CSS variables in `src/style.css` (light / dark / system); never hardcode colours.
- Wording: streams are **working**, **not working**, **not checked yet** or **excluded** (not "dead"/"unknown"); channels can
  also be **partly working**.
- Keep layouts working at 390px wide with no horizontal scrolling.
- `overflow-x:auto` also turns `overflow-y` into `auto`, so a 1px vertical overflow (a negative margin, an outline) shows a
  stray scrollbar with arrows. Set `overflow-y:hidden` too, and draw dividers with `box-shadow: inset` instead of
  negative margins. Headless Chrome hides scrollbar widths, so test for `scrollHeight > clientHeight` on scrolling elements.

## Dependencies and CI

- `.github/dependabot.yml` watches Go modules, `web/` (npm), the Dockerfile base images and the GitHub Actions, weekly, grouped.
  When you add another place that pins versions (a new directory with a `package.json`, another Dockerfile, …) add it there too.
- **Node follows the current LTS automatically**: the `Dockerfile` uses `node:lts-alpine` (both stages) and CI uses `lts/*`, so nothing to
  bump by hand and never an end-of-life odd release. A new LTS arrives in October; the weekly rebuild then moves to it, and the UI build
  inside the image (and CI) fails before anything is published if it breaks, and so does the EPG grabber smoke test (below).
  Go is pinned to one patch in `go.mod` and the `Dockerfile`: bump both together.
- **EPG grabber tests.** `internal/epg/grab_test.go` runs `doGrab` against a fake `npm` (command line, channel file, settings read at use,
  a failed or broken grab keeps the last good guide). `scripts/epg-smoke.sh` runs the real iptv-org/epg inside the built image against a local
  fixture site, with no internet: every option we pass must still exist (the grabber follows its `master` branch) and a grab must produce
  valid XMLTV. CI runs it in the `epg-grabber` job, so it also gates the image build. If you change how the grabber is called in
  `internal/epg`, change both.
- `ci.yml` (vet, tests, govulncheck, UI build, the `epg-grabber` image test) runs on every pull request; `release.yml` runs it and then publishes the image on every push to `main`, weekly and on demand.

## License

The code is under the Unlicense (`LICENSE`). Third-party code keeps its own licence: `THIRD_PARTY_NOTICES` lists the Go modules linked
into the binary and the npm packages bundled into the UI, and the image carries both files in `/licenses`. After a dependency
changes (a Dependabot pull request, or a new import), run `python3 scripts/notices.py` and commit the result; check that the `SHIPPED`
list in the script still names the npm packages Vite bundles. Don't add a dependency under a copyleft licence (GPL, AGPL, LGPL) without
asking.

## Environment gotchas

- `rm` is aliased to interactive mode in the shell: use `/bin/rm -f` or the command hangs.
- Commands that run longer than ~2 minutes get moved to the background; wrap browser tests in `timeout`.
- Commit messages and PR text must never mention Claude, Anthropic or AI (see the user's global instructions).
