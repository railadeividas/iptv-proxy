<script setup>
import { computed } from 'vue'
import { useStatsQuery, STEP_OPTIONS, STEPS, MAX_BARS, bucketWord } from '../../stats'

// How finely the charts are cut: per hour, day or week. "Auto" picks the finest that gives about 120 bars for the range.
// The choice is kept in the URL, so every chart of the tab follows it. bucket = the step the data actually came in.
const props = defineProps({ bucket: { type: Number, default: 0 }, min: { type: Number, default: 0 } }) // min: the finest step the tab's data comes in
const q = useStatsQuery()
const span = computed(() => { q.rangeKey.value; const r = q.range(); return r.to - r.from })
// a step longer than the range, or one that would need too many bars, cannot be chosen
const usable = (key) => !key || (span.value >= STEPS[key] && span.value / STEPS[key] <= MAX_BARS)
const shown = computed(() => (props.bucket ? Math.max(props.bucket, props.min) : 0))
const label = (o) => (o[0] === '' ? `Auto${shown.value ? ` (per ${bucketWord(shown.value)})` : ''}` : `Per ${o[1].toLowerCase()}`)
const raised = computed(() => q.stepKey.value && shown.value && shown.value !== STEPS[q.stepKey.value])
</script>

<template>
  <label class="f" :title="raised ? `That would be too many bars for this range: showing per ${bucketWord(shown)}.` : 'How finely every chart of this tab is cut'">
    <span>Group by</span>
    <select :value="q.stepKey.value" aria-label="Group charts by" @change="q.setQuery({ step: $event.target.value })">
      <option v-for="o in STEP_OPTIONS" :key="o[0]" :value="o[0]" :disabled="!usable(o[0])">{{ label(o) }}</option>
    </select>
  </label>
</template>

<style scoped>
.f { display:flex; flex-direction:column; gap:4px; min-width:0; max-width:100%; }
.f > span { font-size:11px; text-transform:uppercase; letter-spacing:.04em; color:var(--muted); }
</style>
