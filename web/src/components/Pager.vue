<script setup>
import { computed } from 'vue'
// page is 0-based. With total/size/count it also shows the visible range and
// (with sizes) a per-page selector, so one bar holds everything about paging.
const props = defineProps({
  page: Number, pages: Number,
  total: { type: Number, default: null }, size: Number, count: { type: Number, default: 0 },
  sizes: { type: Array, default: null },
})
const emit = defineEmits(['go', 'size'])
const items = computed(() => {
  const set = new Set([0, props.pages - 1])
  for (let i = props.page - 2; i <= props.page + 2; i++) if (i >= 0 && i < props.pages) set.add(i)
  const sorted = [...set].sort((a, b) => a - b)
  const out = []
  sorted.forEach((n, i) => {
    if (i && n - sorted[i - 1] > 1) out.push({ gap: true, key: 'g' + n })
    out.push({ n, key: n })
  })
  return out
})
const range = computed(() => {
  if (props.total == null) return ''
  if (!props.total) return '0 of 0'
  const from = props.page * props.size + 1
  return `${from.toLocaleString()}–${(from - 1 + props.count).toLocaleString()} of ${props.total.toLocaleString()}`
})
const show = computed(() => props.pages > 1 || (props.total != null && props.total > 0))
</script>

<template>
  <nav v-if="show" class="pager" aria-label="Pagination">
    <span class="range muted">{{ range }}</span>
    <div class="pages">
      <template v-if="pages > 1">
        <button :disabled="page === 0" @click="emit('go', page - 1)">‹ Previous</button>
        <template v-for="i in items" :key="i.key">
          <span v-if="i.gap" class="muted">…</span>
          <button v-else :class="{ primary: i.n === page }" :aria-current="i.n === page ? 'page' : null" @click="emit('go', i.n)">{{ i.n + 1 }}</button>
        </template>
        <button :disabled="page >= pages - 1" @click="emit('go', page + 1)">Next ›</button>
      </template>
    </div>
    <label v-if="sizes" class="per">Per page
      <select :value="size" @change="emit('size', +$event.target.value)">
        <option v-for="n in sizes" :key="n" :value="n">{{ n }}</option>
      </select>
    </label>
  </nav>
</template>

<style scoped>
.pager { display:grid; grid-template-columns:1fr auto 1fr; align-items:center; gap:10px 16px; margin:16px 0; }
.range { justify-self:start; font-variant-numeric:tabular-nums; }
.pages { display:flex; gap:6px; flex-wrap:wrap; align-items:center; justify-content:center; }
.pages button { min-width:36px; }
.per { justify-self:end; display:inline-flex; align-items:center; gap:8px; color:var(--muted); }
.per select { color:var(--text); }
@media (max-width:760px) {
  .pager { grid-template-columns:1fr auto; }
  .pages { grid-column:1 / -1; grid-row:1; }
}
</style>
