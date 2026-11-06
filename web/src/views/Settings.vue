<script setup>
const TABS = [
  { group: 'iptv-org', label: 'iptv-org' },
  { group: 'health', label: 'Health' },
  { group: 'proxy', label: 'Proxy' },
  { group: 'stats', label: 'Stats' },
  { group: 'security', label: 'Security' },
]
</script>

<template>
  <section class="settings">
    <nav class="tabs" aria-label="Settings sections">
      <RouterLink :to="{ name: 'settings' }" active-class="" exact-active-class="on">General</RouterLink>
      <RouterLink v-for="t in TABS" :key="t.group" :to="{ name: 'settings-group', params: { group: t.group } }" active-class="" exact-active-class="on">{{ t.label }}</RouterLink>
      <RouterLink :to="{ name: 'settings-users' }" active-class="" exact-active-class="on">Users</RouterLink>
    </nav>
    <RouterView />
  </section>
</template>

<style scoped>
.settings h1 { font-size:22px; margin:0 0 12px; }
.tabs { display:flex; gap:4px; margin-bottom:16px; overflow-x:auto; overflow-y:hidden; box-shadow:inset 0 -1px 0 var(--line); }
.tabs a { padding:8px 14px; color:var(--muted); border-bottom:2px solid transparent; white-space:nowrap; }
.tabs a:hover { color:var(--text); }
.tabs a.on { color:var(--text); border-bottom-color:var(--accent); font-weight:500; }
.tabs a:focus-visible { outline:2px solid var(--accent); outline-offset:-2px; border-radius:6px; }
</style>
