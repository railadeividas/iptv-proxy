<script setup>
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { api } from '../api'
import { toast } from '../store'

// Run controls of the health checker: what to check now, pause and stop.
const props = defineProps({ h: Object, running: Boolean })
const emit = defineEmits(['changed'])

const open = ref(false), profiles = ref([]), busy = ref(false)
const root = ref(null)

const state = computed(() => {
  if (props.running) return { cls: 'run', text: 'Checking' }
  if (props.h?.paused) return { cls: 'off', text: 'Paused' }
  if (props.h?.quiet) return { cls: 'off', text: 'Quiet hours' }
  if (props.h?.scope === 'off') return { cls: 'off', text: 'Off' }
  return { cls: 'idle', text: 'Idle' }
})
const n = (x) => (x ?? 0).toLocaleString()

async function toggle() {
  open.value = !open.value
  if (open.value) { try { profiles.value = await api.get('/profiles') } catch {} }
}
const close = () => { open.value = false }
const onDoc = (e) => { if (root.value && !root.value.contains(e.target)) close() }
const onKey = (e) => { if (e.key === 'Escape') close() }
onMounted(() => { document.addEventListener('click', onDoc); document.addEventListener('keydown', onKey) })
onUnmounted(() => { document.removeEventListener('click', onDoc); document.removeEventListener('keydown', onKey) })

async function run(kind, id = '') {
  close()
  busy.value = true
  try {
    const r = await api.post('/health/run', { kind, id: String(id) })
    toast(`Checking ${r.streams.toLocaleString()} stream${r.streams === 1 ? '' : 's'}…`)
    emit('changed')
  } catch (e) { toast(e.message) } finally { busy.value = false }
}
async function stop() { try { await api.post('/health/stop'); toast('Stopping…'); emit('changed') } catch (e) { toast(e.message) } }
async function pause(p) { try { await api.post('/health/pause', { paused: p }); emit('changed') } catch (e) { toast(e.message) } }
</script>

<template>
  <div class="controls">
    <div class="row1">
      <div ref="root" class="menu">
        <button :disabled="running || busy" aria-haspopup="menu" :aria-expanded="open" @click.stop="toggle">Check now <span aria-hidden="true">▾</span></button>
        <div v-if="open" class="pop" role="menu">
          <p class="grp first">What to check</p>
          <button role="menuitem" class="it" :disabled="!h?.profiles.channels" @click="run('profiles')">Channels in profiles <span class="muted">{{ n(h?.profiles.streams) }} streams</span></button>
          <button role="menuitem" class="it" @click="run('all')">Every stream <span class="muted">{{ n(h?.totals.streams) }}</span></button>
          <button role="menuitem" class="it" :disabled="!h?.totals.bad" @click="run('bad')">Only streams marked not working <span class="muted">{{ n(h?.totals.bad) }}</span></button>
          <button role="menuitem" class="it" :disabled="!h?.totals.unchecked" @click="run('unchecked')">Only streams not checked yet <span class="muted">{{ n(h?.totals.unchecked) }}</span></button>
          <hr class="sep">
          <p class="grp">A profile</p>
          <p v-if="!profiles.length" class="muted none">No profiles yet.</p>
          <button v-for="p in profiles" :key="p.id" role="menuitem" class="it" @click="run('profile', p.id)">{{ p.name }} <span class="muted">{{ p.count }} channels</span></button>
          <hr class="sep">
          <p class="grp">A source</p>
          <button v-for="s in h?.sources" :key="s.id" role="menuitem" class="it" @click="run('source', s.id)">{{ s.name }} <span class="muted">{{ n(s.streams) }}</span></button>
        </div>
      </div>

      <button v-if="running" class="danger" @click="stop">Stop</button>
      <button v-if="h?.paused" class="primary" @click="pause(false)">Resume automatic checks</button>
      <button v-else @click="pause(true)">Pause automatic checks</button>

      <span class="state" :class="state.cls"><span class="dot" :class="{ pulse: running }" />{{ state.text }}</span>
    </div>
  </div>
</template>

<style scoped>
.controls { display:flex; flex-direction:column; gap:10px; align-items:flex-end; }
.row1 { display:flex; align-items:center; gap:10px; flex-wrap:wrap; justify-content:flex-end; }
.state { display:inline-flex; align-items:center; gap:6px; font-size:12px; padding:3px 10px; border-radius:999px; border:1px solid var(--line); color:var(--muted); background:var(--bg); }
.state .dot { margin:0; width:8px; height:8px; }
.state.idle .dot { background:var(--ok); }
.state.run { color:var(--accent); border-color:var(--accent); } .state.run .dot { background:var(--accent); }
.state.off .dot { background:var(--muted); }
.dot.pulse { animation:pulse 1.2s ease-in-out infinite; }
@keyframes pulse { 50% { opacity:.3; } }
@media (prefers-reduced-motion: reduce) { .dot.pulse { animation:none; } }
.menu { position:relative; }
.pop { position:absolute; right:0; top:calc(100% + 6px); z-index:30; min-width:300px; max-height:70vh; overflow:auto; padding:6px; background:var(--panel); border:1px solid var(--line); border-radius:10px; box-shadow:0 8px 28px rgba(0,0,0,.2); }
.it { display:flex; width:100%; height:auto; min-height:34px; padding:6px 10px; justify-content:space-between; gap:16px; border:0; background:none; text-align:left; border-radius:6px; }
.it:hover:not(:disabled) { background:var(--bg); border:0; }
.sep { border:0; border-top:1px solid var(--line); margin:6px 0 0; }
.grp.first { margin-top:4px; }
.grp { margin:8px 10px 2px; font-size:11px; text-transform:uppercase; letter-spacing:.04em; color:var(--muted); }
.none { margin:2px 10px 6px; font-size:13px; }
@media (max-width:760px) { .controls { align-items:flex-start; } .row1 { justify-content:flex-start; } .pop { right:auto; left:0; min-width:min(300px, calc(100vw - 48px)); } }
</style>
