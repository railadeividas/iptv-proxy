<script setup>
import { ref, reactive, onMounted } from 'vue'
import { api } from '../api'
import { toast } from '../store'

const props = defineProps({ user: Object })
const emit = defineEmits(['close', 'saved'])
const dlg = ref(null)
const f = reactive({ username: props.user.username, role: props.user.role, password: '' })
const busy = ref(false), err = ref('')
const isSuper = props.user.role === 'superuser'

onMounted(() => dlg.value.showModal())
const onBackdrop = (e) => { if (e.target === dlg.value) dlg.value.close() }

async function save() {
  err.value = ''; busy.value = true
  try {
    const body = { username: f.username }
    if (!isSuper) body.role = f.role
    if (f.password) body.password = f.password
    await api.patch('/users/' + props.user.id, body)
    toast('Saved')
    emit('saved')
    dlg.value.close()
  } catch (e) { err.value = e.message } finally { busy.value = false }
}
</script>

<template>
  <dialog ref="dlg" aria-labelledby="eu-title" @close="emit('close')" @click="onBackdrop">
    <form class="box" @submit.prevent="save">
      <h2 id="eu-title">Edit {{ user.username }}</h2>
      <label>User name<input v-model="f.username" autocomplete="off"></label>
      <label>Role
        <select v-model="f.role" :disabled="isSuper">
          <option v-if="isSuper" value="superuser">Superuser</option>
          <option value="user">User</option>
          <option value="admin">Admin</option>
        </select>
        <span v-if="isSuper" class="hint">The superuser's role can't be changed.</span>
      </label>
      <label>New password <span class="opt">(optional)</span>
        <input v-model="f.password" type="password" autocomplete="new-password" placeholder="Leave empty to keep the current one">
        <span class="hint">Setting a new password signs this user out everywhere.</span>
      </label>
      <p v-if="err" class="err">{{ err }}</p>
      <div class="actions">
        <button type="button" :disabled="busy" @click="dlg.close()">Cancel</button>
        <button class="primary" :disabled="busy || !f.username">Save</button>
      </div>
    </form>
  </dialog>
</template>

<style scoped>
dialog { border:1px solid var(--line); border-radius:12px; padding:0; background:var(--panel); color:var(--text); width:min(420px, calc(100vw - 24px)); }
dialog::backdrop { background:rgba(0,0,0,.5); }
.box { padding:18px 20px; }
h2 { margin:0 0 14px; font-size:17px; }
label { display:block; font-size:13px; color:var(--muted); margin-bottom:12px; }
label input, label select { display:block; width:100%; margin:4px 0 0; color:var(--text); }
.hint { display:block; margin-top:4px; font-size:12px; }
.opt { font-weight:400; }
.err { margin:0 0 10px; }
.actions { display:flex; justify-content:flex-end; gap:8px; margin-top:6px; }
</style>
