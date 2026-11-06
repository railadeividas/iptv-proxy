<script setup>
import { computed } from 'vue'
// A horizontal bar split into coloured segments. parts: [{ value, tone, label }]
const props = defineProps({ parts: Array, height: { type: Number, default: 10 } })
const total = computed(() => props.parts.reduce((a, p) => a + p.value, 0))
const segs = computed(() => props.parts.filter((p) => p.value > 0).map((p) => ({ ...p, pct: (p.value / total.value) * 100 })))
</script>

<template>
  <div class="stack" :style="{ height: height + 'px' }" role="img" :aria-label="parts.map((p) => p.value.toLocaleString() + ' ' + p.label).join(', ')">
    <span v-for="s in segs" :key="s.label" class="seg" :class="s.tone" :style="{ width: s.pct + '%' }" :title="s.label + ': ' + s.value.toLocaleString()" />
  </div>
</template>

<style scoped>
.stack { display:flex; width:100%; border-radius:999px; overflow:hidden; background:var(--bg); border:1px solid var(--line); box-sizing:content-box; }
.seg { display:block; height:100%; min-width:2px; }
.seg.ok { background:var(--ok); } .seg.bad { background:var(--bad); } .seg.idle { background:color-mix(in srgb, var(--muted) 45%, transparent); }
.seg.warn { background:var(--warn); } .seg.accent { background:var(--accent); }
.seg.excl { background:repeating-linear-gradient(135deg, color-mix(in srgb, var(--muted) 60%, transparent) 0 3px, transparent 3px 6px); }
</style>
