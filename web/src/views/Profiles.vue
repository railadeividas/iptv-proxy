<script setup>
import { ref, computed, watch, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { api } from '../api'
import { toast, can } from '../store'
import { useLive } from '../util'
import ProfileDetail from '../components/ProfileDetail.vue'

const route = useRoute(), router = useRouter()
const profiles = ref([]), name = ref(''), loaded = ref(false)
const IDEAS = ['Living room', 'Bedroom', 'Kitchen', 'Phone']
const id = computed(() => (route.params.id ? Number(route.params.id) : null))

async function load() {
  profiles.value = await api.get('/profiles')
  loaded.value = true
  // landing on /profiles opens the first profile; a deleted one falls back too
  if (!id.value || !profiles.value.some((p) => p.id === id.value)) {
    if (profiles.value.length) router.replace({ name: 'profiles', params: { id: profiles.value[0].id } })
    else if (id.value) router.replace({ name: 'profiles' })
  }
}
onMounted(() => load().catch(() => {}))
watch(() => route.params.id, () => { if (loaded.value && !id.value) load() })
useLive({ profiles: () => load().catch(() => {}), catalog: () => load().catch(() => {}) })

async function create() {
  try {
    const p = await api.post('/profiles', { name: name.value })
    name.value = ''
    await load()
    router.push({ name: 'profiles', params: { id: p.id } })
  } catch (e) { toast(e.message) }
}
</script>

<template>
  <div v-if="loaded && !profiles.length" class="welcome">
    <div class="hero" aria-hidden="true">
      <svg viewBox="0 0 64 64" width="56" height="56" fill="none" stroke="currentColor" stroke-width="3" stroke-linecap="round" stroke-linejoin="round">
        <rect x="8" y="16" width="48" height="32" rx="5" />
        <path d="M24 56h16M32 48v8M24 8l8 8 8-8" />
        <path d="M22 28h20M22 36h12" />
      </svg>
    </div>
    <h1>Create your first profile</h1>
    <p class="lead">A profile is one playlist and TV guide for one player, TV or person. Pick the channels you want in it, then paste its two links into your player.</p>

    <form class="create" @submit.prevent="create">
      <input v-model="name" placeholder="Profile name, e.g. Living room" aria-label="Profile name" autofocus>
      <button class="primary" :disabled="!name.trim()">Create profile</button>
    </form>
    <div class="ideas">
      <span class="muted">Ideas:</span>
      <button v-for="i in IDEAS" :key="i" type="button" class="idea" @click="name = i">{{ i }}</button>
    </div>

    <ol class="steps">
      <li><span class="n">1</span><b>Create a profile</b><span class="muted">Give it a name you will recognise.</span></li>
      <li><span class="n">2</span><b>Add channels</b><span class="muted">Browse the <RouterLink to="/channels">Channels</RouterLink> tab, <template v-if="can('sources')">or bring your own from <RouterLink to="/sources">Sources</RouterLink>.</template></span></li>
      <li><span class="n">3</span><b>Copy the links</b><span class="muted">Paste the playlist and guide links into VLC, Kodi, TiviMate or any player.</span></li>
    </ol>
  </div>

  <div v-else-if="loaded" class="split">
    <aside>
      <form class="inline" @submit.prevent="create">
        <input v-model="name" placeholder="New profile (e.g. Living room)">
        <button class="primary">Add</button>
      </form>
      <RouterLink v-for="p in profiles" :key="p.id" :to="{ name: 'profiles', params: { id: p.id } }" class="pitem" active-class="active">
        <b>{{ p.name }}</b><div class="muted">{{ p.count }} channels<template v-if="can('profiles.all')"> · {{ p.owner }}</template></div>
      </RouterLink>
    </aside>
    <div>
      <ProfileDetail v-if="id" :key="id" :id="id" />
    </div>
  </div>
</template>

<style scoped>
.welcome { max-width:680px; margin:6vh auto 0; text-align:center; padding:0 4px; }
.welcome h1 { font-size:26px; margin:0 0 8px; }
.lead { margin:0 auto 24px; max-width:520px; color:var(--muted); font-size:15px; line-height:1.5; }
.create { display:flex; gap:10px; max-width:460px; margin:0 auto; }
.create input { flex:1; min-width:0; }
.ideas { display:flex; gap:8px; flex-wrap:wrap; justify-content:center; align-items:center; margin:12px 0 0; }
.idea { height:30px; padding:0 12px; border-radius:999px; background:var(--panel); color:var(--muted); font-size:13px; }
.idea:hover { color:var(--text); }
.steps { list-style:none; margin:40px 0 0; padding:0; display:grid; grid-template-columns:repeat(3, 1fr); gap:12px; text-align:left; }
.steps li { display:flex; flex-direction:column; gap:4px; padding:14px; border:1px solid var(--line); border-radius:10px; background:var(--panel); font-size:13px; }
.steps b { font-size:14px; }
.steps a { color:var(--accent); }
.n { display:inline-flex; align-items:center; justify-content:center; width:24px; height:24px; border-radius:50%; background:color-mix(in srgb, var(--accent) 14%, transparent); color:var(--accent); font-weight:600; font-size:12px; margin-bottom:4px; }
@media (max-width:760px) { .welcome { margin-top:2vh; } .steps { grid-template-columns:1fr; } .create { flex-direction:column; } .welcome h1 { font-size:22px; } }
.split { display:grid; grid-template-columns:260px 1fr; gap:20px; }
@media (max-width:760px) { .split { grid-template-columns:1fr; } }
aside .inline { margin-bottom:12px; }
.pitem { display:block; padding:10px 12px; border:1px solid var(--line); border-radius:8px; margin-bottom:8px; background:var(--panel); }
.pitem.active { border-color:var(--accent); }
</style>
