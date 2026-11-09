<script setup>
import Scope from './Scope.vue'
import { ref, computed } from 'vue'
import { api } from '../../api'
import { rel, dur, bytes } from '../../util'
import { useStatsQuery, useLoader, profileColor } from '../../stats'
import Tiles from './Tiles.vue'
import BarList from './BarList.vue'
import StatChart from './StatChart.vue'

// Who is watching right now, how much the proxy sends and how busy it was over the last hour.
// The samples are taken by the server every time it saves running sessions and are kept in memory only.
const props = defineProps({ o: Object })
const q = useStatsQuery()
const live = ref({ interval: 10, samples: [] })

useLoader(async (cur) => {
  const d = await api.get('/stats/live?' + q.params())
  if (cur()) live.value = d
}, { deps: () => [q.profile.value], every: 5000 })

const many = computed(() => !!props.o?.all_profiles)
const proxy = computed(() => (props.o?.now || []).filter((x) => x.mode === 'proxy'))
const sent = computed(() => (props.o?.now || []).filter((x) => x.mode === 'redirect'))
const rows = computed(() => [...proxy.value, ...sent.value])
const addr = (x) => x.ip || 'address not recorded'
const agent = (x) => x.ua || 'player not recorded'

// outgoing Mbit/s between neighbouring samples
const rates = computed(() => {
  const s = live.value.samples, out = []
  for (let i = 1; i < s.length; i++) {
    const dt = s[i].t - s[i - 1].t
    if (dt > 0) out.push({ t: s[i].t, v: Math.max(0, ((s[i].bytes - s[i - 1].bytes) * 8) / dt / 1e6) })
  }
  return out
})
const lastT = computed(() => live.value.samples.at(-1)?.t || 0)
const bwRows = computed(() => {
  const iv = live.value.interval || 10
  return rates.value.filter((r) => r.t >= lastT.value - 300).map((r) => {
    const ago = lastT.value - r.t
    return { l: ago < iv ? 'now' : ago % 60 < iv ? `${Math.round(ago / 60)} min` : '', tt: ago < iv ? 'now' : `${Math.round(ago)} s ago`, v: { bw: r.v } }
  })
})
const viewerRows = computed(() => {
  const by = new Map()
  for (const s of live.value.samples) if (s.t >= lastT.value - 3600) by.set(Math.floor(s.t / 60), Math.max(by.get(Math.floor(s.t / 60)) || 0, s.watching))
  const mins = [...by.keys()].sort((a, b) => a - b), end = Math.floor(lastT.value / 60)
  return mins.map((m) => {
    const ago = end - m
    return { l: ago === 0 ? 'now' : ago % 10 === 0 ? `${ago} min` : '', tt: ago === 0 ? 'now' : `${ago} min ago`, v: { n: by.get(m) } }
  })
})
const mbit = (v) => v.toFixed(v < 10 ? 1 : 0) + ' Mbit/s'
const rateNow = computed(() => rates.value.at(-1)?.v || 0)
const conn = computed(() => props.o?.connections)
const tiles = computed(() => [
  { label: 'Watching now', scope: 'now', value: proxy.value.length, pulse: true, tone: proxy.value.length ? 'ok' : '', sub: 'through the proxy' },
  { label: 'Redirected now', scope: 'now', value: sent.value.length, sub: 'not carried by the proxy', title: 'Players sent straight to the stream' },
  { label: 'Outgoing now', scope: 'now', value: live.value.samples.length > 1 ? mbit(rateNow.value) : '–', sub: 'through the proxy' },
  ...(conn.value ? [{ label: 'Proxy connections', scope: 'now', value: `${conn.value.in_use} / ${conn.value.limit}`, tone: conn.value.in_use >= conn.value.limit ? 'bad' : '', gauge: conn.value.limit ? conn.value.in_use / conn.value.limit : 0, title: 'Streams the proxy is carrying, and the most it allows (Settings > Proxy)' }] : []),
])
const byChannel = computed(() => {
  const m = new Map()
  for (const x of proxy.value) m.set(x.name, (m.get(x.name) || 0) + 1)
  return [...m].sort((a, b) => b[1] - a[1]).map(([name, v]) => ({ name, v }))
})
const collecting = computed(() => live.value.samples.length < 2)
</script>

<template>
  <Tiles :items="tiles" :cols="tiles.length" />
  <div class="card-row">
    <div class="card">
      <h2>Watching now<Scope kind="now" /></h2>
      <p v-if="!rows.length" class="muted small">Nobody is watching right now.</p>
      <ul v-else class="rows">
        <li v-for="(x, i) in rows" :key="i">
          <div class="main">
            <div class="line"><b>{{ x.name }}</b>
              <span class="badge" :class="x.mode === 'proxy' ? 'good' : ''" :title="x.mode === 'proxy' ? 'The proxy carries this stream' : 'The player was sent straight to the stream'">{{ x.mode }}</span></div>
            <div class="muted small">{{ x.profile }}<template v-if="many && x.owner"> ({{ x.owner }})</template> · {{ addr(x) }} · {{ agent(x) }}<template v-if="x.host"> · {{ x.host }}</template></div>
          </div>
          <div class="nums muted small">
            <template v-if="x.mode === 'proxy'"><span>{{ bytes(x.bytes) }}</span><span>for {{ dur(Math.max(0, x.last - x.started)) }}</span></template>
            <span v-else>{{ rel(x.started) }}</span>
          </div>
        </li>
      </ul>
    </div>
    <div class="card">
      <h2>Channels right now<Scope kind="now" /></h2><p class="muted small">Viewers per channel, through the proxy</p>
      <BarList :rows="byChannel" :fmt="(v) => v.toLocaleString()" color="var(--s3)" empty="Nobody is watching through the proxy." />
    </div>
  </div>
  <div class="card">
    <h2>Outgoing bandwidth<Scope kind="now" text="Last 5 minutes" /></h2><p class="muted small">Sent to players through the proxy</p>
    <p v-if="collecting" class="muted small">Collecting samples: the chart fills as the server saves running sessions (every {{ live.interval }} s). It starts empty after the app restarts.</p>
    <StatChart v-else :rows="bwRows" :keys="['bw']" :names="{ bw: 'Outgoing' }" :colors="{ bw: 'var(--s1)' }" type="area" :fmt="mbit" :axis="(v) => +v.toFixed(1)" :height="180" label="Outgoing bandwidth" sparse />
  </div>
  <div class="card">
    <h2>Viewers over the last hour<Scope kind="now" text="Last hour" /></h2><p class="muted small">Streams the proxy carried, the most in each minute</p>
    <p v-if="collecting" class="muted small">Collecting samples…</p>
    <StatChart v-else :rows="viewerRows" :keys="['n']" :names="{ n: 'Watching' }" :colors="{ n: 'var(--s3)' }" type="bars" int :fmt="(v) => v.toLocaleString()" :height="180" :max-bar="10" label="Viewers over the last hour" sparse />
  </div>
</template>

<style scoped>
.small { font-size:13px; margin:0 0 10px; }
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
@media (max-width:760px) { .rows li { flex-wrap:wrap; } .nums { flex-direction:row; gap:8px; } }
</style>
