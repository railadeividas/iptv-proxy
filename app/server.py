"""HTTP routing for the proxy endpoints, the management API and the web UI."""

import hashlib
import hmac
import json
import logging
import re
import secrets
import threading
import time
import zlib
from http.cookies import CookieError, SimpleCookie
from http.server import BaseHTTPRequestHandler
from pathlib import Path
from urllib.error import HTTPError, URLError
from urllib.parse import parse_qs, urlsplit
from urllib.request import Request

from .m3u import LIMIT, http_url
from .version import VERSION

LOG = logging.getLogger("iptv")
STATIC = Path(__file__).parent / "static"
STATIC_TYPES = {".html": "text/html; charset=utf-8", ".js": "text/javascript; charset=utf-8",
                ".css": "text/css; charset=utf-8"}
CHANNEL_PATH = re.compile(r"/channel/([0-9a-f]{24})(?:\.[a-z0-9]+)?")
STREAM_PATH = re.compile(r"/stream(?:\.[a-z0-9]{1,12})?")
STATIC_NAME = re.compile(r"[\w.-]+")
HLS_TYPE = "application/vnd.apple.mpegurl"
JSON_TYPE = "application/json"
SESSION_COOKIE = "iptv_session"
SESSION_SECONDS = 30 * 24 * 3600
LOGIN_ATTEMPTS = 8  # wrong passwords allowed per client address and minute
MAX_SESSIONS = 1000

# Routes that need no authentication: health checks and the player-facing endpoints.
PUBLIC_ROUTES = {"/healthz": "serve_health", "/playlist.m3u": "serve_playlist"}
# The management API needs a signed-in session when UI_PASSWORD is set.
MANAGED_ROUTES = {"/api/sources": "api_sources", "/api/content": "api_content", "/api/custom": "api_custom",
                  "/api/sources/raw": "api_raw"}


def make_handler(proxy, manager, password=None, version=VERSION):
    failed_logins, failed_lock = {}, threading.Lock()
    # Random session ids live only in memory (as hashes): a restart or a sign-out ends them.
    sessions, sessions_lock = {}, threading.Lock()

    class Handler(BaseHTTPRequestHandler):
        protocol_version = "HTTP/1.1"
        timeout = 60
        upstream = ("-", "-")

        def handle(self):
            # Clients routinely drop keep-alive connections; that is not an error.
            try:
                super().handle()
            except (ConnectionResetError, BrokenPipeError, TimeoutError):
                self.close_connection = True

        # ---- helpers -------------------------------------------------------------
        def public_base(self):
            host = self.headers.get("Host", "")
            parts = urlsplit("http://" + host)
            if (not host or len(host) > 255 or parts.netloc != host or parts.path
                    or parts.query or parts.fragment or parts.username is not None
                    or any(c.isspace() for c in host)):
                raise ValueError("Invalid Host header")
            # Use the visited address; untrusted forwarding headers do not override the host.
            return http_url(self.scheme() + "://" + host)

        def scheme(self):
            """https when a TLS-terminating reverse proxy says so, otherwise http.

            Only the two exact values are honored, and a forged header only affects the
            response sent to the forger (a Secure cookie is simply ignored over plain HTTP)."""
            forwarded = self.headers.get("X-Forwarded-Proto", "").split(",")[0].strip().lower()
            return "https" if forwarded == "https" else "http"

        def cookie_flags(self):
            # Secure only when the browser reached us over HTTPS; plain HTTP must not get it,
            # or the browser would drop the cookie and sign-in would loop.
            return "; Secure" if self.scheme() == "https" else ""

        # Deliberately omit full request URLs (provider URLs may contain credentials).
        def log_message(self, fmt, *args):
            pass

        def reply(self, status, data, content_type="text/plain; charset=utf-8", headers=None):
            data = data.encode() if isinstance(data, str) else data
            self.send_response(status)
            self.send_header("Content-Type", content_type)
            self.send_header("Content-Length", str(len(data)))
            for name, value in {"Cache-Control": "no-store", **(headers or {})}.items():
                self.send_header(name, value)
            self.end_headers()
            if self.command != "HEAD" and status != 304:
                self.wfile.write(data)

        def reply_json(self, status, payload):
            self.reply(status, json.dumps(payload), JSON_TYPE)

        def fail(self, status, message, **kwargs):
            # The request body may be unread, so the connection cannot be reused.
            self.close_connection = True
            self.reply(status, message, **kwargs)

        # ---- sign-in (only when UI_PASSWORD is set) -------------------------------
        def session_key(self):
            """Hash of the session id in the request's cookie, or None."""
            try:
                value = SimpleCookie(self.headers.get("Cookie", ""))[SESSION_COOKIE].value
            except (KeyError, CookieError):
                return None
            return hashlib.sha256(value.encode()).hexdigest()

        def has_session(self):
            if not password:
                return True
            key = self.session_key()
            with sessions_lock:
                expires = sessions.get(key)
                if expires is not None and expires <= time.time():
                    del sessions[key]
                    expires = None
            return expires is not None

        def authorized(self):
            if self.has_session():
                return True
            self.fail(401, json.dumps({"error": "Sign in required."}), content_type=JSON_TYPE)
            return False

        def login(self):
            if not password:
                return self.reply_json(200, {"ok": True})
            client, now = self.client_address[0], time.monotonic()
            with failed_lock:
                recent = [t for t in failed_logins.get(client, []) if now - t < 60]
                failed_logins[client] = recent
                if len(recent) >= LOGIN_ATTEMPTS:
                    return self.fail(429, json.dumps({"error": "Too many attempts. Try again in a minute."}), content_type=JSON_TYPE)
            size = int(self.headers.get("Content-Length", "0"))
            if not 0 < size <= 4096:
                return self.fail(413, json.dumps({"error": "Enter the password."}), content_type=JSON_TYPE)
            payload = json.loads(self.rfile.read(size))
            supplied = payload.get("password") if isinstance(payload, dict) else None
            if not isinstance(supplied, str) or not hmac.compare_digest(supplied.encode(), password.encode()):
                with failed_lock:
                    failed_logins[client].append(now)
                time.sleep(0.4)  # slows down guessing without a lockout
                return self.fail(401, json.dumps({"error": "Wrong password."}), content_type=JSON_TYPE)
            with failed_lock:
                failed_logins.pop(client, None)
            session_id = secrets.token_urlsafe(32)
            with sessions_lock:
                for key in [k for k, expires in sessions.items() if expires <= time.time()]:
                    del sessions[key]
                while len(sessions) >= MAX_SESSIONS:
                    del sessions[min(sessions, key=sessions.get)]  # drop the oldest
                sessions[hashlib.sha256(session_id.encode()).hexdigest()] = time.time() + SESSION_SECONDS
            cookie = f"{SESSION_COOKIE}={session_id}; Path=/; Max-Age={SESSION_SECONDS}; HttpOnly; SameSite=Strict{self.cookie_flags()}"
            self.reply(200, json.dumps({"ok": True}), JSON_TYPE, headers={"Set-Cookie": cookie})

        def logout(self):
            with sessions_lock:
                sessions.pop(self.session_key(), None)
            cookie = f"{SESSION_COOKIE}=; Path=/; Max-Age=0; HttpOnly; SameSite=Strict{self.cookie_flags()}"
            self.reply(200, json.dumps({"ok": True}), JSON_TYPE, headers={"Set-Cookie": cookie})

        def status_payload(self, base_url):
            return {**manager.status(base_url), "version": version, "password_protected": bool(password)}

        # ---- GET / HEAD ----------------------------------------------------------
        def do_HEAD(self):
            self.do_GET()

        def do_GET(self):
            self.upstream = ("-", "-")
            try:
                self.base = self.public_base()
                route = urlsplit(self.path)
                if name := PUBLIC_ROUTES.get(route.path):
                    getattr(self, name)(route)
                elif name := MANAGED_ROUTES.get(route.path):
                    if self.authorized():
                        getattr(self, name)(route)
                elif match := CHANNEL_PATH.fullmatch(route.path):
                    channel = proxy.catalog.get()[1].get(match[1])
                    if channel is None:
                        return self.reply(404, "Channel is unavailable or excluded\n")
                    # The URL stays fixed; the provider URL and headers are resolved at play time.
                    self.stream(channel, channel.url)
                elif STREAM_PATH.fullmatch(route.path):
                    self.serve_signed_stream(route)
                elif route.path == "/" or STATIC_NAME.fullmatch(route.path[1:]):
                    self.serve_static(route.path)
                else:
                    self.reply(404, "Not found\n")
            except (BrokenPipeError, ConnectionResetError, TimeoutError):
                self.close_connection = True
            except HTTPError as error:
                LOG.warning("Upstream HTTP %s from %s (channel %s)", error.code, self.upstream[1], self.upstream[0])
                error.close()
                self.reply(error.code, f"Upstream returned HTTP {error.code}\n")
            except (ValueError, OSError, URLError, UnicodeError) as error:
                # Never log exception messages: they can contain upstream credentials.
                LOG.warning("Request failed: %s (upstream host %s, channel %s)",
                            type(error).__name__, self.upstream[1], self.upstream[0])
                self.reply(502, "Unable to load playlist or upstream stream\n")

        def serve_health(self, route):
            self.reply(200, "ok\n")

        def serve_playlist(self, route):
            if not manager.ready():
                return self.reply(503, "Playlists are still loading\n", headers={"Retry-After": "5"})
            # This is a channel directory, not an HLS media manifest.
            # VLC explicitly rejects the HLS MIME type in its M3U reader.
            self.reply(200, proxy.playlist(self.base), "audio/x-mpegurl; charset=utf-8")

        def serve_static(self, path):
            name = "index.html" if path == "/" else path[1:]
            if name == "index.html" and not self.has_session():
                name = "login.html"  # scripts and styles hold no secrets; only the UI page is gated
            file = STATIC / name
            if file.suffix not in STATIC_TYPES or not file.is_file():
                return self.reply(404, "Not found\n")
            data = file.read_bytes()
            etag = '"' + hashlib.sha1(data).hexdigest()[:20] + '"'
            headers = {"ETag": etag, "Cache-Control": "no-cache"}
            if self.headers.get("If-None-Match") == etag:
                return self.reply(304, b"", STATIC_TYPES[file.suffix], headers)
            self.reply(200, data, STATIC_TYPES[file.suffix], headers)

        def api_sources(self, route):
            data = self.status_payload(self.base)
            data["token"] = proxy.management_token
            self.reply_json(200, data)

        def api_content(self, route):
            try:
                self.reply_json(200, manager.content(parse_qs(route.query).get("source", [""])[0]))
            except ValueError as error:
                self.reply_json(404, {"error": str(error)})

        def api_raw(self, route):
            # Shown inline as plain text so the browser displays it instead of downloading it.
            try:
                text = manager.raw_playlist(parse_qs(route.query).get("source", [""])[0])
            except ValueError as error:
                return self.reply(404, str(error) + "\n")
            self.reply(200, text, "text/plain; charset=utf-8", headers={"X-Content-Type-Options": "nosniff"})

        def api_custom(self, route):
            try:
                self.reply_json(200, {"content": manager.custom_content(parse_qs(route.query).get("source", [""])[0])})
            except ValueError as error:
                self.reply_json(404, {"error": str(error)})

        def serve_signed_stream(self, route):
            query = parse_qs(route.query)
            channel_id = query.get("channel", [""])[0]
            url = query.get("url", [""])[0]
            sig = query.get("sig", [""])[0]
            if not re.fullmatch(r"[0-9a-f]{64}", sig) or not hmac.compare_digest(proxy.signature(channel_id, url), sig):
                return self.reply(403, "Invalid stream signature\n")
            channel = proxy.catalog.get()[1].get(channel_id)
            if channel is None:
                return self.reply(404, "Channel no longer in source playlist\n")
            self.stream(channel, http_url(url))

        # ---- upstream streaming ----------------------------------------------------
        def send_hls_head(self):
            # The rewritten length is unknown without fetching the whole manifest.
            self.close_connection = True
            self.send_response(200)
            self.send_header("Content-Type", HLS_TYPE)
            self.send_header("Cache-Control", "no-store")
            self.send_header("Connection", "close")
            self.end_headers()

        def send_manifest(self, data, channel, response):
            if len(data) > LIMIT:
                raise ValueError("HLS manifest exceeds size limit")
            text = proxy.rewrite(data.decode("utf-8-sig"), channel.id, response.geturl(), self.base)
            self.reply(200, text, HLS_TYPE)

        def stream(self, channel, url):
            headers = {"User-Agent": "IPTV-Header-Proxy/1.0", **channel.headers, "Accept-Encoding": "identity"}
            for name in ("Range", "If-Range"):
                if self.headers.get(name):
                    headers[name] = self.headers[name]
            self.upstream = (channel.id, urlsplit(url).hostname)
            head = self.command == "HEAD"
            # Many CDNs reject HEAD, so always GET upstream and discard the body for HEAD.
            with proxy.open(Request(url, headers=headers)) as response:
                content_type = response.headers.get("Content-Type", "application/octet-stream")
                encoding = (response.headers.get("Content-Encoding") or "").lower()
                manifest_type = "mpegurl" in content_type.lower() or urlsplit(url).path.lower().endswith((".m3u8", ".m3u"))
                if encoding in ("gzip", "x-gzip", "deflate") and manifest_type:
                    # Some providers compress manifests even though we ask for identity.
                    if head:
                        return self.send_hls_head()
                    try:
                        decoder = zlib.decompressobj(zlib.MAX_WBITS | 32)
                        data = decoder.decompress(response.read(LIMIT + 1), LIMIT + 1)
                    except zlib.error as error:
                        raise ValueError("Invalid compressed manifest") from error
                    return self.send_manifest(data, channel, response)
                first = response.read(10)
                is_hls = first.removeprefix(b"\xef\xbb\xbf").startswith(b"#EXTM3U") or "mpegurl" in content_type.lower()
                if is_hls and head:
                    return self.send_hls_head()
                if is_hls:
                    return self.send_manifest(first + response.read(LIMIT + 1), channel, response)
                self.send_response(response.status)
                self.send_header("Content-Type", content_type)
                self.send_header("Cache-Control", "no-store")
                for name in ("Content-Length", "Content-Range", "Accept-Ranges", "Content-Encoding"):
                    value = response.headers.get(name)
                    if value:
                        self.send_header(name, value)
                if not response.headers.get("Content-Length"):
                    # Live streams have no length; the end of the body is the end of the connection.
                    self.close_connection = True
                    self.send_header("Connection", "close")
                self.end_headers()
                if head:
                    return
                try:
                    self.wfile.write(first)
                    while chunk := response.read1(64 * 1024):
                        self.wfile.write(chunk)
                except (BrokenPipeError, ConnectionResetError, TimeoutError):
                    self.close_connection = True
                except OSError:
                    # Headers are already sent; terminate the response on upstream failure.
                    LOG.warning("Upstream stream interrupted (host %s, channel %s)", self.upstream[1], self.upstream[0])
                    self.close_connection = True

        # ---- POST ------------------------------------------------------------------
        def do_POST(self):
            try:
                path = urlsplit(self.path).path
                if path == "/api/login":
                    return self.login()
                if path == "/api/logout":
                    return self.logout()
                if not self.authorized():
                    return
                token = self.headers.get("X-Management-Token", "")
                if not re.fullmatch(r"[0-9a-f]{64}", token) or not hmac.compare_digest(token, proxy.management_token):
                    return self.fail(403, "Invalid management token\n")
                base_url = self.public_base()
                size = int(self.headers.get("Content-Length", "0"))
                if not 0 < size <= LIMIT * 2:
                    return self.fail(413, "Request too large or empty\n")
                payload = json.loads(self.rfile.read(size))
                if not isinstance(payload, dict):
                    raise ValueError("Expected an object")
                manager.action(path, payload)
                self.reply_json(200, self.status_payload(base_url))
            except (ValueError, KeyError) as error:
                self.fail(400, json.dumps({"error": str(error)}), content_type=JSON_TYPE)
            except (BrokenPipeError, ConnectionResetError, TimeoutError):
                self.close_connection = True
            except OSError:
                self.fail(500, '{"error":"Unable to save playlist settings"}', content_type=JSON_TYPE)

    return Handler
