<script setup>
import { ref, reactive, computed, watch, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { api } from '../api'
import { toast } from '../store'
import Pager from '../components/Pager.vue'
import AddToProfileModal from '../components/AddToProfileModal.vue'
import ChannelModal from '../components/ChannelModal.vue'
import { streamState, stateTone, stateTitle, debounced, useLive, PAGE_SIZES as SIZES, PAGE_SIZE } from '../util'

const SORTS = [
  ['name', 'Name A–Z'], ['name_desc', 'Name Z–A'], ['country', 'Country'], ['status', 'Status (working first, then partly working)'], ['streams', 'Most streams'],
]
const FILTER_KEYS = ['q', 'country', 'category', 'language', 'source', 'working', 'inprofile', 'nsfw']
const savedSize = (() => { try { return +localStorage.getItem('pageSize') } catch { return 0 } })()
const route = useRoute(), router = useRouter()

const items = ref([]), total = ref(0), loading = ref(false), loadedOnce = ref(false)
const filters = ref({ countries: [], categories: [], languages: [], sources: [] })
const profiles = ref([]) // to name the profiles a channel is in
const modalItems = ref(null) // channels shown in the "add to profile" dialog
const opened = ref(null) // channel key shown in the details dialog
const q = ref(route.query.q || '')
const selected = reactive(new Map()) // ck -> { ck, name, profiles }, kept while paging and filtering
const badLogo = reactive({})
let addedSomething = false

const size = computed(() => {
  const n = parseInt(route.query.size) || savedSize
  return SIZES.includes(n) ? n : PAGE_SIZE
})
const sort = computed(() => (SORTS.some((x) => x[0] === route.query.sort) ? route.query.sort : 'name'))
const page = computed(() => Math.max(0, (parseInt(route.query.page) || 1) - 1))
const pages = computed(() => Math.max(1, Math.ceil(total.value / size.value)))
const activeFilters = computed(() => FILTER_KEYS.filter((k) => route.query[k]))

// Changing the page size keeps the first visible channel on screen.
function setSize(n) {
  try { localStorage.setItem('pageSize', String(n)) } catch {}
  const firstIndex = page.value * size.value
  const newPage = Math.floor(firstIndex / n)
  setQuery({ size: n === 100 ? undefined : n, page: newPage === 0 ? undefined : newPage + 1 })
}

// All filters live in the URL, so back/forward and shared links just work.
function setQuery(patch, replace = false) {
  const query = { ...route.query, ...patch }
  for (const k of Object.keys(query)) if (query[k] === '' || query[k] == null || query[k] === false) delete query[k]
  router[replace ? 'replace' : 'push']({ query })
}
const setFilter = (k, v) => setQuery({ [k]: v, page: undefined })
const goPage = (n) => setQuery({ page: n === 0 ? undefined : n + 1 })
const setSort = (v) => setQuery({ sort: v === 'name' ? undefined : v, page: undefined })
function clearFilters() {
  const { size: sz, sort: so } = route.query
  q.value = ''
  router.push({ query: { ...(sz && { size: sz }), ...(so && { sort: so }) } })
}

const applyQ = debounced(() => setQuery({ q: q.value.trim(), page: undefined }, true))
watch(q, applyQ)
watch(() => route.query.q, (v) => { if ((v || '') !== q.value.trim()) q.value = v || '' })

// the filters alone (no paging or sorting): also what "select all matching" asks for
function filterParams() {
  const p = new URLSearchParams()
  const r = route.query
  for (const k of ['q', 'country', 'category', 'language', 'source']) if (r[k]) p.set(k, r[k])
  if (r.working) p.set('working', '1')
  if (r.nsfw) p.set('nsfw', '1')
  if (r.inprofile) p.set('profile', '1')
  return p
}
function params(pg) {
  const p = filterParams()
  p.set('offset', pg * size.value); p.set('limit', size.value); p.set('sort', sort.value)
  return p
}

async function load() {
  loading.value = true
  try {
    let r = await api.get('/channels?' + params(page.value))
    const last = Math.max(0, Math.ceil(r.total / size.value) - 1)
    if (page.value > last) { goPageReplace(last); return }
    items.value = r.items; total.value = r.total; loadedOnce.value = true
  } catch (e) { if (e.message !== 'sign in required') toast(e.message) }
  finally { loading.value = false }
}
const goPageReplace = (n) => setQuery({ page: n === 0 ? undefined : n + 1 }, true)

async function loadFilters() { filters.value = await api.get('/filters') }
async function loadProfiles() { try { profiles.value = await api.get('/profiles') } catch {} }

watch(() => route.query, load)
onMounted(async () => { loadProfiles(); await loadFilters(); load() })

const reloadSoon = debounced(load, 600)
useLive({
  streams: reloadSoon,
  catalog: () => { loadFilters(); reloadSoon() },
  profiles: () => { loadProfiles(); reloadSoon() },
})

// ---- selection ----
const pick = (c) => ({ ck: c.ck, name: c.name, profiles: [...c.profiles] })
function toggle(c) { selected.has(c.ck) ? selected.delete(c.ck) : selected.set(c.ck, pick(c)) }
const onPage = computed(() => items.value.filter((c) => selected.has(c.ck)).length)
const pageAll = computed(() => items.value.length > 0 && onPage.value === items.value.length)
function togglePage() {
  if (pageAll.value) items.value.forEach((c) => selected.delete(c.ck))
  else items.value.forEach((c) => selected.set(c.ck, pick(c)))
}
const selectingAll = ref(false)
async function selectAllMatching() {
  selectingAll.value = true
  try { for (const c of await api.get('/channels/keys?' + filterParams())) selected.set(c.ck, c) } catch (e) { toast(e.message) }
  finally { selectingAll.value = false }
}
const addSelected = () => { addedSomething = false; modalItems.value = [...selected.values()].map((c) => ({ ...c })) }
const addOne = (c) => { addedSomething = false; modalItems.value = [pick(c)] }
function modalClosed() {
  modalItems.value = null
  if (addedSomething && selected.size > 1) selected.clear() // a bulk add is done
}
function modalChanged() { addedSomething = true; load() }

// ---- rows ----
const countryOf = computed(() => Object.fromEntries(filters.value.countries.map((c) => [c.id, c])))
const profileName = (id) => profiles.value.find((p) => p.id === id)?.name
const inLabel = (c) => {
  if (c.profiles.length === 1) return profileName(c.profiles[0]) || '1 profile'
  return `${c.profiles.length} profiles`
}
const inTitle = (c) => 'In ' + c.profiles.map((id) => profileName(id) || '…').join(', ') + ' – click to change'
const state = (c) => streamState(c)
const initials = (name) => (name.match(/[\p{L}\p{N}]+/gu) || ['?']).slice(0, 2).map((w) => w[0]).join('').toUpperCase()
function openRow(e, c) {
  if (e.target.closest('button, input, a, label')) return
  opened.value = c.ck
}
</script>

<template>
  <section>
    <div class="panel">
      <div class="grid">
        <input v-model="q" type="search" placeholder="Search channels…" aria-label="Search channels">
        <select v-if="filters.sources.length" :value="route.query.source || ''" aria-label="Source" @change="setFilter('source', $event.target.value)">
          <option value="">All sources</option>
          <option value="api">iptv-org</option>
          <option v-for="s in filters.sources" :key="s.id" :value="String(s.id)">{{ s.name }}</option>
        </select>
        <select :value="route.query.country || ''" aria-label="Country" @change="setFilter('country', $event.target.value)">
          <option value="">All countries</option>
          <option v-for="c in filters.countries" :key="c.id" :value="c.id">{{ c.name }}{{ c.flag ? " " + c.flag : "" }}</option>
        </select>
        <select :value="route.query.category || ''" aria-label="Category" @change="setFilter('category', $event.target.value)">
          <option value="">All categories</option>
          <option v-for="c in filters.categories" :key="c.id" :value="c.id">{{ c.name }}</option>
        </select>
        <select :value="route.query.language || ''" aria-label="Language" @change="setFilter('language', $event.target.value)">
          <option value="">All languages</option>
          <option v-for="c in filters.languages" :key="c.id" :value="c.id">{{ c.name }}</option>
        </select>
      </div>
      <div class="chips">
        <label class="chip" :class="{ on: !!route.query.working }">
          <input type="checkbox" :checked="!!route.query.working" @change="setFilter('working', $event.target.checked ? '1' : '')"><span class="tick" aria-hidden="true">✓</span>Working only
        </label>
        <label class="chip" :class="{ on: !!route.query.inprofile }">
          <input type="checkbox" :checked="!!route.query.inprofile" @change="setFilter('inprofile', $event.target.checked ? '1' : '')"><span class="tick" aria-hidden="true">✓</span>In a profile
        </label>
        <label class="chip" :class="{ on: !!route.query.nsfw }">
          <input type="checkbox" :checked="!!route.query.nsfw" @change="setFilter('nsfw', $event.target.checked ? '1' : '')"><span class="tick" aria-hidden="true">✓</span>Show NSFW
        </label>
        <button v-if="activeFilters.length" class="ghost clear" @click="clearFilters">Clear filters ({{ activeFilters.length }})</button>
      </div>
    </div>

    <div class="bar">
      <label class="selpage" :title="pageAll ? 'Unselect this page' : 'Select every channel on this page'">
        <input type="checkbox" :checked="pageAll" :indeterminate.prop="onPage > 0 && !pageAll" :disabled="!items.length" aria-label="Select all channels on this page" @change="togglePage">
        <span>{{ total.toLocaleString() }} channel{{ total === 1 ? '' : 's' }}<template v-if="activeFilters.length"> match</template></span>
      </label>
      <span class="spacer" />
      <label class="sort">Sort by
        <select :value="sort" aria-label="Sort by" @change="setSort($event.target.value)">
          <option v-for="o in SORTS" :key="o[0]" :value="o[0]">{{ o[1] }}</option>
        </select>
      </label>
    </div>

    <div class="list" :style="{ opacity: loading ? .6 : 1 }">
      <div v-for="c in items" :key="c.ck" class="crow" :class="{ sel: selected.has(c.ck) }" @click="openRow($event, c)">
        <input type="checkbox" class="cb" :checked="selected.has(c.ck)" :aria-label="'Select ' + c.name" @change="toggle(c)">
        <span class="logo">
          <img v-if="c.logo && !badLogo[c.ck]" loading="lazy" :src="c.logo" alt="" @error="badLogo[c.ck] = true">
          <span v-else class="ini" aria-hidden="true">{{ initials(c.name) }}</span>
        </span>
        <div class="info">
          <div class="name">
            <button class="namebtn" :title="'Details and check history of ' + c.name" @click="opened = c.ck">{{ c.name }}</button>
            <span v-if="c.custom" class="badge" :title="`From your custom source “${c.source || 'custom'}”`">{{ c.source || 'custom' }}</span>
            <span v-if="c.nsfw" class="badge">NSFW</span>
            <span v-if="c.closed" class="badge">closed</span>
          </div>
          <div class="meta">
            <span v-if="c.country" class="tag" :title="countryOf[c.country]?.name || c.country">{{ countryOf[c.country]?.flag }} {{ c.country }}</span>
            <span v-for="k in c.categories.slice(0, 2)" :key="k" class="tag">{{ k }}</span>
            <span v-if="c.categories.length > 2" class="tag" :title="c.categories.slice(2).join(', ')">+{{ c.categories.length - 2 }}</span>
          </div>
        </div>
        <!-- the status only shows once there is something to say; "not checked" is the quiet default -->
        <span v-if="state(c) !== 'pending'" class="st" :class="stateTone(c)" :title="stateTitle(c)">
          <i class="dot" :class="stateTone(c)" />{{ state(c) }}<template v-if="c.streams > 1"> · {{ c.working }}/{{ c.streams }}</template>
        </span>
        <button v-if="!c.profiles.length" class="add" title="Add to profile" aria-label="Add to profile" @click="addOne(c)"><svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M12 5v14M5 12h14" /></svg></button>
        <button v-else class="in" :title="inTitle(c)" @click="addOne(c)"><span class="chk" aria-hidden="true">✓</span><span class="lbl">{{ inLabel(c) }}</span></button>
      </div>
      <div v-if="!items.length && loadedOnce && !loading" class="crow muted none">No channels match<template v-if="activeFilters.length">. <button class="ghost" @click="clearFilters">Clear filters</button></template></div>
    </div>

    <div class="foot">
      <div v-if="selected.size" class="bulk" role="region" aria-label="Selected channels">
        <b>{{ selected.size.toLocaleString() }} selected</b>
        <button v-if="selected.size < total" class="ghost" :disabled="selectingAll" @click="selectAllMatching">{{ selectingAll ? 'Selecting…' : `Select all ${total.toLocaleString()} matching` }}</button>
        <span class="spacer" />
        <button class="ghost" @click="selected.clear()">Clear</button>
        <button class="primary" @click="addSelected">Add to profile…</button>
      </div>
      <Pager :page="page" :pages="pages" :total="total" :size="size" :count="items.length" :sizes="SIZES" @go="goPage" @size="setSize" />
    </div>

    <ChannelModal :ck="opened" @close="opened = null" @changed="load" />
    <AddToProfileModal v-if="modalItems" :items="modalItems" @close="modalClosed" @changed="modalChanged" />
  </section>
</template>

<style scoped>
.panel { background:var(--panel); border:1px solid var(--line); border-radius:10px; padding:12px; margin-bottom:12px; }
.grid { display:grid; grid-template-columns:minmax(220px, 2fr) repeat(auto-fit, minmax(130px, 1fr)); gap:10px; }
.grid input, .grid select { width:100%; }
.chips { display:flex; gap:8px; flex-wrap:wrap; margin-top:10px; align-items:center; }
.chip { display:inline-flex; align-items:center; gap:6px; height:var(--ctl); padding:0 14px; border:1px solid var(--line); border-radius:999px; cursor:pointer; user-select:none; color:var(--muted); background:var(--bg); position:relative; }
.chip:hover { border-color:var(--accent); }
.chip .tick { display:none; font-size:12px; }
.chip.on { background:var(--accent); border-color:var(--accent); color:#fff; font-weight:500; }
.chip.on .tick { display:inline; }
.chip input { position:absolute; opacity:0; pointer-events:none; }
.chip:has(input:focus-visible) { outline:2px solid var(--accent); outline-offset:2px; }
.clear { margin-left:auto; color:var(--accent); border-color:transparent; }
.bar { display:flex; align-items:center; gap:10px; margin-bottom:8px; flex-wrap:wrap; }
.spacer { flex:1; }
.selpage { display:inline-flex; align-items:center; gap:10px; cursor:pointer; color:var(--muted); }
.sort { display:inline-flex; align-items:center; gap:8px; color:var(--muted); }
.sort select { color:var(--text); }
input[type=checkbox] { width:18px; height:18px; accent-color:var(--accent); margin:0; cursor:pointer; }

.crow { display:flex; align-items:center; gap:12px; padding:9px 12px; border-bottom:1px solid var(--line); cursor:pointer; }
.crow:last-child { border-bottom:0; }
.crow:hover { background:color-mix(in srgb, var(--accent) 5%, transparent); }
.crow.sel { background:color-mix(in srgb, var(--accent) 10%, transparent); }
.crow.none { cursor:default; } .crow.none:hover { background:none; }
.cb { flex:none; }
.logo { width:48px; height:32px; flex:none; border-radius:6px; background:var(--bg); border:1px solid var(--line); display:flex; align-items:center; justify-content:center; overflow:hidden; }
.logo img { max-width:100%; max-height:100%; object-fit:contain; padding:2px; }
.ini { font-size:12px; font-weight:600; color:var(--muted); letter-spacing:.03em; }
.info { flex:1; min-width:0; }
.name { display:flex; align-items:center; gap:2px 4px; flex-wrap:wrap; }
.namebtn { height:auto; padding:0; border:0; background:none; font-weight:500; text-align:left; border-radius:4px; white-space:normal; }
.namebtn:hover:not(:disabled) { text-decoration:underline; border:0; }
.name .badge { margin:0; }
.meta { display:flex; gap:4px; flex-wrap:wrap; margin-top:3px; }
.tag { font-size:11px; padding:1px 7px; border-radius:999px; background:var(--bg); border:1px solid var(--line); color:var(--muted); white-space:nowrap; text-transform:capitalize; }
.st { display:inline-flex; align-items:center; gap:6px; font-size:12px; white-space:nowrap; flex:none; }
.st.ok { color:var(--ok); } .st.bad { color:var(--bad); } .st.warn { color:var(--warn); }
.st .dot { margin:0; }
.add { flex:none; width:var(--ctl); padding:0; display:inline-flex; align-items:center; justify-content:center; color:var(--text); }
.in { flex:none; min-width:92px; max-width:180px; border-color:color-mix(in srgb, var(--ok) 55%, var(--line)); }
.in .chk { color:var(--ok); font-weight:700; }
.in .lbl { overflow:hidden; text-overflow:ellipsis; }

.foot { position:sticky; bottom:0; z-index:4; margin-top:8px; padding:8px 0 10px; background:var(--bg); }
.foot :deep(.pager) { margin:0; }
.bulk { display:flex; align-items:center; gap:10px; flex-wrap:wrap; padding:8px 12px; margin-bottom:8px; background:var(--panel); border:1px solid var(--accent); border-radius:10px; box-shadow:0 6px 24px rgba(0,0,0,.14); }
@media (max-width:760px) {
  .grid { grid-template-columns:1fr 1fr; } .grid input { grid-column:1 / -1; }
  .st { display:none; }
  .in { min-width:0; }
}
</style>
