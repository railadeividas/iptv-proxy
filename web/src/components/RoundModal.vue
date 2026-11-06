<script setup>
import { ref, computed, watch, nextTick } from 'vue'
import { api } from '../api'
import { dur, debounced, statusCode, streamFormat, PAGE_SIZES as SIZES, PAGE_SIZE } from '../util'
import Pager from './Pager.vue'
import ChannelModal from './ChannelModal.vue'

// What one check round found: every stored result, failures first.
const props = defineProps({ round: Object })
const emit = defineEmits(['close'])
const dlg = ref(null)
const d = ref(null), err = ref('')
const result = ref('bad'), q = ref(''), page = ref(0), size = ref(PAGE_SIZE)
const opened = ref(null) // channel key shown in the details dialog

const pages = computed(() => Math.max(1, Math.ceil((d.value?.total || 0) / size.value)))
async function load() {
  if (!props.round) return
  const p = new URLSearchParams({ id: props.round.id, result: result.value, q: q.value.trim(), limit: size.value, offset: page.value * size.value })
  try { d.value = await api.get('/health/round?' + p); err.value = '' } catch (e) { err.value = e.message }
}
watch(() => props.round, async (r) => {
  if (!r || !dlg.value || dlg.value.open) return
  d.value = null; q.value = ''; page.value = 0
  result.value = r.bad ? 'bad' : '' // open on the failures, when there were any
  dlg.value.showModal()
  await load()
  await nextTick()
})
const search = debounced(() => { page.value = 0; load() })
watch(q, search)
watch(result, () => { page.value = 0; load() })
const go = (n) => { page.value = n; load() }
const setSize = (n) => { page.value = Math.floor((page.value * size.value) / n); size.value = n; load() }
const onBackdrop = (e) => { if (e.target === dlg.value) dlg.value.close() }
const when = (t) => new Date(t * 1000).toLocaleString()
</script>

<template>
  <dialog ref="dlg" aria-labelledby="rd-title" @close="emit('close')" @click="onBackdrop">
    <div class="box">
      <p v-if="err" class="err">{{ err }}</p>
      <template v-else-if="d">
        <div class="head">
          <h2 id="rd-title">{{ d.round.label }}</h2>
          <p class="muted sub">
            {{ when(d.round.started) }} · {{ d.round.kind === 'manual' ? 'by hand' : 'automatic' }}<template v-if="d.round.finished"> · took {{ dur(d.round.finished - d.round.started) }}</template>
            <span v-if="d.round.stopped" class="badge warn">stopped early</span><span v-if="!d.round.finished" class="badge">running</span>
          </p>
          <p class="sum"><b>{{ d.round.probed.toLocaleString() }}</b> streams checked ·
            <span class="okc">{{ d.round.ok.toLocaleString() }} working</span> ·
            <span class="badc">{{ d.round.bad.toLocaleString() }} not working</span></p>
          <p v-if="d.stored < d.round.probed" class="muted small">Only {{ d.stored.toLocaleString() }} of the results are still stored. Older ones are removed by the history limits in Settings.</p>
        </div>

        <div class="filters">
          <input v-model="q" type="search" placeholder="Search channels…" aria-label="Search channels">
          <select v-model="result" aria-label="Result">
            <option value="">All results ({{ d.stored.toLocaleString() }})</option>
            <option value="bad">Not working ({{ d.bad.toLocaleString() }})</option>
            <option value="ok">Working ({{ d.ok.toLocaleString() }})</option>
          </select>
        </div>

        <p v-if="!d.items.length" class="muted small">{{ d.stored ? 'No result matches.' : 'No results are stored for this round.' }}</p>
        <div v-else class="scroll">
          <table class="t">
            <thead><tr><th>Channel</th><th>Stream</th><th>Result</th><th>Status code</th><th class="r">Time</th><th /></tr></thead>
            <tbody>
              <tr v-for="(x, i) in d.items" :key="i">
                <td class="chan"><b>{{ x.name }}</b><span class="muted small">{{ x.ck }}</span></td>
                <td class="host">{{ x.host }}</td>
                <td><span class="badge" :class="x.ok ? 'good' : 'bad'">{{ x.ok ? 'working' : 'failed' }}</span></td>
                <td>{{ statusCode(x.reason) }}<span v-if="streamFormat(x.reason)" class="muted"> · {{ streamFormat(x.reason) }}</span><span v-if="!statusCode(x.reason) && !x.ok" class="muted">{{ x.reason }}</span></td>
                <td class="r nowrap">{{ x.ms }} ms</td>
                <td class="r"><button class="small-btn" @click="opened = x.ck">Details</button></td>
              </tr>
            </tbody>
          </table>
        </div>
        <Pager v-if="d.total > SIZES[0]" :page="page" :pages="pages" :total="d.total" :size="size" :count="d.items.length" :sizes="SIZES" @go="go" @size="setSize" />
      </template>
      <p v-else class="muted">Loading…</p>
      <div class="actions"><button @click="dlg.close()">Close</button></div>
    </div>
  </dialog>
  <ChannelModal :ck="opened" @close="opened = null" />
</template>

<style scoped>
dialog { border:1px solid var(--line); border-radius:12px; padding:0; background:var(--panel); color:var(--text); width:min(920px, calc(100vw - 24px)); max-width:none; max-height:calc(100vh - 32px); }
dialog::backdrop { background:rgba(0,0,0,.5); }
.box { padding:18px 20px; }
h2 { margin:0; font-size:18px; }
.sub { margin:2px 0 0; font-size:12px; }
.sum { margin:8px 0 0; font-size:14px; }
.small { font-size:12px; margin:6px 0 0; }
.filters { display:flex; gap:10px; flex-wrap:wrap; margin:14px 0 10px; }
.filters input { flex:1; min-width:180px; }
.scroll { overflow-x:auto; }
.t { width:100%; border-collapse:collapse; font-size:13px; }
.t th { font-size:12px; font-weight:500; color:var(--muted); text-align:left; }
.t td, .t th { padding:8px 10px 8px 0; vertical-align:top; border-bottom:1px solid var(--line); text-align:left; }
.t td.r, .t th.r { text-align:right; padding-right:0; }
.chan { min-width:160px; overflow-wrap:anywhere; }
.chan b, .chan span { display:block; }
.host { overflow-wrap:anywhere; }
.nowrap { white-space:nowrap; }
.badge { margin:0; }
.small-btn { height:30px; padding:0 10px; font-size:12px; }
.actions { display:flex; justify-content:flex-end; margin-top:16px; }
</style>
