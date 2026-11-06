<script setup>
import { ref, computed, onMounted } from 'vue'
import { api } from '../api'
import { toast, ask } from '../store'
import { debounced, throttle, useLive, reasonHint, PAGE_SIZES as SIZES, PAGE_SIZE } from '../util'
import Pager from './Pager.vue'
import ChannelModal from './ChannelModal.vue'

// Channels in profiles whose streams all fail: search, filter by why, re-check, open details.
const emit = defineEmits(['changed'])
const saved = (() => { try { return +localStorage.getItem('healthDownSize') } catch { return 0 } })()
const size = ref(SIZES.includes(saved) ? saved : PAGE_SIZE)
const page = ref(0)
const f = ref({ q: '', reason: '' })
const data = ref({ total: 0, items: [], reasons: [] })
const opened = ref(null)

const pages = computed(() => Math.max(1, Math.ceil(data.value.total / size.value)))
const filtered = computed(() => !!(f.value.q || f.value.reason))
const all = computed(() => data.value.reasons.reduce((a, r) => a + r.count, 0))

async function load() {
  try {
    const p = new URLSearchParams({ ...f.value, limit: size.value, offset: page.value * size.value })
    data.value = await api.get('/health/down?' + p)
    if (page.value > 0 && !data.value.items.length && data.value.total) { page.value = Math.max(0, pages.value - 1); load() }
  } catch (e) { toast(e.message) }
}
onMounted(load)
const soon = throttle(load, 1500)
useLive({ streams: soon, health: soon, profiles: soon, catalog: soon })
const refilter = () => { page.value = 0; load() }
const typed = debounced(refilter, 300)
const clear = () => { f.value = { q: '', reason: '' }; refilter() }
const go = (n) => { page.value = n; load() }
function setSize(n) {
  try { localStorage.setItem('healthDownSize', String(n)) } catch {}
  page.value = Math.floor((page.value * size.value) / n); size.value = n; load()
}
// every failing stream, at once
async function recheckAll() {
  try { const r = await api.post('/health/run', { kind: 'bad' }); toast(`Checking ${r.streams} stream${r.streams === 1 ? '' : 's'}…`); emit('changed') } catch (e) { toast(e.message) }
}
// take every listed channel (all pages, current filters) out of the profiles that hold it
async function removeAll() {
  try {
    const rows = []
    for (let off = 0; ; off += 500) {
      const p = new URLSearchParams({ ...f.value, limit: 500, offset: off })
      const r = await api.get('/health/down?' + p)
      rows.push(...r.items)
      if (rows.length >= r.total || !r.items.length) break
    }
    const byProfile = new Map()
    for (const d of rows) for (const pr of d.profiles) byProfile.set(pr.id, [...(byProfile.get(pr.id) || []), d.ck])
    if (!byProfile.size) return
    const names = Object.fromEntries(rows.flatMap((d) => d.profiles.map((pr) => [pr.id, pr.name])))
    const list = [...byProfile].map(([id, cks]) => `${names[id]} (${cks.length})`).join(', ')
    const ok = await ask({ title: `Remove ${rows.length} failing channel${rows.length === 1 ? '' : 's'} from your profiles?`, message: `They are taken out of: ${list}. You can add them again from Channels.`, confirmText: 'Remove channels', danger: true })
    if (!ok) return
    for (const [id, cks] of byProfile) await api.post(`/profiles/${id}/remove`, { cks })
    toast(`Removed ${rows.length} channel${rows.length === 1 ? '' : 's'}`)
    emit('changed'); load()
  } catch (e) { toast(e.message) }
}
async function recheck(ck) {
  try { const r = await api.post('/health/run', { kind: 'channel', id: ck }); toast(`Checking ${r.streams} stream${r.streams === 1 ? '' : 's'}…`); emit('changed') } catch (e) { toast(e.message) }
}
</script>

<template>
  <div>
    <p v-if="!data.total && !filtered && !all" class="muted small">No channel in your profiles fails on all its streams.</p>
    <template v-else>
      <div class="filters">
        <input v-model="f.q" type="search" placeholder="Search channels" aria-label="Search channels needing attention" @input="typed">
        <select v-model="f.reason" aria-label="Reason" @change="refilter">
          <option value="">All reasons ({{ all.toLocaleString() }})</option>
          <option v-for="r in data.reasons" :key="r.reason" :value="r.reason">{{ r.reason }} ({{ r.count.toLocaleString() }})</option>
        </select>
        <button v-if="filtered" class="ghost" @click="clear">Clear</button>
        <span class="grow" />
        <button title="Check every stream marked not working again" @click="recheckAll">Re-check all failing</button>
        <button class="danger" title="Take these channels out of the profiles that hold them" @click="removeAll">Remove from profiles…</button>
      </div>
      <p v-if="!data.total" class="muted small">No channels match these filters.</p>
      <ul v-else class="down">
        <li v-for="d in data.items" :key="d.ck">
          <div class="main">
            <div class="line"><b>{{ d.name }}</b> <span class="badge bad" :title="reasonHint(d.reason) || 'Most recent failure reason'">{{ d.reason }}</span></div>
          </div>
          <div class="acts">
            <button @click="opened = d.ck">Details</button>
            <button @click="recheck(d.ck)">Re-check</button>
          </div>
        </li>
      </ul>
      <Pager :page="page" :pages="pages" :total="data.total" :size="size" :count="data.items.length" :sizes="SIZES" @go="go" @size="setSize" />
    </template>
    <ChannelModal :ck="opened" @close="opened = null" @changed="emit('changed'); load()" />
  </div>
</template>

<style scoped>
.filters { display:flex; gap:8px; flex-wrap:wrap; margin:0 0 10px; }
.filters input { flex:1; min-width:200px; margin:0; }
.filters select { max-width:280px; }
.grow { flex:1; }
.small { font-size:13px; margin:6px 0; }
.down { list-style:none; margin:0; padding:0; border:1px solid var(--line); border-radius:8px; overflow:hidden; }
.down li { display:flex; justify-content:space-between; gap:12px; align-items:center; padding:8px 12px; border-bottom:1px solid var(--line); }
.down li:last-child { border-bottom:0; }
.main { min-width:0; flex:1; }
.line { display:flex; align-items:center; gap:6px; flex-wrap:nowrap; }
.line b { overflow:hidden; text-overflow:ellipsis; white-space:nowrap; min-width:0; }
.badge { margin:0; flex:none; max-width:60%; overflow:hidden; text-overflow:ellipsis; white-space:nowrap; }
@media (max-width:760px) { .line { flex-wrap:wrap; } .line b { white-space:normal; overflow-wrap:anywhere; } .badge { max-width:100%; } }
.acts { display:flex; gap:8px; }
</style>
