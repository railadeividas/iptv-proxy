<script setup>
import { ref } from 'vue'
import { api } from '../api'
import { ask, toast } from '../store'

// Deleting all stored stats (the superuser's Settings > Stats tab). Always asks first: it cannot be undone.
const busy = ref(false)
const n = (v) => (v || 0).toLocaleString()

async function clear() {
  const ok = await ask({
    title: 'Delete all stored stats?',
    message: 'Every stored session, device and hourly cache and fetch record of every profile will be deleted. Profiles, settings and playlist links are not touched. This cannot be undone.',
    confirmText: 'Delete everything',
    danger: true,
  })
  if (!ok) return
  busy.value = true
  try {
    const r = await api.post('/stats/clear', { older_than_days: 0 })
    toast(`Deleted ${n(r.sessions)} sessions, ${n(r.devices)} devices and ${n(r.samples)} hourly records`)
  } catch (e) { toast(e.message) } finally { busy.value = false }
}
</script>

<template>
  <div class="card clear">
    <div class="text">
      <h2>Delete stored stats</h2>
      <p class="muted">Removes sessions, devices and the hourly cache and fetch history of all profiles, to start counting again. Profiles, settings and playlist links are not touched, and what is being watched right now keeps playing.</p>
    </div>
    <button class="danger" :disabled="busy" @click="clear">Delete everything…</button>
  </div>
</template>

<style scoped>
.clear { display:flex; align-items:center; gap:16px 24px; }
.text { flex:1; min-width:0; }
.text h2 { margin:0 0 4px; }
.text p { margin:0; }
.clear button { flex:none; }
@media (max-width:640px) { .clear { flex-direction:column; align-items:stretch; } }
</style>
