import { createRouter, createWebHistory } from 'vue-router'
import { store, can } from './store'

export const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/', redirect: '/channels' },
    { path: '/setup', name: 'setup', component: () => import('./views/Setup.vue'), meta: { public: true } },
    { path: '/login', name: 'login', component: () => import('./views/Login.vue'), meta: { public: true } },
    { path: '/channels', name: 'channels', component: () => import('./views/Channels.vue') },
    { path: '/profiles/:id?', name: 'profiles', component: () => import('./views/Profiles.vue') },
    { path: '/stats/:tab(live|overview|channels|profiles|devices|sessions|proxy)?', name: 'stats', component: () => import('./views/Stats.vue') },
    { path: '/sources', name: 'sources', component: () => import('./views/Sources.vue'), meta: { perm: 'sources' } },
    { path: '/health', name: 'health', component: () => import('./views/Health.vue'), meta: { perm: 'health' } },
    {
      path: '/settings',
      component: () => import('./views/Settings.vue'),
      meta: { perm: 'settings' },
      children: [
        { path: '', name: 'settings', component: () => import('./components/SettingsPanel.vue'), props: { group: 'general' } },
        { path: 'users', name: 'settings-users', component: () => import('./components/UsersPanel.vue') },
        { path: ':group(iptv-org|health|proxy|stats|metrics|security)', name: 'settings-group', component: () => import('./components/SettingsPanel.vue'), props: true },
      ],
    },
    { path: '/:rest(.*)*', redirect: '/channels' },
  ],
  scrollBehavior: (to, from, saved) => saved || (to.path === from.path ? false : { top: 0 }),
})

// The Channels filters live in the URL. Remember them for this browser tab, so coming back from
// another page (Profiles, Health, …) shows the list as it was left.
const KEEP = 'channelsQuery'
export const forgetChannelsQuery = () => { try { sessionStorage.removeItem(KEEP) } catch {} }
const savedChannelsQuery = () => { try { return JSON.parse(sessionStorage.getItem(KEEP) || '{}') } catch { return {} } }

router.afterEach((to) => {
  if (to.name !== 'channels') return
  try { sessionStorage.setItem(KEEP, JSON.stringify(to.query)) } catch {}
})

router.beforeEach(async (to, from) => {
  if (store.user === undefined) {
    try {
      const s = await (await fetch('/api/session')).json()
      store.user = s.user
      store.setupNeeded = s.setup_needed
      store.version = s.version || ''
    } catch {
      store.user = null
    }
  }
  // first start: the only thing to do is to create the superuser
  if (store.setupNeeded) return to.name === 'setup' ? undefined : { name: 'setup' }
  if (to.name === 'setup') return { name: store.user ? 'channels' : 'login' }
  if (!to.meta.public && !store.user) return { name: 'login', query: { next: to.fullPath } }
  if (to.name === 'login' && store.user) return { name: 'channels' }
  if (to.matched.some((m) => m.meta.perm && !can(m.meta.perm))) return { name: 'channels' }
  // arriving at Channels from another page without filters: bring the last ones back
  if (to.name === 'channels' && from.name && from.name !== 'channels' && !Object.keys(to.query).length) {
    const q = savedChannelsQuery()
    if (Object.keys(q).length) return { name: 'channels', query: q }
  }
})
