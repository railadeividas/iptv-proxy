<script setup>
import { ref, reactive, computed, onMounted } from 'vue'
import { api } from '../api'
import { toast } from '../store'
import { useLive } from '../util'

// items: channel entries ({ck, name, profiles:[ids]}). One item = tick profiles on/off;
// several (e.g. "all on this page") = add them all to a chosen profile.
const props = defineProps({ items: Array })
const emit = defineEmits(['close', 'changed'])

const dlg = ref(null)
const profiles = ref([]), loaded = ref(false)
const member = reactive({}) // profileId -> Set of cks already in it
const newName = ref(''), busy = ref(false)
const single = computed(() => props.items.length === 1)

for (const it of props.items) for (const pid of it.profiles) (member[pid] ||= new Set()).add(it.ck)
const have = (pid) => props.items.filter((it) => member[pid]?.has(it.ck)).length

async function loadProfiles() {
  profiles.value = await api.get('/profiles')
  loaded.value = true
}
onMounted(() => {
  dlg.value.showModal()
  loadProfiles().catch(() => {})
})
useLive({ profiles: () => loadProfiles().catch(() => {}) })

async function add(pid) {
  const cks = props.items.filter((it) => !member[pid]?.has(it.ck)).map((it) => it.ck)
  if (!cks.length) return
  await api.post(`/profiles/${pid}/add`, { cks })
  for (const ck of cks) (member[pid] ||= new Set()).add(ck)
}
async function remove(pid) {
  const cks = props.items.map((it) => it.ck)
  await api.post(`/profiles/${pid}/remove`, { cks })
  for (const ck of cks) member[pid]?.delete(ck)
}
async function toggle(p) {
  busy.value = true
  try {
    have(p.id) ? await remove(p.id) : await add(p.id)
    emit('changed')
  } catch (e) { toast(e.message) } finally { busy.value = false }
}
async function addAllTo(p) {
  busy.value = true
  try {
    const before = have(p.id)
    await add(p.id)
    toast(`Added ${props.items.length - before} channels to ${p.name}`)
    emit('changed')
  } catch (e) { toast(e.message) } finally { busy.value = false }
}
async function createAndAdd() {
  if (!newName.value.trim()) return
  busy.value = true
  try {
    const p = await api.post('/profiles', { name: newName.value })
    newName.value = ''
    await loadProfiles()
    await add(p.id)
    toast(`Added to ${p.name}`)
    emit('changed')
  } catch (e) { toast(e.message) } finally { busy.value = false }
}
const onBackdrop = (e) => { if (e.target === dlg.value) dlg.value.close() }
</script>

<template>
  <dialog ref="dlg" @close="emit('close')" @click="onBackdrop">
    <div class="box">
      <header>
        <h2 v-if="single">Add “{{ items[0].name }}” to…</h2>
        <h2 v-else>Add {{ items.length }} channels to…</h2>
        <button class="ghost" aria-label="Close" @click="dlg.close()">✕</button>
      </header>

      <p v-if="loaded && !profiles.length" class="muted">You have no profiles yet. Create your first one below.</p>
      <ul class="plist">
        <li v-for="p in profiles" :key="p.id">
          <label v-if="single" class="pick">
            <input type="checkbox" :checked="have(p.id) === 1" :disabled="busy" @change="toggle(p)">
            <span><b>{{ p.name }}</b> <span class="muted">· {{ p.count }} channels</span></span>
          </label>
          <template v-else>
            <span><b>{{ p.name }}</b> <span class="muted">· {{ have(p.id) }} of {{ items.length }} already in</span></span>
            <button class="primary" :disabled="busy || have(p.id) === items.length" @click="addAllTo(p)">
              {{ have(p.id) === items.length ? 'All added' : 'Add ' + (items.length - have(p.id)) }}
            </button>
          </template>
        </li>
      </ul>

      <form class="new" @submit.prevent="createAndAdd">
        <input v-model="newName" placeholder="New profile name (e.g. Living room)">
        <button :disabled="busy || !newName.trim()">Create &amp; add</button>
      </form>
      <footer><button @click="dlg.close()">Done</button></footer>
    </div>
  </dialog>
</template>

<style scoped>
dialog { border:1px solid var(--line); border-radius:12px; padding:0; background:var(--panel); color:var(--text); width:min(460px, calc(100vw - 24px)); }
dialog::backdrop { background:rgba(0,0,0,.5); }
.box { padding:16px 18px; }
.box header { display:flex; justify-content:space-between; align-items:flex-start; gap:12px; position:static; padding:0; border:0; background:none; margin-bottom:8px; }
.box h2 { margin:0; word-break:break-word; }
.plist { list-style:none; margin:8px 0; padding:0; max-height:50vh; overflow:auto; border:1px solid var(--line); border-radius:8px; }
.plist li { display:flex; justify-content:space-between; align-items:center; gap:10px; padding:10px 12px; border-bottom:1px solid var(--line); }
.plist li:last-child { border-bottom:0; }
.pick { display:flex; gap:10px; align-items:center; cursor:pointer; flex:1; }
.pick input { width:18px; height:18px; }
.new { display:flex; gap:8px; margin-top:12px; } .new input { flex:1; }
footer { display:flex; justify-content:flex-end; margin-top:14px; }
</style>
