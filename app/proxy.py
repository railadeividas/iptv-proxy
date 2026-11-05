"""Stream signing, HLS manifest rewriting and the upstream HTTP opener."""

import base64
import hashlib
import hmac
import os
import re
import secrets
import xml.etree.ElementTree as ET
from pathlib import Path
from urllib.parse import unquote, urlencode, urljoin, urlsplit
from urllib.request import HTTPRedirectHandler, build_opener

from .m3u import http_url

URI_ATTRIBUTE = re.compile(r'''\bURI=(['"])(.*?)\1''')
CHANNEL_EXTENSIONS = (".m3u8", ".mpd", ".ts", ".mp4", ".mp3", ".aac")
# Attributes of DASH elements that hold a URL (or a URL template) to the media.
DASH_URL_ATTRIBUTES = {
    "SegmentTemplate": ("media", "initialization", "index", "bitstreamSwitching"),
    "Initialization": ("sourceURL",), "RepresentationIndex": ("sourceURL",), "BitstreamSwitching": ("sourceURL",),
    "SegmentURL": ("media", "index"),
}
for _prefix, _uri in {"": "urn:mpeg:dash:schema:mpd:2011", "cenc": "urn:mpeg:cenc:2013", "mspr": "urn:microsoft:playready",
                      "xsi": "http://www.w3.org/2001/XMLSchema-instance", "xlink": "http://www.w3.org/1999/xlink"}.items():
    ET.register_namespace(_prefix, _uri)


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

    def dash_signature(self, channel_id, base):
        return hmac.new(self.secret, ("dash\n" + channel_id + "\n" + base).encode(), hashlib.sha256).hexdigest()

    def dash_link(self, channel_id, url, base_url):
        """Proxy link for a DASH media URL or URL template.

        Templates such as seg-$Number$.m4s cannot be signed one by one, so the signature covers
        the directory in front of the first template or query part. The player appends the rest."""
        http_url(url)
        cut = min((i for i in (url.find("$"), url.find("?")) if i >= 0), default=len(url))
        split = url.rfind("/", 0, cut) + 1
        base, rest = url[:split], url[split:]
        if "//" not in base or base.endswith("//"):
            raise ValueError("Unsupported DASH URL")
        token = base64.urlsafe_b64encode(base.encode()).decode().rstrip("=")
        return f"{base_url}/dash/{channel_id}/{self.dash_signature(channel_id, base)}/{token}/{rest}"

    def dash_upstream(self, channel_id, signature, token, rest, query):
        """Return the upstream URL for a proxied DASH request, or None when it is not allowed."""
        try:
            base = base64.urlsafe_b64decode(token + "=" * (-len(token) % 4)).decode()
        except (ValueError, UnicodeError):
            return None
        if not hmac.compare_digest(self.dash_signature(channel_id, base), signature):
            return None
        # The signature allows only URLs below the signed directory.
        if any(unquote(part) in (".", "..") or "\\" in unquote(part) for part in rest.split("/")):
            return None
        return http_url(base + rest + ("?" + query if query else ""))

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

    def rewrite_dash(self, text, channel_id, source_url, base_url):
        """Point every media URL of a DASH manifest at the proxy, so request headers apply to it."""
        try:
            root = ET.fromstring(text)
        except ET.ParseError as error:
            raise ValueError("Invalid DASH manifest") from error
        if not root.tag.rpartition("}")[2] == "MPD":
            raise ValueError("Invalid DASH manifest")

        def visit(element, scope):
            urls = [child for child in element if child.tag.rpartition("}")[2] == "BaseURL"]
            for extra in urls[1:]:
                element.remove(extra)  # alternative locations: use the first one
            if urls and (urls[0].text or "").strip():
                scope = urljoin(scope, urls[0].text.strip())
                urls[0].text = self.dash_link(channel_id, scope, base_url)
            for attribute in DASH_URL_ATTRIBUTES.get(element.tag.rpartition("}")[2], ()):
                if element.get(attribute):
                    element.set(attribute, self.dash_link(channel_id, urljoin(scope, element.get(attribute)), base_url))
            for child in list(element):
                if child.tag.rpartition("}")[2] == "Location":
                    element.remove(child)  # refreshes must keep coming through the proxy
                else:
                    visit(child, scope)
        visit(root, source_url)
        return '<?xml version="1.0" encoding="UTF-8"?>\n' + ET.tostring(root, encoding="unicode") + "\n"


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
