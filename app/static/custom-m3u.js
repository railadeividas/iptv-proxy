// Reading and writing custom playlists. Pure functions: no DOM access.
const KNOWN = {id: 'tvg-id', logo: 'tvg-logo', group: 'group-title'};
const PRESERVED_LINES = ['#EXTVLCOPT:', '#EXTHTTP:', '#EXTGRP:'];
// Request headers the proxy understands. Keys of PLAYLIST_KEYS mirror the names its parser accepts.
const HEADER_NAMES = {referer: 'Referer', referrer: 'Referer', 'user-agent': 'User-Agent', origin: 'Origin',
  cookie: 'Cookie', authorization: 'Authorization', accept: 'Accept'};
const PLAYLIST_KEYS = {'http-referrer': 'Referer', 'http-referer': 'Referer', 'http-user-agent': 'User-Agent',
  'http-origin': 'Origin', 'http-cookie': 'Cookie', 'http-authorization': 'Authorization', ...HEADER_NAMES};
const VLC_KEYS = {Referer: 'http-referrer', 'User-Agent': 'http-user-agent', Origin: 'http-origin',
  Cookie: 'http-cookie', Authorization: 'http-authorization', Accept: 'accept'};
export const SUPPORTED_HEADERS = Object.keys(VLC_KEYS);

export class ChannelError extends Error {
  constructor(index, message, field) { super(message); this.index = index; this.field = field; }
}

// Older versions escaped & and " as HTML entities; undo that so those values are repaired.
const decode = value => value.replace(/&quot;/g, '"').replace(/&amp;/g, '&');
const clean = value => String(value ?? '').replace(/[\r\n]+/g, ' ').trim();

// Move a header line into the editable header list. Returns false to keep the line untouched.
function absorbHeaders(line, headers) {
  if (line.startsWith('#EXTVLCOPT:')) {
    const text = line.slice('#EXTVLCOPT:'.length), equals = text.indexOf('=');
    const name = PLAYLIST_KEYS[text.slice(0, equals).trim().toLowerCase()];
    if (equals < 0 || !name) return false;
    headers.set(name, text.slice(equals + 1).trim().replace(/^"|"$/g, ''));
    return true;
  }
  if (line.startsWith('#EXTHTTP:')) {
    let data;
    try { data = JSON.parse(line.slice('#EXTHTTP:'.length)); } catch { return false; }
    if (!data || typeof data !== 'object' || Array.isArray(data)) return false;
    const entries = Object.entries(data);
    if (!entries.length || entries.some(([key, value]) => !PLAYLIST_KEYS[key.trim().toLowerCase()] || typeof value !== 'string')) return false;
    for (const [key, value] of entries) headers.set(PLAYLIST_KEYS[key.trim().toLowerCase()], value.trim().replace(/^"|"$/g, ''));
    return true;
  }
  return false;
}

// "Name: value" lines to #EXTVLCOPT lines; unknown names are rejected so typos never silently drop.
function headerLines(channel, index) {
  const headers = new Map();
  for (const raw of String(channel.headers ?? '').split(/\r?\n/)) {
    const line = raw.trim();
    if (!line) continue;
    const colon = line.indexOf(':');
    const name = HEADER_NAMES[(colon < 0 ? line : line.slice(0, colon)).trim().toLowerCase()];
    const value = colon < 0 ? '' : line.slice(colon + 1).trim();
    if (!name) throw new ChannelError(index, `Unsupported header “${line.slice(0, 40)}” for “${clean(channel.name)}”. Supported: ${SUPPORTED_HEADERS.join(', ')}.`, 'headers');
    if (!value) throw new ChannelError(index, `Enter a value for the ${name} header of “${clean(channel.name)}”.`, 'headers');
    headers.set(name, value);
  }
  return [...headers].map(([name, value]) => `#EXTVLCOPT:${VLC_KEYS[name]}=${value}`);
}

export function parseCustom(content = '') {
  const lines = content.split(/\r?\n/);
  const channels = [];
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i].trim();
    if (!line.startsWith('#EXTINF:')) continue;
    // Quoted attributes can contain commas; mask them before looking for the name.
    const masked = line.replace(/"[^"]*"/g, match => '_'.repeat(match.length));
    const comma = masked.indexOf(',');
    const name = comma < 0 ? '' : line.slice(comma + 1).trim();
    const attrs = Object.fromEntries([...line.matchAll(/([\w-]+)="([^"]*)"/g)].map(m => [m[1], decode(m[2])]));
    const extra = [], headers = new Map();
    let url = '', options = '';
    while (++i < lines.length) {
      const candidate = lines[i].trim();
      if (!candidate) continue;
      if (candidate.startsWith('#EXTINF:')) { i--; break; }
      if (candidate.startsWith('#')) {
        // Keep per-channel headers and groups so editing never drops them.
        if (PRESERVED_LINES.some(prefix => candidate.startsWith(prefix)) && !absorbHeaders(candidate, headers)) extra.push(candidate);
        continue;
      }
      const bar = candidate.indexOf('|');
      url = bar < 0 ? candidate : candidate.slice(0, bar);
      options = bar < 0 ? '' : candidate.slice(bar + 1);
      break;
    }
    if (!url) continue;
    const channel = {name, url, extra, options, attrs: {...attrs}, headers: [...headers].map(([key, value]) => `${key}: ${value}`).join('\n')};
    for (const [field, attribute] of Object.entries(KNOWN)) {
      channel[field] = attrs[attribute] || '';
      delete channel.attrs[attribute];
    }
    channels.push(channel);
  }
  return channels;
}

export function validateChannel(channel) {
  const name = clean(channel.name), url = clean(channel.url);
  if (!name) return 'Enter a channel name.';
  if (!url) return `Enter a stream URL for “${name}”.`;
  let parsed;
  try { parsed = new URL(url); } catch { return `“${name}” has an invalid stream URL.`; }
  if (!['http:', 'https:'].includes(parsed.protocol)) return `Enter an HTTP(S) stream URL for “${name}”.`;
  return '';
}

export function duplicateWarnings(channels) {
  const seen = new Map(), warnings = new Map();
  channels.forEach((channel, index) => {
    const url = clean(channel.url);
    if (!url) return;
    if (seen.has(url)) warnings.set(index, `Same stream URL as channel ${seen.get(url) + 1}.`);
    else seen.set(url, index);
  });
  return warnings;
}

export function serializeCustom(channels) {
  const lines = ['#EXTM3U'];
  channels.forEach((channel, index) => {
    const problem = validateChannel(channel);
    if (problem) throw new ChannelError(index, problem);
    const name = clean(channel.name);
    const other = Object.entries(channel.attrs ?? {}).filter(([key]) => key !== 'tvg-name');
    // M3U attributes have no escape syntax, so a quote cannot be stored; use an apostrophe.
    const attributes = [
      ['tvg-id', channel.id], ['tvg-name', channel.attrs?.['tvg-name'] ?? name], ['tvg-logo', channel.logo],
      ['group-title', channel.group], ...other,
    ].map(([key, value]) => [key, clean(value).replace(/"/g, "'")]).filter(([, value]) => value)
      .map(([key, value]) => ` ${key}="${value}"`).join('');
    lines.push(`#EXTINF:-1${attributes},${name}`, ...headerLines(channel, index), ...(channel.extra ?? []));
    lines.push(clean(channel.url) + (channel.options ? '|' + channel.options : ''));
  });
  return lines.join('\n') + '\n';
}
