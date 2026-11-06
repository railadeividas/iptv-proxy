<script setup>
import { ref, computed, watch, onMounted, onUnmounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { api } from '../api'
import { store, can, toast } from '../store'
import { rel, dur, bytes, throttle, useLive, PAGE_SIZES as SIZES, PAGE_SIZE } from '../util'
import Pager from '../components/Pager.vue'

// Who uses the playlist links: who is watching now, how much, which channels, which devices. A user sees their own
// profiles, an admin everybody's (the server decides). Period, profile, list, page and size live in the URL.
const route = useRoute()
const router = useRouter()

const PERIODS = [[1, 'Last 24 hours'], [7, 'Last 7 days'], [30, 'Last 30 days'], [0, 'Everything stored']]
const VIEWS = [['sessions', 'Sessions'], ['channels', 'Channels'], ['devices', 'Devices']]
const days = computed(() => (PERIODS.some((p) => String(p[0]) === route.query.days) ? +route.query.days : 7))
const profile = computed(() => parseInt(route.query.profile) || 0)
const view = computed(() => (VIEWS.some((v) => v[0] === route.query.view) ? route.query.view : 'sessions'))
const mode = computed(() => (['proxy', 'redirect'].includes(route.query.mode) ? route.query.mode : ''))
const savedSize = (() => { try { return +localStorage.getItem('statsSize') } catch { return 0 } })()
const size = computed(() => { const n = parseInt(route.query.size) || savedSize; return SIZES.includes(n) ? n : PAGE_SIZE })
const page = computed(() => Math.max(0, (parseInt(route.query.page) || 1) - 1))

function setQuery(patch, replace = false) {
  const query = { ...route.query, ...patch }
  for (const k of Object.keys(query)) if (query[k] === '' || query[k] == null) delete query[k]
  router[replace ? 'replace' : 'push']({ query })
}
const setFilter = (k, v) => setQuery({ [k]: v, page: '' })
const goPage = (n) => setQuery({ page: n ? n + 1 : '' })
function setSize(n) {
  try { localStorage.setItem('statsSize', String(n)) } catch {}
  setQuery({ size: n === PAGE_SIZE ? '' : n, page: '' })
}

const o = ref(null) // overview: now, totals, profiles, settings that matter here
const list = ref({ total: 0, items: [] })
const loaded = ref(false)
const pages = computed(() => Math.max(1, Math.ceil(list.value.total / size.value)))

function params(extra = {}) {
  const p = new URLSearchParams({ days: days.value, ...extra })
  if (profile.value) p.set('profile', profile.value)
  return p
}
async function loadOverview() {
  try { o.value = await api.get('/stats?' + params()) } catch (e) { if (e.message !== 'sign in required') toast(e.message) }
}
const endpoint = { sessions: '/stats/sessions', channels: '/stats/channels', devices: '/stats/clients' }
async function loadList() {
  try {
    const extra = { limit: size.value, offset: page.value * size.value }
    if (view.value === 'sessions' && mode.value) extra.mode = mode.value
    const data = await api.get(endpoint[view.value] + '?' + params(extra))
    list.value = data
    if (page.value > 0 && !data.items.length && data.total) goPage(Math.max(0, pages.value - 1))
  } catch (e) { if (e.message !== 'sign in required') toast(e.message) }
}
async function load() { await Promise.all([loadOverview(), loadList()]); loaded.value = true }
watch(() => route.query, load)
// running sessions change by the second: the events say when one starts or ends, the timer keeps the numbers fresh
const soon = throttle(load, 1500)
let timer
onMounted(() => { load(); timer = setInterval(load, 5000) })
onUnmounted(() => clearInterval(timer))
useLive({ activity: soon, profiles: soon, settings: soon })

const totals = computed(() => o.value?.totals || {})
const profiles = computed(() => o.value?.profiles || [])
const many = computed(() => !!o.value?.all_profiles) // owner names matter when you see everybody's
const watchingNow = computed(() => (o.value?.now || []).filter((x) => x.mode === 'proxy'))
const sentAway = computed(() => (o.value?.now || []).filter((x) => x.mode === 'redirect'))
const tiles = computed(() => {
  const t = totals.value
  const out = [
    ['Watching now', watchingNow.value.length.toLocaleString(), 'ok', 'Devices watching through the proxy at this moment'],
    ['Sessions', (t.sessions || 0).toLocaleString(), '', 'A device watching one channel is one session'],
    ['Through the proxy', (t.proxy || 0).toLocaleString(), '', 'Sessions the proxy carried: its data and watching time are counted'],
    ['Redirected', (t.redirect || 0).toLocaleString(), '', 'Players sent straight to the stream. The proxy never sees what they do after that, so there is no data or time for them'],
    ['Data served', bytes(t.bytes || 0), '', 'Video sent to players through the proxy'],
    ['Watched', dur(t.seconds || 0), '', 'Total time of the proxied sessions'],
    ['Devices', (t.clients || 0).toLocaleString(), '', 'Different devices (address and player) that used a link'],
    ['Channels', (t.channels || 0).toLocaleString(), '', 'Different channels played'],
  ]
  if (o.value?.connections) out.splice(1, 0, ['Proxy connections', `${o.value.connections.in_use} / ${o.value.connections.limit}`,
    o.value.connections.in_use >= o.value.connections.limit ? 'bad' : '', 'Streams the proxy is carrying right now, and the most it allows (Settings > Proxy)'])
  return out
})

const addr = (x) => x.ip || 'address not recorded'
const agent = (x) => x.ua || 'player not recorded'
const privacy = computed(() => {
  const m = o.value?.ip_mode
  if (m === 'masked') return 'Addresses are masked: devices on the same network look alike.'
  if (m === 'none') return 'Addresses are not recorded.'
  return ''
})
const periodText = computed(() => (days.value ? PERIODS.find((p) => p[0] === days.value)[1].toLowerCase() : 'everything stored'))
const keep = computed(() => o.value?.history_days)
const duration = (x) => (x.mode === 'redirect' ? '' : dur(Math.max(0, x.last - x.started)))
const pickProfile = (id) => setQuery({ profile: id, page: '' })
</script>

<template>
  <section class="stats">
    <div class="filters">
      <select :value="days" aria-label="Period" @change="setFilter('days', $event.target.value === '7' ? '' : $event.target.value)">
        <option v-for="p in PERIODS" :key="p[0]" :value="p[0]">{{ p[1] }}</option>
      </select>
      <select :value="profile" aria-label="Profile" @change="setFilter('profile', $event.target.value === '0' ? '' : $event.target.value)">
        <option :value="0">{{ many ? 'All profiles' : 'All my profiles' }}</option>
        <option v-for="p in profiles" :key="p.profile_id" :value="p.profile_id">{{ p.profile }}{{ many && p.owner ? ` (${p.owner})` : '' }}</option>
      </select>
    </div>

    <p v-if="o && !o.enabled" class="note warn">Recording is off, so nothing new is counted.
      <RouterLink v-if="can('settings')" :to="{ name: 'settings-group', params: { group: 'stats' } }">Turn it on</RouterLink>
      <template v-else>Ask an administrator to turn it on.</template></p>
    <p v-if="o && privacy" class="muted small">{{ privacy }}</p>

    <div v-if="o" class="tiles">
      <div v-for="t in tiles" :key="t[0]" class="tile" :class="t[2]" :title="t[3]"><span class="lbl">{{ t[0] }}</span><b>{{ t[1] }}</b></div>
    </div>
    <p v-if="o" class="muted small">Numbers cover {{ periodText }}. History is kept for {{ keep }} {{ keep === 1 ? 'day' : 'days' }} (Settings > Stats).</p>

    <div class="card">
      <h2>Watching now</h2>
      <p v-if="!loaded" class="muted small">Loading…</p>
      <p v-else-if="!watchingNow.length && !sentAway.length" class="muted small">Nobody is watching right now.</p>
      <ul v-else class="rows">
        <li v-for="(x, i) in [...watchingNow, ...sentAway]" :key="i">
          <div class="main">
            <div class="line"><b>{{ x.name }}</b>
              <span class="badge" :class="x.mode === 'proxy' ? 'good' : ''" :title="x.mode === 'proxy' ? 'The proxy carries this stream' : 'The player was sent straight to the stream'">{{ x.mode === 'proxy' ? 'proxy' : 'redirect' }}</span></div>
            <div class="muted small">{{ x.profile }} · {{ addr(x) }} · {{ agent(x) }}<template v-if="x.host"> · {{ x.host }}</template></div>
          </div>
          <div class="nums muted small">
            <template v-if="x.mode === 'proxy'"><span>{{ bytes(x.bytes) }}</span><span>for {{ dur(Math.max(0, x.last - x.started)) }}</span></template>
            <span v-else>{{ rel(x.started) }}</span>
          </div>
        </li>
      </ul>
    </div>

    <div class="card">
      <h2>Profiles</h2>
      <p v-if="loaded && !profiles.length" class="muted small">No profiles yet.</p>
      <div v-else class="scroll">
        <table class="t">
          <thead><tr><th>Profile</th><th>Last seen</th><th class="r">Devices</th><th class="r" title="Times the playlist was fetched">Playlist</th><th class="r" title="Times the TV guide was fetched">Guide</th>
            <th class="r">Sessions</th><th class="r" title="Sessions through the proxy">Proxy</th><th class="r" title="Players sent straight to the stream">Redirect</th><th class="r">Data</th><th class="r">Watched</th></tr></thead>
          <tbody>
            <tr v-for="p in profiles" :key="p.profile_id" :class="{ on: profile === p.profile_id }">
              <td><button class="link" :title="`Show only ${p.profile}`" @click="pickProfile(profile === p.profile_id ? '' : p.profile_id)"><b>{{ p.profile }}</b></button><span v-if="many && p.owner" class="muted small block">{{ p.owner }}</span></td>
              <td class="nowrap" :class="{ muted: !p.last_seen }">{{ rel(p.last_seen) }}</td>
              <td class="r">{{ p.clients.toLocaleString() }}</td>
              <td class="r">{{ p.playlists.toLocaleString() }}</td>
              <td class="r">{{ p.guides.toLocaleString() }}</td>
              <td class="r">{{ p.sessions.toLocaleString() }}</td>
              <td class="r">{{ p.proxy.toLocaleString() }}</td>
              <td class="r">{{ p.redirect.toLocaleString() }}</td>
              <td class="r nowrap">{{ bytes(p.bytes) }}</td>
              <td class="r nowrap">{{ dur(p.seconds) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
      <p class="muted small foot">Playlist and Guide count every fetch since recording began. Everything else covers the period above.</p>
    </div>

    <div class="card">
      <div class="head">
        <nav class="tabs" aria-label="Lists">
          <button v-for="v in VIEWS" :key="v[0]" type="button" :class="{ on: view === v[0] }" :aria-current="view === v[0] ? 'true' : null" @click="setQuery({ view: v[0] === 'sessions' ? '' : v[0], page: '', mode: '' })">{{ v[1] }}</button>
        </nav>
        <select v-if="view === 'sessions'" :value="mode" aria-label="Kind of session" @change="setFilter('mode', $event.target.value)">
          <option value="">Proxy and redirect</option>
          <option value="proxy">Through the proxy</option>
          <option value="redirect">Redirected</option>
        </select>
      </div>

      <p v-if="loaded && !list.total" class="muted small">{{ view === 'devices' ? 'No device used a link in this period.' : 'Nothing was played in this period.' }}</p>

      <div v-else-if="view === 'sessions'" class="scroll">
        <table class="t">
          <thead><tr><th>Channel</th><th>Profile</th><th>Device</th><th>Kind</th><th>Started</th><th class="r">Time</th><th class="r">Data</th></tr></thead>
          <tbody>
            <tr v-for="x in list.items" :key="x.id">
              <td><b>{{ x.name }}</b><span v-if="x.host" class="muted small block">{{ x.host }}</span></td>
              <td>{{ x.profile }}<span v-if="many && x.owner" class="muted small block">{{ x.owner }}</span></td>
              <td>{{ addr(x) }}<span class="muted small block">{{ agent(x) }}</span></td>
              <td><span class="badge" :class="x.mode === 'proxy' ? 'good' : ''">{{ x.mode }}</span></td>
              <td class="nowrap" :title="new Date(x.started * 1000).toLocaleString()">{{ rel(x.started) }}</td>
              <td class="r nowrap">{{ duration(x) || '–' }}</td>
              <td class="r nowrap">{{ x.mode === 'redirect' ? '–' : bytes(x.bytes) }}</td>
            </tr>
          </tbody>
        </table>
      </div>

      <div v-else-if="view === 'channels'" class="scroll">
        <table class="t">
          <thead><tr><th>Channel</th><th class="r">Sessions</th><th class="r">Proxy</th><th class="r">Redirect</th><th class="r" title="Different devices">Viewers</th><th class="r">Watched</th><th class="r">Data</th><th>Last played</th></tr></thead>
          <tbody>
            <tr v-for="x in list.items" :key="x.ck">
              <td><b>{{ x.name }}</b><span v-if="x.name !== x.ck" class="muted small block">{{ x.ck }}</span></td>
              <td class="r">{{ x.sessions.toLocaleString() }}</td>
              <td class="r">{{ x.proxy.toLocaleString() }}</td>
              <td class="r">{{ x.redirect.toLocaleString() }}</td>
              <td class="r">{{ x.viewers.toLocaleString() }}</td>
              <td class="r nowrap">{{ dur(x.seconds) }}</td>
              <td class="r nowrap">{{ bytes(x.bytes) }}</td>
              <td class="nowrap">{{ rel(x.last) }}</td>
            </tr>
          </tbody>
        </table>
      </div>

      <div v-else class="scroll">
        <table class="t">
          <thead><tr><th>Device</th><th>Profile</th><th>First seen</th><th>Last seen</th><th class="r">Playlist</th><th class="r">Guide</th><th class="r">Plays</th></tr></thead>
          <tbody>
            <tr v-for="x in list.items" :key="x.id">
              <td><b>{{ addr(x) }}</b><span class="muted small block">{{ agent(x) }}</span></td>
              <td>{{ x.profile }}<span v-if="many && x.owner" class="muted small block">{{ x.owner }}</span></td>
              <td class="nowrap" :title="new Date(x.first_seen * 1000).toLocaleString()">{{ rel(x.first_seen) }}</td>
              <td class="nowrap" :title="new Date(x.last_seen * 1000).toLocaleString()">{{ rel(x.last_seen) }}</td>
              <td class="r">{{ x.playlists.toLocaleString() }}</td>
              <td class="r">{{ x.guides.toLocaleString() }}</td>
              <td class="r">{{ x.plays.toLocaleString() }}</td>
            </tr>
          </tbody>
        </table>
      </div>

      <Pager v-if="list.total" :page="page" :pages="pages" :total="list.total" :size="size" :count="list.items.length" :sizes="SIZES" @go="goPage" @size="setSize" />
    </div>
  </section>
</template>

<style scoped>
.filters { margin-bottom:12px; }
.small { font-size:13px; margin:6px 0; }
.note { padding:8px 12px; border:1px solid var(--line); border-radius:8px; background:var(--panel); margin:0 0 12px; }
.note.warn { border-color:var(--warn); }
.tiles { display:grid; grid-template-columns:repeat(auto-fit, minmax(150px, 1fr)); gap:12px; }
.tile { background:var(--panel); border:1px solid var(--line); border-left:3px solid var(--line); border-radius:10px; padding:12px 14px; }
.tile .lbl { display:block; font-size:12px; color:var(--muted); }
.tile b { display:block; font-size:22px; line-height:1.2; font-variant-numeric:tabular-nums; }
.tile.ok { border-left-color:var(--ok); } .tile.bad { border-left-color:var(--bad); }
.card h2 { margin:0 0 8px; }
.rows { list-style:none; margin:0; padding:0; border:1px solid var(--line); border-radius:8px; overflow:hidden; }
.rows li { display:flex; justify-content:space-between; gap:12px; align-items:center; padding:8px 12px; border-bottom:1px solid var(--line); }
.rows li:last-child { border-bottom:0; }
.main { min-width:0; flex:1; }
.main .small { margin:2px 0 0; overflow-wrap:anywhere; }
.line { display:flex; align-items:center; gap:6px; }
.line b { min-width:0; overflow:hidden; text-overflow:ellipsis; white-space:nowrap; }
.badge { margin:0; flex:none; }
.nums { display:flex; flex-direction:column; align-items:flex-end; margin:0; white-space:nowrap; }
.nums span { margin:0; }
.scroll { overflow-x:auto; overflow-y:hidden; }
.t { width:100%; border-collapse:collapse; font-size:13px; }
.t th { font-size:12px; font-weight:500; color:var(--muted); }
.t td, .t th { padding:8px 10px 8px 0; vertical-align:top; border-bottom:1px solid var(--line); text-align:left; }
.t td.r, .t th.r { text-align:right; }
.t tr.on td { background:color-mix(in srgb, var(--accent) 8%, transparent); }
.t td .small { margin:2px 0 0; }
.block { display:block; }
.nowrap { white-space:nowrap; }
.foot { margin:8px 0 0; }
button.link { height:auto; padding:0; border:0; background:none; text-align:left; cursor:pointer; color:inherit; }
button.link:hover b { color:var(--accent); }
.head { display:flex; justify-content:space-between; align-items:center; gap:10px; flex-wrap:wrap; margin-bottom:10px; }
.tabs { display:flex; gap:4px; overflow-x:auto; overflow-y:hidden; }
.tabs button { background:none; border:0; border-radius:6px; padding:0 12px; color:var(--muted); cursor:pointer; }
.tabs button:hover { color:var(--text); }
.tabs button.on { color:var(--text); font-weight:500; box-shadow:inset 0 -2px 0 var(--accent); border-radius:0; }
.tabs button:focus-visible { outline:2px solid var(--accent); outline-offset:-2px; }
@media (max-width:760px) { .rows li { flex-wrap:wrap; } .nums { flex-direction:row; gap:8px; } }
</style>
