<script setup>
import { ref, computed, onMounted } from 'vue'
import { onBeforeRouteLeave, onBeforeRouteUpdate, useRouter } from 'vue-router'
import { api } from '../api'
import { toast, ask } from '../store'
import EditChannelModal from './EditChannelModal.vue'
import ChannelModal from './ChannelModal.vue'
import { stateLabel, stateClass, stateTitle, useLive, debounced, copy } from '../util'

const props = defineProps({ id: Number })
const router = useRouter()

const profile = ref(null)          // saved server state
const meta = ref({ name: '', mode: 'auto', compat: '' }) // editable profile settings
const rows = ref([])               // editable channel list
const snapshot = ref('')           // JSON of the saved editable state
const qr = ref(''), missing = ref(false), staleNotice = ref(false)
const streamOptions = ref({})      // ck -> loaded stream list

const editable = (r) => ({ ck: r.ck, name: r.name, stream_url: r.stream_url, mode: r.mode, epg_id: r.epg_id })
const current = computed(() => JSON.stringify({ meta: meta.value, rows: rows.value.map(editable) }))
const dirty = computed(() => profile.value && current.value !== snapshot.value)

function adopt(p) {
  profile.value = p
  meta.value = { name: p.name, mode: p.mode, compat: p.compat }
  rows.value = p.channels.map((c) => ({ ...c }))
  for (const r of rows.value) if (r.stream_url) loadStreams(r).catch(() => {}) // so the meta line can name the chosen stream
  snapshot.value = current.value
  staleNotice.value = false
}
async function load(force = false) {
  try {
    const p = await api.get('/profiles/' + props.id)
    if (!profile.value || force || !dirty.value) return adopt(p)
    // unsaved edits: only refresh live health numbers, keep the user's edits
    profile.value = { ...profile.value, urls: p.urls, token: p.token }
    const byCk = Object.fromEntries(p.channels.map((c) => [c.ck, c]))
    for (const r of rows.value) {
      const n = byCk[r.ck]
      if (n) Object.assign(r, { streams: n.streams, working: n.working, dead: n.dead, excluded: n.excluded, total_streams: n.total_streams, has_guide: n.has_guide })
    }
  } catch (e) { if (/not found/.test(e.message)) missing.value = true }
}
onMounted(() => load(true))

// Health ticks refresh the status badges quietly; edits made elsewhere ask first.
useLive({
  streams: debounced(() => load(), 800),
  profiles: debounced(async () => {
    if (dirty.value) { staleNotice.value = true; return }
    load(true)
  }, 300),
})

function confirmLeave() {
  return !dirty.value || ask({ title: 'Discard unsaved changes?', message: 'You have edits on this profile that have not been saved.', confirmText: 'Discard changes', danger: true })
}
onBeforeRouteLeave(confirmLeave)
onBeforeRouteUpdate(confirmLeave)

const move = (i, d) => { const j = i + d; if (j < 0 || j >= rows.value.length) return; const [r] = rows.value.splice(i, 1); rows.value.splice(j, 0, r) }
const editing = ref(null)
const details = ref(null) // channel key shown in the details dialog
function applyEdit(v) { Object.assign(editing.value, v); editing.value = null }
const dragFrom = ref(-1), overIdx = ref(-1)
const onDrop = (i) => { const f = dragFrom.value; dragFrom.value = -1; if (f < 0 || f === i) return; const [r] = rows.value.splice(f, 1); rows.value.splice(i, 0, r) }

async function loadStreams(r) {
  if (streamOptions.value[r.ck]) return
  streamOptions.value[r.ck] = await api.get('/entry/' + encodeURIComponent(r.ck) + '/streams')
}
async function startEdit(r) { editing.value = r; try { await loadStreams(r) } catch {} }
const streamLabel = (r) => {
  const s = (streamOptions.value[r.ck] || []).find((x) => x.url === r.stream_url)
  return s ? `${s.quality || '?'} · ${host(s.url)}` : host(r.stream_url)
}
const host = (u) => { try { return new URL(u).host } catch { return u } }

async function save() {
  try {
    await api.patch('/profiles/' + props.id, meta.value)
    await api.put(`/profiles/${props.id}/channels`, { channels: rows.value.map(editable) })
    toast('Saved')
    await load(true)
  } catch (e) { toast(e.message) }
}
async function rotate() {
  if (!(await ask({ title: 'Regenerate secret token?', message: 'The current playlist and EPG links will stop working. Update them in your players afterwards.', confirmText: 'Regenerate' }))) return
  await api.post(`/profiles/${props.id}/rotate-token`); toast('Token regenerated'); qr.value = ''; load()
}
async function removeRow(r) {
  const name = r.name || r.ck
  if (!(await ask({ title: `Remove “${name}”?`, message: 'It is taken out of this profile. Nothing changes until you press Save, and the channel stays in the catalogue.', confirmText: 'Remove', danger: true }))) return
  const i = rows.value.findIndex((x) => x.ck === r.ck)
  if (i >= 0) rows.value.splice(i, 1)
}
async function remove() {
  if (!(await ask({ title: `Delete “${meta.value.name || profile.value.name}”?`, message: `This removes the profile and its ${rows.value.length} channel${rows.value.length === 1 ? '' : 's'} from it, and its playlist and EPG links stop working. The channels themselves stay in the catalogue.`, confirmText: 'Delete profile', danger: true }))) return
  snapshot.value = current.value // not "dirty" anymore: skip the leave prompt
  await api.del('/profiles/' + props.id)
  router.replace({ name: 'profiles' })
}
async function doCopy(v) { await copy(v); toast('Copied') }
const urlRows = [['playlist', 'Playlist (M3U)'], ['epg', 'EPG (XMLTV)'], ['epg_gz', 'EPG (gzip)']]
</script>

<template>
  <p v-if="missing" class="muted">Profile not found.</p>
  <div v-else-if="profile" class="detail">
    <div v-if="staleNotice" class="alert warn">This profile was changed elsewhere.
      <button @click="load(true)">Reload and drop my edits</button></div>

    <section class="card">
      <div class="head">
        <input v-model="meta.name" class="title" aria-label="Profile name" placeholder="Profile name">
        <div class="head-actions">
          <button class="ghost" @click="rotate">Regenerate token</button>
          <button class="ghost danger" @click="remove">Delete</button>
        </div>
      </div>
      <div class="settings">
        <label>Delivery
          <select v-model="meta.mode">
            <option value="auto">Auto (direct when possible)</option>
            <option value="proxy">Always proxy</option>
            <option value="redirect">Always redirect</option>
          </select>
        </label>
        <label>Compatibility
          <select v-model="meta.compat">
            <option value="">Default</option><option value="vlc">VLC user-agent</option>
            <option value="browser">Browser user-agent</option><option value="exoplayer">ExoPlayer user-agent</option>
          </select>
        </label>
      </div>
    </section>

    <section class="card">
      <h2>Links</h2>
      <div class="links">
        <template v-for="[k, label] in urlRows" :key="k">
          <span class="label">{{ label }}</span>
          <input readonly :value="profile.urls[k]" :aria-label="label" @focus="$event.target.select()">
          <button @click="doCopy(profile.urls[k])">Copy</button>
          <button :class="{ active: qr === profile.urls[k] }" @click="qr = qr === profile.urls[k] ? '' : profile.urls[k]">QR</button>
        </template>
      </div>
      <div v-if="qr" class="qrbox">
        <img class="qr" alt="QR code" :src="'/api/qr?text=' + encodeURIComponent(qr)">
        <span class="muted">Scan with your TV or phone to open this link.</span>
      </div>
    </section>

    <section class="card">
      <div class="chead">
        <div>
          <h2>Channels <span class="count">{{ rows.length }}</span></h2>
        </div>
      </div>
      <p v-if="!rows.length" class="muted">No channels yet. Add some from the Channels tab.</p>

      <div class="rows">
        <div v-for="(r, i) in rows" :key="r.ck" class="chrow" :class="{ over: overIdx === i && dragFrom !== i }"
             @dragover.prevent="overIdx = i" @dragleave="overIdx = -1" @drop="onDrop(i); overIdx = -1">
          <div class="pos">
            <span class="grip" draggable="true" title="Drag to reorder" @dragstart="dragFrom = i" @dragend="dragFrom = -1; overIdx = -1">⋮⋮</span>
            <span class="num">{{ i + 1 }}</span>
          </div>
          <div class="who">
            <img v-if="r.logo" class="logo" loading="lazy" :src="r.logo" alt="" @error="$event.target.style.visibility = 'hidden'">
            <span v-else class="logo" />
            <div class="names">
              <div class="line1"><b>{{ r.name || r.default_name || r.ck }}</b></div>
              <div class="meta">{{ r.ck }} · {{ r.working }}/{{ r.streams }} streams<template v-if="r.stream_url"> · Stream: {{ streamLabel(r) }}</template><template v-if="r.mode"> · Delivery: {{ r.mode }}</template><template v-if="r.epg_id"> · EPG: {{ r.epg_id }}</template></div>
            </div>
          </div>
          <span class="stat">
            <span class="badge" :class="stateClass(r)" :title="stateTitle(r)">{{ stateLabel(r) }}</span>
            <span v-if="!r.has_guide" class="badge warn" title="No iptv-org EPG source found. Use Edit to enter an EPG ID and borrow another channel's guide.">no guide</span>
          </span>
          <span class="rm">
            <button class="mini ghost" :disabled="i === 0" title="Move up" aria-label="Move up" @click="move(i, -1)">↑</button>
            <button class="mini ghost" :disabled="i === rows.length - 1" title="Move down" aria-label="Move down" @click="move(i, 1)">↓</button>
            <button class="mini ghost" title="Channel details and check history" aria-label="Details" @click="details = r.ck">ⓘ</button>
            <button class="mini ghost" title="Edit name, EPG ID, stream and delivery" aria-label="Edit" @click="startEdit(r)">✎</button>
            <button class="mini ghost danger" title="Remove from profile" aria-label="Remove" @click="removeRow(r)">✕</button>
          </span>
        </div>
      </div>
    </section>

    <ChannelModal :ck="details" @close="details = null" />
    <EditChannelModal :row="editing" :streams="streamOptions[editing?.ck]" @save="applyEdit" @close="editing = null" />

    <Transition name="rise">
      <div v-if="dirty" class="savebar" role="region" aria-label="Unsaved changes">
        <span class="badge warn">unsaved changes</span>
        <span class="grow" />
        <button @click="load(true)">Discard</button>
        <button class="primary" @click="save">Save changes</button>
      </div>
    </Transition>
  </div>
</template>

<style scoped>
.detail { display:flex; flex-direction:column; }
.card { margin-bottom:16px; }
.card h2 { margin:0 0 12px; font-size:15px; }
.count { color:var(--muted); font-weight:400; margin-left:4px; }
.head { display:flex; gap:12px; align-items:center; justify-content:space-between; flex-wrap:wrap; }
.head .title { flex:1; min-width:200px; font-size:18px; font-weight:600; background:var(--bg); border:1px solid var(--line); padding:6px 10px; }
.head .title:focus { border-color:var(--accent); outline:none; }
.head-actions { display:flex; gap:6px; }
.settings { display:grid; grid-template-columns:repeat(auto-fit, minmax(220px, 1fr)); gap:12px; margin-top:12px; }
.settings label { display:flex; flex-direction:column; gap:4px; font-size:12px; color:var(--muted); }
.settings select { width:100%; color:var(--text); }

.links { display:grid; grid-template-columns:auto 1fr auto auto; gap:8px 8px; align-items:center; }
.links .label { color:var(--muted); font-size:13px; white-space:nowrap; padding-right:6px; }
.links input { width:100%; font-family:ui-monospace, monospace; font-size:12px; }
.links button.active { border-color:var(--accent); color:var(--accent); }
.qrbox { display:flex; gap:14px; align-items:center; margin-top:14px; padding-top:14px; border-top:1px solid var(--line); }
.qr { background:#fff; padding:8px; border-radius:8px; width:160px; height:160px; }

.chead { display:flex; justify-content:space-between; align-items:center; gap:12px; margin-bottom:8px; flex-wrap:wrap; }
.chead h2 { margin:0; }
.hint { color:var(--muted); font-size:12px; margin:0 0 8px; }
.alert { display:flex; gap:8px; align-items:center; flex-wrap:wrap; padding:8px 12px; border:1px solid var(--line); border-radius:8px; background:var(--bg); margin-bottom:10px; font-size:13px; }
.alert.warn { border-color:var(--warn); margin-bottom:16px; background:var(--panel); }
.alert .badge { margin:0; }

.rows { border:1px solid var(--line); border-radius:10px; overflow:hidden; }
.chrow { display:grid; grid-template-columns:44px minmax(0, 1fr) auto auto; gap:8px 12px; align-items:center; padding:12px; border-bottom:1px solid var(--line); }
.chrow:last-child { border-bottom:0; }
.chrow.over { box-shadow:inset 0 2px 0 var(--accent); }
.pos { grid-column:1; grid-row:1; } .who { grid-column:2; grid-row:1; }
.stat { grid-column:3; grid-row:1; display:flex; gap:4px; justify-content:flex-end; } .stat .badge { margin:0; }
.rm { grid-column:4; grid-row:1; justify-self:end; display:flex; gap:2px; }
.pos { display:flex; align-items:center; gap:4px; color:var(--muted); }
.grip { cursor:grab; letter-spacing:-2px; padding:4px 2px; user-select:none; }
.num { min-width:18px; text-align:right; font-size:12px; font-variant-numeric:tabular-nums; }
.mini { padding:0; width:var(--ctl); }
.who { display:flex; gap:10px; align-items:center; min-width:0; }
.logo { width:42px; height:28px; object-fit:contain; background:#8881; border-radius:4px; flex:none; }
.names { min-width:0; }
.line1 { display:flex; align-items:center; gap:2px 0; flex-wrap:wrap; }
.line1 b { margin-right:4px; }
.line1 .badge { margin-left:2px; margin-right:2px; }
.meta { font-size:12px; color:var(--muted); white-space:nowrap; overflow:hidden; text-overflow:ellipsis; }

@media (max-width: 760px) {
  .chrow { grid-template-columns:auto minmax(0, 1fr) auto; }
  .pos { grid-column:1; } .who { grid-column:2; }
  .stat { grid-column:2; grid-row:2; justify-content:flex-start; } .rm { grid-column:3; grid-row:1 / 3; }
  .links { grid-template-columns:1fr auto auto; } .links .label { grid-column:1 / -1; padding-top:6px; }
}
</style>
