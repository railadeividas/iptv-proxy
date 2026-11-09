<script setup>
import { ref, onUnmounted } from 'vue'
import { useStatsQuery, RANGES, DEFAULT_RANGE, fmtStamp } from '../../stats'

// The time range, as in Grafana: a button with the current range that opens a list of quick ranges and a from/to picker.
const q = useStatsQuery()
const open = ref(false)
const root = ref(null)
const trigger = ref(null)
const from = ref(''), to = ref(''), err = ref('')

const pad = (n) => String(n).padStart(2, '0')
const toInput = (t) => { const d = new Date(t * 1000); return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}` }
const fromInput = (v) => Math.floor(new Date(v).getTime() / 1000)
const away = (e) => { if (!root.value?.contains(e.target)) close() }
const key = (e) => { if (e.key === 'Escape') { close(); trigger.value?.focus() } }
function show() {
  const r = q.range()
  from.value = toInput(r.from); to.value = toInput(r.to); err.value = ''
  open.value = true
  document.addEventListener('mousedown', away)
  document.addEventListener('keydown', key)
}
function close() {
  open.value = false
  document.removeEventListener('mousedown', away)
  document.removeEventListener('keydown', key)
}
onUnmounted(close)
function pick(k) { q.setQuery({ range: k === DEFAULT_RANGE ? '' : k, from: '', to: '', page: '' }); close() }
function apply() {
  const f = fromInput(from.value), t = fromInput(to.value)
  if (!f || !t || Number.isNaN(f) || Number.isNaN(t)) { err.value = 'Enter both a start and an end.'; return }
  if (t <= f) { err.value = 'The end must be after the start.'; return }
  if (t - f > 3650 * 86400) { err.value = 'The range can be at most 10 years.'; return }
  q.setQuery({ range: '', from: f, to: t, page: '' })
  close()
}
const resolved = () => { const r = q.range(); return `${fmtStamp(r.from)} – ${fmtStamp(r.to)}` }
</script>

<template>
  <div ref="root" class="range">
    <button ref="trigger" type="button" class="trigger" aria-haspopup="dialog" :aria-expanded="open" :title="resolved()" @click="open ? close() : show()">
      <svg viewBox="0 0 16 16" width="14" height="14" aria-hidden="true"><circle cx="8" cy="8" r="6.5" fill="none" stroke="currentColor" stroke-width="1.4" /><path d="M8 4.5V8l2.4 1.6" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" /></svg>
      <span class="lbl">{{ q.rangeLabel.value }}</span>
      <svg viewBox="0 0 10 6" width="10" height="6" aria-hidden="true"><path d="M1 1l4 4 4-4" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" /></svg>
    </button>
    <div v-if="open" class="pop" role="dialog" aria-label="Time range">
      <form class="abs" @submit.prevent="apply">
        <h3>Absolute time range</h3>
        <label>From<input v-model="from" type="datetime-local" required></label>
        <label>To<input v-model="to" type="datetime-local" required></label>
        <p v-if="err" class="err small">{{ err }}</p>
        <button class="primary" type="submit">Apply time range</button>
        <p class="muted small">Your local time. The charts are cut in your time zone.</p>
      </form>
      <div class="quick">
        <h3>Quick ranges</h3>
        <ul>
          <li v-for="r in RANGES" :key="r[0]"><button type="button" :class="{ on: q.rangeKey.value === r[0] }" @click="pick(r[0])">{{ r[1] }}</button></li>
        </ul>
      </div>
    </div>
  </div>
</template>

<style scoped>
.range { position:relative; }
.trigger { gap:8px; padding:0 12px; }
.trigger .lbl { max-width:260px; overflow:hidden; text-overflow:ellipsis; }
.pop { position:absolute; z-index:30; top:calc(100% + 6px); left:0; width:min(560px, calc(100vw - 32px)); display:grid; grid-template-columns:1fr 1fr; background:var(--panel); border:1px solid var(--line); border-radius:10px; box-shadow:0 10px 32px rgba(0,0,0,.22); }
@media (max-width:560px) { .pop { grid-template-columns:1fr; } }
h3 { margin:0 0 8px; font-size:12px; text-transform:uppercase; letter-spacing:.04em; color:var(--muted); font-weight:500; }
.abs { padding:14px; display:flex; flex-direction:column; gap:10px; border-right:1px solid var(--line); }
@media (max-width:560px) { .abs { border-right:0; border-bottom:1px solid var(--line); } }
.abs label { display:flex; flex-direction:column; gap:4px; font-size:13px; color:var(--muted); }
.abs .small { margin:0; font-size:12px; }
.quick { padding:14px 6px 8px 14px; min-width:0; }
.quick ul { list-style:none; margin:0; padding:0 8px 0 0; max-height:300px; overflow-y:auto; overflow-x:hidden; }
.quick button { width:100%; justify-content:flex-start; height:auto; border:0; border-radius:6px; background:none; padding:6px 10px; }
.quick button:hover { background:color-mix(in srgb, var(--accent) 10%, transparent); }
.quick button.on { background:color-mix(in srgb, var(--accent) 16%, transparent); color:var(--accent); font-weight:500; }
</style>
