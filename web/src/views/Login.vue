<script setup>
import { ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { api } from '../api'
import { store } from '../store'

const username = ref(''), pw = ref(''), err = ref('')
const route = useRoute(), router = useRouter()
async function submit() {
  err.value = ''
  try {
    store.user = await api.post('/login', { username: username.value, password: pw.value })
    router.replace(typeof route.query.next === 'string' && route.query.next.startsWith('/') ? route.query.next : '/channels')
  } catch (e) { err.value = e.message }
}
</script>

<template>
  <div class="login">
    <form class="card" @submit.prevent="submit">
      <h1>IPTV Proxy</h1>
      <input v-model="username" placeholder="User name" autocomplete="username" aria-label="User name" autofocus>
      <input v-model="pw" type="password" placeholder="Password" autocomplete="current-password" aria-label="Password">
      <button class="primary">Sign in</button>
      <p v-if="err" class="err">{{ err }}</p>
    </form>
  </div>
</template>

<style scoped>
.login { display:grid; place-items:center; min-height:100vh; padding:16px; }
.card { width:min(320px, 100%); }
.card h1 { margin:0 0 12px; font-size:20px; }
.card p { margin:0 0 10px; font-size:13px; }
.card button { width:100%; }
</style>
