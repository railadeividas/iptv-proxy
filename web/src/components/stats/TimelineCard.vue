<script setup>
import MeasureSwitch from './MeasureSwitch.vue'
import Scope from './Scope.vue'
import { ref, computed } from 'vue'
import StatChart from './StatChart.vue'
import { useStatsQuery, history, bucketWord, METRICS, axisOf, seriesFor, chartRows, completeSeries, perBucket, maxOf } from '../../stats'

// The main chart: the chosen measure over time, as bars, area or lines, optionally split and set against the previous period.
// t / prev are /api/stats/timeline answers.
const props = defineProps({ t: Object, prev: Object, profiles: { type: Array, default: () => [] }, title: String })
const q = useStatsQuery()
const table = ref(false)

const M = computed(() => METRICS[q.metric.value])
const base = computed(() => seriesFor(q.split.value, props.profiles, q.metric.value, q.selected.value))
const rows = computed(() => (props.t ? chartRows(props.t, q.metric.value, base.value) : []))
const series = computed(() => completeSeries(base.value, rows.value))
const prevValues = computed(() => (q.compare.value && props.prev ? perBucket(props.prev, q.metric.value) : null))
const ax = computed(() => axisOf(q.metric.value, maxOf(rows.value, series.value.keys, q.type.value !== 'line', prevValues.value || [])))
const total = (r) => series.value.keys.reduce((a, k) => a + r.v[k], 0)
const per = computed(() => (props.t ? `per ${bucketWord(props.t.bucket)}` : ''))
</script>

<template>
  <div class="card">
    <div class="head">
      <h2>{{ title || `${M.label} over time` }}<Scope /> <span class="muted small">{{ per }}</span></h2>
      <div class="ctl">
        <MeasureSwitch />
        <select :value="q.type.value" aria-label="Chart type" @change="q.setQuery({ type: $event.target.value === 'bars' ? '' : $event.target.value })">
          <option value="bars">Bars</option><option value="area">Area</option><option value="line">Lines</option>
        </select>
        <select :value="q.split.value" aria-label="Split by" @change="q.setQuery({ split: $event.target.value === 'profile' ? '' : $event.target.value })">
          <option value="profile">By profile</option><option value="mode">Proxy and redirect</option><option value="none">No split</option>
        </select>
        <span v-if="q.compareBlocked.value" class="muted small nocmp" :title="`History is kept for ${history} days`">No earlier history to compare with</span>
        <label v-else-if="q.canCompare.value" class="chk"><input type="checkbox" :checked="q.compare.value" @change="q.setQuery({ compare: $event.target.checked ? '1' : '' })"> Compare with the period before</label>
        <button type="button" :class="{ on: table }" @click="table = !table">{{ table ? 'Chart only' : 'Table' }}</button>
      </div>
    </div>
    <p v-if="prevValues && q.coverage.value === 'partial'" class="muted small partial">The period before reaches past the {{ history }} days of stored history, so its numbers are incomplete.</p>
    <div v-if="series.keys.length > 1 || prevValues" class="legend">
      <span v-for="k in series.keys.length > 1 ? series.keys : []" :key="k"><i :style="{ background: series.colors[k] }"></i>{{ series.names[k] }}</span>
      <span v-if="prevValues"><i class="dash"></i>Period before</span>
    </div>
    <StatChart :rows="rows" :keys="series.keys" :names="series.names" :colors="series.colors" :type="q.type.value" :fmt="M.fmt" :axis="ax.axis" :scale="ax.scale"
      :int="q.metric.value === 'sessions'" :prev="prevValues" :label="`${M.label} over time`" />
    <div v-if="table" class="scroll tbl">
      <table class="t">
        <thead><tr><th>Time</th><th v-for="k in series.keys" :key="k" class="r">{{ series.names[k] }}</th><th v-if="series.keys.length > 1" class="r">Total</th></tr></thead>
        <tbody>
          <tr v-for="r in [...rows].reverse()" :key="r.tt">
            <td class="nowrap">{{ r.tt }}</td>
            <td v-for="k in series.keys" :key="k" class="r">{{ M.fmt(r.v[k]) }}</td>
            <td v-if="series.keys.length > 1" class="r">{{ M.fmt(total(r)) }}</td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>

<style scoped>
.head { display:flex; justify-content:space-between; align-items:center; gap:10px; flex-wrap:wrap; margin-bottom:8px; }
.head h2 { margin:0; }
.ctl { display:flex; gap:8px; flex-wrap:wrap; align-items:center; }
.partial { margin:0 0 8px; }
.nocmp { max-width:220px; line-height:1.2; }
.chk { display:inline-flex; align-items:center; gap:6px; font-size:13px; color:var(--muted); }
button.on { border-color:var(--accent); }
.legend { display:flex; flex-wrap:wrap; gap:4px 14px; margin:0 0 8px; font-size:12px; }
.legend span { display:inline-flex; align-items:center; gap:6px; }
.legend i { width:10px; height:10px; border-radius:2px; display:inline-block; }
.legend i.dash { height:0; width:16px; border-top:2px dashed var(--muted); border-radius:0; }
.small { font-size:12px; font-weight:400; }
.tbl { margin-top:12px; max-height:320px; overflow-y:auto; }
.t { width:100%; border-collapse:collapse; font-size:13px; }
.t th { font-size:12px; font-weight:500; color:var(--muted); position:sticky; top:0; background:var(--panel); }
.t td, .t th { padding:6px 10px 6px 0; border-bottom:1px solid var(--line); text-align:left; }
.t td.r, .t th.r { text-align:right; font-variant-numeric:tabular-nums; }
.nowrap { white-space:nowrap; }
.scroll { overflow-x:auto; }
</style>
