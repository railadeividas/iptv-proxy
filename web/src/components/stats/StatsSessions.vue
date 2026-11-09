<script setup>
import Scope from './Scope.vue'
import { ref, computed } from 'vue'
import { api } from '../../api'
import { rel, dur, bytes } from '../../util'
import { useStatsQuery, useLoader, usePaging } from '../../stats'
import Pager from '../Pager.vue'

// Every stored session, newest first.
const props = defineProps({ o: Object })
const q = useStatsQuery()
const pg = usePaging()
const mode = computed(() => (['proxy', 'redirect'].includes(q.route.query.mode) ? q.route.query.mode : ''))
const list = ref(null)

useLoader(async (cur) => {
  const extra = { limit: pg.size.value, offset: pg.page.value * pg.size.value }
  if (mode.value) extra.mode = mode.value
  const d = await api.get('/stats/sessions?' + q.params(extra))
  if (!cur()) return
  list.value = d
  if (pg.page.value > 0 && !d.items.length && d.total) pg.goPage(Math.max(0, Math.ceil(d.total / pg.size.value) - 1))
}, { deps: () => [q.rangeKey.value, q.profile.value, mode.value, pg.page.value, pg.size.value] })

const tot = computed(() => props.o?.totals || {})
const share = computed(() => {
  const x = tot.value, n = x.sessions || 0
  return [['Through the proxy', x.proxy || 0, 'var(--s1)'], ['Redirected', x.redirect || 0, 'var(--s2)']].map(([name, v, c]) => ({ name, v, c, pct: n ? Math.round((v / n) * 100) : 0 }))
})
const many = computed(() => !!props.o?.all_profiles)
const pages = computed(() => Math.max(1, Math.ceil((list.value?.total || 0) / pg.size.value)))
const addr = (x) => x.ip || 'address not recorded'
const agent = (x) => x.ua || 'player not recorded'
const duration = (x) => (x.mode === 'redirect' ? '' : dur(Math.max(0, x.last - x.started)))
</script>

<template>
  <div class="card">
    <h2>Proxy and redirect<Scope /></h2><p class="muted small">Share of sessions</p>
    <div v-if="tot.sessions" class="stack">
      <div v-for="s in share.filter((x) => x.v)" :key="s.name" :style="{ flex: s.v, background: s.c }" :title="`${s.name}: ${s.v.toLocaleString()}`">{{ s.pct }}%</div>
    </div>
    <p v-else class="muted small">Nothing was played in this period.</p>
    <div class="legend"><span v-for="s in share" :key="s.name"><i :style="{ background: s.c }"></i>{{ s.name }} · {{ s.v.toLocaleString() }}</span></div>
    <p class="muted small">Redirected players are sent straight to the stream: the proxy never sees what they do after that, so there is no data or time for them.</p>
  </div>
  <div class="card">
    <div class="head">
      <h2>Sessions<Scope /> <span v-if="list" class="muted small">{{ list.total.toLocaleString() }}</span></h2>
      <select :value="mode" aria-label="Kind of session" @change="q.setQuery({ mode: $event.target.value, page: '' })">
        <option value="">Proxy and redirect</option>
        <option value="proxy">Through the proxy</option>
        <option value="redirect">Redirected</option>
      </select>
    </div>
    <p v-if="list && !list.total" class="muted small">Nothing was played in this period.</p>
    <p v-else-if="!list" class="muted small">Loading…</p>
    <div v-else class="scroll">
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
    <Pager v-if="list?.total" :page="pg.page.value" :pages="pages" :total="list.total" :size="pg.size.value" :count="list.items.length" :sizes="pg.SIZES" @go="pg.goPage" @size="pg.setSize" />
  </div>
</template>

<style scoped>
.stack { display:flex; height:22px; gap:2px; }
.stack div { display:flex; align-items:center; padding:0 8px; color:#fff; font-size:12px; font-weight:600; overflow:hidden; }
.stack div:first-child { border-radius:6px 0 0 6px; } .stack div:last-child { border-radius:0 6px 6px 0; } .stack div:only-child { border-radius:6px; }
.legend { display:flex; flex-wrap:wrap; gap:4px 14px; margin:8px 0; font-size:12px; }
.legend span { display:inline-flex; align-items:center; gap:6px; }
.legend i { width:10px; height:10px; border-radius:2px; display:inline-block; }
.head { display:flex; justify-content:space-between; align-items:center; gap:10px; flex-wrap:wrap; margin-bottom:10px; }
.head h2 { margin:0; }
.small { font-size:13px; }
.scroll { overflow-x:auto; overflow-y:hidden; }
.t { width:100%; border-collapse:collapse; font-size:13px; }
.t th { font-size:12px; font-weight:500; color:var(--muted); }
.t td, .t th { padding:8px 10px 8px 0; vertical-align:top; border-bottom:1px solid var(--line); text-align:left; }
.t td.r, .t th.r { text-align:right; }
.t td .small { margin:2px 0 0; }
.block { display:block; }
.nowrap { white-space:nowrap; }
</style>
