# 1. Vue admin UI (Vite build)
FROM node:lts-alpine AS ui
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ .
RUN npm run build

# 2. Go binary (UI embedded via go:embed)
FROM golang:1.26.10-alpine AS app
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=ui /web/dist ./web/dist
# the publish workflow passes the short commit hash; a local build shows "dev"
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X iptv-proxy/internal/version.Version=${VERSION}" -o /iptv-proxy ./cmd/server

# 3. Runtime (Node is needed only for the iptv-org/epg grabber)
FROM node:lts-alpine
RUN apk add --no-cache git ca-certificates tzdata
# Pinned grabber ref; bump EPG_REF (or rebuild weekly in CI) to update.
ARG EPG_REF=master
RUN git clone --depth 1 --branch ${EPG_REF} https://github.com/iptv-org/epg /opt/epg \
 && cd /opt/epg && HUSKY=0 npm ci --omit=dev && npm cache clean --force \
 && rm -rf .git && chown -R node:node /opt/epg
COPY --from=app /iptv-proxy /usr/local/bin/iptv-proxy
COPY LICENSE THIRD_PARTY_NOTICES /licenses/
LABEL org.opencontainers.image.licenses="Unlicense" org.opencontainers.image.source="https://github.com/railadeividas/iptv-proxy"
RUN mkdir /data && chown node:node /data
USER node
VOLUME /data
EXPOSE 8810
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s CMD wget -qO- http://127.0.0.1:8810/healthz || exit 1
ENTRYPOINT ["iptv-proxy"]
