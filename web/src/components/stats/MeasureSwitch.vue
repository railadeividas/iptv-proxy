<script setup>
import { useStatsQuery } from '../../stats'

// Sessions / Data / Watch time for the card it sits in. The choice is kept in the URL, so every card of the tab that
// follows the measure changes together.
const q = useStatsQuery()
const OPTIONS = [['sessions', 'Sessions'], ['bytes', 'Data'], ['seconds', 'Watch time']]
</script>

<template>
  <div class="seg" role="group" aria-label="Measure">
    <button v-for="o in OPTIONS" :key="o[0]" type="button" :class="{ on: q.metric.value === o[0] }" :aria-pressed="q.metric.value === o[0]"
      @click="q.setQuery({ metric: o[0] === 'sessions' ? '' : o[0], page: '' })">{{ o[1] }}</button>
  </div>
</template>

<style scoped>
.seg { display:inline-flex; border:1px solid var(--line); border-radius:6px; overflow:hidden; height:var(--ctl); flex:none; }
.seg button { border:0; border-radius:0; background:var(--panel); padding:0 12px; border-right:1px solid var(--line); height:100%; }
.seg button:last-child { border-right:0; }
.seg button.on { background:var(--accent); color:#fff; }
</style>
