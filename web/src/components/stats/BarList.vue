<script setup>
import { computed } from 'vue'
// Ranked horizontal bars: name, bar, value (and an optional change). rows: [{ name, v, c?: colour, chg?: text, title? }]
const props = defineProps({
  rows: { type: Array, default: () => [] },
  fmt: { type: Function, required: true },
  color: { type: String, default: 'var(--s1)' },
  empty: { type: String, default: 'Nothing in this period.' },
  change: Boolean, // reserve a column for chg
})
const max = computed(() => Math.max(1, ...props.rows.map((r) => r.v)))
</script>

<template>
  <p v-if="!rows.length" class="muted small empty">{{ empty }}</p>
  <div v-else class="bars">
    <div v-for="r in rows" :key="r.key ?? r.name" class="row" :class="{ chg: change }" :title="r.title || `${r.name}: ${fmt(r.v)}`">
      <span class="name">{{ r.name }}</span>
      <span class="track"><i :style="{ width: Math.max(1.5, (r.v / max) * 100) + '%', background: r.c || color }"></i></span>
      <span class="val">{{ fmt(r.v) }}</span>
      <span v-if="change" class="delta muted">{{ r.chg }}</span>
    </div>
  </div>
</template>

<style scoped>
.bars { display:flex; flex-direction:column; gap:8px; }
.row { display:grid; grid-template-columns:minmax(80px, 38%) 1fr auto; gap:10px; align-items:center; }
.row.chg { grid-template-columns:minmax(80px, 34%) 1fr auto 52px; }
.name { white-space:nowrap; overflow:hidden; text-overflow:ellipsis; }
.track { height:10px; background:color-mix(in srgb, var(--muted) 14%, transparent); border-radius:5px; overflow:hidden; }
.track i { display:block; height:100%; border-radius:5px; }
.val { font-variant-numeric:tabular-nums; text-align:right; min-width:48px; }
.delta { font-size:11px; text-align:right; font-variant-numeric:tabular-nums; white-space:nowrap; }
.empty { margin:0; padding:6px 0; }
.small { font-size:12px; }
</style>
