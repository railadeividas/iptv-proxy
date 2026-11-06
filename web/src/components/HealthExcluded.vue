<script setup>
import { ref, computed, onMounted } from 'vue'
import { api } from '../api'
import { toast } from '../store'
import { debounced, throttle, useLive, reasonHint, PAGE_SIZES as SIZES, PAGE_SIZE } from '../util'
import Pager from './Pager.vue'
import ChannelModal from './ChannelModal.vue'

// Streams left out of health checks (by hand, or automatically after failing for a number of days): search, details, include again.
const props = defineProps({ h: Object })
const emit = defineEmits(['changed'])
const saved = (() => { try { return +localStorage.getItem('healthExcludedSize') } catch { return 0 } })()
const size = ref(SIZES.includes(saved) ? saved : PAGE_SIZE)
const page = ref(0)
const q = ref('')
const data = ref({ total: 0, items: [] })
const opened = ref(null)

const pages = computed(() => Math.max(1, Math.ceil(data.value.total / size.value)))
const days = computed(() => props.h?.auto_exclude_days ?? 0)

async function load() {
  try {
    const p = new URLSearchParams({ q: q.value, limit: size.value, offset: page.value * size.value })
    data.value = await api.get('/health/excluded?' + p)
    if (page.value > 0 && !data.value.items.length && data.value.total) { page.value = Math.max(0, pages.value - 1); load() }
  } catch (e) { toast(e.message) }
}
onMounted(load)
const soon = throttle(load, 1500)
useLive({ streams: soon, health: soon, catalog: soon })
const refilter = () => { page.value = 0; load() }
const typed = debounced(refilter, 300)
const go = (n) => { page.value = n; load() }
function setSize(n) {
  try { localStorage.setItem('healthExcludedSize', String(n)) } catch {}
  page.value = Math.floor((page.value * size.value) / n); size.value = n; load()
}
async function include(x) {
  try { await api.post('/health/exclude', { url: x.url, excluded: false }); toast(`${x.name}: stream is checked again`); emit('changed'); load() } catch (e) { toast(e.message) }
}
</script>

<template>
  <div>
    <p class="muted small">{{ days ? `A stream whose every check has failed for ${days === 1 ? '1 day' : days.toLocaleString() + ' days'} is excluded automatically.` : 'Streams are never excluded automatically.' }}
      Exclude a stream by hand from its channel details. Excluded streams keep their last result.</p>
    <p v-if="!data.total && !q" class="muted small">No stream is excluded from health checks.</p>
    <template v-else>
      <div class="filters">
        <input v-model="q" type="search" placeholder="Search channels or addresses" aria-label="Search excluded streams" @input="typed">
      </div>
      <p v-if="!data.total" class="muted small">No excluded streams match.</p>
      <ul v-else class="excl">
        <li v-for="x in data.items" :key="x.url">
          <div class="main">
            <div class="line"><b>{{ x.name }}</b> <span v-if="x.reason" class="badge bad" :title="reasonHint(x.reason) || 'Most recent failure reason'">{{ x.reason }}</span></div>
          </div>
          <div class="acts">
            <button @click="opened = x.ck">Details</button>
            <button title="Check this stream again in health checks" @click="include(x)">Include again</button>
          </div>
        </li>
      </ul>
      <Pager v-if="data.total" :page="page" :pages="pages" :total="data.total" :size="size" :count="data.items.length" :sizes="SIZES" @go="go" @size="setSize" />
    </template>
    <ChannelModal :ck="opened" @close="opened = null" @changed="emit('changed'); load()" />
  </div>
</template>

<style scoped>
.filters { display:flex; gap:8px; flex-wrap:wrap; margin:0 0 10px; }
.filters input { flex:1; min-width:200px; margin:0; }
.small { font-size:13px; margin:6px 0; }
.excl { list-style:none; margin:0; padding:0; border:1px solid var(--line); border-radius:8px; overflow:hidden; }
.excl li { display:flex; justify-content:space-between; gap:12px; align-items:center; padding:8px 12px; border-bottom:1px solid var(--line); }
.excl li:last-child { border-bottom:0; }
.main { min-width:0; flex:1; }
.line { display:flex; align-items:center; gap:6px; flex-wrap:nowrap; }
.line b { overflow:hidden; text-overflow:ellipsis; white-space:nowrap; min-width:0; }
.badge { margin:0; flex:none; max-width:60%; overflow:hidden; text-overflow:ellipsis; white-space:nowrap; }
@media (max-width:760px) { .line { flex-wrap:wrap; } .line b { white-space:normal; overflow-wrap:anywhere; } .badge { max-width:100%; } }
.acts { display:flex; gap:8px; flex-wrap:wrap; }
</style>
