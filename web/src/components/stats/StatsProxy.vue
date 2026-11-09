<script setup>
import Scope from './Scope.vue'
import { ref, computed } from 'vue'
import { api } from '../../api'
import { bytes } from '../../util'
import { useStatsQuery, useLoader, METRICS, numberRows, perBucket, pct, axisOf } from '../../stats'
import Tiles from './Tiles.vue'
import BarList from './BarList.vue'
import StatChart from './StatChart.vue'

// The proxy itself (roles that see everybody's activity): connections, cache, the stream servers behind it.
const props = defineProps({ o: Object })
const q = useStatsQuery()
const t = ref(null), hosts = ref([]), samples = ref(null)

useLoader(async (cur) => {
  // the samples are hourly at best: the timeline is cut the same way, so the two line up bar by bar
  const [hs, sm] = await Promise.all([
    api.get('/stats/breakdown?' + q.params({ by: 'host', sort: 'bytes', limit: 8 })),
    api.get('/stats/samples?' + q.params()),
  ])
  const tl = await api.get('/stats/timeline?' + q.params({ step: sm.bucket }))
  if (!cur()) return
  t.value = tl; hosts.value = hs.items; samples.value = sm
}, { deps: () => [q.rangeKey.value, q.stepKey.value, q.profile.value] })

const tiles = computed(() => {
  const c = props.o?.cache, k = props.o?.connections
  if (!c) return []
  const asked = c.hits + c.misses
  return [
    { label: 'Proxy connections', scope: 'now', value: `${k.in_use} / ${k.limit}`, tone: k.in_use >= k.limit ? 'bad' : '', gauge: k.limit ? k.in_use / k.limit : 0, title: 'Streams the proxy is carrying right now, and the most it allows (Settings > Proxy)' },
    { label: 'Cache hit rate', scope: 'start', value: c.limit === 0 ? 'off' : asked ? `${Math.round((c.hits / asked) * 100)}%` : '–', gauge: asked ? c.hits / asked : null, title: 'Segments answered from the cache out of all segment requests since the app started (Settings > Proxy)' },
    { label: 'Cache in use', scope: 'now', value: c.limit === 0 ? 'off' : bytes(c.bytes), gauge: c.limit ? c.bytes / c.limit : null, sub: c.limit ? `of ${bytes(c.limit)}` : '', title: `${c.entries.toLocaleString()} segments held right now` },
    { label: 'Served from cache', scope: 'start', value: bytes(c.hit_bytes), title: 'Video sent to players from the cache instead of the stream server' },
    { label: 'Prefetched', scope: 'start', value: c.prefetched.toLocaleString(), title: 'Segments fetched ahead of the player' },
    { label: 'Shared fetches', scope: 'start', value: c.coalesced.toLocaleString(), title: 'Requests that waited for a fetch already running instead of asking the server again' },
    { label: 'Retries', scope: 'start', value: c.retries.toLocaleString(), title: 'Stream server requests repeated after a temporary failure' },
    { label: 'Playlist hold-overs', scope: 'start', value: c.holdovers.toLocaleString(), title: 'Playlists served from the last good copy because the server was down' },
  ]
})
const hostRows = computed(() => hosts.value.map((x) => ({ key: x.key, name: x.name || 'host not recorded', v: x.bytes })))

// what the hourly samples say, per bucket
const server = computed(() => {
  const s = samples.value
  if (!s?.server) return null
  const z = () => Array(s.n).fill(0)
  const hit = z(), miss = z(), hitBytes = z(), retries = z(), hold = z()
  for (const r of s.server) { hit[r.i] = r.cache_hits; miss[r.i] = r.cache_misses; hitBytes[r.i] = r.cache_hit_bytes; retries[r.i] = r.retries; hold[r.i] = r.holdovers }
  return { hit, miss, hitBytes, retries, hold }
})
const any = computed(() => !!server.value && [...server.value.hit, ...server.value.miss, ...server.value.retries, ...server.value.hold].some(Boolean))
const hitRows = computed(() => (server.value ? numberRows(t.value, server.value.hit.map((h, i) => (h + server.value.miss[i] ? (100 * h) / (h + server.value.miss[i]) : null)), 'h') : []))
const sourceRows = computed(() => {
  if (!server.value) return []
  const total = perBucket(t.value, 'bytes')
  return numberRows(t.value, total, 'x').map((r, i) => { const c = Math.min(total[i], server.value.hitBytes[i]); return { ...r, v: { up: total[i] - c, cache: c } } })
})
const sourceAxis = computed(() => axisOf('bytes', Math.max(0, ...sourceRows.value.map((r) => r.v.up + r.v.cache))))
const retryRows = computed(() => (server.value ? numberRows(t.value, server.value.retries, 'r').map((r, i) => ({ ...r, v: { r: server.value.retries[i], hold: server.value.hold[i] } })) : []))
const count = (v) => Math.round(v).toLocaleString()
</script>

<template>
  <Tiles :items="tiles" :cols="4" />
  <div class="card-row">
    <div class="card"><h2>Stream servers<Scope /></h2><p class="muted small">Data carried by the proxy, by the server it came from</p>
      <BarList :rows="hostRows" :fmt="bytes" color="var(--s2)" empty="Nothing was carried in this period." /></div>
    <div v-if="server" class="card"><h2>Retries and hold-overs<Scope /></h2><p class="muted small">Temporary failures of stream servers</p>
      <p v-if="!any" class="muted small">Nothing recorded in this period. Hourly numbers are kept since this feature was added.</p>
      <template v-else>
        <div class="legend"><span><i style="background:var(--s4)"></i>Retries</span><span><i style="background:var(--s5)"></i>Playlist hold-overs</span></div>
        <StatChart :rows="retryRows" :keys="['r', 'hold']" :names="{ r: 'Retries', hold: 'Playlist hold-overs' }" :colors="{ r: 'var(--s4)', hold: 'var(--s5)' }" type="bars" int :fmt="count" :height="200" label="Retries and hold-overs" />
      </template></div>
  </div>
  <div v-if="server && any" class="card-row">
    <div class="card"><h2>Where the video came from<Scope /></h2><p class="muted small">Data served to players: from the cache or from the stream server</p>
      <div class="legend"><span><i style="background:var(--s2)"></i>From the stream server</span><span><i style="background:var(--s3)"></i>From the cache</span></div>
      <StatChart :rows="sourceRows" :keys="['up', 'cache']" :names="{ up: 'From the stream server', cache: 'From the cache' }" :colors="{ up: 'var(--s2)', cache: 'var(--s3)' }" type="bars" :fmt="bytes" :axis="sourceAxis.axis" :scale="sourceAxis.scale" :height="200" label="Data from cache and stream server" /></div>
    <div class="card"><h2>Cache hit rate<Scope /></h2><p class="muted small">Segment requests answered from the cache</p>
      <StatChart :rows="hitRows" :keys="['h']" :names="{ h: 'Hit rate' }" :colors="{ h: 'var(--s3)' }" type="line" :fmt="pct" :max="100" :height="200" label="Cache hit rate" /></div>
  </div>
</template>

<style scoped>
.small { font-size:13px; margin:0 0 10px; }
.legend { display:flex; flex-wrap:wrap; gap:4px 14px; margin:0 0 8px; font-size:12px; }
.legend span { display:inline-flex; align-items:center; gap:6px; }
.legend i { width:10px; height:10px; border-radius:2px; display:inline-block; }
</style>
