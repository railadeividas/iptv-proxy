<script setup>
import { can } from '../store'
import { ref, computed, onMounted } from 'vue'
import { api } from '../api'
import { dur, rel, throttle, useLive, PAGE_SIZES as SIZES, PAGE_SIZE } from '../util'
import Pager from './Pager.vue'
import RoundModal from './RoundModal.vue'

// Trend of working / not working over the last rounds, and the list of rounds (the table view of the chart).
const props = defineProps({ h: Object })
const saved = (() => { try { return +localStorage.getItem('healthRoundsSize') } catch { return 0 } })()
const size = ref(SIZES.includes(saved) ? saved : PAGE_SIZE)
const page = ref(0)
const data = ref({ total: 0, items: [], trend: [] })
const tip = ref(null) // { i, left }
const opened = ref(null) // round whose results are shown
const plot = ref(null)

const kept = computed(() => props.h?.history.rounds_kept ?? 0)
const pages = computed(() => Math.max(1, Math.ceil(data.value.total / size.value)))

async function load() { try { data.value = await api.get(`/health/rounds?limit=${size.value}&offset=${page.value * size.value}`) } catch {} }
onMounted(load)
const soon = throttle(load, 1500)
useLive({ health: soon, settings: soon })
const go = (n) => { page.value = n; load() }
function setSize(n) {
  try { localStorage.setItem('healthRoundsSize', String(n)) } catch {}
  page.value = Math.floor((page.value * size.value) / n); size.value = n; load()
}

const trend = computed(() => data.value.trend)
// only checked streams (and excluded ones) are drawn: the unchecked part of a big catalogue would flatten the rest
const max = computed(() => Math.max(1, ...trend.value.map((r) => r.total_ok + r.total_bad + (r.total_excluded || 0))))
const compact = (n) => (n >= 10000 ? (n / 1000).toFixed(n >= 100000 ? 0 : 1).replace('.0', '') + 'K' : n.toLocaleString())
// segments bottom to top; the top-most visible one gets the rounded end
const segs = (r) => {
  const parts = [
    { tone: 'ok', value: r.total_ok }, { tone: 'bad', value: r.total_bad }, { tone: 'excl', value: r.total_excluded || 0 },
  ].filter((p) => p.value > 0).map((p) => ({ ...p, pct: (p.value / max.value) * 100 }))
  if (parts.length) parts[parts.length - 1].top = true
  return parts
}
const when = (t) => new Date(t * 1000).toLocaleString()

function show(i, e) {
  const col = e.currentTarget, wrap = plot.value
  if (!wrap) return
  const c = col.getBoundingClientRect(), w = wrap.getBoundingClientRect()
  tip.value = { i, left: Math.min(Math.max(c.left - w.left + c.width / 2, 120), w.width - 120) }
}
const hide = () => { tip.value = null }
const summary = computed(() => {
  const r = trend.value[trend.value.length - 1]
  return r ? `Working, not working and excluded streams after each of the last ${trend.value.length} rounds. Latest: ${r.total_ok} working, ${r.total_bad} not working, ${r.total_excluded || 0} excluded, ${r.total_unchecked} not checked yet.` : ''
})
</script>

<template>
  <div>
    <p v-if="!kept" class="muted small">The history is switched off. Set “Check rounds to keep” on <RouterLink v-if="can('settings')" :to="{ name: 'settings-group', params: { group: 'health' } }">Settings > Health</RouterLink><template v-else>Settings > Health</template> to record rounds.</p>
    <template v-else>
      <p class="muted small keep">Keeping up to {{ kept.toLocaleString() }} round{{ kept === 1 ? '' : 's' }}, {{ h.history.per_channel_kept.toLocaleString() }} result{{ h.history.per_channel_kept === 1 ? '' : 's' }} per channel
        and {{ h.history.total_kept.toLocaleString() }} overall, none older than {{ h.history.days }} day{{ h.history.days === 1 ? '' : 's' }}
        ({{ h.history.rounds.toLocaleString() }} rounds and {{ h.history.checks.toLocaleString() }} results stored now). Change it in <RouterLink v-if="can('settings')" :to="{ name: 'settings-group', params: { group: 'health' } }">Settings</RouterLink><template v-else>Settings</template>.</p>

      <p v-if="!data.total" class="muted small">No round has finished yet.</p>
      <template v-else>
        <figure v-if="trend.length > 1" class="chart" :aria-label="summary">
          <figcaption class="legend">
            <span><i class="sw ok" />Working</span><span><i class="sw bad" />Not working</span><span><i class="sw excl-fill" />Excluded</span>
            <span class="muted">Checked streams after each of the last {{ trend.length }} rounds (streams not checked yet are not drawn)</span>
          </figcaption>
          <div class="frame">
            <div class="yaxis"><span>{{ compact(max) }}</span><span>0</span></div>
            <div ref="plot" class="plot" @mouseleave="hide">
              <div class="cols">
                <div v-for="(r, i) in trend" :key="r.id" class="col" tabindex="0" :aria-label="`${when(r.finished)}: ${r.total_ok} working, ${r.total_bad} not working, ${r.total_excluded || 0} excluded, ${r.total_unchecked} not checked yet`"
                     @mouseenter="show(i, $event)" @focus="show(i, $event)" @blur="hide">
                  <span v-for="s in segs(r)" :key="s.tone" class="seg" :class="[s.tone === 'excl' ? 'excl-fill' : s.tone, { top: s.top }]" :style="{ height: s.pct + '%' }" />
                </div>
              </div>
              <div v-if="tip" class="tip" :style="{ left: tip.left + 'px' }" role="status">
                <b>{{ when(trend[tip.i].finished) }}</b>
                <span>{{ trend[tip.i].label }} · {{ trend[tip.i].kind === 'manual' ? 'by hand' : 'automatic' }}</span>
                <span><i class="sw ok" />{{ trend[tip.i].total_ok.toLocaleString() }} working</span>
                <span><i class="sw bad" />{{ trend[tip.i].total_bad.toLocaleString() }} not working</span>
                <span><i class="sw excl-fill" />{{ (trend[tip.i].total_excluded || 0).toLocaleString() }} excluded</span>
                <span><i class="sw idle" />{{ trend[tip.i].total_unchecked.toLocaleString() }} not checked yet</span>
              </div>
            </div>
          </div>
          <div class="xaxis"><span>{{ when(trend[0].finished) }}</span><span>{{ when(trend[trend.length - 1].finished) }}</span></div>
        </figure>

        <div class="scroll">
          <table class="t">
            <thead><tr><th>Started</th><th>What was checked</th><th class="r">Streams</th><th class="r">Working</th><th class="r">Not working</th><th class="r">Took</th><th /></tr></thead>
            <tbody>
              <tr v-for="r in data.items" :key="r.id">
                <td class="nowrap" :title="when(r.started)">{{ rel(r.started) }}</td>
                <td>{{ r.label }} <span class="badge">{{ r.kind === 'manual' ? 'by hand' : 'automatic' }}</span>
                  <span v-if="r.stopped" class="badge warn">stopped early</span><span v-if="!r.finished" class="badge">running</span></td>
                <td class="r">{{ r.probed.toLocaleString() }}</td>
                <td class="r okc">{{ r.ok.toLocaleString() }}</td>
                <td class="r badc">{{ r.bad.toLocaleString() }}</td>
                <td class="r nowrap">{{ r.finished ? dur(r.finished - r.started) : '' }}</td>
                <td class="r"><button class="small-btn" :disabled="!r.probed" title="What this round found, channel by channel" @click="opened = r">Results</button></td>
              </tr>
            </tbody>
          </table>
        </div>
        <RoundModal :round="opened" @close="opened = null" />
        <Pager :page="page" :pages="pages" :total="data.total" :size="size" :count="data.items.length" :sizes="SIZES" @go="go" @size="setSize" />
      </template>
    </template>
  </div>
</template>

<style scoped>
.small { font-size:13px; margin:6px 0; }
.keep a, .small a { color:var(--accent); }
.chart { margin:12px 0 18px; }
.legend { display:flex; gap:6px 18px; flex-wrap:wrap; font-size:13px; margin-bottom:8px; align-items:center; }
.legend span { display:inline-flex; align-items:center; gap:6px; }
.sw { width:10px; height:10px; border-radius:3px; display:inline-block; }
.sw.ok, .seg.ok { background:var(--ok); } .sw.bad, .seg.bad { background:var(--bad); }
.sw.idle, .seg.idle { background:color-mix(in srgb, var(--muted) 45%, transparent); }
.frame { display:flex; gap:8px; }
.yaxis { display:flex; flex-direction:column; justify-content:space-between; align-items:flex-end; font-size:11px; color:var(--muted); font-variant-numeric:tabular-nums; min-width:34px; height:140px; }
.plot { position:relative; flex:1; height:140px; border-top:1px solid var(--line); border-bottom:1px solid var(--line); }
.cols { display:flex; align-items:flex-end; gap:4px; height:100%; }
.col { flex:1 1 0; max-width:24px; min-width:3px; height:100%; display:flex; flex-direction:column-reverse; gap:2px; cursor:default; border-radius:4px; }
.col:hover, .col:focus-visible { background:color-mix(in srgb, var(--accent) 10%, transparent); outline:none; }
.col:focus-visible { outline:2px solid var(--accent); outline-offset:1px; }
.seg { display:block; width:100%; min-height:2px; }
.seg.top { border-radius:4px 4px 0 0; }
.xaxis { display:flex; justify-content:space-between; margin:4px 0 0 42px; font-size:11px; color:var(--muted); }
.tip { position:absolute; bottom:calc(100% - 8px); transform:translateX(-50%); z-index:5; display:flex; flex-direction:column; gap:2px; min-width:210px; padding:8px 10px; font-size:12px; background:var(--panel); border:1px solid var(--line); border-radius:8px; box-shadow:0 6px 20px rgba(0,0,0,.2); pointer-events:none; }
.tip span { display:flex; align-items:center; gap:6px; color:var(--muted); }
.tip b { color:var(--text); font-weight:600; }
.scroll { overflow-x:auto; }
.t { width:100%; border-collapse:collapse; font-size:13px; }
.t th { font-size:12px; font-weight:500; color:var(--muted); }
.t td, .t th { padding:8px 10px 8px 0; border-bottom:1px solid var(--line); text-align:left; }
.t .r { text-align:right; padding-right:12px; font-variant-numeric:tabular-nums; }
.small-btn { height:30px; padding:0 10px; font-size:12px; }
.nowrap { white-space:nowrap; }
.okc { color:var(--ok); } .badc { color:var(--bad); }
.badge { margin:0 0 0 6px; }
</style>
