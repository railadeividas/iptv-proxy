import assert from 'node:assert/strict';
import test from 'node:test';
import {ChannelError, duplicateWarnings, parseCustom, serializeCustom} from '../app/static/custom-m3u.js';

const SAMPLE = '#EXTM3U\n#EXTINF:-1 tvg-id="a" tvg-logo="https://x/l.png?a=1&b=2" group-title="News, World" tvg-chno="7",Channel, One\n'
  + '#EXTVLCOPT:http-referrer=https://p/\nhttps://x/live.m3u8?t=1&u=2|User-Agent=Test\n';

test('round trip keeps ampersands, headers, options and unknown attributes', () => {
  const channels = parseCustom(SAMPLE);
  assert.equal(channels.length, 1);
  assert.equal(channels[0].name, 'Channel, One');
  assert.equal(channels[0].logo, 'https://x/l.png?a=1&b=2');
  assert.equal(channels[0].group, 'News, World');
  const output = serializeCustom(channels);
  assert.ok(output.includes('tvg-logo="https://x/l.png?a=1&b=2"'));
  assert.ok(output.includes('tvg-chno="7"'));
  assert.ok(output.includes('#EXTVLCOPT:http-referrer=https://p/'));
  assert.ok(output.includes('https://x/live.m3u8?t=1&u=2|User-Agent=Test'));
  assert.equal(serializeCustom(parseCustom(output)), output);
});

test('repeated saves are stable and repair entities written by older versions', () => {
  const legacy = '#EXTM3U\n#EXTINF:-1 tvg-logo="https://x/l.png?a=1&amp;b=2",Name\nhttps://x/a.m3u8\n';
  const once = serializeCustom(parseCustom(legacy));
  assert.ok(once.includes('a=1&b=2'));
  assert.equal(serializeCustom(parseCustom(once)), once);
});

test('quotes cannot break attributes', () => {
  const output = serializeCustom([{name: 'Q', url: 'https://x/a', group: 'A "B"', attrs: {}, extra: []}]);
  assert.ok(output.includes(`group-title="A 'B'"`));
});

test('validation reports the failing channel', () => {
  assert.throws(() => serializeCustom([{name: 'Ok', url: 'https://x/a'}, {name: 'Bad', url: 'rtsp://x'}]),
    error => error instanceof ChannelError && error.index === 1);
  assert.throws(() => serializeCustom([{name: '', url: 'https://x/a'}]), ChannelError);
});

test('duplicate stream URLs are flagged', () => {
  const warnings = duplicateWarnings([{url: 'https://x/a'}, {url: 'https://x/b'}, {url: 'https://x/a'}]);
  assert.deepEqual([...warnings.keys()], [2]);
});

test('headers from M3U lines become editable text and are written back', () => {
  const text = '#EXTM3U\n#EXTINF:-1,A\n#EXTVLCOPT:http-referrer=https://p/\n#EXTHTTP:{"User-Agent":"UA","Cookie":"c=1"}\n#EXTGRP:News\nhttps://x/a.m3u8\n';
  const [channel] = parseCustom(text);
  assert.equal(channel.headers, 'Referer: https://p/\nUser-Agent: UA\nCookie: c=1');
  assert.deepEqual(channel.extra, ['#EXTGRP:News']);
  const output = serializeCustom([channel]);
  assert.ok(output.includes('#EXTVLCOPT:http-referrer=https://p/\n#EXTVLCOPT:http-user-agent=UA\n#EXTVLCOPT:http-cookie=c=1\n#EXTGRP:News\nhttps://x/a.m3u8'));
  assert.equal(serializeCustom(parseCustom(output)), output);
});

test('headers typed in the editor are written and validated', () => {
  const output = serializeCustom([{name: 'A', url: 'https://x/a', headers: 'referer: https://p/\n\nUSER-AGENT: Mozilla/5.0 (X11)', attrs: {}, extra: []}]);
  assert.ok(output.includes('#EXTVLCOPT:http-referrer=https://p/\n#EXTVLCOPT:http-user-agent=Mozilla/5.0 (X11)'));
  for (const headers of ['X-Custom: 1', 'Referer', 'Referer:']) {
    assert.throws(() => serializeCustom([{name: 'A', url: 'https://x/a', headers}]),
      error => error instanceof ChannelError && error.field === 'headers');
  }
});

test('unrecognized #EXTHTTP content is kept as it was', () => {
  const [channel] = parseCustom('#EXTM3U\n#EXTINF:-1,A\n#EXTHTTP:{"X-Token":"1"}\nhttps://x/a\n');
  assert.equal(channel.headers, '');
  assert.ok(serializeCustom([channel]).includes('#EXTHTTP:{"X-Token":"1"}'));
});
