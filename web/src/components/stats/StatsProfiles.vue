<script setup>
import MeasureSwitch from './MeasureSwitch.vue'
import Scope from './Scope.vue'
import { ref, computed } from 'vue'
import { api } from '../../api'
import { rel, dur, bytes } from '../../util'
import { useStatsQuery, useLoader, usePaging, bucketWord, METRICS, numberRows, profileColor } from '../../stats'
import StatChart from './StatChart.vue'
import BarList from './BarList.vue'
import TimelineCard from './TimelineCard.vue'
import Pager from '../Pager.vue'

// A compact card per profile (searchable, sortable and paged, so many profiles stay manageable), how each one's share changed, and how often players refresh their playlist and guide.
const props = defineProps({ o: Object })
const q = useStatsQuery()
const t = ref(null), prevT = ref(null), samples = ref(null), ranking = ref([])

useLoader(async (cur) => {
  const [tl, ptl, sm, rk] = await Promise.all([
    api.get('/stats/timeline?' + q.params()),
    q.compare.value ? api.get('/stats/timeline?' + q.params({}, true)) : null,
    api.get('/stats/samples?' + q.params()),
    api.get('/stats/breakdown?' + q.params({ by: 'profile', sort: q.metric.value, limit: 10 })),
  ])
  if (!cur()) return
  t.value = tl; prevT.value = ptl; samples.value = sm; ranking.value = rk.items
}, { deps: () => [q.rangeKey.value, q.stepKey.value, q.profile.value, q.compare.value, q.metric.value] })

const M = computed(() => METRICS[q.metric.value])
const many = computed(() => !!props.o?.all_profiles)
const all = computed(() => props.o?.profiles || [])
const shown = computed(() => (q.selected.value.length ? all.value.filter((p) => q.selected.value.includes(p.profile_id)) : all.value))
const pg = usePaging('statsProfilesSize')
const SORTS = [['sessions', 'Most sessions'], ['bytes', 'Most data'], ['seconds', 'Most watch time'], ['name', 'Name'], ['last_seen', 'Last seen']]
const search = computed(() => q.route.query.search || '')
const sort = computed(() => (SORTS.some((x) => x[0] === q.route.query.sort) ? q.route.query.sort : 'sessions'))
const list = computed(() => {
  const s = search.value.trim().toLowerCase()
  const r = s ? shown.value.filter((p) => p.profile.toLowerCase().includes(s) || (p.owner || '').toLowerCase().includes(s)) : [...shown.value]
  const by = sort.value
  return r.sort((a, b) => (by === 'name' ? a.profile.localeCompare(b.profile) : b[by] - a[by] || a.profile.localeCompare(b.profile)))
})
const pages = computed(() => Math.max(1, Math.ceil(list.value.length / pg.size.value)))
const page = computed(() => Math.min(pg.page.value, pages.value - 1))
const visible = computed(() => list.value.slice(page.value * pg.size.value, (page.value + 1) * pg.size.value))
const fetchesOf = computed(() => new Map((samples.value?.by_profile || []).map((f) => [f.profile_id, f])))
const fetchOf = (p) => ({ playlists: 0, guides: 0, ...fetchesOf.value.get(p.profile_id) })
const fetchRows = computed(() => {
  if (!samples.value) return []
  const s = samples.value
  const pl = Array(s.n).fill(0), gu = Array(s.n).fill(0)
  for (const f of s.fetches) { pl[f.i] = f.playlists; gu[f.i] = f.guides }
  return numberRows(s, pl, 'playlists').map((r, i) => ({ ...r, v: { playlists: pl[i], guides: gu[i] } }))
})
const rankRows = computed(() => ranking.value.map((x) => ({ key: x.key, name: x.name, v: M.value.pick(x), c: profileColor(x.key, all.value) })))
</script>

<template>
  <div class="card">
    <div class="chead">
      <h2>Profiles<Scope /> <span v-if="list.length" class="muted count">{{ list.length.toLocaleString() }}</span></h2>
      <div v-if="shown.length > 6" class="tools">
        <input type="search" :value="search" placeholder="Search profiles" aria-label="Search profiles" @input="q.setQuery({ search: $event.target.value, page: '' }, true)">
        <select :value="sort" aria-label="Sort profiles" @change="q.setQuery({ sort: $event.target.value === 'sessions' ? '' : $event.target.value, page: '' })">
          <option v-for="x in SORTS" :key="x[0]" :value="x[0]">{{ x[1] }}</option>
        </select>
      </div>
    </div>
    <p v-if="o && !shown.length" class="muted small">No profiles yet.</p>
    <p v-else-if="o && !list.length" class="muted small">No profile matches “{{ search }}”.</p>
    <div v-else class="plist">
      <div class="ph" aria-hidden="true">
        <span>Profile</span><span>Sessions</span><span>Watched</span><span>Data</span><span>Devices</span><span>Playlist fetches</span><span>Guide fetches</span><span>Last seen</span>
      </div>
      <div v-for="p in visible" :key="p.profile_id" class="pr">
        <div class="who">
          <i class="dot" :style="{ background: profileColor(p.profile_id, all) }"></i>
          <div class="nm"><b :title="p.profile">{{ p.profile }}</b><span v-if="many && p.owner" class="muted small">{{ p.owner }}</span></div>
        </div>
        <div class="c" data-l="Sessions">{{ p.sessions.toLocaleString() }}</div>
        <div class="c" data-l="Watched">{{ dur(p.seconds) }}</div>
        <div class="c" data-l="Data">{{ bytes(p.bytes) }}</div>
        <div class="c" data-l="Devices">{{ p.clients.toLocaleString() }}</div>
        <div class="c" data-l="Playlist fetches">{{ fetchOf(p).playlists.toLocaleString() }}</div>
        <div class="c" data-l="Guide fetches">{{ fetchOf(p).guides.toLocaleString() }}</div>
        <div class="c seen" data-l="Last seen">{{ rel(p.last_seen) }}</div>
      </div>
    </div>
    <Pager v-if="list.length > 5" :page="page" :pages="pages" :total="list.length" :size="pg.size.value" :count="visible.length" :sizes="pg.SIZES" @go="pg.goPage" @size="pg.setSize" />
  </div>
  <TimelineCard :t="t" :prev="prevT" :profiles="all" />
  <div class="card-row">
  <div class="card"><div class="chead"><h2>Profiles by {{ M.unit }}<Scope /></h2><MeasureSwitch /></div><p class="muted small">Who watches the most</p>
    <BarList :rows="rankRows" :fmt="M.fmt" empty="Nothing was played in this period." /></div>
  <div class="card">
    <h2>Playlist and guide fetches<Scope /></h2><p class="muted small">Players refreshing their lists, per {{ bucketWord(samples?.bucket) }}<template v-if="samples && t && samples.bucket !== t.bucket"> (recorded hour by hour, so never finer than that)</template></p>
      <div class="legend"><span><i style="background:var(--s1)"></i>Playlist</span><span><i style="background:var(--s3)"></i>Guide</span></div>
      <StatChart :rows="fetchRows" :keys="['playlists', 'guides']" :names="{ playlists: 'Playlist', guides: 'Guide' }" :colors="{ playlists: 'var(--s1)', guides: 'var(--s3)' }" type="bars" int :fmt="(v) => v.toLocaleString()" :height="200" label="Playlist and guide fetches" />
  </div>
  </div>
</template>

<style scoped>
.plist { border:1px solid var(--line); border-radius:10px; overflow:hidden; }
.ph, .pr { display:grid; grid-template-columns:minmax(150px, 2.2fr) repeat(4, minmax(64px, 1fr)) repeat(2, minmax(80px, 1fr)) minmax(80px, 1fr); gap:12px; align-items:center; padding:10px 14px; }
.ph { font-size:12px; color:var(--muted); background:color-mix(in srgb, var(--muted) 7%, transparent); border-bottom:1px solid var(--line); }
.ph span:not(:first-child), .pr .c { text-align:right; }
.pr { border-bottom:1px solid var(--line); font-variant-numeric:tabular-nums; }
.pr:last-child { border-bottom:0; }
.pr:hover { background:color-mix(in srgb, var(--accent) 5%, transparent); }
.who { display:flex; align-items:center; gap:10px; min-width:0; }
.dot { flex:none; width:10px; height:10px; border-radius:3px; }
.nm { display:flex; flex-direction:column; min-width:0; line-height:1.3; }
.nm b { overflow:hidden; text-overflow:ellipsis; white-space:nowrap; }
.nm .small { margin:0; font-size:12px; }
.seen { white-space:nowrap; color:var(--muted); }
/* a phone has no room for columns: every row becomes a small card, two values side by side */
@media (max-width:900px) {
  .ph { display:none; }
  .pr { grid-template-columns:repeat(3, minmax(0, 1fr)); gap:10px 12px; padding:12px 14px; }
  .pr .who { grid-column:1 / 3; }
  .pr .seen { grid-column:3; grid-row:1; text-align:right; font-weight:400; font-size:12px; }
  .pr .seen::before { display:none; }
  .pr .c { text-align:left; display:flex; flex-direction:column; font-weight:600; font-size:14px; }
  .pr .c::before { content:attr(data-l); font-size:11px; font-weight:400; color:var(--muted); }
}
@media (max-width:360px) { .pr { grid-template-columns:repeat(2, minmax(0, 1fr)); } .pr .who { grid-column:1; } .pr .seen { grid-column:2; } }
.tools { display:grid; grid-template-columns:minmax(150px, 260px) auto; gap:8px; align-items:center; }
.tools input:not([type=file]):not([type=checkbox]) { width:100%; }
@media (max-width:560px) { .tools { width:100%; grid-template-columns:minmax(0, 1fr) auto; } }
.count { font-size:12px; font-weight:400; }
.small { font-size:13px; margin:0 0 10px; }
.legend { display:flex; flex-wrap:wrap; gap:4px 14px; margin:0 0 8px; font-size:12px; }
.legend span { display:inline-flex; align-items:center; gap:6px; }
.legend i { width:10px; height:10px; border-radius:2px; display:inline-block; }
</style>
