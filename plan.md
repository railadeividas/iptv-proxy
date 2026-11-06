i want to build iptv proxy that is will have tight integration with https://github.com/iptv-org/epg
https://github.com/iptv-org/iptv
https://github.com/iptv-org/api

i want to have web ui where i can select channels and have link to generated playlist and epg file, proxy is needed to handle all headers and compatibility for all players

can you make plan of iptv proxy that would be easy to maintain and anyone would have to run just one docker container to have it own iptv

Here's a plan built around one Go binary with an embedded web UI, shipped as a single Docker image.

**Architecture**
- **Backend:** Go (static binary, good at streaming and concurrency) with embedded SQLite. No external DB or Redis.
- **Web UI:** small Svelte/Preact SPA embedded in the binary, so there's nothing else to deploy.
- **Container:** one image with one volume (`/data`) and one port. Config comes from env vars (`BASE_URL`, `ADMIN_PASSWORD`, `SYNC_INTERVAL`).

**Data sync (iptv-org/api)**
- A scheduler pulls `channels`, `streams`, `feeds`, `guides`, `categories`, `countries` and `languages` JSON from the API into SQLite (ETag/If-Modified-Since, daily). Check the current schema first, since it has changed over time.
- `streams` carries the per-stream `http_referrer`, `user_agent` and quality, which is exactly the header data the proxy needs.
- Optionally use the `iptv` repo's M3U files as a fallback source.

**EPG (iptv-org/epg)**
- The repo is a grabber, not a hosted XML file. `guides` maps channel → site + site_id.
- When you save a selection, the app generates a `channels.xml` for the chosen channels and runs the grabber as a subprocess on a schedule. Node goes in the image for this.
- The result is merged into one cached XMLTV file, served as `.xml` and `.xml.gz`. Use the same channel/feed IDs in `tvg-id` so players match guide data automatically.
- This is the heaviest part: grabbing is slow and flaky per site. Run it in the background, keep the last good copy, and only grab selected channels.

**Web UI**
- Search and filter by country, language and category, with a "working only" toggle from health checks.
- Select channels into named profiles (e.g. "Living room"). Reorder and rename them, and pick a quality or backup stream.
- Each profile gets a secret-token URL: `/p/{token}/playlist.m3u` and `/p/{token}/epg.xml`. A copy button and QR code help with TVs.

**Proxy layer**
- `/p/{token}/s/{channelId}` resolves the stream, applies its stored headers, and follows redirects.
- HLS: rewrite `.m3u8` manifests (master and media) so segments, keys and sub-playlists go back through the proxy. Handle relative URLs, byte ranges and encryption keys.
- Raw TS and DASH: stream through with correct `Content-Type`, range support and CORS headers.
- Per-channel mode: **proxy** (headers fixed) or **redirect** (direct, to save bandwidth when headers aren't needed).
- Fallback: if the primary stream fails, try the next stream for the same channel.
- Include short segment caching, connection limits and timeouts.
- Add an optional "compat profile" per client (user-agent spoofing, MIME fixes) for fussy players.

**Maintenance**
- Health checker: periodically probe streams and store status, so dead links are hidden.
- Almost no code is tied to the data format, since everything comes from the API.
- A `/healthz` endpoint, structured logs and a GitHub Actions build for multi-arch (amd64/arm64) images.

**Phases**
1. API sync, channel browser, M3U generation (redirect mode only).
2. Profiles, tokens and the HLS rewriting proxy with headers.
3. EPG integration (grabber subprocess, merge, serve).
4. Health checks, fallbacks, compat profiles and a QR/share UI.

**Risks to plan for**
- Many public streams are dead or geo-blocked, so health checks matter more than any feature.
- Proxying everything uses your upstream bandwidth. Keep redirect mode as the default where possible.
- Running it publicly means tokens and rate limiting are essential. Consider keeping it LAN or VPN only.

I can draft the DB schema and API endpoints, or the Dockerfile and project layout, next if you'd like.

I've laid out the plan above. Tell me which part to expand first: the DB schema and API endpoints, or the Dockerfile and project layout.

how build would look?

The build is a three-stage Docker build that ends in one image.

**Project layout**
```
cmd/server/        main.go
internal/
  sync/            iptv-org API fetcher
  store/           SQLite + migrations
  proxy/           HLS rewrite, header injection
  epg/             grabber runner, XMLTV merge
  playlist/        M3U generator
  health/          stream prober
  api/             REST for the UI
web/               Svelte/Preact UI (Vite)
Dockerfile
.github/workflows/release.yml
```

**Dockerfile sketch**
```dockerfile
# 1. UI
FROM node:22-alpine AS ui
WORKDIR /web
COPY web/ .
RUN npm ci && npm run build

# 2. Go binary (UI embedded via go:embed)
FROM golang:1.23-alpine AS app
WORKDIR /src
COPY . .
COPY --from=ui /web/dist ./web/dist
RUN CGO_ENABLED=0 go build -o /iptv-proxy ./cmd/server

# 3. Runtime (Node is needed only for the EPG grabber)
FROM node:22-alpine
RUN apk add --no-cache git ca-certificates tzdata
ARG EPG_REF=master
RUN git clone --depth 1 --branch $EPG_REF \
      https://github.com/iptv-org/epg /opt/epg \
 && cd /opt/epg && npm ci --omit=dev
COPY --from=app /iptv-proxy /usr/local/bin/iptv-proxy
VOLUME /data
EXPOSE 8080
ENTRYPOINT ["iptv-proxy"]
```

Use a pure-Go SQLite driver (`modernc.org/sqlite`) so `CGO_ENABLED=0` works. That keeps the build static and cross-compiles easily.

**How the pieces fit**
- **UI** builds to static files and is embedded in the Go binary, so there's no separate web server.
- **EPG grabber** is baked into the image at a pinned ref and called as `node /opt/epg/...` from Go. Bump `EPG_REF` to update it. Optionally allow a runtime `git pull` via an env flag, but pinning is more reproducible.
- **iptv-org API data** is not baked in. It's fetched at runtime into `/data`, so the image stays valid as channels change.

**CI (GitHub Actions)**
- On a tag, run `docker buildx` for `linux/amd64` and `linux/arm64` and push to GHCR.
- A weekly scheduled rebuild picks up grabber updates.
- Run `go test` and a UI build check on pull requests.

**User side**
```
docker run -d -p 8080:8080 -v iptv:/data \
  -e BASE_URL=http://192.168.1.10:8080 \
  -e ADMIN_PASSWORD=changeme \
  ghcr.io/you/iptv-proxy
```
The image will be about 200-250 MB because of Node. If you later drop the Node grabber, it could shrink to under 30 MB (Go only).

Want me to scaffold the repo with these files, or go deeper on the HLS rewriting code first?

so we use iptv-org epg software and iptv-org api?

Yes, both, and they do different jobs:

- **iptv-org/api** is the data source. We fetch its JSON (channels, streams, feeds, guides, categories, countries) at runtime. This powers the channel browser, stream URLs, headers, and the M3U generation. It replaces needing the `iptv` repo's playlists, since the API already has the same stream data in structured form.
- **iptv-org/epg** is software we run. It scrapes TV guide sites and outputs XMLTV. We use it only for the guide data, and feed it a `channels.xml` built from the user's selection plus the `guides` mapping from the API.

The **iptv-org/iptv** repo is optional. You'd only use it as a fallback or cross-check, not as a main dependency.

The only real runtime dependency on their code is the EPG grabber, which is why Node ends up in the image. The API is just HTTP/JSON, so there's nothing to bundle.

If you'd rather avoid Node, one alternative is to skip the grabber and pull a pre-generated XMLTV from a public mirror. The trade-off is that you depend on someone else's hosting and can't limit it to your selected channels. I'd start with the bundled grabber.

now check this project https://github.com/railadeividas/iptv-proxy

does this project has any good features that we should include in our app?

It's a small, young repo (4 commits, 0 stars), and it's a generic M3U header proxy with no iptv-org integration and no EPG. But its proxy design is solid, and several ideas are worth copying.

**Worth including**

- **Signed stream links:** every proxied URL (HLS segments, DASH segments, keys) is HMAC-signed with a key stored in `/data`, so nobody can use your proxy to fetch arbitrary URLs. Signing DASH segments per directory so `$Number$` templates still work is a nice detail. This replaces my simpler token idea for the stream layer.
- **Stable channel URLs:** `/channel/<id>.m3u8`, where the ID doesn't depend on provider tokens. The real URL and headers are resolved at play time, so a stream URL change never breaks the player's channel list. For us the ID would simply be the iptv-org channel/feed ID.
- **DASH support:** manifest rewriting for `BaseURL`, `SegmentTemplate` and `SegmentList`, with range requests. I only listed it loosely before, so it's worth adding as a defined feature.
- **Safe redirect handling:** cookies and credentials are dropped when a redirect goes to another host. This is an easy security detail to forget.
- **Playlist hygiene:** `http-*` attributes are stripped from the exported M3U so credentials never reach players. `HEAD` requests are answered via an upstream `GET`, compressed manifests are decoded, and there's a 16 MiB limit on manifests.
- **Resilient refresh:** a failing source keeps its last good list (also across restarts) and never blocks others. `/playlist.m3u` returns 503 until the first load completes, so players don't import an empty list. We should do the same for the API sync and EPG grabber.
- **Operational touches:** the "address for your player" field for copied URLs, generated links following the host the client connected to, `X-Forwarded-Proto` handling, a `UI_PASSWORD` sign-in with login rate limiting, and a `/healthz` endpoint. Player endpoints stay open while the manager is protected.
- **Custom sources:** adding a playlist by URL, file upload or hand-built M3U. For us this means users could add their own streams alongside the iptv-org ones.
- **Skipped entries listed with reasons:** bad entries don't reject a whole source, and the UI shows why they were skipped.

**Skip or treat differently**
- **File-based JSON state** (`sources.json`, `content.json`): we need SQLite for searching thousands of channels.
- **Group rules and per-channel overrides:** our profiles and selection UI already cover this.
- **"Don't expose to the internet":** we can do better with per-profile tokens, but the same caution applies to us.

Since it's MIT-licensed, you can borrow the approach freely. I'd still write our own proxy layer, because the iptv-org headers, fallbacks and health checks need to be built in from the start. Want me to update the plan with these additions?

update

Here's the updated plan.

**Stack**
- Go binary with embedded SQLite (pure-Go driver) and an embedded Svelte/Preact UI.
- One image, one `/data` volume, one port. Node is included only for the EPG grabber.
- Env vars: `BASE_URL` (optional), `ADMIN_PASSWORD`, `SYNC_INTERVAL`.

**Data (iptv-org/api)**
- Scheduled sync of channels, streams, feeds, guides, categories and countries into SQLite.
- Keep the last good data if a sync fails (also across restarts). Serve 503 on playlist/EPG endpoints until the first load completes.
- Custom sources: users can add their own streams by URL, file upload or hand-built M3U, alongside the iptv-org ones.
- Bad entries are skipped and listed in the UI with the reason, never failing the whole import.

**EPG (iptv-org/epg)**
- Build `channels.xml` from the selected channels via the `guides` mapping, run the grabber on a schedule, and merge the output into one cached XMLTV (`.xml` and `.xml.gz`).
- Keep the last good file if a grab fails. `tvg-id` matches the XMLTV IDs.

**Profiles and URLs**
- Each profile has a secret token: `/p/{token}/playlist.m3u` and `/p/{token}/epg.xml`.
- Stable channel URLs: `/p/{token}/channel/{channelId}.m3u8`, where the ID is the iptv-org channel/feed ID. Stream URL and headers are resolved at play time, so upstream changes never break player channel lists.
- Generated links follow the host the client connected to. An "address for your player" field overrides it, and `X-Forwarded-Proto` is honored.

**Proxy layer**
- **HLS:** rewrite master and media manifests, including sub-playlists, alternate audio/subtitles, init segments, segments and keys.
- **DASH:** rewrite `BaseURL`, `SegmentTemplate` and `SegmentList`, with range support.
- **Signing:** every rewritten URL is HMAC-signed with a key stored in `/data`, so the proxy can't fetch arbitrary URLs. DASH segments are signed per directory so templates still work.
- **Headers:** apply the stored Referer, User-Agent and Origin from the API per channel. Credentials never appear in the exported playlist.
- **Redirects:** drop cookies and credentials when a redirect goes to another host.
- **Compatibility:** forward range requests, answer `HEAD` via upstream `GET`, decode compressed manifests, cap manifests at 16 MiB, and use a 30 s upstream timeout. Optional per-client compat profiles handle fussy players.
- **Modes:** proxy or direct redirect per channel.
- **Fallback:** try the next stream for the same channel on failure.
- No transcoding or recording.

**Web UI**
- Filter by country, language and category, with a "working only" toggle.
- Select channels into named profiles, reorder, rename, and pick a preferred or backup stream.
- Copy buttons and a QR code for playlist and EPG URLs.
- A skipped-entries view with reasons.
- Admin sign-in with rate limiting (8 failed attempts per minute per address). Player endpoints stay token-only.

**Operations**
- Health checker probes streams in the background and stores status.
- `/healthz`, structured logs, and a reverse-proxy example (nginx) in the README.
- GitHub Actions builds multi-arch (amd64/arm64) images to GHCR, with a weekly rebuild to pick up grabber updates.

**Phases**
1. API sync, channel browser, profiles, M3U generation (redirect mode).
2. HLS and DASH proxy with signing, headers and stable channel URLs.
3. EPG grabber integration.
4. Health checks, fallbacks, custom sources and compat profiles.
5. Polish: QR/share, rate limiting and docs.

Want me to start with the DB schema and API endpoints, or scaffold the repo?
