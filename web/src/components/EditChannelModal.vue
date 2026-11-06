<script setup>
import { ref, watch, nextTick } from 'vue'

const props = defineProps({ row: Object, streams: Array })   // channel row being edited, or null; its stream list (null while loading)
const emit = defineEmits(['save', 'close'])
const dlg = ref(null), nameIn = ref(null)
const name = ref(''), epg = ref(''), stream = ref(''), mode = ref('')
const host = (u) => { try { return new URL(u).host } catch { return u } }
const mark = (s) => (s.status === 1 ? '● ' : s.status === 2 ? '✕ ' : '○ ')

const defaultName = () => props.row?.default_name || props.row?.ck || ''
const defaultEpg = () => (props.row?.ck?.startsWith('custom:') ? 'From the source’s tvg-id' : props.row?.ck || '')

watch(() => props.row, async (r) => {
  if (r && dlg.value && !dlg.value.open) {
    name.value = r.name || ''
    epg.value = r.epg_id || ''
    stream.value = r.stream_url || ''
    mode.value = r.mode || ''
    dlg.value.showModal()
    await nextTick()
    nameIn.value?.focus()
  }
})
function save() { emit('save', { name: name.value.trim(), epg_id: epg.value.trim(), stream_url: stream.value, mode: mode.value }); dlg.value?.close() }
const onBackdrop = (e) => { if (e.target === dlg.value) dlg.value.close() }
</script>

<template>
  <dialog ref="dlg" aria-labelledby="edit-title" @close="emit('close')" @click="onBackdrop">
    <form v-if="row" class="box" @submit.prevent="save">
      <h2 id="edit-title">Edit channel</h2>
      <p class="muted sub">{{ row.default_name || row.ck }} · {{ row.ck }}</p>
      <label>Name
        <input ref="nameIn" v-model="name" :placeholder="defaultName()" autocomplete="off">
        <span class="hint">Shown in the player. Leave empty to use “{{ defaultName() }}”.</span>
      </label>
      <label>EPG ID
        <input v-model="epg" :placeholder="defaultEpg()" autocomplete="off" spellcheck="false">
        <span class="hint">Borrow the guide of another iptv-org channel (Channel.cc or Channel.cc@Feed). Leave empty to use the default.</span>
      </label>
      <label>Preferred stream
        <select v-model="stream">
          <option value="">Best stream</option>
          <option v-if="stream && !streams?.some((x) => x.url === stream)" :value="stream">Preferred (saved)</option>
          <option v-for="x in streams || []" :key="x.url" :value="x.url">{{ mark(x) }}{{ x.quality || '?' }} · {{ host(x.url) }}</option>
        </select>
        <span class="hint">{{ streams ? 'Which stream the playlist uses. “Best stream” picks a working one automatically.' : 'Loading streams…' }}</span>
      </label>
      <label>Delivery
        <select v-model="mode">
          <option value="">Profile default</option><option value="auto">Auto</option><option value="proxy">Proxy</option><option value="redirect">Redirect</option>
        </select>
        <span class="hint">Overrides the profile’s delivery mode for this channel.</span>
      </label>
      <div class="actions">
        <button type="button" @click="dlg.close()">Cancel</button>
        <button class="primary" type="submit">Apply</button>
      </div>
    </form>
  </dialog>
</template>

<style scoped>
dialog { border:1px solid var(--line); border-radius:12px; padding:0; background:var(--panel); color:var(--text); width:min(460px, calc(100vw - 24px)); }
dialog::backdrop { background:rgba(0,0,0,.5); }
.box { padding:18px 20px; }
h2 { margin:0 0 2px; font-size:17px; }
.sub { margin:0 0 14px; font-size:12px; }
label { display:block; font-size:13px; color:var(--muted); margin-bottom:12px; }
label input, label select { display:block; width:100%; box-sizing:border-box; margin:4px 0 0; color:var(--text); }
.hint { display:block; margin-top:4px; font-size:12px; }
.actions { display:flex; justify-content:flex-end; gap:8px; margin-top:6px; }
</style>
