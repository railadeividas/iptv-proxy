<script setup>
import { ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { store, can } from './store'
import { forgetChannelsQuery } from './router'
import { api } from './api'
import { connect, disconnect } from './ws'
import ThemeToggle from './components/ThemeToggle.vue'
import ConfirmDialog from './components/ConfirmDialog.vue'
import AccountModal from './components/AccountModal.vue'
import { REPO, versionUrl } from './about'

const router = useRouter()

// Live connection only while signed in.
watch(() => !!store.user, (ok) => (ok ? connect() : disconnect()), { immediate: true })
const account = ref(false)


async function logout() {
  await api.post('/logout')
  forgetChannelsQuery() // the next person starts with clean filters
  store.user = null
  store.status = null
  router.push({ name: 'login' })
}
</script>

<template>
  <template v-if="$route.name === 'login' || $route.name === 'setup'"><RouterView /></template>
  <template v-else>
    <header>
      <strong>IPTV Proxy</strong>
      <nav>
        <RouterLink to="/channels">Channels</RouterLink>
        <!-- /profiles/:id has a parameter, which the link itself does not, so the router would not mark it active -->
        <RouterLink to="/profiles" :class="{ 'router-link-active': $route.name === 'profiles' }">Profiles</RouterLink>
        <RouterLink to="/stats">Stats</RouterLink>
        <RouterLink v-if="can('sources')" to="/sources">Sources</RouterLink>
        <RouterLink v-if="can('health')" to="/health">Health</RouterLink>
        <RouterLink v-if="can('settings')" to="/settings">Settings</RouterLink>
      </nav>
      <span v-if="store.status && !store.status.ready" class="pill warn">
        {{ store.status.sync.running ? 'Loading channel data…' : 'Waiting for first sync' }}
      </span>
      <span class="live" :class="{ on: store.wsConnected }" :title="store.wsConnected ? 'Live updates connected' : 'Reconnecting…'":aria-label="store.wsConnected ? 'Live updates connected' : 'Reconnecting…'">●</span>
      <ThemeToggle />
      <button class="ghost icon" :title="`Signed in as ${store.user?.username} — change your password`" :aria-label="`Account: ${store.user?.username}`" @click="account = true">
        <svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="12" cy="8" r="4" /><path d="M4 21a8 8 0 0 1 16 0" /></svg>
      </button>
      <button class="ghost icon" title="Sign out" aria-label="Sign out" @click="logout">
        <svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4" /><path d="M16 17l5-5-5-5M21 12H9" /></svg>
      </button>
    </header>
    <main><RouterView /></main>
    <footer class="site">
      <a v-if="store.version" class="fver" :href="versionUrl(store.version)" target="_blank" rel="noopener noreferrer" title="Version of this app (the commit it was built from): open it on GitHub">{{ store.version }}</a>
      <span class="flinks">
        <a :href="REPO" target="_blank" rel="noopener noreferrer" title="Source code on GitHub">
          <svg viewBox="0 0 16 16" width="16" height="16" fill="currentColor" aria-hidden="true"><path d="M8 0C3.58 0 0 3.58 0 8c0 3.54 2.29 6.53 5.47 7.59.4.07.55-.17.55-.38 0-.19-.01-.82-.01-1.49-2.01.37-2.53-.49-2.69-.94-.09-.23-.48-.94-.82-1.13-.28-.15-.68-.52-.01-.53.63-.01 1.08.58 1.23.82.72 1.21 1.87.87 2.33.66.07-.52.28-.87.51-1.07-1.78-.2-3.64-.89-3.64-3.95 0-.87.31-1.59.82-2.15-.08-.2-.36-1.02.08-2.12 0 0 .67-.21 2.2.82.64-.18 1.32-.27 2-.27s1.36.09 2 .27c1.53-1.04 2.2-.82 2.2-.82.44 1.1.16 1.92.08 2.12.51.56.82 1.27.82 2.15 0 3.07-1.87 3.75-3.65 3.95.29.25.54.73.54 1.48 0 1.07-.01 1.93-.01 2.2 0 .21.15.46.55.38A8.01 8.01 0 0 0 16 8c0-4.42-3.58-8-8-8z" /></svg>
          GitHub
        </a>
        <a :href="`${REPO}/commits/main`" target="_blank" rel="noopener noreferrer">Changes</a>
        <a :href="`${REPO}/issues`" target="_blank" rel="noopener noreferrer">Report an issue</a>
      </span>
    </footer>
  </template>
  <AccountModal v-if="account" @close="account = false" />
  <ConfirmDialog />
  <div v-if="store.toast" class="toast">{{ store.toast }}</div>
</template>

<style>
/* the bar spans the page, its content lines up with <main> (max-width 1240 incl. 20px padding) */
header { display:flex; align-items:center; gap:16px; padding:10px max(20px, calc((100% - 1240px) / 2 + 20px)); background:var(--panel); border-bottom:1px solid var(--line); position:sticky; top:0; z-index:5; flex-wrap:wrap; }
header nav { display:flex; gap:4px; flex:1; min-width:0; }
header nav a { color:var(--muted); padding:6px 12px; border-radius:6px; white-space:nowrap; }
@media (max-width:760px) {
  header { gap:8px 12px; padding:8px 12px; }
  header strong { margin-right:auto; }
  header nav { order:5; flex:1 0 100%; gap:2px; overflow-x:auto; scrollbar-width:none; }
  header nav a { padding:6px 8px; font-size:13px; }
  header nav::-webkit-scrollbar { display:none; }
}
header nav a.router-link-active { color:var(--text); font-weight:500; box-shadow:inset 0 -1px 0 var(--accent); }
/* short pages: the footer sticks to the bottom of the window (not on login/setup, which have no footer) */
#app:has(> footer.site) { min-height:100vh; min-height:100dvh; display:flex; flex-direction:column; }
#app:has(> footer.site) > main { flex:1 0 auto; }
main { box-sizing:border-box; width:100%; max-width:1240px; margin:0 auto; padding:20px; }
.site { box-sizing:border-box; width:100%; max-width:1240px; margin:0 auto 0; padding:0 20px; font-size:13px; color:var(--muted); display:flex; align-items:center; justify-content:space-between; gap:8px 16px; flex-wrap:wrap; }
.site::before { content:''; flex:1 0 100%; height:1px; background:var(--line); }
.site .flinks { display:flex; align-items:center; gap:24px; flex-wrap:wrap; margin-left:auto; padding:10px 0 16px; }
.site .fver { padding:10px 0 16px; font-variant-numeric:tabular-nums; }
.site a { display:inline-flex; align-items:center; gap:6px; color:var(--muted); }
.site a:hover { color:var(--text); }
@media (max-width:760px) { .site { padding:0 12px; } .site .flinks { gap:16px; } }
.pill { font-size:12px; padding:3px 10px; border-radius:999px; background:var(--bg); border:1px solid var(--line); color:var(--muted); }
.pill.ok { color:var(--ok); } .pill.warn { color:var(--warn); }
.live { font-size:12px; color:var(--bad); display:inline-flex; align-items:center; gap:4px; } .live.on { color:var(--ok); }
header .icon { width:var(--ctl); padding:0; display:inline-flex; align-items:center; justify-content:center; }
.toast { position:fixed; bottom:20px; left:50%; transform:translateX(-50%); background:var(--text); color:var(--bg); padding:8px 16px; border-radius:8px; z-index:20; }
</style>
