<script setup>
import { ref, onMounted } from 'vue'
import { api } from '../api'
import { toast } from '../store'

// Change your own password.
const emit = defineEmits(['close'])
const dlg = ref(null)
const cur = ref(''), pw = ref(''), pw2 = ref(''), err = ref('')
onMounted(() => dlg.value.showModal())
const onBackdrop = (e) => { if (e.target === dlg.value) dlg.value.close() }
async function save() {
  err.value = ''
  if (pw.value !== pw2.value) { err.value = 'The two new passwords are not the same'; return }
  try {
    await api.post('/account/password', { current: cur.value, password: pw.value })
    toast('Password changed')
    dlg.value.close()
  } catch (e) { err.value = e.message }
}
</script>

<template>
  <dialog ref="dlg" aria-labelledby="acc-title" @close="emit('close')" @click="onBackdrop">
    <form class="box" @submit.prevent="save">
      <h2 id="acc-title">Change password</h2>
      <label>Current password<input v-model="cur" type="password" autocomplete="current-password" autofocus></label>
      <label>New password<input v-model="pw" type="password" autocomplete="new-password"></label>
      <label>Repeat the new password<input v-model="pw2" type="password" autocomplete="new-password"></label>
      <p v-if="err" class="err">{{ err }}</p>
      <div class="actions">
        <button type="button" @click="dlg.close()">Cancel</button>
        <button class="primary" :disabled="!cur || !pw">Change password</button>
      </div>
    </form>
  </dialog>
</template>

<style scoped>
dialog { border:1px solid var(--line); border-radius:12px; padding:0; background:var(--panel); color:var(--text); width:min(380px, calc(100vw - 24px)); }
dialog::backdrop { background:rgba(0,0,0,.5); }
.box { padding:18px 20px; display:flex; flex-direction:column; gap:12px; }
h2 { margin:0; font-size:18px; }
label { display:flex; flex-direction:column; gap:4px; font-size:13px; color:var(--muted); }
.err { margin:0; }
.actions { display:flex; justify-content:flex-end; gap:8px; }
</style>
