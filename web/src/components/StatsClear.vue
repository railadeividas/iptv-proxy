<script setup>
import { ref } from 'vue'
import { api } from '../api'
import { ask, toast } from '../store'

// Deleting stored stats (the superuser's Settings > Stats tab). Always asks first: it cannot be undone.
const days = ref(90)
const busy = ref(false)
const n = (v) => (v || 0).toLocaleString()

async function clear(everything) {
  const d = everything ? 0 : Math.round(Number(days.value))
  if (!everything && !(d >= 1 && d <= 3650)) { toast('Enter a number of days from 1 to 3650'); return }
  const ok = await ask({
    title: everything ? 'Delete all stored stats?' : `Delete stats older than ${d} ${d === 1 ? 'day' : 'days'}?`,
    message: (everything ? 'Every stored session, device and hourly cache and fetch record of every profile will be deleted.' : `Sessions, devices and hourly records from before ${d} ${d === 1 ? 'day' : 'days'} ago will be deleted.`)
      + ' Profiles, settings and playlist links are not touched. This cannot be undone.',
    confirmText: everything ? 'Delete everything' : 'Delete',
    danger: true,
  })
  if (!ok) return
  busy.value = true
  try {
    const r = await api.post('/stats/clear', { older_than_days: d })
    toast(`Deleted ${n(r.sessions)} sessions, ${n(r.devices)} devices and ${n(r.samples)} hourly records`)
  } catch (e) { toast(e.message) } finally { busy.value = false }
}
</script>

<template>
  <div class="card">
    <h2>Delete stored stats</h2>
    <p class="muted sub">Removes sessions, devices and the hourly cache and fetch history of all profiles, to start counting again. Profiles, settings and playlist links are not touched, and what is being watched right now keeps playing.</p>
    <div class="row">
      <span>Older than</span>
      <input v-model="days" type="number" min="1" max="3650" step="1" aria-label="Days" :disabled="busy">
      <span>days</span>
      <button class="danger" :disabled="busy" @click="clear(false)">Delete older stats…</button>
      <span class="sp"></span>
      <button class="danger" :disabled="busy" @click="clear(true)">Delete everything…</button>
    </div>
  </div>
</template>

<style scoped>
.sub { margin:0 0 12px; }
.row { display:flex; align-items:center; gap:8px; flex-wrap:wrap; }
.card .row input[type=number] { width:100px; flex:none; }
.sp { flex:1; }
@media (max-width:560px) { .sp { display:none; } }
</style>
