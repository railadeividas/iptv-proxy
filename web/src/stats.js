// Shared pieces of the Stats page: the URL state, the measures, the colours of series and the data shaping for charts.
import { computed, ref, watch, onMounted, onUnmounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { bytes, dur, throttle, useLive, PAGE_SIZES, PAGE_SIZE } from './util'
import { toast } from './store'

export const TABS = [
  ['live', 'Live'], ['overview', 'Overview'], ['channels', 'Channels'],
  ['profiles', 'Profiles'], ['devices', 'Devices'], ['sessions', 'Sessions'], ['proxy', 'Proxy'],
]
export const DEFAULT_TAB = 'live'

// ---- time range (like Grafana): a preset that moves with the clock, or an absolute from/to ----
export const RANGES = [
  ['15m', 'Last 15 minutes'], ['1h', 'Last hour'], ['3h', 'Last 3 hours'], ['6h', 'Last 6 hours'], ['12h', 'Last 12 hours'], ['24h', 'Last 24 hours'],
  ['2d', 'Last 2 days'], ['7d', 'Last 7 days'], ['30d', 'Last 30 days'], ['90d', 'Last 90 days'],
  ['today', 'Today so far'], ['yesterday', 'Yesterday'], ['week', 'This week so far'], ['month', 'This month so far'], ['all', 'Everything stored'],
]
export const DEFAULT_RANGE = '7d'
const REL = { '15m': 900, '1h': 3600, '3h': 10800, '6h': 21600, '12h': 43200, '24h': 86400, '2d': 172800, '7d': 604800, '30d': 2592000, '90d': 7776000 }
export const history = ref(30) // days the server keeps (set by the page): "Everything stored"
const unix = (d) => Math.floor(d.getTime() / 1000)
export function resolveRange(key, now = Math.floor(Date.now() / 1000)) {
  if (REL[key]) return { from: now - REL[key], to: now }
  const d = new Date(now * 1000)
  const midnight = new Date(d.getFullYear(), d.getMonth(), d.getDate())
  switch (key) {
    case 'today': return { from: unix(midnight), to: now }
    case 'yesterday': return { from: unix(new Date(d.getFullYear(), d.getMonth(), d.getDate() - 1)), to: unix(midnight) }
    case 'week': return { from: unix(new Date(d.getFullYear(), d.getMonth(), d.getDate() - ((d.getDay() + 6) % 7))), to: now } // weeks start on Monday
    case 'month': return { from: unix(new Date(d.getFullYear(), d.getMonth(), 1)), to: now }
    default: return { from: now - history.value * 86400, to: now } // all
  }
}
// How finely the charts are cut. The server raises a step that would give too many bars and picks one itself on "auto".
export const STEPS = { '1h': 3600, '1d': 86400, '1w': 604800 }
export const STEP_OPTIONS = [['', 'Auto'], ['1h', 'Hour'], ['1d', 'Day'], ['1w', 'Week']]
// Auto: the finest step that gives at most about 120 bars (never finer than an hour: that is how finely most of the data is kept)
export const autoStep = (span) => [3600, 86400].find((s) => span / s <= 120) || 604800
export const MAX_BARS = 500
export const bucketWord = (b) => ({ 300: '5 minutes', 900: '15 minutes', 3600: 'hour', 86400: 'day', 604800: 'week' })[b] || `${Math.round(b / 60)} minutes`
const stampFmt = new Intl.DateTimeFormat(undefined, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit', hour12: false })
export const fmtStamp = (t) => stampFmt.format(new Date(t * 1000))
const count = (n) => Math.round(n).toLocaleString()

// What the charts measure. `fmt` writes a value for people; `scale(max)` picks the unit the axis counts in, so the grid lines are round numbers.
export const METRICS = {
  sessions: { label: 'Sessions', unit: 'sessions', fmt: count, scale: () => ({ div: 1, suffix: '' }), pick: (r) => r.sessions },
  bytes: {
    label: 'Data served', unit: 'data', fmt: bytes, pick: (r) => r.bytes,
    scale: (max) => { const i = Math.min(4, Math.max(0, Math.floor(Math.log(Math.max(max, 1)) / Math.log(1024)))); return { div: 1024 ** i, suffix: ' ' + ['B', 'KB', 'MB', 'GB', 'TB'][i] } },
  },
  seconds: {
    label: 'Watch time', unit: 'watch time', fmt: dur, pick: (r) => r.seconds,
    scale: (max) => (max < 7200 ? { div: 60, suffix: ' min' } : max < 172800 ? { div: 3600, suffix: ' h' } : { div: 86400, suffix: ' d' }),
  },
}
// the axis writer for a measure and the biggest value on it
export function axisOf(metric, max) {
  const s = METRICS[metric].scale(max)
  return { scale: s.div, axis: (v) => `${+(v / s.div).toFixed(1)}${s.suffix}` }
}

export const tz = () => -new Date().getTimezoneOffset() * 60 // seconds east of UTC: the server cuts days where the viewer's days are

// The state of the page lives in the URL. Defaults stay out of it.
export function useStatsQuery() {
  const route = useRoute()
  const router = useRouter()
  const pick = (k, allowed, def) => (allowed.includes(route.query[k]) ? route.query[k] : def)
  const absFrom = computed(() => parseInt(route.query.from) || 0)
  const absTo = computed(() => parseInt(route.query.to) || 0)
  // a string that changes when the range does, to watch (the range itself moves with the clock)
  const rangeKey = computed(() => (absFrom.value ? `${absFrom.value}-${absTo.value}` : RANGES.some((r) => r[0] === route.query.range) ? route.query.range : DEFAULT_RANGE))
  const range = () => (absFrom.value ? { from: absFrom.value, to: absTo.value || Math.floor(Date.now() / 1000) } : resolveRange(rangeKey.value))
  const rangeLabel = computed(() => {
    if (!absFrom.value) return RANGES.find((r) => r[0] === rangeKey.value)[1]
    return `${fmtStamp(absFrom.value)} – ${absTo.value ? fmtStamp(absTo.value) : 'now'}`
  })
  // How much of the period before is still stored: 'full', 'partial' (it reaches past the oldest stored day) or 'none'
  const coverage = computed(() => {
    if (rangeKey.value === 'all') return 'full'
    const r = range(), span = r.to - r.from
    const oldest = Math.floor(Date.now() / 1000) - history.value * 86400
    return r.from <= oldest ? 'none' : r.from - span < oldest ? 'partial' : 'full'
  })
  const canCompare = computed(() => rangeKey.value !== 'all' && coverage.value !== 'none')
  const compareBlocked = computed(() => rangeKey.value !== 'all' && coverage.value === 'none') // nothing to compare with: not kept that far back
  const stepKey = computed(() => (STEPS[route.query.step] ? route.query.step : ''))
  // one or several profiles, ids separated by commas in the URL; none = all of them
  const selected = computed(() => [...new Set(String(route.query.profile || '').split(',').map((n) => parseInt(n)).filter((n) => n > 0))])
  const profile = computed(() => selected.value.join(','))
  const metric = computed(() => pick('metric', Object.keys(METRICS), 'sessions'))
  const split = computed(() => pick('split', ['none', 'profile', 'mode'], 'profile'))
  const type = computed(() => pick('type', ['bars', 'area', 'line'], 'bars'))
  const compare = computed(() => route.query.compare === '1' && canCompare.value)
  const tab = computed(() => route.params.tab || DEFAULT_TAB)
  function setQuery(patch, replace = false) {
    const query = { ...route.query, ...patch }
    for (const k of Object.keys(query)) if (query[k] === '' || query[k] == null) delete query[k]
    router[replace ? 'replace' : 'push']({ query })
  }
  // /api/stats* parameters for the period and profile of the page; shift = the period before
  function params(extra = {}, shift = false) {
    const r = range()
    const p = new URLSearchParams({ from: r.from, to: r.to, tz: tz(), ...extra })
    if (!extra.step) p.set('step', stepKey.value ? STEPS[stepKey.value] : autoStep(r.to - r.from))
    if (profile.value) p.set('profile', profile.value)
    if (shift) p.set('shift', 1)
    return p
  }
  return { rangeKey, range, rangeLabel, canCompare, compareBlocked, coverage, stepKey, profile, selected, metric, split, type, compare, tab, setQuery, params, route }
}

// ---- series ----
export const OTHER = 'other'
// A profile keeps its colour wherever it is drawn: the first five by id get a hue, the rest are folded into "other".
export function profileKey(id, profiles) {
  const ids = [...profiles].map((p) => p.profile_id).sort((a, b) => a - b)
  const i = ids.indexOf(+id)
  return i >= 0 && i < 5 ? String(id) : OTHER
}
export function profileColor(id, profiles) {
  const ids = [...profiles].map((p) => p.profile_id).sort((a, b) => a - b)
  const i = ids.indexOf(+id)
  return i >= 0 && i < 5 ? `var(--s${i + 1})` : 'var(--so)'
}
export function seriesFor(split, profiles, metric, only = []) {
  if (split === 'mode') {
    // a redirect has no data and no watching time: only sessions have something to show for it
    return { keys: metric === 'sessions' ? ['proxy', 'redirect'] : ['proxy'], names: { proxy: 'Through the proxy', redirect: 'Redirected' }, colors: { proxy: 'var(--s1)', redirect: 'var(--s2)' }, keyOf: (r) => r.mode }
  }
  if (split === 'profile' && profiles.length) {
    const ids = [...profiles].map((p) => p.profile_id).sort((a, b) => a - b)
    const keys = ids.slice(0, 5).map(String)
    const names = Object.fromEntries(profiles.map((p) => [String(p.profile_id), p.profile]))
    const colors = Object.fromEntries(keys.map((k, i) => [k, `var(--s${i + 1})`]))
    if (ids.length > 5) { keys.push(OTHER); names[OTHER] = 'Other profiles'; colors[OTHER] = 'var(--so)' }
    // a selection of profiles draws only those (their colours stay the ones they always have)
    const shown = only.length ? keys.filter((k) => (k === OTHER ? only.some((id) => profileKey(id, profiles) === OTHER) : only.includes(+k))) : keys
    return { keys: shown, names, colors, keyOf: (r) => profileKey(r.p, profiles) }
  }
  return { keys: ['all'], names: { all: METRICS[metric].label }, colors: { all: 'var(--s1)' }, keyOf: () => 'all' }
}

// ---- time buckets ----
const dayFmt = new Intl.DateTimeFormat(undefined, { month: 'short', day: 'numeric' })
const dowFmt = new Intl.DateTimeFormat(undefined, { weekday: 'short', month: 'short', day: 'numeric' })
const clockFmt = (d) => `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`
export function bucketTime(t, i) { return t.start + i * t.bucket }
export function bucketLabel(t, i) {
  const d = new Date(bucketTime(t, i) * 1000)
  if (t.bucket >= 86400) return dayFmt.format(d)
  const time = clockFmt(d)
  return t.n * t.bucket > 129600 ? `${dayFmt.format(d)} ${time}` : time // more than a day and a bit: say which day
}
export function bucketTitle(t, i) {
  const d = new Date(bucketTime(t, i) * 1000)
  if (t.bucket >= 604800) return `Week of ${dowFmt.format(d)}`
  if (t.bucket >= 86400) return dowFmt.format(d)
  return `${dowFmt.format(d)}, ${clockFmt(d)} – ${clockFmt(new Date((bucketTime(t, i) + t.bucket) * 1000))}`
}

// one value per bucket from sparse rows [{i, p, mode, sessions, bytes, seconds}]
export function perBucket(t, metric, rows = t.rows) {
  const out = Array(t.n).fill(0)
  for (const r of rows) out[r.i] += METRICS[metric].pick(r)
  return out
}
// rows for StatChart: [{l, tt, v: {seriesKey: value}}]
export function chartRows(t, metric, series) {
  const out = Array.from({ length: t.n }, (_, i) => ({ l: bucketLabel(t, i), tt: bucketTitle(t, i), v: Object.fromEntries(series.keys.map((k) => [k, 0])) }))
  // A row for a series the chart does not know yet (a profile created a moment ago, before the page learned of it) must not
  // be lost: it is counted under its own key, which completeSeries then adds to the chart.
  const extra = new Set()
  for (const r of t.rows) {
    const k = series.keyOf(r)
    if (!(k in out[r.i].v)) { extra.add(k); for (const o of out) o.v[k] = 0 }
    out[r.i].v[k] += METRICS[metric].pick(r)
  }
  return out
}
// the series plus any key that only showed up in the data (shown as "Other")
export function completeSeries(series, rows) {
  const extra = [...new Set(rows.flatMap((r) => Object.keys(r.v)))].filter((k) => !series.keys.includes(k))
  if (!extra.length) return series
  return {
    ...series,
    keys: [...series.keys, ...extra],
    names: { ...series.names, ...Object.fromEntries(extra.map((k) => [k, k === OTHER ? 'Other profiles' : k])) },
    colors: { ...series.colors, ...Object.fromEntries(extra.map((k) => [k, 'var(--so)'])) },
  }
}
// a line of plain numbers per bucket as chart rows
export function numberRows(t, values, key = 'n') {
  return values.map((v, i) => ({ l: bucketLabel(t, i), tt: bucketTitle(t, i), v: { [key]: v } }))
}

// change against the previous period, "▲ 12%" / "▼ 3%"; '' when not comparing
export function change(cur, prev, on) {
  if (!on) return ''
  if (!prev) return cur ? 'new' : ''
  const p = ((cur - prev) / prev) * 100
  return Math.abs(p) < 0.5 ? 'no change' : `${p > 0 ? '▲' : '▼'} ${Math.abs(p).toFixed(0)}%`
}
export const pct = (v) => Math.round(v) + '%'

export const REFRESH_MS = 10000 // how often a tab reloads by itself

// Loads data for a tab: now, when the watched values change, when the server says activity happened (at most every few
// seconds) and optionally on a timer. fn(isCurrent) fetches and assigns; a slower, older answer must not overwrite a newer one,
// so check isCurrent() before assigning. Old data stays on screen while the new arrives.
export function useLoader(fn, { deps = null, every = REFRESH_MS } = {}) {
  const loaded = ref(false)
  let seq = 0
  async function run() {
    const mine = ++seq
    try { await fn(() => mine === seq) } catch (e) { if (e.message !== 'sign in required' && e.message !== 'profile not found') toast(e.message) } // a deleted profile in the URL is dropped by the page itself
    if (mine === seq) loaded.value = true
  }
  if (deps) watch(deps, run)
  let timer
  // A running session grows without any event, and a "last 24 hours" window moves with the clock, so every tab also reloads on a
  // timer (not while the browser tab is hidden, and at once when it comes back).
  const tick = () => { if (!document.hidden) run() }
  onMounted(() => { run(); if (every) timer = setInterval(tick, every); document.addEventListener('visibilitychange', tick) })
  onUnmounted(() => { clearInterval(timer); document.removeEventListener('visibilitychange', tick) })
  const soon = throttle(run, 3000)
  useLive({ activity: soon, profiles: soon })
  return { loaded, reload: run }
}

// Page and page size of a list live in the URL (and the size in this browser), like everywhere else.
export function usePaging(key = 'statsSize') {
  const { route, setQuery } = useStatsQuery()
  const saved = (() => { try { return +localStorage.getItem(key) } catch { return 0 } })()
  const size = computed(() => { const n = parseInt(route.query.size) || saved; return PAGE_SIZES.includes(n) ? n : PAGE_SIZE })
  const page = computed(() => Math.max(0, (parseInt(route.query.page) || 1) - 1))
  const goPage = (n) => setQuery({ page: n ? n + 1 : '' })
  function setSize(n) {
    try { localStorage.setItem(key, String(n)) } catch {}
    setQuery({ size: n === PAGE_SIZE ? '' : n, page: '' })
  }
  return { size, page, goPage, setSize, SIZES: PAGE_SIZES }
}

// the biggest value a chart will show, to choose the axis unit
export function maxOf(rows, keys, stacked, extra = []) {
  return Math.max(0, ...extra, ...rows.map((r) => (stacked ? keys.reduce((a, k) => a + (r.v[k] || 0), 0) : Math.max(0, ...keys.map((k) => r.v[k] || 0)))))
}
export const playerName = (ua) => ua || 'player not recorded'
export const networkName = (ip) => ip || 'address not recorded'
