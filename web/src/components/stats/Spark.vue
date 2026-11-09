<script setup>
import { computed } from 'vue'
// A tiny trend line with a light fill under it.
const props = defineProps({ values: { type: Array, default: () => [] }, color: { type: String, default: 'var(--s1)' }, w: { type: Number, default: 56 }, h: { type: Number, default: 26 } })
const pts = computed(() => {
  const v = props.values
  const mx = Math.max(...v, 1)
  return v.map((x, i) => `${(i / (v.length - 1 || 1)) * props.w},${props.h - 2 - (x / mx) * (props.h - 4)}`).join(' L')
})
</script>

<template>
  <svg v-if="values.length > 1" :width="w" :height="h" :viewBox="`0 0 ${w} ${h}`" aria-hidden="true">
    <path :d="`M${pts} L${w},${h} L0,${h} Z`" :fill="color" fill-opacity=".15" />
    <path :d="`M${pts}`" fill="none" :stroke="color" stroke-width="1.5" />
  </svg>
</template>
