<script setup>
import { ref, computed, watch } from 'vue'
import { useRouter } from 'vue-router'
import { api } from '../api'
import { can } from '../store'
import { useStatsQuery, useLoader, TABS, DEFAULT_TAB, history } from '../stats'
import StatsLive from '../components/stats/StatsLive.vue'
import StatsOverview from '../components/stats/StatsOverview.vue'
import StatsChannels from '../components/stats/StatsChannels.vue'
import StatsProfiles from '../components/stats/StatsProfiles.vue'
import StatsDevices from '../components/stats/StatsDevices.vue'
import StatsSessions from '../components/stats/StatsSessions.vue'
import StatsProxy from '../components/stats/StatsProxy.vue'
import Scope from '../components/stats/Scope.vue'
import TimeRange from '../components/stats/TimeRange.vue'
import ProfilePicker from '../components/stats/ProfilePicker.vue'
import StepSelect from '../components/stats/StepSelect.vue'

// Who uses the playlist links, in tabs: totals, who is watching now, an overview of the range, channels, profiles, devices, every session and
// the proxy itself. A user sees their own profiles, an administrator everybody's (the server decides). The tab, period,
// profile and measure live in the URL, so back/forward and shared links work.
const router = useRouter()
const q = useStatsQuery()
const PANES = { live: StatsLive, overview: StatsOverview, channels: StatsChannels, profiles: StatsProfiles, devices: StatsDevices, sessions: StatsSessions, proxy: StatsProxy }

const o = ref(null) // overview: now, totals, profiles, the load of the proxy, settings that matter here
useLoader(async (cur) => {
  let d
  try {
    d = await api.get('/stats?' + q.params())
  } catch (e) {
    // a selected profile was deleted (or is not yours): drop the selection instead of failing the whole page
    if (e.message === 'profile not found' && q.profile.value) { q.setQuery({ profile: '' }, true); return }
    throw e
  }
  if (cur()) o.value = d
}, { deps: () => [q.rangeKey.value, q.stepKey.value, q.profile.value], every: 5000 })

const many = computed(() => !!o.value?.all_profiles)
// the Proxy tab is for roles that see the whole server
const tabs = computed(() => TABS.filter((t) => t[0] !== 'proxy' || !o.value || !!o.value.cache))
const pane = computed(() => PANES[q.tab.value])
watch([o, () => q.tab.value], () => { if (o.value && !o.value.cache && q.tab.value === 'proxy') router.replace({ name: 'stats', params: { tab: DEFAULT_TAB }, query: tabQuery() }) })
const KEEP = ['range', 'from', 'to', 'step', 'profile', 'metric', 'split', 'type', 'compare']
function tabQuery() { return Object.fromEntries(KEEP.filter((k) => q.route.query[k] != null).map((k) => [k, q.route.query[k]])) }
const to = (tab) => ({ name: 'stats', params: { tab: tab === DEFAULT_TAB ? undefined : tab }, query: tabQuery() })
const keep = computed(() => o.value?.history_days)
watch(keep, (d) => { if (d) history.value = d }, { immediate: true })
const isLive = computed(() => q.tab.value === 'live')
</script>

<template>
  <section class="stats">
    <div class="filters">
      <div class="f"><span>Time range</span><TimeRange /></div>
      <StepSelect :bucket="o?.window?.bucket || 0" :min="q.tab.value === 'proxy' ? 3600 : 0" />
      <div class="f"><span>Profiles</span><ProfilePicker :profiles="o?.profiles || []" :many="many" /></div>
      <span v-if="o" class="muted small keep">History is kept for {{ keep }} {{ keep === 1 ? 'day' : 'days' }}<template v-if="can('settings')"> (<RouterLink :to="{ name: 'settings-group', params: { group: 'stats' } }">Settings</RouterLink>)</template>.</span>
    </div>

    <p v-if="o && !o.enabled" class="note warn">Recording is off, so nothing new is counted.
      <RouterLink v-if="can('settings')" :to="{ name: 'settings-group', params: { group: 'stats' } }">Turn it on</RouterLink>
      <template v-else>Ask an administrator to turn it on.</template></p>

    <nav class="tabs" aria-label="Stats sections">
      <RouterLink v-for="t in tabs" :key="t[0]" :to="to(t[0])" active-class="" exact-active-class="" :class="{ on: q.tab.value === t[0] }" :aria-current="q.tab.value === t[0] ? 'page' : null">{{ t[1] }}<span v-if="t[0] === 'live' && o?.now?.some((x) => x.mode === 'proxy')" class="live-dot" title="Somebody is watching"></span></RouterLink>
    </nav>

    <div class="pane">
      <component :is="pane" :key="q.tab.value" :o="o" />
    </div>
  </section>
</template>

<style scoped>
.filters { display:flex; gap:12px; flex-wrap:wrap; align-items:flex-end; margin-bottom:12px; }
.f { display:flex; flex-direction:column; gap:4px; min-width:0; max-width:100%; }
.f select { max-width:100%; text-overflow:ellipsis; }
.f > span { font-size:11px; text-transform:uppercase; letter-spacing:.04em; color:var(--muted); }
.live-note { margin:-4px 0 12px; }
.keep { margin-left:auto; align-self:center; }
.small { font-size:13px; }
.note { padding:8px 12px; border:1px solid var(--line); border-radius:8px; background:var(--panel); margin:0 0 12px; }
.note.warn { border-color:var(--warn); }
.tabs { display:flex; gap:4px; margin-bottom:16px; overflow-x:auto; overflow-y:hidden; box-shadow:inset 0 -1px 0 var(--line); }
.tabs a { padding:8px 14px; color:var(--muted); border-bottom:2px solid transparent; white-space:nowrap; }
.tabs a:hover { color:var(--text); }
.tabs a.on { color:var(--text); border-bottom-color:var(--accent); font-weight:500; }
.tabs a:focus-visible { outline:2px solid var(--accent); outline-offset:-2px; border-radius:6px; }
.live-dot { display:inline-block; width:7px; height:7px; border-radius:50%; background:var(--ok); margin-left:6px; vertical-align:middle; }
.pane { display:flex; flex-direction:column; gap:16px; }
.pane :deep(.card), .pane :deep(.card-row) { margin-bottom:0; }
</style>
