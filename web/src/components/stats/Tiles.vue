<script setup>
import Scope from './Scope.vue'
import Spark from './Spark.vue'
// A row of headline numbers. items: [{ label, value, sub?, tone?: 'ok'|'bad', title?, spark?: number[], pulse?, gauge?: 0..1 }]
defineProps({ items: { type: Array, required: true }, cols: { type: Number, default: 4 } })
</script>

<template>
  <div class="tiles" :style="{ '--cols': cols }">
    <div v-for="t in items" :key="t.label" class="tile" :class="t.tone" :title="t.title">
      <span class="lbl"><span v-if="t.pulse" class="pulse"></span>{{ t.label }}<Scope v-if="t.scope" :kind="t.scope" :text="t.scopeText" /></span>
      <div class="line"><b>{{ t.value }}</b><Spark v-if="t.spark" :values="t.spark" /></div>
      <div v-if="t.gauge != null" class="gauge"><i :style="{ width: Math.min(100, t.gauge * 100) + '%' }"></i></div>
      <span v-if="t.sub" class="sub muted">{{ t.sub }}</span>
    </div>
  </div>
</template>

<style scoped>
.tiles { display:grid; grid-template-columns:repeat(var(--cols), minmax(0, 1fr)); gap:12px; }
@media (max-width:1000px) { .tiles { grid-template-columns:repeat(min(var(--cols), 3), minmax(0, 1fr)); } }
@media (max-width:560px) { .tiles { grid-template-columns:repeat(2, minmax(0, 1fr)); } }
.tile { background:var(--panel); border:1px solid var(--line); border-left:3px solid var(--line); border-radius:10px; padding:12px 14px; display:flex; flex-direction:column; gap:2px; min-width:0; }
.tile.ok { border-left-color:var(--ok); } .tile.bad { border-left-color:var(--bad); }
.lbl { display:flex; align-items:center; flex-wrap:wrap; gap:2px 6px; font-size:12px; color:var(--muted); }
.line { display:flex; align-items:flex-end; justify-content:space-between; gap:8px; flex-wrap:wrap; }
b { font-size:22px; line-height:1.2; font-variant-numeric:tabular-nums; white-space:nowrap; }
.sub { font-size:12px; font-variant-numeric:tabular-nums; }
.gauge { height:6px; border-radius:3px; background:color-mix(in srgb, var(--muted) 14%, transparent); overflow:hidden; margin-top:4px; }
.gauge i { display:block; height:100%; background:var(--accent); border-radius:3px; }
.pulse { width:8px; height:8px; border-radius:50%; background:var(--ok); animation:pulse 1.8s infinite; }
@keyframes pulse { 0% { box-shadow:0 0 0 0 color-mix(in srgb, var(--ok) 60%, transparent); } 70% { box-shadow:0 0 0 7px transparent; } 100% { box-shadow:0 0 0 0 transparent; } }
@media (prefers-reduced-motion: reduce) { .pulse { animation:none; } }
</style>
