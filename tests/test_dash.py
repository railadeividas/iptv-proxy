import tempfile
import threading
import unittest
import xml.etree.ElementTree as ET
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from urllib.error import HTTPError
from urllib.parse import urlsplit
from urllib.request import Request, urlopen

from app.proxy import Proxy
from app.server import make_handler
from app.sources import SourceManager

NS = "{urn:mpeg:dash:schema:mpd:2011}"
MPD = """<?xml version="1.0" encoding="UTF-8"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" xmlns:cenc="urn:mpeg:cenc:2013" type="dynamic" minimumUpdatePeriod="PT5S">
  <Location>https://elsewhere.example/live.mpd</Location>
  <BaseURL>media/</BaseURL>
  <BaseURL>https://backup.example/media/</BaseURL>
  <Period id="1">
    <AdaptationSet mimeType="video/mp4">
      <ContentProtection schemeIdUri="urn:mpeg:dash:mp4protection:2011" cenc:default_KID="abc"/>
      <SegmentTemplate timescale="1000" media="video-$RepresentationID$-$Number%05d$.m4s?tok=1" initialization="video-init.mp4" startNumber="1">
        <SegmentTimeline><S t="0" d="2000" r="2"/></SegmentTimeline>
      </SegmentTemplate>
      <Representation id="v1" bandwidth="1000"/>
    </AdaptationSet>
    <AdaptationSet mimeType="audio/mp4">
      <Representation id="a1" bandwidth="100">
        <BaseURL>audio.mp4</BaseURL>
        <SegmentBase indexRange="0-99"><Initialization range="0-50"/></SegmentBase>
      </Representation>
    </AdaptationSet>
    <AdaptationSet mimeType="video/mp4">
      <Representation id="v2" bandwidth="500">
        <SegmentList><Initialization sourceURL="https://cdn.example/abs/init.mp4"/><SegmentURL media="../seg1.m4s"/></SegmentList>
      </Representation>
    </AdaptationSet>
  </Period>
</MPD>
"""


class DashTests(unittest.TestCase):
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
                if self.path == "/live/stream.mpd":
                    data, kind = MPD.encode(), "application/dash+xml"
                elif self.path.startswith("/live/media/") or self.path == "/live/seg1.m4s":
                    data, kind = b"segment:" + self.path.encode(), "video/mp4"
                else:
                    self.send_error(404)
                    return
                self.send_response(200)
                self.send_header("Content-Type", kind)
                self.send_header("Content-Length", str(len(data)))
                self.end_headers()
                self.wfile.write(data)

        cls.upstream = ThreadingHTTPServer(("127.0.0.1", 0), Upstream)
        cls.upstream_url = f"http://127.0.0.1:{cls.upstream.server_port}"
        cls.temp = tempfile.TemporaryDirectory()
        source = Path(cls.temp.name) / "playlist.m3u"
        source.write_text('#EXTM3U\n#EXTINF:-1 tvg-id="dash",Dash\n#EXTVLCOPT:http-referrer=https://provider.example/\n'
                          f'{cls.upstream_url}/live/stream.mpd\n')
        cls.manager = SourceManager(Path(cls.temp.name) / "data", str(source))
        cls.manager.refresh()
        cls.proxy = Proxy(b"test-secret", cls.manager)
        cls.server = ThreadingHTTPServer(("127.0.0.1", 0), make_handler(cls.proxy, cls.manager))
        cls.base = f"http://127.0.0.1:{cls.server.server_port}"
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

    def manifest(self):
        link = self.fetch(self.base + "/playlist.m3u").decode().splitlines()[-1]
        self.assertTrue(link.endswith(".mpd"))
        with urlopen(link) as response:
            self.assertEqual(response.headers.get_content_type(), "application/dash+xml")
            return response.read().decode()

    def test_manifest_is_rewritten_and_media_gets_headers(self):
        text = self.manifest()
        self.assertNotIn(self.upstream_url, text)
        self.assertNotIn("elsewhere.example", text)  # Location would bypass the proxy
        self.assertNotIn("backup.example", text)  # only the first BaseURL is kept
        root = ET.fromstring(text)
        template = root.find(f".//{NS}SegmentTemplate")
        media = template.get("media")
        self.assertTrue(media.startswith(f"{self.base}/dash/"))
        self.assertIn("video-$RepresentationID$-$Number%05d$.m4s?tok=1", media)
        self.assertEqual(root.find(f".//{NS}ContentProtection").get("{urn:mpeg:cenc:2013}default_KID"), "abc")
        # The player fills in the template; the proxy serves it with the channel's headers.
        segment = self.fetch(media.replace("$RepresentationID$", "v1").replace("$Number%05d$", "00002"))
        self.assertEqual(segment, b"segment:/live/media/video-v1-00002.m4s?tok=1")
        self.assertEqual(self.requests[-1][1]["Referer"], "https://provider.example/")
        self.assertTrue(self.fetch(template.get("initialization")).startswith(b"segment:/live/media/video-init.mp4"))

    def test_base_url_files_and_relative_lists(self):
        root = ET.fromstring(self.manifest())
        audio = root.findall(f".//{NS}BaseURL")[-1].text
        self.assertEqual(self.fetch(audio), b"segment:/live/media/audio.mp4")
        segment = root.find(f".//{NS}SegmentURL").get("media")
        self.assertEqual(self.fetch(segment), b"segment:/live/seg1.m4s")  # "../seg1.m4s" resolved against the BaseURL
        init = root.find(f".//{NS}SegmentList/{NS}Initialization").get("sourceURL")
        self.assertIn("/dash/", init)  # absolute URLs go through the proxy as well

    def test_range_requests_reach_the_segment(self):
        root = ET.fromstring(self.manifest())
        audio = root.findall(f".//{NS}BaseURL")[-1].text
        self.assertEqual(self.fetch(Request(audio, headers={"Range": "bytes=0-9"})), b"segment:/live/media/audio.mp4")

    def test_signature_limits_requests_to_the_signed_directory(self):
        root = ET.fromstring(self.manifest())
        audio = root.findall(f".//{NS}BaseURL")[-1].text
        parts = urlsplit(audio)
        directory = parts.path.rsplit("/", 1)[0]
        for path in (directory + "/../secret", directory + "/%2e%2e/secret", directory.replace("/dash/", "/dash/x")):
            with self.assertRaises(HTTPError) as error:
                self.fetch(f"{self.base}{path}")
            self.assertIn(error.exception.code, (403, 404))
            error.exception.close()
        other = Proxy(b"other-secret", self.manager).dash_link(next(iter(self.manager.get()[1])), self.upstream_url + "/live/media/audio.mp4", self.base)
        with self.assertRaises(HTTPError) as error:
            self.fetch(other)
        self.assertEqual(error.exception.code, 403)
        error.exception.close()

    def test_invalid_manifest_is_rejected(self):
        with self.assertRaises(ValueError):
            self.proxy.rewrite_dash("<html/>", "c", "http://x/a.mpd", self.base)
        with self.assertRaises(ValueError):
            self.proxy.rewrite_dash("not xml", "c", "http://x/a.mpd", self.base)


if __name__ == "__main__":
    unittest.main()
