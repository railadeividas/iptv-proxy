<script setup>
import { ref, computed, onMounted } from 'vue'
import { onBeforeRouteLeave, onBeforeRouteUpdate } from 'vue-router'
import { api } from '../api'
import { toast, ask } from '../store'
import { useLive } from '../util'

// Renders the settings of one tab from the list the server sends, so a new
// setting needs no UI work.
const props = defineProps({ group: String })

const fields = ref([]), detected = ref('')
const saved = ref({}), draft = ref({}), errors = ref({})
const loaded = ref(false), saving = ref(false)

const mine = computed(() => fields.value.filter((f) => f.group === props.group))
const sections = computed(() => {
  const by = new Map()
  for (const f of mine.value) (by.get(f.section) || by.set(f.section, []).get(f.section)).push(f)
  return [...by].map(([title, list]) => ({ title, list }))
})
const changed = computed(() => mine.value.filter((f) => draft.value[f.key] !== saved.value[f.key]))

async function load() {
  const r = await api.get('/settings')
  fields.value = r.fields
  detected.value = r.detected_address
  saved.value = Object.fromEntries(r.fields.map((f) => [f.key, f.value]))
  draft.value = { ...saved.value }
  errors.value = {}
  loaded.value = true
}
onMounted(load)
useLive({ settings: () => { if (!changed.value.length) load() } })

async function save() {
  saving.value = true
  errors.value = {}
  try {
    await api.put('/settings', { values: Object.fromEntries(changed.value.map((f) => [f.key, draft.value[f.key]])) })
    await load()
    toast('Settings saved')
  } catch (e) {
    errors.value = e.fields || {}
    toast(e.fields ? 'Some values are not valid' : e.message)
  } finally { saving.value = false }
}
const discard = () => { draft.value = { ...saved.value }; errors.value = {} }

const leave = () => !changed.value.length || ask({ title: 'Discard unsaved changes?', message: 'You changed settings on this tab that have not been saved.', confirmText: 'Discard changes', danger: true })
onBeforeRouteLeave(leave)
onBeforeRouteUpdate(leave)

// every IANA zone the browser knows (the server image ships the same tz database)
const zones = (() => {
  try { return ['UTC', ...Intl.supportedValuesOf('timeZone').filter((z) => z !== 'UTC')] } catch { return ['UTC'] }
})()
// "UTC+02:00" for a zone as it is right now (daylight saving included), so the list says what time each one uses
const offsets = (() => {
  const out = {}, now = new Date()
  for (const z of zones) {
    try {
      const o = new Intl.DateTimeFormat('en-US', { timeZone: z, timeZoneName: 'longOffset' }).formatToParts(now).find((x) => x.type === 'timeZoneName')?.value || ''
      out[z] = o === 'GMT' ? 'UTC+00:00' : o.replace('GMT', 'UTC')
    } catch { out[z] = '' }
  }
  return out
})()
const zoneLabel = (z) => z.replaceAll('_', ' ') + (offsets[z] ? ` (${offsets[z]})` : '')
const zoneChoices = (cur) => (cur && !zones.includes(cur) ? [cur, ...zones] : zones)
const placeholder = (f) => (f.key === 'player_address' ? `Auto-detect (${detected.value})` : f.kind === 'zone' ? 'Server time zone' : '')
const range = (f) => `${f.min.toLocaleString()}–${f.max.toLocaleString()}${f.unit ? ' ' + f.unit : ''}`
const optionLabel = (f, v) => f.options.find((o) => o.value === v)?.label ?? v
</script>

<template>
  <div v-if="loaded" class="forms">
    <section v-for="s in sections" :key="s.title" class="card">
      <h2>{{ s.title }}</h2>
      <div v-for="f in s.list" :key="f.key" class="field" :class="{ bad: errors[f.key] }">
        <div class="what">
          <label :for="'set-' + f.key">{{ f.label }}</label>
          <p v-if="f.help" class="muted help">{{ f.help }}</p>
        </div>
        <div class="ctl">
          <div class="inp" :class="{ narrow: f.kind === 'int' }">
            <select v-if="f.kind === 'enum'" :id="'set-' + f.key" v-model="draft[f.key]">
              <option v-for="o in f.options" :key="o.value" :value="o.value">{{ o.label }}</option>
            </select>
            <select v-else-if="f.kind === 'zone'" :id="'set-' + f.key" v-model="draft[f.key]">
              <option value="">Server time zone</option>
              <option v-for="z in zoneChoices(draft[f.key])" :key="z" :value="z">{{ zoneLabel(z) }}</option>
            </select>
            <input v-else-if="f.kind === 'int'" :id="'set-' + f.key" v-model.number="draft[f.key]" type="number" step="1" :min="f.min" :max="f.max" inputmode="numeric">
            <input v-else-if="f.kind === 'time'" :id="'set-' + f.key" v-model="draft[f.key]" type="time">
            <input v-else :id="'set-' + f.key" v-model="draft[f.key]" type="text" spellcheck="false" autocomplete="off" :placeholder="placeholder(f)">
            <span v-if="f.kind === 'int' && f.unit" class="unit">{{ f.unit }}</span>
          </div>
          <p v-if="errors[f.key]" class="err msg">{{ errors[f.key] }}</p>
          <p v-else class="muted msg">
            <template v-if="f.kind === 'int'">{{ range(f) }} · default {{ f.default.toLocaleString() }}</template>
            <template v-else-if="f.kind === 'enum'">Default: {{ optionLabel(f, f.default) }}</template>
            <template v-else-if="f.default">Default: <span class="mono" :title="f.default">{{ f.default.length > 48 ? f.default.slice(0, 48) + '…' : f.default }}</span></template>
            <button v-if="draft[f.key] !== f.default" type="button" class="link" @click="draft[f.key] = f.default">Reset to default</button>
          </p>
        </div>
      </div>
    </section>

    <Transition name="rise">
      <div v-if="changed.length" class="savebar" role="region" aria-label="Unsaved changes">
        <span class="badge warn">{{ changed.length }} unsaved change{{ changed.length === 1 ? '' : 's' }}</span>
        <span class="grow" />
        <button :disabled="saving" @click="discard">Discard</button>
        <button class="primary" :disabled="saving" @click="save">{{ saving ? 'Saving…' : 'Save changes' }}</button>
      </div>
    </Transition>
  </div>
  <p v-else class="muted">Loading…</p>
</template>

<style scoped>
.forms { display:flex; flex-direction:column; }
.card { margin:0 0 16px; }
.card h2 { margin:0 0 4px; font-size:16px; }
.field { display:grid; grid-template-columns:minmax(220px, 1fr) minmax(240px, 420px); gap:6px 24px; padding:14px 0; border-top:1px solid var(--line); align-items:start; }
.card h2 + .field { border-top:0; }
.what label { font-weight:500; }
.help { margin:2px 0 0; font-size:12px; max-width:56ch; }
.inp { display:flex; align-items:center; gap:8px; }
.inp input, .inp select { width:100%; color:var(--text); }
.inp.narrow input, .inp input[type=time] { width:140px; }
.unit { color:var(--muted); font-size:13px; white-space:nowrap; }
.msg { margin:4px 0 0; font-size:12px; display:flex; gap:10px; flex-wrap:wrap; align-items:baseline; min-height:1em; }
.mono { font-family:ui-monospace, monospace; }
.link { height:auto; padding:0; border:0; background:none; color:var(--accent); font-size:12px; cursor:pointer; }
.link:hover { text-decoration:underline; border:0; }
.field.bad .inp input, .field.bad .inp select { border-color:var(--bad); }
@media (max-width:760px) { .field { grid-template-columns:1fr; } }
</style>
