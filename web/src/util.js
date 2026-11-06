import { onUnmounted } from 'vue'
import { on } from './ws'
import { store } from './store'

// The status of a channel from the counts the server sends (streams excluded from health checks are not in them,
// unless every stream is excluded; see internal/store/status.go): working = a working stream and none failing,
// partly working = working and failing streams, not working = every stream fails, pending = no verdict yet.
export function streamState(c) {
  if (c.working > 0) return c.dead > 0 ? 'partly working' : 'working'
  if (c.streams > 0 && c.dead >= c.streams) return 'not working'
  return 'pending'
}
// colour of a status: ok, warn, bad (or '' for no verdict)
export const stateTone = (c) => ({ working: 'ok', 'partly working': 'warn', 'not working': 'bad', pending: '' })[streamState(c)]
// Will the health checker ever look at this channel on its own? Depends on "Streams to check" in Settings.
// Rows without a `profiles` list (channels of a profile) are in a profile by definition.
function autoChecked(c) {
  const scope = store.status?.health?.scope
  if (scope === 'all') return true
  if (scope === 'selected') return c.profiles ? c.profiles.length > 0 : true
  return scope == null // unknown yet: assume it will be
}
export const stateLabel = (c) => {
  const s = streamState(c)
  return s !== 'pending' ? s : autoChecked(c) ? 'check pending' : 'not checked'
}
export const stateClass = (c) => ({ ok: 'good', warn: 'warn', bad: 'bad', '': '' })[stateTone(c)]
export const stateTitle = (c) => {
  const n = c.streams - c.working - c.dead
  const excl = !c.excluded ? '' : c.excluded >= (c.total_streams || 0)
    ? ` (all ${c.excluded} excluded from checks: their last results count)`
    : `, plus ${c.excluded} excluded from checks (not counted)`
  const base = `${c.working} working, ${c.dead} not working, ${n} not checked yet${excl}`
  return streamState(c) === 'pending' && !autoChecked(c) ? `${base}. Not in the automatic checks: see “Streams to check” in Settings > Health` : base
}

export function debounced(fn, ms = 300) {
  let t
  const d = (...a) => { clearTimeout(t); t = setTimeout(() => fn(...a), ms) }
  d.cancel = () => clearTimeout(t)
  return d
}

// Subscribe to WebSocket events for the lifetime of a component.
export function useLive(map) {
  const offs = Object.entries(map).map(([type, fn]) => on(type, fn))
  onUnmounted(() => offs.forEach((off) => off()))
}

// "HTTP 404" -> "404", "HTTP 200 · HLS" -> "200"; failures that never got an answer (timeout, DNS, …) have no status code
export const statusCode = (reason) => /^HTTP (\d{3})(?: · |$)/.exec(reason || '')?.[1] || ''
// the format a working stream serves: "HTTP 200 · HLS" -> "HLS"
export const streamFormat = (reason) => /^HTTP \d{3} · (.+)$/.exec(reason || '')?.[1] || ''

// Page sizes of every list (the backend caps a page at 500), and the size a list opens with.
export const PAGE_SIZES = [5, 10, 25, 50, 100, 200, 500]
export const PAGE_SIZE = 10

export const ago = (t) => (t ? new Date(t * 1000).toLocaleString() : 'never')
// Runs fn at most once per `ms`, on the trailing edge: for events that arrive in bursts (probe results).
export function throttle(fn, ms = 1500) {
  let timer = null
  return () => { if (!timer) timer = setTimeout(() => { timer = null; fn() }, ms) }
}
// "5 min ago", "3 h ago" … for a unix time; "never" when unset
export function rel(t) {
  if (!t) return 'never'
  const d = Math.max(0, Date.now() / 1000 - t)
  if (d < 45) return 'just now'
  if (d < 3600) return Math.round(d / 60) + ' min ago'
  if (d < 86400) return Math.round(d / 3600) + ' h ago'
  return Math.round(d / 86400) + ' d ago'
}
// 134 -> "2 min 14 s"
export function dur(sec) {
  sec = Math.round(sec)
  if (sec < 60) return sec + ' s'
  const m = Math.floor(sec / 60), s = sec % 60
  if (m < 60) return m + ' min' + (s ? ' ' + s + ' s' : '')
  return Math.floor(m / 60) + ' h ' + (m % 60) + ' min'
}
// 1536000 -> "1.5 MB"
export function bytes(n) {
  if (!n) return '0 B'
  const u = ['B', 'KB', 'MB', 'GB', 'TB']
  const i = Math.min(u.length - 1, Math.floor(Math.log(n) / Math.log(1024)))
  const v = n / 1024 ** i
  return (i === 0 || v >= 100 ? Math.round(v) : v.toFixed(1)) + ' ' + u[i]
}
export const copy = async (text) => navigator.clipboard.writeText(text)

// one line on what a failure reason usually means
export function reasonHint(reason) {
  // a master playlist works but its first variant fails: explain the variant's reason
  if (reason?.startsWith('Variant playlist: ')) {
    const inner = reasonHint(reason.slice('Variant playlist: '.length))
    return 'The stream list opens, but its first quality level fails' + (inner ? ': ' + inner.charAt(0).toLowerCase() + inner.slice(1) : '.')
  }
  const code = /^HTTP (\d{3})$/.exec(reason || '')?.[1]
  if (code) {
    const n = +code
    if (n === 401 || n === 403) return 'The server refuses this request (blocked, or it needs a login). Another stream may work.'
    if (n === 404 || n === 410) return 'The stream address no longer exists.'
    if (n === 429) return 'The server is limiting how often it can be asked.'
    if (n >= 500) return 'The stream server has a problem.'
    return 'The server answered with an error.'
  }
  return ({
    'Timed out': 'The server is too slow or cannot be reached.',
    'DNS lookup failed': 'The server name does not exist (any more).',
    'Connection refused': 'Nothing is listening at that address.',
    'TLS or certificate error': 'The security certificate is not valid.',
    'Host unreachable': 'The server cannot be reached from here.',
    'Returned a web page': 'The address opens a web page, not a stream.',
    'Not a valid playlist': 'The server answered with something that is not a stream playlist.',
    'Not a valid manifest': 'The server answered with something that is not a stream manifest.',
    'Empty response': 'The server answered but sent no data.',
    'Playlist has no segments': 'The playlist opens but lists no video: the channel is probably off air.',
    'Playlist has no streams': 'The stream list opens but names no quality levels.',
    'Manifest has no periods': 'The manifest opens but describes no video.',
    'Returned text instead of video': 'The server answered with a message instead of video (often an error or an expired link).',
    'Connection dropped': 'The connection was cut off.',
  })[reason] || ''
}
