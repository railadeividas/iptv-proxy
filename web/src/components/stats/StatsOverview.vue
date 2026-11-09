<script setup>
import MeasureSwitch from './MeasureSwitch.vue'
import Scope from './Scope.vue'
import { ref, computed } from 'vue'
import { api } from '../../api'
import { dur, bytes } from '../../util'
import { useStatsQuery, useLoader, bucketWord, METRICS, perBucket, numberRows, bucketTitle, change } from '../../stats'
import StatChart from './StatChart.vue'
import Heatmap from './Heatmap.vue'
import TimelineCard from './TimelineCard.vue'
import Tiles from './Tiles.vue'

// The summary of the selected time range: headline numbers, the timeline, the most viewers at once, how long sessions last, the busiest hours and some records.
const props = defineProps({ o: Object })
const q = useStatsQuery()
const t = ref(null), prevT = ref(null), pat = ref(null), prevO = ref(null)

useLoader(async (cur) => {
  const cmp = q.compare.value
  const [tl, ptl, pa, po] = await Promise.all([
    api.get('/stats/timeline?' + q.params()),
    cmp ? api.get('/stats/timeline?' + q.params({}, true)) : null,
    api.get('/stats/patterns?' + q.params()),
    cmp ? api.get('/stats?' + q.params({}, true)) : null,
  ])
  if (!cur()) return
  t.value = tl; prevT.value = ptl; pat.value = pa; prevO.value = po
}, { deps: () => [q.rangeKey.value, q.stepKey.value, q.profile.value, q.compare.value] })

const M = computed(() => METRICS[q.metric.value])
const peakRows = computed(() => (t.value ? numberRows(t.value, t.value.peaks) : []))
const LENGTHS = ['under 5 min', '5–15 min', '15–30 min', '30–60 min', '1–2 h', 'over 2 h']
const lengthRows = computed(() => (pat.value ? pat.value.durations.map((n, i) => ({ l: LENGTHS[i].replace('under ', '< ').replace('over ', '> '), tt: `Sessions of ${LENGTHS[i]}`, v: { n } })) : []))
const cells = computed(() => (pat.value?.heat || []).map((c) => ({ d: c.d, h: c.h, value: M.value.pick(c) })))
const unit = computed(() => bucketWord(t.value?.bucket))
function best(metric) {
  if (!t.value) return null
  const v = perBucket(t.value, metric)
  const i = v.indexOf(Math.max(...v))
  return v[i] > 0 ? { v: v[i], title: bucketTitle(t.value, i) } : null
}
// the headline numbers of the period, with the change against the period before when comparing
const tot = computed(() => props.o?.totals || {})
const tiles = computed(() => {
  const x = tot.value, p = prevO.value?.totals || {}, c = q.compare.value
  const avg = (v) => (v.proxy ? v.seconds / v.proxy : 0)
  const sub = (a, b) => change(a, b, c)
  return [
    { label: 'Sessions', scope: 'period', value: (x.sessions || 0).toLocaleString(), sub: sub(x.sessions, p.sessions), spark: t.value && perBucket(t.value, 'sessions'), title: 'A device watching one channel is one session' },
    { label: 'Data served', scope: 'period', value: bytes(x.bytes || 0), sub: sub(x.bytes, p.bytes), spark: t.value && perBucket(t.value, 'bytes'), title: 'Video sent to players through the proxy' },
    { label: 'Watched', scope: 'period', value: dur(x.seconds || 0), sub: sub(x.seconds, p.seconds), spark: t.value && perBucket(t.value, 'seconds'), title: 'Total time of the proxied sessions' },
    { label: 'Average session', scope: 'period', value: dur(avg(x)), sub: sub(avg(x), avg(p)), title: 'Average watching time of a proxied session' },
    { label: 'Peak at once', scope: 'period', value: (t.value?.peak.max || 0).toLocaleString(), sub: change(t.value?.peak.max, prevT.value?.peak.max, c) || (t.value?.peak.max ? new Date(t.value.peak.at * 1000).toLocaleString() : ''), title: 'The most streams the proxy carried at the same moment' },
  ]
})
const records = computed(() => {
  const busy = best('sessions'), data = best('bytes'), long = pat.value?.longest
  return [
    { label: `Busiest ${unit.value}`, value: busy ? busy.title : '–', sub: busy ? `${busy.v.toLocaleString()} sessions` : '' },
    { label: `Most data in a ${unit.value}`, value: data ? bytes(data.v) : '–', sub: data?.title || '' },
    { label: 'Longest session', value: long ? dur(long.seconds) : '–', sub: long ? `${long.name} · ${long.profile}` : '' },
  ]
})
</script>

<template>
  <Tiles :items="tiles" :cols="5" />
  <TimelineCard :t="t" :prev="prevT" :profiles="o?.profiles || []" />
  <div class="card-row">
    <div class="card"><h2>Peak viewers at once<Scope /></h2><p class="muted small">The most streams the proxy carried together, per {{ unit }}</p>
      <StatChart :rows="peakRows" :keys="['n']" :names="{ n: 'Peak viewers' }" :colors="{ n: 'var(--s3)' }" type="line" int :fmt="(v) => v.toLocaleString()" :height="200" label="Peak viewers at once" /></div>
    <div class="card"><h2>Session length<Scope /></h2><p class="muted small">Proxied sessions by how long they lasted</p>
      <StatChart :rows="lengthRows" :keys="['n']" :names="{ n: 'Sessions' }" :colors="{ n: 'var(--s1)' }" type="bars" int :fmt="(v) => v.toLocaleString()" :height="200" :max-bar="48" :label-width="70" label="Session length" /></div>
  </div>
  <div class="card"><div class="chead"><h2>Busiest hours<Scope /></h2><MeasureSwitch /></div><p class="muted small">{{ M.label }} by weekday and hour of day (your time zone)</p>
    <Heatmap :cells="cells" :fmt="M.fmt" /></div>
  <div class="card"><h2>Records<Scope /></h2><p class="muted small">In the selected period</p>
    <div class="records">
      <div v-for="r in records" :key="r.label"><span class="muted small">{{ r.label }}</span><b>{{ r.value }}</b><span class="muted small">{{ r.sub }}</span></div>
    </div>
  </div>
</template>

<style scoped>
.small { font-size:13px; margin:0 0 10px; }
.records { display:grid; grid-template-columns:repeat(auto-fit, minmax(180px, 1fr)); gap:10px; }
.records > div { border:1px solid var(--line); border-radius:8px; padding:8px 12px; display:flex; flex-direction:column; min-width:0; }
.records .small { margin:0; }
.records b { font-size:16px; }
</style>
