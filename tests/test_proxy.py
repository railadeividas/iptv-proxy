import gzip
import tempfile
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from urllib.error import HTTPError
from urllib.request import Request, urlopen
from urllib.parse import urlsplit

from app.m3u import parse_playlist
from app.proxy import CrossHostRedirects, Proxy, load_secret
from app.server import make_handler
from app.sources import SourceManager


class ProxyTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.requests = []

        class Upstream(BaseHTTPRequestHandler):
            def log_message(self, *args):
                pass

            def do_GET(self):
                cls.requests.append((self.path, dict(self.headers)))
                if self.headers.get("Referer") != "https://provider.example/":
                    self.send_error(403)
                    return
                if self.path == "/master.m3u8":
                    data = b'#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1000\nvariant/live.m3u8\n'
                elif self.path == "/variant/live.m3u8":
                    data = b'#EXTM3U\n#EXT-X-KEY:METHOD=AES-128,URI="../key"\n#EXTINF:5,\nsegment.ts?token=a%2Bb\n'
                elif self.path == "/redirect":
                    self.send_response(302)
                    self.send_header("Location", "/variant/live.m3u8")
                    self.end_headers()
                    return
                elif self.path == "/gzip.m3u8":
                    # Some providers compress manifests even when asked for identity.
                    data = gzip.compress(b'#EXTM3U\n#EXTINF:5,\nvariant/segment.ts?token=z\n')
                    self.send_response(200)
                    self.send_header("Content-Type", "application/vnd.apple.mpegurl")
                    self.send_header("Content-Encoding", "gzip")
                    self.send_header("Content-Length", str(len(data)))
                    self.end_headers()
                    self.wfile.write(data)
                    return
                elif self.path == "/key":
                    data = b"0123456789abcdef"
                elif self.path.startswith("/variant/segment.ts"):
                    data = bytes(range(256)) * 1024
                else:
                    self.send_error(404)
                    return
                if self.headers.get("Range") == "bytes=0-9":
                    self.send_response(206)
                    self.send_header("Content-Range", f"bytes 0-9/{len(data)}")
                    data = data[:10]
                else:
                    self.send_response(200)
                self.send_header("Content-Length", str(len(data)))
                self.end_headers()
                self.wfile.write(data)

        cls.upstream = ThreadingHTTPServer(("127.0.0.1", 0), Upstream)
        cls.upstream_url = f"http://127.0.0.1:{cls.upstream.server_port}"
        cls.temp = tempfile.TemporaryDirectory()
        cls.source = Path(cls.temp.name) / "playlist.m3u"
        cls.source.write_text(
            '#EXTM3U\n#EXTINF:-1 tvg-id="example",Example\n'
            '#EXTVLCOPT:http-referrer=https://provider.example/\n'
            '#EXTVLCOPT:http-user-agent=CustomPlayer/1.0\n'
            '#EXTVLCOPT:http-origin=https://provider.example\n'
            f'{cls.upstream_url}/master.m3u8\n'
        )
        cls.manager = SourceManager(Path(cls.temp.name) / "data", str(cls.source))
        cls.manager.refresh()
        cls.proxy = Proxy(b"test-secret", cls.manager)
        cls.server = ThreadingHTTPServer(("127.0.0.1", 0), make_handler(cls.proxy, cls.manager))
        cls.base_url = f"http://127.0.0.1:{cls.server.server_port}"
        for server in (cls.upstream, cls.server):
            threading.Thread(target=server.serve_forever, kwargs={"poll_interval": 0.05}, daemon=True).start()

    @classmethod
    def tearDownClass(cls):
        for server in (cls.server, cls.upstream):
            server.shutdown()
            server.server_close()
        cls.temp.cleanup()

    def fetch(self, url):
        with urlopen(url, timeout=5) as response:
            return response.read()

    def channel(self):
        return next(iter(self.proxy.catalog.get()[1].values()))

    def test_full_hls_chain_and_headers(self):
        playlist = self.fetch(self.base_url + "/playlist.m3u").decode()
        self.assertNotIn("EXTVLCOPT", playlist)
        self.assertIn('tvg-id="example"', playlist)
        master_url = playlist.splitlines()[-1]
        self.assertIn('/channel/', master_url)
        self.assertTrue(master_url.endswith('.m3u8'))
        master = self.fetch(master_url).decode()
        media = self.fetch(master.splitlines()[-1]).decode()
        key_url = media.split('URI="')[1].split('"')[0]
        self.assertEqual(self.fetch(key_url), b"0123456789abcdef")
        segment = self.fetch(media.splitlines()[-1])
        self.assertEqual(segment, bytes(range(256)) * 1024)
        request_path, headers = self.requests[-1]
        self.assertEqual(request_path, "/variant/segment.ts?token=a%2Bb")
        self.assertEqual(headers["User-Agent"], "CustomPlayer/1.0")
        self.assertEqual(headers["Origin"], "https://provider.example")

    def test_channel_list_mime_type_is_distinct_from_hls(self):
        with urlopen(self.base_url + '/playlist.m3u') as response:
            self.assertEqual(response.headers.get_content_type(), 'audio/x-mpegurl')
            self.assertEqual(response.headers.get_content_charset(), 'utf-8')
            channel_url = response.read().decode().splitlines()[-1]
        with urlopen(channel_url) as response:
            self.assertEqual(response.headers.get_content_type(), 'application/vnd.apple.mpegurl')

    def test_range_forwarding(self):
        url = self.proxy.link(self.channel().id, self.upstream_url + "/variant/segment.ts", self.base_url)
        with urlopen(Request(url, headers={"Range": "bytes=0-9"})) as response:
            self.assertEqual(response.status, 206)
            self.assertEqual(response.headers["Content-Range"], "bytes 0-9/262144")
            self.assertEqual(response.read(), bytes(range(10)))

    def test_redirect_uses_final_url_for_relative_paths(self):
        url = self.proxy.link(self.channel().id, self.upstream_url + "/redirect", self.base_url)
        manifest = self.fetch(url).decode()
        self.assertEqual(self.fetch(manifest.splitlines()[-1]), bytes(range(256)) * 1024)

    def test_tampered_signature_cannot_select_arbitrary_url(self):
        link = self.proxy.link(self.channel().id, self.upstream_url + "/master.m3u8", self.base_url)
        with self.assertRaises(HTTPError) as error:
            self.fetch(link.replace("master.m3u8", "other.m3u8"))
        self.assertEqual(error.exception.code, 403)
        error.exception.close()

    def test_header_formats_and_channel_isolation(self):
        _, channels = parse_playlist(
            '#EXTINF:-1,One\n#EXTHTTP:{"Referer":"https://one/","Cookie":"a=b"}\n'
            'https://example/one|User-Agent=Player%2F1\n'
            '#EXTINF:-1,Two\nhttps://example/two\n'
        )
        one, two = channels.values()
        self.assertEqual(one.headers, {"Referer": "https://one/", "Cookie": "a=b", "User-Agent": "Player/1"})
        self.assertEqual(two.headers, {})

    def test_uri_attributes_map_audio_and_inline_key(self):
        channel_id = self.channel().id
        rewritten = self.proxy.rewrite(
            '#EXTM3U\n#EXT-X-MAP:URI="init.mp4"\n'
            '#EXT-X-MEDIA:TYPE=AUDIO,URI="audio.m3u8"\n'
            '#EXT-X-KEY:URI="data:application/octet-stream;base64,YQ=="\n',
            channel_id, self.upstream_url + "/variant/live.m3u8", self.base_url,
        )
        self.assertIn(self.proxy.link(channel_id, self.upstream_url + "/variant/init.mp4", self.base_url), rewritten)
        self.assertIn(self.proxy.link(channel_id, self.upstream_url + "/variant/audio.m3u8", self.base_url), rewritten)
        self.assertIn('URI="data:application/octet-stream;base64,YQ=="', rewritten)

    def test_persistent_signing_key(self):
        path = Path(self.temp.name) / "key"
        self.assertEqual(load_secret(path), load_secret(path))

    def test_channel_url_does_not_depend_on_signing_key(self):
        channel = self.channel()
        other = Proxy(b'other-secret', self.manager)
        self.assertEqual(self.proxy.channel_link(channel, self.base_url), other.channel_link(channel, self.base_url))

    def test_request_address_used_for_playlist_and_nested_hls(self):
        server_base = self.base_url
        for host in ('localhost:3333', '192.168.1.10:3333', '[::1]:3333'):
            headers = {'Host': host, 'X-Forwarded-Host': 'untrusted.example'}
            with urlopen(Request(server_base + '/playlist.m3u', headers=headers)) as response:
                playlist = response.read().decode()
            link = playlist.splitlines()[-1]
            self.assertTrue(link.startswith(f'http://{host}/channel/'))
            with urlopen(Request(server_base + urlsplit(link).path, headers=headers)) as response:
                master = response.read().decode()
            nested = master.splitlines()[-1]
            self.assertTrue(nested.startswith(f'http://{host}/stream.m3u8?'))
            parts = urlsplit(nested)
            with urlopen(Request(server_base + parts.path + '?' + parts.query, headers=headers)) as response:
                media = response.read().decode()
            self.assertIn(f'URI="http://{host}/stream?', media)
            self.assertTrue(media.splitlines()[-1].startswith(f'http://{host}/stream.ts?'))

    def test_playlist_and_nested_links_use_https_behind_a_tls_proxy(self):
        headers = {'Host': 'tv.example.com', 'X-Forwarded-Proto': 'https'}
        with urlopen(Request(self.base_url + '/playlist.m3u', headers=headers)) as response:
            link = response.read().decode().splitlines()[-1]
        self.assertTrue(link.startswith('https://tv.example.com/channel/'))
        with urlopen(Request(self.base_url + urlsplit(link).path, headers=headers)) as response:
            nested = response.read().decode().splitlines()[-1]
        self.assertTrue(nested.startswith('https://tv.example.com/stream.m3u8?'))

    def test_hls_proxy_urls_keep_media_extensions_for_ffmpeg(self):
        for suffix in ('.ts', '.m3u8', '.mp4', '.m4s', '.aac', '.vtt'):
            link = self.proxy.link(self.channel().id, 'https://provider.example/segment' + suffix + '?token=123', self.base_url)
            self.assertEqual(urlsplit(link).path, '/stream' + suffix)

    def test_mixed_playlist_skips_unsupported_channels(self):
        _, channels = parse_playlist(
            '#EXTM3U\n#EXTINF:-1,RTSP\nrtsp://example/live\n'
            '#EXTINF:-1,Broken\nnot a url\n'
            '#EXTINF:-1 http-referrer="https://example/",HTTP\nhttps://example/live\n'
        )
        self.assertEqual(len(channels), 1)
        self.assertEqual(next(iter(channels.values())).headers["Referer"], "https://example/")

    def test_gzip_compressed_manifest_is_decoded_and_rewritten(self):
        link = self.proxy.link(self.channel().id, self.upstream_url + "/gzip.m3u8", self.base_url)
        manifest = self.fetch(link).decode()
        self.assertTrue(manifest.startswith("#EXTM3U"))
        self.assertEqual(self.fetch(manifest.splitlines()[-1]), bytes(range(256)) * 1024)
        with urlopen(Request(link, method="HEAD")) as response:
            self.assertEqual(response.headers.get_content_type(), "application/vnd.apple.mpegurl")

    def test_invalid_entries_are_skipped_not_fatal(self):
        stats = {}
        _, channels = parse_playlist(
            '#EXTINF:-1,Bad JSON\n#EXTHTTP:{not json}\nhttps://example/bad\n'
            '#EXTINF:-1,Bad header\n#EXTVLCOPT:http-cookie=a\x01b\nhttps://example/bad2\n'
            '#EXTINF:-1,Good\nhttps://example/good\n', stats)
        self.assertEqual([c.url for c in channels.values()], ["https://example/good"])
        self.assertEqual(stats["skipped"], 2)
        self.assertEqual([e["name"] for e in stats["skipped_entries"]], ["Bad JSON", "Bad header"])
        self.assertIn("EXTHTTP", stats["skipped_entries"][0]["reason"])

    def test_skipped_entries_hide_url_queries(self):
        stats = {}
        parse_playlist('#EXTINF:-1 tvg-id="x",Stream\nrtsp://user:pw@host.example:554/live?token=abc\n#EXTINF:-1,Ok\nhttps://e/a\n', stats)
        entry = stats["skipped_entries"][0]
        self.assertEqual(entry["url"], "rtsp://host.example/live")
        self.assertNotIn("token", str(entry))
        self.assertNotIn("pw", str(entry))

    def test_duplicate_entries_are_kept(self):
        _, channels = parse_playlist('#EXTINF:-1,Same\nhttps://example/a\n#EXTINF:-1,Same\nhttps://example/a\n')
        self.assertEqual(len(channels), 2)

    def test_credentials_are_not_exported_in_extinf(self):
        _, channels = parse_playlist('#EXTINF:-1 tvg-id="x" http-cookie="secret=1",Name\nhttps://example/a\n')
        channel = next(iter(channels.values()))
        self.assertEqual(channel.metadata[0], '#EXTINF:-1 tvg-id="x",Name')
        self.assertEqual(channel.headers["Cookie"], "secret=1")

    def test_head_uses_get_upstream_and_returns_no_body(self):
        link = self.proxy.link(self.channel().id, self.upstream_url + "/variant/segment.ts", self.base_url)
        with urlopen(Request(link, method="HEAD")) as response:
            self.assertEqual(response.status, 200)
            self.assertEqual(response.headers["Content-Length"], "262144")
            self.assertEqual(response.read(), b"")
        self.assertEqual(self.requests[-1][0], "/variant/segment.ts")

    def test_cross_host_redirect_drops_cookies_only(self):
        handler = CrossHostRedirects()
        request = Request("http://one.example/a", headers={"Cookie": "a=b", "Authorization": "x", "Referer": "r"})
        other = handler.redirect_request(request, None, 302, "Found", {}, "http://two.example/b")
        self.assertFalse(other.has_header("Cookie") or other.has_header("Authorization"))
        self.assertTrue(other.has_header("Referer"))
        same = handler.redirect_request(request, None, 302, "Found", {}, "http://one.example/c")
        self.assertTrue(same.has_header("Cookie") and same.has_header("Authorization"))

    def test_playlist_unavailable_until_first_load(self):
        self.manager.loaded = False
        try:
            with self.assertRaises(HTTPError) as context:
                urlopen(self.base_url + "/playlist.m3u")
            self.assertEqual(context.exception.code, 503)
            context.exception.close()
        finally:
            self.manager.loaded = True

    def test_static_files_use_etags(self):
        with urlopen(self.base_url + "/style.css") as response:
            etag = response.headers["ETag"]
        with self.assertRaises(HTTPError) as context:
            urlopen(Request(self.base_url + "/style.css", headers={"If-None-Match": etag}))
        self.assertEqual(context.exception.code, 304)
        context.exception.close()
        with self.assertRaises(HTTPError) as context:
            urlopen(self.base_url + "/server.py")
        self.assertEqual(context.exception.code, 404)
        context.exception.close()


if __name__ == "__main__":
    unittest.main()
