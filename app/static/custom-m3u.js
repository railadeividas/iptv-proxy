// Reading and writing custom playlists. Pure functions: no DOM access.
const KNOWN = {id: 'tvg-id', logo: 'tvg-logo', group: 'group-title'};
const PRESERVED_LINES = ['#EXTVLCOPT:', '#EXTHTTP:', '#EXTGRP:'];

export class ChannelError extends Error {
  constructor(index, message) { super(message); this.index = index; }
}

// Older versions escaped & and " as HTML entities; undo that so those values are repaired.
const decode = value => value.replace(/&quot;/g, '"').replace(/&amp;/g, '&');
const clean = value => String(value ?? '').replace(/[\r\n]+/g, ' ').trim();

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
    const extra = [];
    let url = '', options = '';
    while (++i < lines.length) {
      const candidate = lines[i].trim();
      if (!candidate) continue;
      if (candidate.startsWith('#EXTINF:')) { i--; break; }
      if (candidate.startsWith('#')) {
        // Keep per-channel headers and groups so editing never drops them.
        if (PRESERVED_LINES.some(prefix => candidate.startsWith(prefix))) extra.push(candidate);
        continue;
      }
      const bar = candidate.indexOf('|');
      url = bar < 0 ? candidate : candidate.slice(0, bar);
      options = bar < 0 ? '' : candidate.slice(bar + 1);
      break;
    }
    if (!url) continue;
    const channel = {name, url, extra, options, attrs: {...attrs}};
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
    lines.push(`#EXTINF:-1${attributes},${name}`, ...(channel.extra ?? []));
    lines.push(clean(channel.url) + (channel.options ? '|' + channel.options : ''));
  });
  return lines.join('\n') + '\n';
}
