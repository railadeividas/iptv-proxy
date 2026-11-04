import json
import tempfile
import threading
import time
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from unittest.mock import patch
from urllib.error import HTTPError
from urllib.request import Request, urlopen

from app.proxy import Proxy
from app.server import make_handler
from app.sources import SourceManager


PLAYLIST = '#EXTM3U\n#EXTINF:-1 tvg-id="test",Test channel\n#EXTVLCOPT:http-referrer=https://provider.example/\nhttps://media.example/master.m3u8\n'


class SourceTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.manager = SourceManager(self.temp.name)
        self.proxy = Proxy(b'test-secret', self.manager)
        self.server = ThreadingHTTPServer(('127.0.0.1', 0), make_handler(self.proxy, self.manager))
        self.base_url = f'http://127.0.0.1:{self.server.server_port}'
        threading.Thread(target=self.server.serve_forever, kwargs={'poll_interval': 0.05}, daemon=True).start()
        self.token = self.api()['token']

    def tearDown(self):
        self.server.shutdown()
        self.server.server_close()
        self.temp.cleanup()

    def api(self, route='/api/sources', payload=None, token=True):
        request = Request(self.base_url + route)
        if payload is not None:
            request.data = json.dumps(payload).encode()
            request.add_header('Content-Type', 'application/json')
            if token:
                request.add_header('X-Management-Token', self.token)
        with urlopen(request, timeout=5) as response:
            return json.load(response)

    def add(self, name='Test', content=PLAYLIST, select=True):
        status = self.api('/api/sources/add', {'kind': 'upload', 'name': name, 'content': content})
        if select:
            status = self.api('/api/content/save', {'id': status['sources'][-1]['id'], 'rules': {'default': True, 'groups': {}, 'channels': {}}})
        return status

    def test_upload_combine_and_persist(self):
        self.add('First')
        status = self.add('Second')
        self.assertEqual(status['channels'], 2)
        entries = list(self.manager.get()[1].values())
        self.assertNotEqual(entries[0].id, entries[1].id)
        self.assertEqual(entries[0].headers['Referer'], 'https://provider.example/')
        reloaded = SourceManager(self.temp.name, '/does-not-exist')
        reloaded.refresh()
        self.assertEqual(len(reloaded.get()[1]), 2)
        self.assertEqual(set(reloaded.get()[1]), set(self.manager.get()[1]))

    def test_toggle_and_delete(self):
        source_id = self.add()['sources'][0]['id']
        disabled = self.api('/api/sources/toggle', {'id': source_id})
        self.assertEqual(disabled['channels'], 0)
        self.assertFalse(disabled['sources'][0]['enabled'])
        enabled = self.api('/api/sources/toggle', {'id': source_id})
        self.assertEqual(enabled['channels'], 1)
        removed = self.api('/api/sources/delete', {'id': source_id})
        self.assertEqual(removed['sources'], [])
        self.assertFalse(list(Path(self.temp.name).glob('playlist-*.m3u')))

    def test_management_requires_token(self):
        with self.assertRaises(HTTPError) as context:
            self.api('/api/sources/add', {'kind': 'upload', 'name': 'Blocked', 'content': PLAYLIST}, token=False)
        self.assertEqual(context.exception.code, 403)
        context.exception.close()
        self.assertEqual(self.manager.sources, [])

    def test_invalid_upload_is_not_saved(self):
        with self.assertRaises(HTTPError) as context:
            self.api('/api/sources/add', {'kind': 'upload', 'name': 'Bad', 'content': '<html>Error</html>'})
        self.assertEqual(context.exception.code, 400)
        context.exception.close()
        self.assertEqual(self.manager.sources, [])

    def test_failed_refresh_retains_last_copy(self):
        source = self.add()['sources'][0]
        Path(source['location']).write_text('not a playlist')
        status = self.api('/api/sources/refresh', {'id': source['id']})
        self.assertEqual(status['channels'], 1)
        self.assertIsNotNone(status['sources'][0]['error'])
        self.assertNotIn(source['location'], status['sources'][0]['error'])

    def test_remote_source_and_refresh(self):
        class Provider(BaseHTTPRequestHandler):
            def log_message(self, *args):
                pass

            def do_GET(self):
                self.send_response(200)
                self.end_headers()
                self.wfile.write(PLAYLIST.encode())

        server = ThreadingHTTPServer(('127.0.0.1', 0), Provider)
        threading.Thread(target=server.serve_forever, kwargs={'poll_interval': 0.05}, daemon=True).start()
        try:
            status = self.api('/api/sources/add', {'kind': 'url', 'name': 'Remote', 'url': f'http://127.0.0.1:{server.server_port}/playlist.m3u'})
            self.assertEqual(status['channels'], 0)
            self.assertEqual(status['sources'][0]['channels'], 1)
            self.assertEqual(status['sources'][0]['included'], 0)
            self.assertIsNone(status['sources'][0]['error'])
            self.assertEqual(status['sources'][0]['kind'], 'url')
        finally:
            server.shutdown()
            server.server_close()

    def test_static_ui_and_no_source_boot(self):
        self.assertEqual(self.api()['channels'], 0)
        with urlopen(self.base_url + '/') as response:
            self.assertIn(b'Your playlist', response.read())
            self.assertIn('text/html', response.headers['Content-Type'])

    def test_content_filter_groups_and_channel_overrides(self):
        playlist = (
            '#EXTM3U\n#EXTINF:-1 tvg-id="a" group-title="Sports",F1\nhttps://example/f1?token=old\n'
            '#EXTINF:-1 tvg-id="b" group-title="Sports",Football\nhttps://example/football\n'
            '#EXTINF:-1 tvg-id="c" group-title="News",News\nhttps://example/news\n'
        )
        source = self.add(content=playlist)['sources'][0]
        content = self.api('/api/content?source=' + source['id'])
        sports = next(g for g in content['groups'] if g['name'] == 'Sports')
        football = next(c for c in sports['channels'] if c['name'] == 'Football')
        rules = {'default': False, 'groups': {'Sports': True}, 'channels': {football['id']: False}}
        result = self.api('/api/content/save', {'id': source['id'], 'rules': rules})
        self.assertEqual(result['channels'], 1)
        self.assertEqual(result['sources'][0]['included'], 1)
        self.assertEqual(result['sources'][0]['channels'], 3)
        self.assertIn(',F1\n', self.proxy.playlist(self.base_url))
        self.assertNotIn(',Football\n', self.proxy.playlist(self.base_url))
        reloaded = SourceManager(self.temp.name)
        reloaded.refresh()
        self.assertEqual(len(reloaded.get()[1]), 1)
        # New channels follow the group, and changing a URL token retains exclusions.
        Path(source['location']).write_text(playlist.replace('token=old', 'token=new') +
            '#EXTINF:-1 tvg-id="d" group-title="Sports",Tennis\nhttps://example/tennis\n')
        self.manager.refresh()
        self.assertEqual(len(self.manager.get()[1]), 2)
        self.assertNotIn(',Football\n', self.proxy.playlist(self.base_url))

    def test_channel_choice_survives_rotating_url(self):
        source = self.add()['sources'][0]
        content = self.api('/api/content?source=' + source['id'])
        channel = content['groups'][0]['channels'][0]
        self.api('/api/content/save', {'id': source['id'], 'rules': {'default': False, 'groups': {}, 'channels': {channel['id']: True}}})
        old_playlist = self.proxy.playlist(self.base_url)
        old_id = next(iter(self.manager.get()[1]))
        Path(source['location']).write_text(PLAYLIST.replace('master.m3u8', 'master.m3u8?token=new'))
        self.manager.refresh()
        self.assertEqual(len(self.manager.get()[1]), 1)
        self.assertEqual(old_playlist, self.proxy.playlist(self.base_url))
        self.assertEqual(old_id, next(iter(self.manager.get()[1])))
        self.assertIn('?token=new', self.manager.get()[1][old_id].url)

    def test_excluded_channel_blocks_existing_signed_link(self):
        source = self.add()['sources'][0]
        channel = next(iter(self.manager.get()[1].values()))
        old_link = self.proxy.link(channel.id, channel.url, self.base_url)
        self.api('/api/content/save', {'id': source['id'], 'rules': {'default': False, 'groups': {}, 'channels': {}}})
        self.assertEqual(self.proxy.playlist(self.base_url), '#EXTM3U\n')
        with self.assertRaises(HTTPError) as context:
            urlopen(old_link)
        self.assertEqual(context.exception.code, 404)
        context.exception.close()

    def test_content_rules_validate_booleans(self):
        source = self.add()['sources'][0]
        with self.assertRaises(HTTPError) as context:
            self.api('/api/content/save', {'id': source['id'], 'rules': {'default': 'false', 'groups': {}, 'channels': {}}})
        self.assertEqual(context.exception.code, 400)
        context.exception.close()
        self.assertEqual(len(self.manager.get()[1]), 1)

    def test_edit_name_preserves_id_filters_disabled_and_file(self):
        source = self.add()['sources'][0]
        self.api('/api/content/save', {'id': source['id'], 'rules': {'default': False, 'groups': {}, 'channels': {}}})
        self.api('/api/sources/toggle', {'id': source['id']})
        result = self.api('/api/sources/edit', {'id': source['id'], 'name': 'Renamed', 'kind': 'upload', 'refresh_seconds': 0})
        changed = result['sources'][0]
        self.assertEqual(changed['id'], source['id'])
        self.assertEqual(changed['name'], 'Renamed')
        self.assertEqual(changed['location'], source['location'])
        self.assertFalse(changed['enabled'])
        self.assertEqual(changed['refresh_seconds'], 0)
        self.assertEqual(len(result['sources']), 1)
        self.assertEqual(Path(source['location']).read_text(), PLAYLIST)
        reloaded = SourceManager(self.temp.name)
        self.assertEqual(reloaded.sources[0]['refresh_seconds'], 0)
        self.assertFalse(reloaded.filters[source['id']]['default'])

    def test_edit_replace_upload_and_reject_invalid_replacement(self):
        source = self.add()['sources'][0]
        replacement = PLAYLIST.replace('Test channel', 'Replacement')
        result = self.api('/api/sources/edit', {'id': source['id'], 'name': 'Updated', 'kind': 'upload', 'content': replacement})
        self.assertEqual(result['sources'][0]['id'], source['id'])
        self.assertIn(',Replacement\n', self.proxy.playlist(self.base_url))
        with self.assertRaises(HTTPError) as context:
            self.api('/api/sources/edit', {'id': source['id'], 'name': 'Invalid', 'kind': 'upload', 'content': 'not a playlist'})
        self.assertEqual(context.exception.code, 400)
        context.exception.close()
        self.assertEqual(Path(source['location']).read_text(), replacement)
        self.assertEqual(self.manager.sources[0]['name'], 'Updated')

    def test_edit_remote_url_keeps_selections(self):
        class Provider(BaseHTTPRequestHandler):
            def log_message(self, *args):
                pass

            def do_GET(self):
                self.send_response(200)
                self.end_headers()
                self.wfile.write(PLAYLIST.encode())

        server = ThreadingHTTPServer(('127.0.0.1', 0), Provider)
        threading.Thread(target=server.serve_forever, kwargs={'poll_interval': 0.05}, daemon=True).start()
        try:
            base = f'http://127.0.0.1:{server.server_port}'
            source = self.api('/api/sources/add', {'name': 'Remote', 'kind': 'url', 'url': base + '/old.m3u'})['sources'][0]
            self.api('/api/content/save', {'id': source['id'], 'rules': {'default': False, 'groups': {}, 'channels': {}}})
            result = self.api('/api/sources/edit', {'id': source['id'], 'name': 'Changed', 'kind': 'url', 'url': base + '/new.m3u', 'refresh_seconds': 300})
            self.assertEqual(result['sources'][0]['id'], source['id'])
            self.assertEqual(result['sources'][0]['location'], base + '/new.m3u')
            self.assertEqual(result['channels'], 0)
        finally:
            server.shutdown()
            server.server_close()

    def test_mounted_playlist_edit_does_not_modify_original(self):
        original = Path(self.temp.name) / 'original.m3u'
        original.write_text(PLAYLIST)
        manager = SourceManager(Path(self.temp.name) / 'mounted-data', str(original))
        manager.refresh()
        manager.action('/api/sources/edit', {'id': 'default', 'name': 'Rename', 'kind': 'upload'})
        self.assertEqual(manager.sources[0]['kind'], 'mounted')
        manager.action('/api/sources/edit', {'id': 'default', 'name': 'Replacement', 'kind': 'upload', 'content': PLAYLIST.replace('Test channel', 'New')})
        self.assertEqual(manager.sources[0]['kind'], 'upload')
        self.assertEqual(original.read_text(), PLAYLIST)

    def test_per_source_schedule_and_manual_only(self):
        sources = []
        for interval in (0, 30, 120):
            status = self.api('/api/sources/add', {'name': str(interval), 'kind': 'upload', 'content': PLAYLIST, 'refresh_seconds': interval})
            sources.append(status['sources'][-1])
        for source in sources:
            Path(source['location']).write_text(PLAYLIST.replace('Test channel', 'Updated'))
        self.manager.attempted = {s['id']: 900 for s in sources}
        with patch('app.sources.time.monotonic', return_value=1000):
            self.manager.refresh(due_only=True)
        for source in sources:
            channel = next(iter(self.manager.cache[source['id']][1].values()))
            self.assertEqual('Updated' in channel.metadata[0], source['refresh_seconds'] == 30)
        # Manual-only can still refresh on demand.
        self.manager.refresh(sources[0]['id'])
        self.assertIn('Updated', next(iter(self.manager.cache[sources[0]['id']][1].values())).metadata[0])
        # It also gets one initial load after restarting, then stops polling.
        reloaded = SourceManager(self.temp.name)
        reloaded.refresh(due_only=True)
        self.assertIn(sources[0]['id'], reloaded.cache)

    def test_invalid_refresh_interval_does_not_update_source(self):
        source = self.add()['sources'][0]
        for interval in (-1, 1, True, '60', 2.5):
            with self.assertRaises(HTTPError) as context:
                self.api('/api/sources/edit', {'id': source['id'], 'name': 'Bad', 'refresh_seconds': interval})
            self.assertEqual(context.exception.code, 400)
            context.exception.close()
        self.assertEqual(self.manager.sources[0]['name'], source['name'])

    def test_ui_playlist_url_follows_request_address(self):
        server_base = self.base_url
        with urlopen(Request(server_base + '/api/sources', headers={'Host': '192.168.1.10:3333'})) as response:
            status = json.load(response)
        self.assertEqual(status['playlist_url'], 'http://192.168.1.10:3333/playlist.m3u')

    def test_new_source_defaults_to_no_selected_content(self):
        original = self.add('Existing')['sources'][0]
        status = self.add('New', select=False)
        added = status['sources'][-1]
        self.assertTrue(added['enabled'])
        self.assertEqual(added['channels'], 1)
        self.assertEqual(added['included'], 0)
        self.assertEqual(status['channels'], 1)
        content = self.api('/api/content?source=' + added['id'])
        self.assertFalse(content['rules']['default'])
        self.assertFalse(content['groups'][0]['channels'][0]['included'])
        reloaded = SourceManager(self.temp.name)
        reloaded.refresh()
        self.assertEqual(len(reloaded.get()[1]), 1)
        self.assertTrue(reloaded.filters[original['id']]['default'])
        self.assertFalse(reloaded.filters[added['id']]['default'])

    def test_manage_disabled_source_after_restart_without_exporting(self):
        playlist = PLAYLIST + '#EXTINF:-1 tvg-id="other",Other channel\nhttps://media.example/other.m3u8\n'
        source = self.add(content=playlist, select=False)['sources'][0]
        self.api('/api/sources/toggle', {'id': source['id']})
        # An actual restart has no cached metadata for disabled sources.
        reloaded = SourceManager(self.temp.name)
        reloaded.refresh(due_only=True)
        self.assertNotIn(source['id'], reloaded.cache)
        content = reloaded.content(source['id'])
        self.assertFalse(content['enabled'])
        self.assertEqual(sum(len(g['channels']) for g in content['groups']), 2)
        selected = content['groups'][0]['channels'][0]['id']
        reloaded.save_content({'id': source['id'], 'rules': {'default': False, 'groups': {}, 'channels': {selected: True}}})
        self.assertFalse(reloaded.sources[0]['enabled'])
        self.assertEqual(len(reloaded.get()[1]), 0)
        reloaded.action('/api/sources/toggle', {'id': source['id']})
        self.assertEqual(len(reloaded.get()[1]), 1)

    def test_disabled_source_api_lazily_loads_and_can_refresh(self):
        source = self.add(select=False)['sources'][0]
        self.api('/api/sources/toggle', {'id': source['id']})
        self.manager.cache.clear()
        self.manager.details.clear()
        content = self.api('/api/content?source=' + source['id'])
        self.assertEqual(len(content['groups']), 1)
        self.assertFalse(content['enabled'])
        Path(source['location']).write_text(PLAYLIST.replace('Test channel', 'Updated'))
        status = self.api('/api/sources/refresh', {'id': source['id']})
        self.assertFalse(status['sources'][0]['enabled'])
        self.assertEqual(status['channels'], 0)
        content = self.api('/api/content?source=' + source['id'])
        self.assertEqual(content['groups'][0]['channels'][0]['name'], 'Updated')

    def test_content_lists_skipped_entries(self):
        playlist = PLAYLIST + '#EXTINF:-1,Satellite\nrtsp://example/live?t=1\n'
        source = self.add(content=playlist)['sources'][0]
        self.assertEqual(source['skipped'], 1)
        self.assertNotIn('skipped_entries', source)
        content = self.api('/api/content?source=' + source['id'])
        self.assertEqual(content['skipped_total'], 1)
        self.assertEqual(content['skipped'][0]['name'], 'Satellite')
        self.assertEqual(content['skipped'][0]['url'], 'rtsp://example/live')

    def test_missing_or_empty_initial_source_creates_nothing(self):
        for index, initial in enumerate(('', None, '/does/not/exist.m3u8')):
            manager = SourceManager(Path(self.temp.name) / f'fresh-{index}', initial)
            self.assertEqual(manager.sources, [])

    def test_snapshot_is_reused_until_something_changes(self):
        source = self.add()['sources'][0]
        self.assertIs(self.manager.get()[1], self.manager.get()[1])
        before = self.manager.get()[1]
        self.api('/api/content/save', {'id': source['id'], 'rules': {'default': False, 'groups': {}, 'channels': {}}})
        self.assertIsNot(before, self.manager.get()[1])

    def test_delete_removes_saved_rules_and_cached_copy(self):
        source = self.add()['sources'][0]
        self.assertIn(source['id'], self.manager.filters)
        self.api('/api/sources/delete', {'id': source['id']})
        self.assertNotIn(source['id'], self.manager.filters)
        self.assertNotIn(source['id'], json.loads((Path(self.temp.name) / 'content.json').read_text()))

    def test_remote_copy_allows_warm_start_without_network(self):
        class Provider(BaseHTTPRequestHandler):
            def log_message(self, *args):
                pass

            def do_GET(self):
                self.send_response(200)
                self.end_headers()
                self.wfile.write(PLAYLIST.encode())

        server = ThreadingHTTPServer(('127.0.0.1', 0), Provider)
        threading.Thread(target=server.serve_forever, kwargs={'poll_interval': 0.05}, daemon=True).start()
        try:
            source = self.api('/api/sources/add', {'kind': 'url', 'name': 'Remote', 'url': f'http://127.0.0.1:{server.server_port}/p.m3u'})['sources'][0]
        finally:
            server.shutdown()
            server.server_close()
        self.api('/api/content/save', {'id': source['id'], 'rules': dict(default=True, groups={}, channels={})})
        restarted = SourceManager(self.temp.name)
        self.assertEqual(len(restarted.get()[1]), 1)

    def test_custom_content_has_its_own_endpoint(self):
        status = self.api('/api/sources/add', {'kind': 'custom', 'name': 'Mine', 'content': PLAYLIST})
        source = status['sources'][0]
        self.assertNotIn('custom_content', source)
        self.assertEqual(self.api('/api/custom?source=' + source['id'])['content'], PLAYLIST)
        upload = self.add('Upload')['sources'][-1]
        with self.assertRaises(HTTPError) as context:
            self.api('/api/custom?source=' + upload['id'])
        self.assertEqual(context.exception.code, 404)
        context.exception.close()

    def test_custom_source_edit_without_content_keeps_playlist(self):
        source = self.api('/api/sources/add', {'kind': 'custom', 'name': 'Mine', 'content': PLAYLIST})['sources'][0]
        self.api('/api/sources/edit', {'id': source['id'], 'name': 'Renamed', 'kind': 'custom'})
        self.assertEqual(self.manager.custom_content(source['id']), PLAYLIST)

    def test_corrupt_state_files_give_a_clear_error(self):
        (Path(self.temp.name) / 'sources.json').write_text('{broken')
        with self.assertRaisesRegex(RuntimeError, 'sources.json'):
            SourceManager(self.temp.name)

    def test_slow_source_does_not_block_other_sources(self):
        release = threading.Event()

        class Slow(BaseHTTPRequestHandler):
            def log_message(self, *args):
                pass

            def do_GET(self):
                release.wait(5)
                self.send_response(200)
                self.end_headers()
                self.wfile.write(PLAYLIST.encode())

        server = ThreadingHTTPServer(('127.0.0.1', 0), Slow)
        server.daemon_threads = True
        threading.Thread(target=server.serve_forever, kwargs={'poll_interval': 0.05}, daemon=True).start()
        fast = self.add('Fast')['sources'][0]
        self.manager.sources.append({'id': 'slow', 'name': 'Slow', 'kind': 'url', 'enabled': True,
                                     'location': f'http://127.0.0.1:{server.server_port}/p.m3u'})
        worker = threading.Thread(target=self.manager.refresh, args=('slow',))
        worker.start()
        try:
            started = time.monotonic()
            self.api('/api/sources/refresh', {'id': fast['id']})
            self.assertLess(time.monotonic() - started, 3)
        finally:
            release.set()
            worker.join(10)
            server.shutdown()
            server.server_close()

    def serve(self, **options):
        server = ThreadingHTTPServer(('127.0.0.1', 0), make_handler(self.proxy, self.manager, **options))
        threading.Thread(target=server.serve_forever, kwargs={'poll_interval': 0.05}, daemon=True).start()
        self.addCleanup(lambda: (server.shutdown(), server.server_close()))
        return f'http://127.0.0.1:{server.server_port}'

    # ---- optional password: sign-in page and session cookie ----
    def post_json(self, url, payload, headers=None):
        request = Request(url, data=json.dumps(payload).encode(), headers={'Content-Type': 'application/json', **(headers or {})})
        try:
            with urlopen(request) as response:
                return response.status, json.load(response), response.headers
        except HTTPError as error:
            body = json.load(error)
            error.close()
            return error.code, body, error.headers

    def sign_in(self, base, password='s3cret'):
        status, _, headers = self.post_json(base + '/api/login', {'password': password})
        self.assertEqual(status, 200)
        cookie = headers['Set-Cookie']
        self.assertIn('HttpOnly', cookie)
        self.assertIn('SameSite=Strict', cookie)
        return cookie.split(';')[0]

    def test_password_shows_sign_in_page_instead_of_the_manager(self):
        base = self.serve(password='s3cret')
        with urlopen(base + '/') as response:
            page = response.read().decode()
        self.assertIn('type="password"', page)
        self.assertNotIn('id="sources"', page)
        self.assertNotIn('WWW-Authenticate', str(response.headers))  # no browser pop-up
        with self.assertRaises(HTTPError) as context:
            urlopen(base + '/api/sources')
        self.assertEqual(context.exception.code, 401)
        self.assertNotIn('WWW-Authenticate', context.exception.headers)
        context.exception.close()
        with urlopen(base + '/playlist.m3u') as response:  # players never need the password
            self.assertEqual(response.status, 200)

    def test_sign_in_opens_the_manager_and_sign_out_closes_it(self):
        base = self.serve(password='s3cret')
        cookie = self.sign_in(base)
        with urlopen(Request(base + '/', headers={'Cookie': cookie})) as response:
            self.assertIn('id="sources"', response.read().decode())
        with urlopen(Request(base + '/api/sources', headers={'Cookie': cookie})) as response:
            data = json.load(response)
        self.assertTrue(data['password_protected'])
        # Management writes need both the session and the management token.
        status, body, _ = self.post_json(base + '/api/sources/refresh', {}, {'Cookie': cookie, 'X-Management-Token': data['token']})
        self.assertEqual(status, 200)
        status, _, _ = self.post_json(base + '/api/sources/refresh', {}, {'X-Management-Token': data['token']})
        self.assertEqual(status, 401)
        status, _, headers = self.post_json(base + '/api/logout', {}, {'Cookie': cookie})
        self.assertEqual(status, 200)
        self.assertIn('Max-Age=0', headers['Set-Cookie'])

    def test_wrong_password_and_forged_cookies_are_rejected(self):
        base = self.serve(password='s3cret')
        status, body, headers = self.post_json(base + '/api/login', {'password': 'wrong'})
        self.assertEqual(status, 401)
        self.assertEqual(body['error'], 'Wrong password.')
        self.assertNotIn('Set-Cookie', headers)
        for forged in ('iptv_session=garbage', 'iptv_session=' + 'a' * 43, 'iptv_session=', 'other=1'):
            with self.assertRaises(HTTPError) as context:
                urlopen(Request(base + '/api/sources', headers={'Cookie': forged}))
            self.assertEqual(context.exception.code, 401, forged)
            context.exception.close()

    def test_session_ids_are_random_and_reveal_nothing(self):
        base = self.serve(password='s3cret')
        first, second = self.sign_in(base), self.sign_in(base)
        self.assertNotEqual(first, second)
        for cookie in (first, second):
            value = cookie.split('=', 1)[1]
            self.assertGreaterEqual(len(value), 43)  # 256 random bits
            self.assertNotIn('s3cret', value)
            self.assertFalse(value.partition('.')[0].isdigit())  # no embedded expiry date

    def test_sign_out_ends_the_session_on_the_server(self):
        base = self.serve(password='s3cret')
        cookie = self.sign_in(base)
        with urlopen(Request(base + '/api/sources', headers={'Cookie': cookie})) as response:
            self.assertEqual(response.status, 200)
        self.post_json(base + '/api/logout', {}, {'Cookie': cookie})
        # A copied cookie must not work after signing out.
        with self.assertRaises(HTTPError) as context:
            urlopen(Request(base + '/api/sources', headers={'Cookie': cookie}))
        self.assertEqual(context.exception.code, 401)
        context.exception.close()

    def test_sessions_expire(self):
        base = self.serve(password='s3cret')
        cookie = self.sign_in(base)
        with patch('app.server.time.time', return_value=time.time() + 31 * 24 * 3600):
            with self.assertRaises(HTTPError) as context:
                urlopen(Request(base + '/api/sources', headers={'Cookie': cookie}))
            self.assertEqual(context.exception.code, 401)
            context.exception.close()

    def test_session_does_not_work_on_a_server_with_another_password(self):
        cookie = self.sign_in(self.serve(password='s3cret'))
        other = self.serve(password='new-password')
        with self.assertRaises(HTTPError) as context:
            urlopen(Request(other + '/api/sources', headers={'Cookie': cookie}))
        self.assertEqual(context.exception.code, 401)
        context.exception.close()

    def test_repeated_wrong_passwords_are_rate_limited(self):
        base = self.serve(password='s3cret')
        with patch('app.server.time.sleep'):
            codes = [self.post_json(base + '/api/login', {'password': f'guess{i}'})[0] for i in range(10)]
        self.assertEqual(codes[:8], [401] * 8)
        self.assertEqual(codes[8:], [429, 429])
        status, _, _ = self.post_json(base + '/api/login', {'password': 's3cret'})
        self.assertEqual(status, 429)  # even the right password waits out the window

    def test_cookie_is_secure_only_when_served_over_https(self):
        base = self.serve(password='s3cret')
        login = lambda extra: self.post_json(base + '/api/login', {'password': 's3cret'}, extra)[2]['Set-Cookie']
        plain = login({})
        self.assertNotIn('Secure', plain)  # plain HTTP must keep working
        behind_proxy = login({'X-Forwarded-Proto': 'https'})
        self.assertIn('; Secure', behind_proxy)
        self.assertIn('HttpOnly', behind_proxy)
        self.assertIn('SameSite=Strict', behind_proxy)
        self.assertIn('; Secure', login({'X-Forwarded-Proto': 'HTTPS, http'}))  # first value, any case
        for ignored in ('http', 'javascript', '1', ''):
            self.assertNotIn('Secure', login({'X-Forwarded-Proto': ignored}), ignored)
        cookie = behind_proxy.split(';')[0]
        status, _, headers = self.post_json(base + '/api/logout', {}, {'Cookie': cookie, 'X-Forwarded-Proto': 'https'})
        self.assertIn('Secure', headers['Set-Cookie'])
        self.assertNotIn('Secure', self.post_json(base + '/api/logout', {}, {'Cookie': cookie})[2]['Set-Cookie'])

    def test_session_cookie_works_over_both_schemes(self):
        base = self.serve(password='s3cret')
        cookie = self.post_json(base + '/api/login', {'password': 's3cret'}, {'X-Forwarded-Proto': 'https'})[2]['Set-Cookie'].split(';')[0]
        for headers in ({}, {'X-Forwarded-Proto': 'https'}):
            with urlopen(Request(base + '/api/sources', headers={'Cookie': cookie, **headers})) as response:
                self.assertEqual(response.status, 200)

    def test_links_use_https_behind_a_tls_proxy(self):
        with urlopen(Request(self.base_url + '/api/sources', headers={'Host': 'tv.example.com', 'X-Forwarded-Proto': 'https'})) as response:
            self.assertEqual(json.load(response)['playlist_url'], 'https://tv.example.com/playlist.m3u')
        with urlopen(Request(self.base_url + '/api/sources', headers={'Host': 'tv.example.com'})) as response:
            self.assertEqual(json.load(response)['playlist_url'], 'http://tv.example.com/playlist.m3u')
        with urlopen(Request(self.base_url + '/api/sources', headers={'Host': 'tv.example.com', 'X-Forwarded-Proto': 'ftp'})) as response:
            self.assertTrue(json.load(response)['playlist_url'].startswith('http://'))

    def test_without_a_password_nothing_asks_for_one(self):
        with urlopen(self.base_url + '/') as response:
            self.assertIn('id="sources"', response.read().decode())
        self.assertFalse(self.api()['password_protected'])

    def test_version_is_reported_for_the_footer(self):
        self.assertEqual(self.api()['version'], 'dev')
        base = self.serve(version='1.2.3')
        with urlopen(base + '/api/sources') as response:
            self.assertEqual(json.load(response)['version'], '1.2.3')
        request = Request(base + '/api/sources/refresh', data=b'{}', headers={'Content-Type': 'application/json', 'X-Management-Token': self.proxy.management_token})
        with urlopen(request) as response:
            self.assertEqual(json.load(response)['version'], '1.2.3')

    def test_management_token_is_per_process(self):
        other = Proxy(b'test-secret', self.manager)
        self.assertNotEqual(other.management_token, self.proxy.management_token)


if __name__ == '__main__':
    unittest.main()
