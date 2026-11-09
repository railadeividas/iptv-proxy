<script setup>
import { computed } from 'vue'
import { useStatsQuery, fmtStamp } from '../../stats'

// Says which time a number or chart covers, so "this period", "right now" and "all time" are never mixed up:
//   period (blue)  follows the Time range at the top
//   now (green)    what is happening at this moment, whatever the period
//   ever (grey)    counted since recording began / since the app started, whatever the period
const props = defineProps({ kind: { type: String, default: 'period' }, text: { type: String, default: '' } })
const q = useStatsQuery()
const label = computed(() => props.text || ({
  period: q.rangeLabel.value,
  now: 'Right now',
  ever: 'All time',
  start: 'Since the app started',
})[props.kind])
const title = computed(() => ({
  period: `${(() => { const r = q.range(); return `${fmtStamp(r.from)} – ${fmtStamp(r.to)}` })()}: follows the Time range at the top`,
  now: 'What is happening at this moment, whatever the time range',
  ever: 'Counted since recording began, whatever the time range',
  start: 'Counted since the app started, whatever the time range',
})[props.kind])
</script>

<template><span class="scope" :class="kind" :title="title">{{ label }}</span></template>

<style scoped>
.scope { display:inline-block; font-size:11px; font-weight:500; line-height:1; padding:3px 7px; border-radius:999px; border:1px solid; white-space:nowrap; vertical-align:middle; margin-left:6px; }
.scope.period { color:var(--accent); border-color:color-mix(in srgb, var(--accent) 45%, transparent); background:color-mix(in srgb, var(--accent) 10%, transparent); }
.scope.now { color:var(--ok); border-color:color-mix(in srgb, var(--ok) 45%, transparent); background:color-mix(in srgb, var(--ok) 10%, transparent); }
.scope.ever, .scope.start { color:var(--muted); border-color:var(--line); background:transparent; }
</style>
