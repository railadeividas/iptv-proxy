<script setup>
import MeasureSwitch from './MeasureSwitch.vue'
import Scope from './Scope.vue'
import { ref, computed } from 'vue'
import { api } from '../../api'
import { rel, dur, bytes } from '../../util'
import { useStatsQuery, useLoader, usePaging, history, METRICS, change } from '../../stats'
import BarList from './BarList.vue'
import Tiles from './Tiles.vue'
import Pager from '../Pager.vue'

// Which channels are watched: the leaders, who gained and lost since the period before, and every channel played.
const props = defineProps({ o: Object })
const q = useStatsQuery()
const pg = usePaging()
const M = computed(() => METRICS[q.metric.value])
const top = ref([]), cur = ref([]), before = ref(null), list = ref({ total: 0, items: [] })
const ready = ref(false), prevTotal = ref(0)

useLoader(async (ok) => {
  const m = q.metric.value
  const hasPrev = q.canCompare.value
  const [a, c, b, l] = await Promise.all([
    api.get('/stats/breakdown?' + q.params({ by: 'channel', sort: m, limit: 8 })),
    api.get('/stats/breakdown?' + q.params({ by: 'channel', sort: m, limit: 500 })),
    hasPrev ? api.get('/stats/breakdown?' + q.params({ by: 'channel', sort: m, limit: 500 }, true)) : null,
    api.get('/stats/breakdown?' + q.params({ by: 'channel', sort: m, limit: pg.size.value, offset: pg.page.value * pg.size.value })),
  ])
  if (!ok()) return
  top.value = a.items; cur.value = c.items; before.value = b && new Map(b.items.map((x) => [x.key, x])); prevTotal.value = b ? b.total : 0; list.value = l; ready.value = true
}, { deps: () => [q.rangeKey.value, q.canCompare.value, q.profile.value, q.metric.value, pg.page.value, pg.size.value] })

const val = (x) => M.value.pick(x)
const pages = computed(() => Math.max(1, Math.ceil(list.value.total / pg.size.value)))
const topRows = computed(() => top.value.map((x) => {
  const p = before.value?.get(x.key)
  return { key: x.key, name: x.name, v: val(x), chg: before.value ? change(val(x), p ? val(p) : 0, true) : '' }
}))
// the biggest changes against the period before, in the measure of the page
const movers = computed(() => {
  if (!before.value) return { up: [], down: [] }
  const all = cur.value.map((x) => ({ key: x.key, name: x.name, d: val(x) - (before.value.get(x.key) ? val(before.value.get(x.key)) : 0) }))
  for (const [k, p] of before.value) if (!cur.value.some((x) => x.key === k)) all.push({ key: k, name: p.name, d: -val(p) })
  return {
    up: all.filter((x) => x.d > 0).sort((a, b) => b.d - a.d).slice(0, 5).map((x) => ({ key: x.key, name: x.name, v: x.d, c: 'var(--ok)' })),
    down: all.filter((x) => x.d < 0).sort((a, b) => a.d - b.d).slice(0, 5).map((x) => ({ key: x.key, name: x.name, v: -x.d, c: 'var(--bad)' })),
  }
})
const tiles = computed(() => [
  { label: 'Channels played', scope: 'period', value: list.value.total.toLocaleString(), sub: before.value ? change(list.value.total, prevTotal.value, true) : '', title: 'Different channels played in this period' },
  { label: 'Sessions', scope: 'period', value: (props.o?.totals?.sessions || 0).toLocaleString(), title: 'Sessions in this period' },
  { label: 'Watched', scope: 'period', value: dur(props.o?.totals?.seconds || 0), title: 'Total time of the proxied sessions' },
  { label: 'Data served', scope: 'period', value: bytes(props.o?.totals?.bytes || 0), title: 'Video sent to players through the proxy' },
])
const avg = (x) => (x.proxy ? dur(x.seconds / x.proxy) : '–')
</script>

<template>
  <Tiles :items="tiles" :cols="4" />
  <div class="card"><div class="chead"><h2>Top channels<Scope /></h2><MeasureSwitch /></div><p class="muted small">By {{ M.unit }}<template v-if="before">, with the change against the period before</template></p>
    <BarList :rows="topRows" :fmt="M.fmt" :change="!!before" empty="Nothing was played in this period." /></div>
  <p v-if="q.compareBlocked.value" class="muted small nohist">Rising and falling need the period before, and history is only kept for {{ history }} days.</p>
  <div v-if="before" class="card-row">
    <div class="card"><div class="chead"><h2>Rising<Scope /></h2><MeasureSwitch /></div><p class="muted small">Gained the most against the period before</p>
      <BarList :rows="movers.up" :fmt="M.fmt" empty="No channel gained." /></div>
    <div class="card"><div class="chead"><h2>Falling<Scope /></h2><MeasureSwitch /></div><p class="muted small">Lost the most against the period before</p>
      <BarList :rows="movers.down" :fmt="M.fmt" empty="No channel lost." /></div>
  </div>
  <div class="card">
    <div class="chead"><h2>Channels played<Scope /></h2><MeasureSwitch /></div>
    <p v-if="ready && !list.total" class="muted small">Nothing was played in this period.</p>
    <p v-else-if="!ready" class="muted small">Loading…</p>
    <div v-else class="scroll">
      <table class="t">
        <thead><tr><th>Channel</th><th class="r">Sessions</th><th class="r">Proxy</th><th class="r">Redirect</th><th class="r" title="Different devices">Viewers</th><th class="r">Watched</th><th class="r" title="Average watching time of a proxied session">Average</th><th class="r">Data</th><th>Last played</th></tr></thead>
        <tbody>
          <tr v-for="x in list.items" :key="x.key">
            <td><b>{{ x.name }}</b><span v-if="x.name !== x.key" class="muted small block">{{ x.key }}</span></td>
            <td class="r">{{ x.sessions.toLocaleString() }}</td>
            <td class="r">{{ x.proxy.toLocaleString() }}</td>
            <td class="r">{{ x.redirect.toLocaleString() }}</td>
            <td class="r">{{ x.viewers.toLocaleString() }}</td>
            <td class="r nowrap">{{ dur(x.seconds) }}</td>
            <td class="r nowrap">{{ avg(x) }}</td>
            <td class="r nowrap">{{ bytes(x.bytes) }}</td>
            <td class="nowrap">{{ rel(x.last) }}</td>
          </tr>
        </tbody>
      </table>
    </div>
    <Pager v-if="list.total" :page="pg.page.value" :pages="pages" :total="list.total" :size="pg.size.value" :count="list.items.length" :sizes="pg.SIZES" @go="pg.goPage" @size="pg.setSize" />
  </div>
</template>

<style scoped>
.small { font-size:13px; margin:0 0 10px; }
.scroll { overflow-x:auto; overflow-y:hidden; }
.t { width:100%; border-collapse:collapse; font-size:13px; }
.t th { font-size:12px; font-weight:500; color:var(--muted); }
.t td, .t th { padding:8px 10px 8px 0; vertical-align:top; border-bottom:1px solid var(--line); text-align:left; }
.t td.r, .t th.r { text-align:right; }
.t td .small { margin:2px 0 0; }
.t td:last-child, .t th:last-child { padding-left:14px; }
.nohist { margin:0; }
.block { display:block; }
.nowrap { white-space:nowrap; }
</style>
