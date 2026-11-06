<script setup>
import { ref, computed, watch, nextTick } from 'vue'
import { api } from '../api'

const props = defineProps({ source: Object })   // custom source being edited, or null
const emit = defineEmits(['saved', 'close'])
const dlg = ref(null), nameIn = ref(null)
const f = ref({ name: '', url: '', m3u: '', refresh_hours: 72 })
const err = ref(''), busy = ref(false), loading = ref(false)
let loadedM3U = ''

const PRESETS = [[0, 'Off'], [1, 'Every hour'], [3, 'Every 3 hours'], [6, 'Every 6 hours'], [12, 'Every 12 hours'], [24, 'Every day'], [72, 'Every 3 days'], [168, 'Every week']]
// a source's own interval (any whole number of hours from Settings) is offered even when it is not one of the presets
const INTERVALS = computed(() => {
  const h = +f.value.refresh_hours
  if (PRESETS.some(([p]) => p === h)) return PRESETS
  const label = h % 24 ? `Every ${h} hours` : `Every ${h / 24} days`
  return [...PRESETS, [h, label]].sort((a, b) => a[0] - b[0])
})

watch(() => props.source, async (s) => {
  if (s && dlg.value && !dlg.value.open) {
    f.value = { name: s.name, url: s.url || '', m3u: '', refresh_hours: s.refresh_hours }
    err.value = ''; loadedM3U = ''
    dlg.value.showModal()
    await nextTick()
    nameIn.value?.focus()
    if (!s.url) { // uploaded / pasted source: show what is in it now
      loading.value = true
      try { loadedM3U = (await api.get('/custom/' + s.id)).m3u || ''; f.value.m3u = loadedM3U }
      catch (e) { err.value = e.message } finally { loading.value = false }
    }
  }
})
async function readFile(e) { const file = e.target.files[0]; if (file) f.value.m3u = await file.text() }
async function save() {
  err.value = ''; busy.value = true
  try {
    const body = { ...f.value, refresh_hours: +f.value.refresh_hours }
    if (body.m3u === loadedM3U) body.m3u = '' // untouched text is not re-imported
    await api.patch('/custom/' + props.source.id, body)
    emit('saved')
    dlg.value.close()
  } catch (e) { err.value = e.message } finally { busy.value = false }
}
const onBackdrop = (e) => { if (e.target === dlg.value && !busy.value) dlg.value.close() }
</script>

<template>
  <dialog ref="dlg" :class="{ wide: source && !f.url }" aria-labelledby="src-title" @close="emit('close')" @click="onBackdrop">
    <form v-if="source" class="box" @submit.prevent="save">
      <h2 id="src-title">Edit source</h2>
      <label>Name
        <input ref="nameIn" v-model="f.name" autocomplete="off">
      </label>
      <label>M3U URL
        <input v-model.trim="f.url" placeholder="Leave empty for an uploaded / pasted playlist" autocomplete="off" spellcheck="false">
        <span v-if="f.url !== (source.url || '')" class="hint">The new URL is fetched when you save. If that fails, nothing changes.</span>
      </label>
      <label v-if="f.url">Auto-refresh
        <select v-model="f.refresh_hours">
          <option v-for="[h, label] in INTERVALS" :key="h" :value="h">{{ label }}</option>
        </select>
        <span class="hint">The list is re-fetched on this schedule. Channels in your profiles stay, and a failed refresh keeps the last good list.</span>
      </label>
      <label v-else>Playlist <span class="opt">(M3U text)</span>
        <textarea v-model="f.m3u" rows="16" :disabled="loading" spellcheck="false" :placeholder="loading ? 'Loading…' : '#EXTM3U\n#EXTINF:-1,My channel\nhttps://example.com/live.m3u8'" />
        <span class="hint">Saving changed text updates this source. Channels you remove here are also removed from your profiles; the channels that stay keep their place and settings.</span>
        <input type="file" accept=".m3u,.m3u8,text/plain" @change="readFile">
      </label>
      <p v-if="err" class="err">{{ err }}</p>
      <div class="actions">
        <button type="button" :disabled="busy" @click="dlg.close()">Cancel</button>
        <button class="primary" type="submit" :disabled="busy">{{ busy ? 'Saving…' : 'Save' }}</button>
      </div>
    </form>
  </dialog>
</template>

<style scoped>
dialog { border:1px solid var(--line); border-radius:12px; padding:0; background:var(--panel); color:var(--text); width:min(520px, calc(100vw - 24px)); }
dialog::backdrop { background:rgba(0,0,0,.5); }
.box { padding:18px 20px; }
/* page content is 1200px wide (main: 1240px minus 2 x 20px padding); the dialog is 60px narrower */
dialog.wide { width:min(1140px, calc(100vw - 40px)); max-width:none; max-height:calc(100vh - 32px); }
@media (max-width:760px) { dialog.wide { width:calc(100vw - 24px); } }
dialog.wide textarea { height:clamp(160px, calc(90vh - 460px), 820px); min-height:120px; white-space:pre; overflow:auto; }
h2 { margin:0 0 14px; font-size:17px; }
label { display:block; font-size:13px; color:var(--muted); margin-bottom:12px; }
label input:not([type=file]), label select, label textarea { display:block; width:100%; margin:4px 0 0; color:var(--text); }
label input[type=file] { margin-top:8px; }
textarea { font-family:ui-monospace, monospace; font-size:13px; line-height:1.5; resize:vertical; tab-size:2; }
.hint { display:block; margin-top:4px; font-size:12px; }
.opt { font-weight:400; }
.actions { display:flex; justify-content:flex-end; gap:8px; margin-top:6px; }
</style>
