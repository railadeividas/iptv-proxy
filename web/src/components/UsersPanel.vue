<script setup>
import { ref, reactive, onMounted } from 'vue'
import { api } from '../api'
import { store, toast, ask } from '../store'
import EditUserModal from './EditUserModal.vue'

// Settings > Users: who can sign in and what they may do. Only the superuser sees this page.
const users = ref([]), loaded = ref(false), editing = ref(null)
const form = reactive({ username: '', password: '', role: 'user' })
const busy = ref(false), err = ref('')

async function load() { users.value = await api.get('/users'); loaded.value = true }
onMounted(() => load().catch((e) => toast(e.message)))

async function add() {
  err.value = ''; busy.value = true
  try {
    await api.post('/users', { ...form })
    toast(`User “${form.username}” created`)
    Object.assign(form, { username: '', password: '', role: 'user' })
    await load()
  } catch (e) { err.value = e.message } finally { busy.value = false }
}

async function remove(u) {
  const own = u.profiles ? ` Their ${u.profiles} profile${u.profiles === 1 ? '' : 's'} and the playlist links of ${u.profiles === 1 ? 'it' : 'them'} will stop working.` : ''
  if (!(await ask({ title: `Delete “${u.username}”?`, message: `They can no longer sign in.${own}`, confirmText: 'Delete user', danger: true }))) return
  try { await api.del('/users/' + u.id); toast('User deleted'); await load() } catch (e) { toast(e.message) }
}

const ROLES = { superuser: 'superuser', admin: 'admin', user: 'user' }
const when = (t) => new Date(t * 1000).toLocaleDateString()
</script>

<template>
  <div class="users">
    <section class="card">
      <h2>Add a user</h2>
      <form class="add" @submit.prevent="add">
        <input v-model="form.username" placeholder="User name" autocomplete="off" aria-label="User name">
        <input v-model="form.password" type="password" placeholder="Password" autocomplete="new-password" aria-label="Password">
        <select v-model="form.role" aria-label="Role">
          <option value="user">User</option>
          <option value="admin">Admin</option>
        </select>
        <button class="primary" :disabled="busy || !form.username || !form.password">Add user</button>
      </form>
      <p v-if="err" class="err">{{ err }}</p>
      <dl class="roles">
        <div><dt>User</dt><dd>Browses channels and manages their own profiles.</dd></div>
        <div><dt>Admin</dt><dd>Also sees everyone's profiles, Sources and Health. No access to Settings.</dd></div>
        <div><dt>Superuser</dt><dd>The account made on the first start. Can do everything, including these Settings and this page. There is only one.</dd></div>
      </dl>
    </section>

    <section class="card">
      <h2>Users</h2>
      <div class="list">
        <div v-for="u in users" :key="u.id" class="item">
          <div class="who">
            <b>{{ u.username }}</b><span v-if="u.id === store.user?.id" class="badge">you</span>
            <span class="badge" :class="{ good: u.role === 'superuser' }">{{ ROLES[u.role] }}</span>
            <div class="muted small">{{ u.profiles }} profile{{ u.profiles === 1 ? '' : 's' }} · added {{ when(u.created_at) }}</div>
          </div>
          <button @click="editing = u">Edit</button>
          <button v-if="u.role !== 'superuser'" class="danger" @click="remove(u)">Delete</button>
        </div>
        <div v-if="loaded && !users.length" class="item muted">No users.</div>
      </div>
    </section>

    <EditUserModal v-if="editing" :user="editing" @close="editing = null" @saved="load" />
  </div>
</template>

<style scoped>
.users { display:flex; flex-direction:column; gap:16px; }
.users .card { margin:0; }
h2 { margin:0 0 12px; font-size:16px; }
.add { display:grid; grid-template-columns:minmax(120px, 1fr) minmax(120px, 1fr) 130px auto; gap:10px; }
.add input, .add select { width:100%; }
@media (max-width:760px) { .add { grid-template-columns:1fr 1fr; } .add input { grid-column:1 / -1; } .add button { grid-column:1 / -1; } }
.err { margin:10px 0 0; }
.roles { margin:14px 0 0; display:grid; gap:6px; font-size:13px; }
.roles div { display:flex; gap:10px; }
.roles dt { width:84px; flex:none; font-weight:500; }
.roles dd { margin:0; color:var(--muted); }
.who { flex:1; min-width:0; }
.who b { overflow-wrap:anywhere; }
.small { font-size:12px; }
</style>
