"""M3U parsing helpers shared by the proxy and the source manager."""

import hashlib
import json
import logging
import re
from dataclasses import dataclass
from urllib.parse import parse_qsl, urlsplit

LOG = logging.getLogger("iptv")
LIMIT = 16 * 1024 * 1024
HEADER_NAMES = {
    "http-referrer": "Referer", "http-referer": "Referer",
    "http-user-agent": "User-Agent", "http-origin": "Origin",
    "http-cookie": "Cookie", "http-authorization": "Authorization",
    "referer": "Referer", "referrer": "Referer", "user-agent": "User-Agent",
    "origin": "Origin", "cookie": "Cookie", "authorization": "Authorization",
    "accept": "Accept",
}
HTTP_ATTRIBUTE = re.compile(r'\s+http-[\w-]+="[^"]*"')
HTTP_ATTRIBUTE_PAIR = re.compile(r'(http-[\w-]+)="([^"]*)"')
SKIPPED_LIMIT = 500


def entry_name(extinf):
    # Quoted attributes can contain commas, so mask them before finding the name.
    return re.sub(r'"[^"]*"', '""', extinf).partition(",")[2].strip() or "Unnamed channel"


def redact(url):
    """Keep scheme, host and path only; query strings often carry tokens."""
    parts = urlsplit(url)
    if parts.hostname:
        return f"{parts.scheme}://{parts.hostname}{parts.path}"[:200]
    return url.split("?")[0][:200]


def has_control_characters(value):
    return any(ord(c) < 32 or ord(c) == 127 for c in value)


def http_url(url):
    parts = urlsplit(url)
    if parts.scheme not in ("http", "https") or not parts.hostname:
        raise ValueError("Only absolute HTTP(S) upstream URLs are supported")
    if has_control_characters(url):
        raise ValueError("Control characters in upstream URL")
    return url


def add_header(headers, name, value):
    canonical = HEADER_NAMES.get(name.lower().strip())
    if canonical:
        value = str(value).strip().strip('"')
        if has_control_characters(value):
            raise ValueError("Control characters in upstream header")
        headers[canonical] = value


@dataclass
class Channel:
    id: str
    metadata: list[str]
    url: str
    headers: dict[str, str]


def parse_playlist(text, stats=None):
    """Return (preamble, {id: Channel}). Invalid entries are skipped and counted."""
    channels, occurrences = {}, {}
    metadata, headers, invalid = [], {}, ""
    skipped_entries = []
    preamble = "#EXTM3U"
    skipped = 0
    for raw in text.lstrip("﻿").splitlines():
        line = raw.strip()
        if line.startswith("#EXTM3U"):
            preamble = line
        elif line.startswith("#EXTINF:"):
            # Per-channel credentials stay in the proxy; they are not exported to clients.
            metadata, headers, invalid = [HTTP_ATTRIBUTE.sub("", line)], {}, ""
            try:
                for name, value in HTTP_ATTRIBUTE_PAIR.findall(line):
                    add_header(headers, name, value)
            except ValueError as error:
                invalid = str(error)
        elif metadata and line.startswith("#EXTVLCOPT:"):
            name, separator, value = line[11:].partition("=")
            if separator:
                try:
                    add_header(headers, name, value)
                except ValueError as error:
                    invalid = str(error)
        elif metadata and line.startswith("#EXTHTTP:"):
            try:
                extra = json.loads(line[len("#EXTHTTP:"):])
                if not isinstance(extra, dict):
                    raise ValueError("EXTHTTP must be a JSON object")
                for name, value in extra.items():
                    add_header(headers, name, value)
            except ValueError as error:
                invalid = "Invalid #EXTHTTP line: " + str(error)
        elif metadata and line.startswith("#EXTGRP:"):
            metadata.append(line)
        elif metadata and line and not line.startswith("#"):
            url, _, options = line.partition("|")
            try:
                if invalid:
                    raise ValueError(invalid)
                for name, value in parse_qsl(options):
                    add_header(headers, name, value)
                http_url(url)
            except ValueError as error:
                skipped += 1
                if len(skipped_entries) < SKIPPED_LIMIT:
                    skipped_entries.append({"name": entry_name(metadata[0]), "reason": str(error), "url": redact(url)})
                metadata, headers, invalid = [], {}, ""
                continue
            key = metadata[0] + "\n" + url
            occurrence = occurrences.get(key, 0)
            occurrences[key] = occurrence + 1
            channel_id = hashlib.sha256(f"{key}\n{occurrence}".encode()).hexdigest()[:24]
            channels[channel_id] = Channel(channel_id, metadata, url, headers)
            metadata, headers, invalid = [], {}, ""
    if not channels:
        raise ValueError("Source playlist contains no EXTINF channel entries")
    if skipped:
        LOG.warning("Skipped %s channel entries with unsupported or invalid URLs or headers", skipped)
    if stats is not None:
        stats.update(channels=len(channels), skipped=skipped, skipped_entries=skipped_entries)
    return preamble, channels


def read_limited(stream):
    data = stream.read(LIMIT + 1)
    if len(data) > LIMIT:
        raise ValueError("Playlist exceeds 16 MiB limit")
    return data.decode("utf-8-sig")
