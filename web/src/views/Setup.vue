<script setup>
import { ref, computed } from 'vue'
import { useRouter } from 'vue-router'
import { api } from '../api'
import { store } from '../store'

// First start: nobody exists yet, so the person who opens the page makes the superuser.
const username = ref(''), pw = ref(''), pw2 = ref(''), err = ref('')
const router = useRouter()
const mismatch = computed(() => pw2.value && pw.value !== pw2.value)
async function submit() {
  err.value = ''
  if (pw.value !== pw2.value) { err.value = 'The two passwords are not the same'; return }
  try {
    store.user = await api.post('/setup', { username: username.value, password: pw.value })
    store.setupNeeded = false
    router.replace('/channels')
  } catch (e) { err.value = e.message }
}
</script>

<template>
  <div class="welcome">
    <div class="hero" aria-hidden="true">
      <svg viewBox="0 0 64 64" width="56" height="56" fill="none" stroke="currentColor" stroke-width="3" stroke-linecap="round" stroke-linejoin="round">
        <path d="M32 6l20 8v16c0 13-8.5 22-20 28C20.5 52 12 43 12 30V14z" />
        <circle cx="32" cy="27" r="6" />
        <path d="M21 43c2-6 6-8 11-8s9 2 11 8" />
      </svg>
    </div>
    <h1>Welcome to IPTV Proxy</h1>
    <p class="lead">Start by creating the superuser account.</p>

    <form class="create" @submit.prevent="submit">
      <input v-model="username" placeholder="User name" autocomplete="username" aria-label="User name" autofocus>
      <input v-model="pw" type="password" placeholder="Password" autocomplete="new-password" aria-label="Password">
      <input v-model="pw2" type="password" placeholder="Repeat the password" autocomplete="new-password" aria-label="Repeat the password">
      <p v-if="mismatch" class="err">The two passwords are not the same</p>
      <button class="primary" :disabled="!username || !pw">Create account</button>
      <p v-if="err" class="err">{{ err }}</p>
    </form>
  </div>
</template>

<style scoped>
.welcome { max-width:680px; margin:10vh auto 0; text-align:center; padding:0 16px 24px; }
.welcome h1 { font-size:26px; margin:0 0 8px; }
.lead { margin:0 auto 24px; max-width:520px; color:var(--muted); font-size:15px; line-height:1.5; }
.create { display:flex; flex-direction:column; gap:10px; max-width:380px; margin:0 auto; }
.create .err { margin:0; font-size:13px; text-align:left; }
.steps { list-style:none; margin:40px 0 0; padding:0; display:grid; grid-template-columns:repeat(3, 1fr); gap:12px; text-align:left; }
.steps li { display:flex; flex-direction:column; gap:4px; padding:14px; border:1px solid var(--line); border-radius:10px; background:var(--panel); font-size:13px; }
.steps b { font-size:14px; }
@media (max-width:760px) { .welcome { margin-top:4vh; } .steps { grid-template-columns:1fr; } .welcome h1 { font-size:22px; } }
</style>
