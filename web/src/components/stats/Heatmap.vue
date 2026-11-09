<script setup>
import { computed } from 'vue'
// Weekday by hour of day. cells: [{ d: 0=Sunday..6, h: 0..23, value }]; shows Monday first. fmt writes a value.
const props = defineProps({ cells: { type: Array, default: () => [] }, fmt: { type: Function, required: true } })
const DAYS = ['Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat', 'Sun']
const grid = computed(() => {
  const g = DAYS.map(() => Array(24).fill(0))
  for (const c of props.cells) g[(c.d + 6) % 7][c.h] += c.value
  return g
})
const max = computed(() => Math.max(1, ...grid.value.flat()))
const shade = (v) => `color-mix(in srgb, var(--accent) ${Math.round(8 + (v / max.value) * 92)}%, var(--panel))`
</script>

<template>
  <div class="heat" role="img" aria-label="Activity by weekday and hour">
    <span></span>
    <span v-for="h in 24" :key="'h' + h" class="cl">{{ (h - 1) % 6 === 0 ? String(h - 1).padStart(2, '0') : '' }}</span>
    <template v-for="(row, r) in grid" :key="r">
      <span class="rl">{{ DAYS[r] }}</span>
      <i v-for="(v, h) in row" :key="h" class="cell" :title="`${DAYS[r]} ${String(h).padStart(2, '0')}:00 · ${fmt(v)}`" :style="{ background: shade(v) }"></i>
    </template>
  </div>
  <div class="ramp muted">less<i></i>more</div>
</template>

<style scoped>
.heat { display:grid; grid-template-columns:34px repeat(24, minmax(0, 1fr)); gap:2px; align-items:center; }
.cell { display:block; aspect-ratio:1; min-height:10px; border-radius:3px; }
.rl, .cl { font-size:11px; color:var(--muted); white-space:nowrap; }
.ramp { display:flex; align-items:center; gap:6px; margin-top:8px; font-size:11px; }
.ramp i { width:90px; height:8px; border-radius:4px; background:linear-gradient(90deg, color-mix(in srgb, var(--accent) 8%, var(--panel)), var(--accent)); }
</style>
