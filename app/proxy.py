"""Stream signing, HLS manifest rewriting and the upstream HTTP opener."""

import hashlib
import hmac
import os
import re
import secrets
from pathlib import Path
from urllib.parse import urlencode, urljoin, urlsplit
from urllib.request import HTTPRedirectHandler, build_opener

from .m3u import http_url

URI_ATTRIBUTE = re.compile(r'''\bURI=(['"])(.*?)\1''')
CHANNEL_EXTENSIONS = (".m3u8", ".ts", ".mp4", ".mp3", ".aac")


class CrossHostRedirects(HTTPRedirectHandler):
    """Follow redirects, but never forward cookies or credentials to another host."""

    def redirect_request(self, req, fp, code, msg, headers, newurl):
        new = super().redirect_request(req, fp, code, msg, headers, newurl)
        if new is not None and urlsplit(newurl).hostname != urlsplit(req.full_url).hostname:
            for name in ("Cookie", "Authorization"):
                new.remove_header(name)
        return new


class Proxy:
    def __init__(self, secret, catalog=None):
        self.secret = secret
        self.catalog = catalog
        self.opener = build_opener(CrossHostRedirects)
        # Management tokens are only valid for this process lifetime.
        self.boot_nonce = secrets.token_bytes(16)

    def open(self, request):
        return self.opener.open(request, timeout=30)

    @property
    def management_token(self):
        return hmac.new(self.secret, b"manage-sources:" + self.boot_nonce, hashlib.sha256).hexdigest()

    def signature(self, channel_id, url):
        return hmac.new(self.secret, (channel_id + "\n" + url).encode(), hashlib.sha256).hexdigest()

    def link(self, channel_id, url, base_url):
        http_url(url)
        extension = Path(urlsplit(url).path).suffix.lower()
        if not re.fullmatch(r"\.[a-z0-9]{1,12}", extension):
            extension = ""
        # FFmpeg validates the path extension before opening HLS segments.
        return base_url + "/stream" + extension + "?" + urlencode({
            "channel": channel_id, "url": url, "sig": self.signature(channel_id, url),
        })

    def playlist(self, base_url):
        preamble, channels = self.catalog.get()
        lines = [preamble]
        for channel in channels.values():
            lines.extend(channel.metadata)
            lines.append(self.channel_link(channel, base_url))
        return "\n".join(lines) + "\n"

    def channel_link(self, channel, base_url):
        extension = Path(urlsplit(channel.url).path).suffix.lower()
        if extension == ".m3u":
            extension = ".m3u8"
        if extension not in CHANNEL_EXTENSIONS:
            extension = ""
        return f"{base_url}/channel/{channel.id}{extension}"

    def rewrite(self, text, channel_id, source_url, base_url):
        def proxied(uri):
            # Inline keys need no HTTP request.
            if uri.startswith("data:"):
                return uri
            return self.link(channel_id, urljoin(source_url, uri), base_url)

        lines = []
        for line in text.splitlines():
            trimmed = line.strip()
            if trimmed.startswith("#"):
                line = URI_ATTRIBUTE.sub(lambda m: f'URI="{proxied(m[2])}"', line)
            elif trimmed:
                line = proxied(trimmed)
            lines.append(line)
        return "\n".join(lines) + "\n"


def load_secret(path):
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    try:
        fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(fd, "wb") as stream:
            stream.write(secrets.token_bytes(32))
    except FileExistsError:
        pass
    value = path.read_bytes()
    if len(value) != 32:
        raise ValueError("Proxy signing key must contain 32 bytes")
    return value
