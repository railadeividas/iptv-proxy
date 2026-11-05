"""Persistent playlist sources, with background refresh and cached channel lists."""

import hashlib
import json
import logging
import os
import re
import secrets
import threading
import time
from concurrent.futures import ThreadPoolExecutor
from dataclasses import replace
from pathlib import Path
from urllib.error import HTTPError
from urllib.request import Request, urlopen

from .m3u import LIMIT, http_url, parse_playlist, read_limited

LOG = logging.getLogger("iptv")
SOURCE_FIELDS = ("id", "name", "kind", "location", "enabled")
DEFAULT_RULES = {"default": True, "groups": {}, "channels": {}}


def channel_details(channel):
    line = channel.metadata[0]
    attributes = dict(re.findall(r'([\w-]+)="([^"]*)"', line))
    # Remove quoted attributes before finding the name (attributes can contain commas).
    plain = re.sub(r'"[^"]*"', '""', line)
    name = plain.partition(",")[2].strip() or attributes.get("tvg-name") or "Unnamed channel"
    group = attributes.get("group-title") or next((s[8:].strip() for s in channel.metadata if s.startswith("#EXTGRP:")), "") or "Ungrouped"
    # Exclude the stream URL so rotating provider tokens do not reset selections.
    key = hashlib.sha256(json.dumps([attributes.get("tvg-id", ""), name, group], ensure_ascii=False).encode()).hexdigest()[:24]
    return {"id": key, "name": name, "group": group, "tvg_id": attributes.get("tvg-id", "")}


def is_included(details, rules):
    return rules.get("channels", {}).get(details["id"], rules.get("groups", {}).get(details["group"], rules.get("default", True)))


def write_private(path, text):
    """Atomically write a file that only the proxy's user can read."""
    path = Path(path)
    temporary = path.with_name(path.name + ".tmp")
    descriptor = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(descriptor, "w", encoding="utf-8") as stream:
        stream.write(text)
    temporary.replace(path)


def read_json(path, kind):
    try:
        value = json.loads(Path(path).read_text(encoding="utf-8"))
    except (OSError, ValueError) as error:
        raise RuntimeError(f"Cannot read {path}: {error}. Fix or remove the file and restart.") from error
    if not isinstance(value, kind):
        raise RuntimeError(f"Cannot read {path}: expected a JSON {kind.__name__}. Fix or remove the file and restart.")
    return value


class SourceManager:
    def __init__(self, data_dir, initial_source=None, refresh=86400):
        self.directory = Path(data_dir)
        self.directory.mkdir(parents=True, exist_ok=True)
        self.file = self.directory / "sources.json"
        self.filters_file = self.directory / "content.json"
        self.filters = read_json(self.filters_file, dict) if self.filters_file.exists() else {}
        self.lock = threading.RLock()
        self.source_locks = {}
        self.refresh_seconds = max(10, refresh)
        self.cache = {}
        self.details = {}
        self.reports = {}
        self.skipped = {}
        self.attempted = {}
        self.snapshot = None
        self.loaded = True
        if self.file.exists():
            self.sources = read_json(self.file, list)
            for source in self.sources:
                if not isinstance(source, dict) or any(key not in source for key in SOURCE_FIELDS):
                    raise RuntimeError(f"Cannot read {self.file}: a playlist entry is incomplete.")
        else:
            self.sources = []
            remote = bool(initial_source) and initial_source.startswith(("http://", "https://"))
            if initial_source and not remote and not Path(initial_source).is_file():
                LOG.warning("PLAYLIST_SOURCE %s does not exist; add playlists in the web UI instead", initial_source)
                initial_source = None
            if initial_source:
                self.sources.append({"id": "default", "name": "My playlist", "kind": "url" if remote else "mounted",
                                     "location": initial_source, "enabled": True})
            self.save()
        self.warm_start()

    # ---- persistence -------------------------------------------------------------
    def save(self):
        write_private(self.file, json.dumps(self.sources, indent=2))

    def cache_path(self, source_id):
        return self.directory / f"cache-{source_id}.m3u"

    def warm_start(self):
        """Load the last known channels so playlists are available before the first refresh."""
        for source in self.sources:
            if not source["enabled"]:
                continue
            path = self.cache_path(source["id"]) if source["kind"] == "url" else Path(source["location"])
            try:
                with path.open("rb") as stream:
                    text = read_limited(stream)
                self.ingest(source, text, updated=path.stat().st_mtime)
            except Exception:
                LOG.info("No usable saved copy of source %s; it loads on the first refresh", source["id"])

    # ---- queries -------------------------------------------------------------------
    def build_snapshot(self):
        """Combine enabled sources once; every stream request reuses the result."""
        with self.lock:
            if self.snapshot is None:
                channels, guide_urls, included = {}, [], {}
                for source in self.sources:
                    cached = self.cache.get(source["id"])
                    if cached is None:
                        continue
                    preamble, entries = cached
                    rules = self.filters.get(source["id"], {})
                    details = self.details[source["id"]]
                    selected = {key: c for key, c in entries.items() if is_included(details[key], rules)}
                    included[source["id"]] = len(selected)
                    if not source["enabled"]:
                        continue
                    channels.update(selected)
                    match = re.search(r'(?:x-tvg-url|url-tvg)="([^"]+)"', preamble)
                    if match:
                        guide_urls.extend(url.strip() for url in match[1].split(",") if url.strip())
                preamble = "#EXTM3U"
                if guide_urls:
                    preamble += ' x-tvg-url="' + ",".join(dict.fromkeys(guide_urls)) + '"'
                self.snapshot = preamble, channels, included
            return self.snapshot

    def get(self):
        # Stream requests only read the snapshot; fetching a source never blocks playback.
        return self.build_snapshot()[:2]

    def ready(self):
        return self.loaded

    def content(self, source_id):
        # Disabled sources may have no cache after a restart. Load their metadata
        # on demand without enabling them or holding the state lock during I/O.
        with self.lock:
            if not any(s["id"] == source_id for s in self.sources):
                raise ValueError("Playlist not found.")
            needs_load = source_id not in self.cache
        if needs_load:
            self.refresh(source_id)
        with self.lock:
            source = next((s for s in self.sources if s["id"] == source_id), None)
            if source is None:
                raise ValueError("Playlist not found.")
            entries = self.cache.get(source_id, ("", {}))[1]
            rules = self.filters.get(source_id, dict(DEFAULT_RULES))
            groups = {}
            for key in entries:
                details = dict(self.details[source_id][key])
                details["included"] = is_included(details, rules)
                groups.setdefault(details["group"], {})[details["id"]] = details
            return {"source_id": source_id, "name": source["name"], "enabled": source["enabled"],
                    "error": self.reports.get(source_id, {}).get("error"),
                    "skipped": self.skipped.get(source_id, []), "skipped_total": self.reports.get(source_id, {}).get("skipped", 0),
                    "rules": rules, "groups": [
                        {"name": name, "channels": sorted(items.values(), key=lambda c: c["name"].casefold())}
                        for name, items in sorted(groups.items(), key=lambda pair: pair[0].casefold())]}

    def raw_playlist(self, source_id):
        """The playlist text as stored (a remote source's last good copy), for viewing in the manager."""
        with self.lock:
            source = next((s for s in self.sources if s["id"] == source_id), None)
        if source is None:
            raise ValueError("Playlist not found.")
        path = self.cache_path(source_id) if source["kind"] == "url" else Path(source["location"])
        try:
            with path.open("rb") as stream:
                return read_limited(stream)
        except OSError:
            raise ValueError("This playlist has no saved copy yet. Refresh it first.") from None

    def custom_content(self, source_id):
        with self.lock:
            source = next((s for s in self.sources if s["id"] == source_id and s["kind"] == "custom"), None)
        if source is None:
            raise ValueError("Playlist not found.")
        try:
            return Path(source["location"]).read_text(encoding="utf-8")
        except OSError:
            return ""

    def status(self, base_url):
        with self.lock:
            _, channels, included = self.build_snapshot()
            sources = [{**source, "refresh_seconds": source.get("refresh_seconds", self.refresh_seconds),
                        "channels": 0, "skipped": 0, "updated": None, "error": None,
                        **self.reports.get(source["id"], {}), "included": included.get(source["id"], 0)}
                       for source in self.sources]
            return {"sources": sources, "channels": len(channels),
                    "playlist_url": base_url + "/playlist.m3u", "refresh_seconds": self.refresh_seconds}

    # ---- content rules ---------------------------------------------------------------
    def save_content(self, payload):
        source_id, rules = payload.get("id"), payload.get("rules")
        if not isinstance(rules, dict) or type(rules.get("default")) is not bool:
            raise ValueError("Choose the default channel selection.")
        for key in ("groups", "channels"):
            if not isinstance(rules.get(key), dict) or any(not isinstance(k, str) or len(k) > 1000 or type(v) is not bool for k, v in rules[key].items()):
                raise ValueError("Invalid content selection.")
        with self.lock:
            if not any(s["id"] == source_id for s in self.sources):
                raise ValueError("Playlist not found.")
            self.set_rules(source_id, {key: rules[key] for key in ("default", "groups", "channels")})

    def set_rules(self, source_id, rules):
        with self.lock:
            filters = {**self.filters, source_id: rules}
            write_private(self.filters_file, json.dumps(filters))
            self.filters = filters
            self.snapshot = None

    # ---- refreshing ----------------------------------------------------------------
    def ingest(self, source, text, updated=None):
        """Parse a playlist and publish it as the source's current channel list."""
        source_id, stats = source["id"], {}
        if source["kind"] == "custom" and text.strip() == "#EXTM3U":
            preamble, entries = "#EXTM3U", {}
        else:
            preamble, entries = parse_playlist(text, stats)
        skipped_entries = stats.pop("skipped_entries", [])
        # A tuner must keep the same URL when a provider rotates its tokens.
        stable_entries, stable_details, occurrences = {}, {}, {}
        for channel in entries.values():
            info = channel_details(channel)
            occurrence = occurrences.get(info["id"], 0)
            occurrences[info["id"]] = occurrence + 1
            stable_id = hashlib.sha256(f"{source_id}\n{info['id']}\n{occurrence}".encode()).hexdigest()[:24]
            stable_entries[stable_id] = replace(channel, id=stable_id)
            stable_details[stable_id] = info
        with self.lock:
            self.cache[source_id] = preamble, stable_entries
            self.details[source_id] = stable_details
            self.skipped[source_id] = skipped_entries
            self.reports[source_id] = {**stats, "updated": updated or time.time(), "error": None}
            self.snapshot = None

    def source_lock(self, source_id):
        with self.lock:
            return self.source_locks.setdefault(source_id, threading.Lock())

    def refresh_one(self, source_id):
        # One lock per source: a slow provider never blocks other sources or the UI.
        with self.source_lock(source_id):
            with self.lock:
                source = next((dict(s) for s in self.sources if s["id"] == source_id), None)
                if source is None:
                    return
                self.attempted[source_id] = time.monotonic()
            try:
                if source["kind"] == "url":
                    request = Request(source["location"], headers={"Accept-Encoding": "identity", "User-Agent": "IPTV-Header-Proxy/1.0"})
                    with urlopen(request, timeout=30) as response:
                        text = read_limited(response)
                else:
                    with open(source["location"], "rb") as stream:
                        text = read_limited(stream)
                self.ingest(source, text)
                if source["kind"] == "url":
                    self.save_cache_copy(source_id, text)
            except Exception as error:
                # Do not expose provider URLs, credentials, or local exception details.
                message = f"Provider returned HTTP {error.code}" if isinstance(error, HTTPError) else "Could not load a valid M3U playlist. Check the source and try again."
                LOG.warning("Playlist refresh failed for source %s (%s)", source_id, type(error).__name__)
                with self.lock:
                    self.reports[source_id] = {**self.reports.get(source_id, {}), "error": message}

    def save_cache_copy(self, source_id, text):
        try:
            write_private(self.cache_path(source_id), text)
        except OSError:
            LOG.warning("Could not save a copy of source %s", source_id)

    def refresh(self, source_id=None, due_only=False):
        with self.lock:
            now = time.monotonic()

            def due(source):
                interval = source.get("refresh_seconds", self.refresh_seconds)
                return source["id"] not in self.attempted or (interval > 0 and now - self.attempted[source["id"]] >= interval)
            selected = [s["id"] for s in self.sources
                        if (s["enabled"] or (source_id is not None and not due_only))
                        and (source_id is None or s["id"] == source_id)
                        and (not due_only or due(s))]
        if len(selected) > 1:
            with ThreadPoolExecutor(max_workers=min(4, len(selected))) as pool:
                list(pool.map(self.refresh_one, selected))
        else:
            for selected_id in selected:
                self.refresh_one(selected_id)

    def start(self):
        with self.lock:
            self.loaded = all(s["id"] in self.cache for s in self.sources if s["enabled"])

        def worker():
            while True:
                try:
                    self.refresh(due_only=True)
                except Exception as error:
                    LOG.warning("Background refresh failed (%s)", type(error).__name__)
                self.loaded = True
                time.sleep(1)
        threading.Thread(target=worker, name="playlist-refresh", daemon=True).start()

    # ---- actions ---------------------------------------------------------------------
    def action(self, route, payload):
        handlers = {
            "/api/content/save": self.save_content, "/api/sources/add": self.add_source,
            "/api/sources/edit": self.edit_source, "/api/sources/refresh": self.refresh_source,
            "/api/sources/toggle": self.toggle_source, "/api/sources/delete": self.delete_source,
        }
        if route not in handlers:
            raise ValueError("Unknown action.")
        handlers[route](payload)

    def find(self, source_id):
        source = next((s for s in self.sources if s["id"] == source_id), None)
        if source is None:
            raise ValueError("Playlist not found.")
        return source

    def parse_name(self, payload):
        name = payload.get("name", "")
        if not isinstance(name, str) or not name.strip() or len(name.strip()) > 100:
            raise ValueError("Enter a playlist name (up to 100 characters).")
        return name.strip()

    def parse_interval(self, payload, default):
        interval = payload.get("refresh_seconds", default)
        if type(interval) not in (int, float) or not (interval == 0 or 10 <= interval <= 604800) or interval != int(interval):
            raise ValueError("Refresh interval must be 0 (manual only) or 10–604800 seconds.")
        return int(interval)

    def resolve_location(self, payload, existing, source_id):
        """Return (kind, location) for the submitted source, writing uploaded content to disk."""
        kind = payload.get("kind", existing["kind"] if existing else "url")
        if kind == "url":
            location = payload.get("url", existing["location"] if existing and existing["kind"] == "url" else "")
            if not isinstance(location, str):
                raise ValueError("Enter an HTTP or HTTPS playlist URL.")
            return kind, http_url(location.strip())
        if (existing and existing["kind"] != "url" and "content" not in payload
                and kind in (existing["kind"], "upload")):
            return existing["kind"], existing["location"]
        if kind in ("upload", "custom"):
            content = payload.get("content", "")
            if not isinstance(content, str) or len(content.encode()) > LIMIT:
                raise ValueError("Playlist must be smaller than 16 MiB.")
            if not (kind == "custom" and content.strip() == "#EXTM3U"):
                parse_playlist(content)  # Validate before saving.
            location = str(self.directory / f"playlist-{source_id}.m3u")
            write_private(location, content)
            return kind, location
        raise ValueError("Choose a URL or uploaded file.")

    def add_source(self, payload):
        self.save_source(payload, editing=False)

    def edit_source(self, payload):
        self.save_source(payload, editing=True)

    def save_source(self, payload, editing):
        with self.lock:
            existing = dict(self.find(payload.get("id"))) if editing else None
        name = self.parse_name(payload)
        source_id = existing["id"] if existing else secrets.token_hex(8)
        interval = self.parse_interval(payload, existing.get("refresh_seconds", self.refresh_seconds) if existing else self.refresh_seconds)
        kind, location = self.resolve_location(payload, existing, source_id)
        with self.lock:
            updated = {"id": source_id, "name": name, "kind": kind, "location": location,
                       "enabled": existing["enabled"] if existing else True, "refresh_seconds": interval}
            if editing:
                position = next((i for i, source in enumerate(self.sources) if source["id"] == source_id), None)
                if position is None:
                    raise ValueError("Playlist no longer exists.")
                self.sources[position] = updated
            else:
                self.sources.append(updated)
                # New sources export nothing until the user selects content (custom lists start included).
                self.set_rules(source_id, {"default": kind == "custom", "groups": {}, "channels": {}})
            self.save()
            self.snapshot = None
        self.refresh(source_id)

    def refresh_source(self, payload):
        source_id = payload.get("id")
        if source_id is not None:
            with self.lock:
                self.find(source_id)
        self.refresh(source_id)

    def toggle_source(self, payload):
        with self.lock:
            source = self.find(payload.get("id"))
            source["enabled"] = not source["enabled"]
            enabled = source["enabled"]
            self.snapshot = None
            self.save()
        if enabled:
            self.refresh(source["id"])

    def delete_source(self, payload):
        with self.lock:
            source = self.find(payload.get("id"))
            source_id = source["id"]
            self.sources.remove(source)
            for state in (self.cache, self.details, self.reports, self.skipped, self.attempted, self.source_locks):
                state.pop(source_id, None)
            self.snapshot = None
            if source_id in self.filters:
                remaining = {key: value for key, value in self.filters.items() if key != source_id}
                write_private(self.filters_file, json.dumps(remaining))
                self.filters = remaining
            if source["kind"] in ("upload", "custom"):
                Path(source["location"]).unlink(missing_ok=True)
            self.cache_path(source_id).unlink(missing_ok=True)
            self.save()
