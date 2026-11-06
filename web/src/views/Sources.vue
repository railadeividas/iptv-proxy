<script setup>
import { ref, computed, onMounted } from 'vue'
import { api } from '../api'
import { toast, ask } from '../store'
import { useLive, debounced, copy, ago, PAGE_SIZES as SIZES, PAGE_SIZE } from '../util'
import Pager from '../components/Pager.vue'
import EditSourceModal from '../components/EditSourceModal.vue'

const saved = (() => { try { return +localStorage.getItem('skippedPageSize') } catch { return 0 } })()
const size = ref(SIZES.includes(saved) ? saved : PAGE_SIZE)
const sources = ref([]), skipped = ref({ total: 0, items: [], reasons: [], sources: [] }), page = ref(0)
const editing = ref(null)
const f = ref({ q: '', source: '', reason: '' })
const filtered = computed(() => !!(f.value.q || f.value.source || f.value.reason))
const form = ref({ name: '', url: '', m3u: '' }), err = ref(''), busy = ref(false)
const pages = computed(() => Math.max(1, Math.ceil(skipped.value.total / size.value)))

async function load() {
  const [s, k] = await Promise.all([api.get('/custom'), api.get('/skipped?' + new URLSearchParams({ ...f.value, limit: size.value, offset: page.value * size.value }))])
  sources.value = s; skipped.value = k
}
onMounted(load)
useLive({ catalog: debounced(() => load().catch(() => {}), 500) })

async function readFile(e) { const f = e.target.files[0]; if (f) form.value.m3u = await f.text() }
async function add() {
  err.value = ''; busy.value = true
  try {
    await api.post('/custom', form.value)
    form.value = { name: '', url: '', m3u: '' }
    toast('Source added'); await load()
  } catch (e) { err.value = e.message } finally { busy.value = false }
}
async function refresh(s) { try { await api.post(`/custom/${s.id}/refresh`); toast('Refreshed') } catch (e) { toast(e.message) } load() }
async function remove(s) {
  const ok = await ask({ title: `Delete “${s.name}”?`, message: `Its ${s.streams} stream${s.streams === 1 ? '' : 's'} will be removed from the catalogue and from any profile that uses them.`, confirmText: 'Delete source', danger: true })
  if (ok) { await api.del('/custom/' + s.id); load() }
}
const go = (n) => { page.value = n; load() }
function setSize(n) { // keep the first visible entry on screen
  try { localStorage.setItem('skippedPageSize', String(n)) } catch {}
  page.value = Math.floor((page.value * size.value) / n); size.value = n; load()
}
async function showSkipped(s) {
  f.value = { q: '', reason: '', source: 'custom:' + s.id }
  page.value = 0
  await load()
  document.getElementById('skipped')?.scrollIntoView({ behavior: 'smooth', block: 'start' })
}
const refilter = () => { page.value = 0; load() }
const typed = debounced(refilter, 300)
const clear = () => { f.value = { q: '', source: '', reason: '' }; refilter() }
const srcLabel = (v) => (v === 'api' ? 'iptv-org API' : v.startsWith('custom:') ? (sources.value.find((s) => 'custom:' + s.id === v)?.name || v) : v)
async function copyUrl(u) { try { await copy(u); toast('Copied') } catch { toast('Copy failed') } }
</script>

<template>
  <section>
    <div class="card">
      <h2>Add custom source</h2>
      <input v-model="form.name" placeholder="Name (optional)">
      <input v-model="form.url" placeholder="M3U URL (leave empty to use the text below)">
      <textarea v-model="form.m3u" rows="6" placeholder='#EXTM3U&#10;#EXTINF:-1 tvg-id="BTV.lt" group-title="News",My channel&#10;https://example.com/live.m3u8' />
      <div class="row between"><input type="file" accept=".m3u,.m3u8,text/plain" @change="readFile"><button class="primary" :disabled="busy" @click="add">Add source</button></div>
      <p v-if="err" class="err">{{ err }}</p>
    </div>

    <div v-if="sources.length" class="card">
      <h2>Custom sources</h2>
      <div v-for="s in sources" :key="s.id" class="src">
        <div class="src-main">
          <div class="src-name"><b>{{ s.name }}</b>
            <span class="badge" :class="s.last_error ? 'bad' : s.streams ? 'good' : 'warn'">{{ s.last_error ? 'last refresh failed' : s.streams ? 'imported' : 'empty' }}</span></div>
          <div class="muted url" :title="s.url">{{ s.url || 'uploaded / pasted' }}</div>
          <div class="src-stats">
            <span><b>{{ s.channels }}</b> channels</span>
            <span class="ok"><b>{{ s.working }}</b> working</span>
            <span class="bad"><b>{{ s.dead }}</b> not working</span>
            <span class="muted"><b>{{ s.unchecked }}</b> not checked yet</span>
            <span v-if="s.excluded" class="muted" title="Excluded from health checks"><b>{{ s.excluded }}</b> excluded</span>
            <span :class="s.skipped ? 'warn' : 'muted'"><b>{{ s.skipped }}</b> skipped</span>
          </div>
          <div class="muted small">{{ s.url ? 'Last successful refresh: ' + ago(s.last_ok) : 'Imported ' + ago(s.last_ok) }}
            <template v-if="s.url"> · {{ s.refresh_hours ? 'auto-refresh every ' + (s.refresh_hours % 24 ? s.refresh_hours + ' h' : s.refresh_hours / 24 + ' d') + ', next ' + ago(s.next_refresh) : 'auto-refresh off' }}</template></div>
          <div v-if="s.last_error" class="err">{{ s.last_error }} (kept the last good list)</div>
        </div>
        <div class="acts">
          <RouterLink class="btn" :to="{ path: '/channels', query: { source: String(s.id) } }">View channels</RouterLink>
          <button v-if="s.skipped" @click="showSkipped(s)">View skipped</button>
          <button v-if="s.url" @click="refresh(s)">Refresh now</button>
          <button @click="editing = s">Edit</button>
          <button class="danger" @click="remove(s)">Delete</button>
        </div>
      </div>
    </div>

    <EditSourceModal :source="editing" @saved="load(); toast('Source saved')" @close="editing = null" />

    <div class="card">
      <h2 id="skipped">Skipped entries</h2>
      <p class="muted">Entries that could not be imported, with the reason. They never block the rest of the import.</p>
      <p v-if="!skipped.total && !filtered" class="muted">Nothing skipped.</p>
      <template v-else>
        <div class="skfilters">
          <input v-model="f.q" type="search" placeholder="Search name, URL or reason" aria-label="Search skipped entries" @input="typed">
          <select v-model="f.reason" aria-label="Reason" @change="refilter">
            <option value="">All reasons</option>
            <option v-for="r in skipped.reasons" :key="r.value" :value="r.value">{{ r.value }} ({{ r.count.toLocaleString() }})</option>
          </select>
          <select v-model="f.source" aria-label="Source" @change="refilter">
            <option value="">All sources</option>
            <option v-for="r in skipped.sources" :key="r.value" :value="r.value">{{ srcLabel(r.value) }} ({{ r.count.toLocaleString() }})</option>
          </select>
          <button v-if="filtered" class="ghost" @click="clear">Clear</button>
        </div>
        <p class="muted">{{ skipped.total.toLocaleString() }} {{ filtered ? 'matching' : 'skipped' }}</p>
        <p v-if="!skipped.total" class="muted">No skipped entries match these filters.</p>
        <table v-else class="sk">
          <tr v-for="(i, n) in skipped.items" :key="n">
            <td class="nm">{{ i.ref }}<div v-if="i.url" class="muted url" :title="i.url">{{ i.url }}</div></td>
            <td class="muted">{{ srcLabel(i.source) }}</td>
            <td>{{ i.reason }}</td>
            <td class="cp"><button v-if="i.url" class="mini-copy" @click="copyUrl(i.url)">Copy URL</button></td>
          </tr>
        </table>
        <Pager :page="page" :pages="pages" :total="skipped.total" :size="size" :count="skipped.items.length" :sizes="SIZES" @go="go" @size="setSize" />
      </template>
    </div>
  </section>
</template>

<style scoped>
.src { display:flex; gap:12px; justify-content:space-between; align-items:flex-start; padding:12px 0; border-top:1px solid var(--line); }
.src:first-of-type { border-top:0; }
.src-main { min-width:0; flex:1; }
.src-name { display:flex; align-items:center; gap:6px; flex-wrap:wrap; }
.src-name .badge { margin:0; }
.src .url { font-size:12px; overflow:hidden; text-overflow:ellipsis; white-space:nowrap; }
.src-stats { display:flex; gap:6px 16px; flex-wrap:wrap; margin:8px 0 4px; font-size:13px; }
.src-stats .ok { color:var(--ok); } .src-stats .bad { color:var(--bad); } .src-stats .warn { color:var(--warn); }
.small { font-size:12px; }
.btn { display:inline-flex; align-items:center; height:var(--ctl); padding:0 12px; border:1px solid var(--line); border-radius:6px; background:var(--panel); white-space:nowrap; }
.btn:hover { border-color:var(--accent); }
.src .acts { flex-wrap:wrap; justify-content:flex-end; }
.row.between { justify-content:space-between; margin:0; }
.acts { display:flex; gap:8px; justify-content:flex-end; }
.skfilters { display:flex; gap:8px; flex-wrap:wrap; margin:8px 0; }
.skfilters input { flex:1; min-width:220px; margin:0; }
.skfilters select { max-width:260px; }
.sk .nm { max-width:420px; overflow-wrap:anywhere; }
.sk .url { font-size:12px; font-family:ui-monospace, monospace; white-space:nowrap; overflow:hidden; text-overflow:ellipsis; max-width:420px; }
.sk .cp { text-align:right; white-space:nowrap; }
.mini-copy { padding:0 10px; font-size:12px; }
/* phones: one entry per block instead of four columns, so the page never scrolls sideways */
@media (max-width:560px) {
  .src { flex-direction:column; } /* the buttons go below the source instead of squeezing it */
  .src-main { width:100%; }
  .src .acts { justify-content:flex-start; }
  .sk, .sk tbody, .sk tr, .sk td { display:block; }
  .sk tr { padding:8px 0; box-shadow:inset 0 -1px 0 var(--line); }
  .sk td { padding:2px 0; border:0; }
  .sk .nm, .sk .url { max-width:100%; }
  .sk .cp { text-align:left; padding-top:6px; }
}
</style>
