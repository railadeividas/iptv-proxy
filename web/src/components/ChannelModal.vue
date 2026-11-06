<script setup>
import { ref, computed, watch, nextTick } from 'vue'
import { api } from '../api'
import { toast, can } from '../store'
import { rel, throttle, useLive, statusCode, streamFormat } from '../util'

// Everything about one channel: catalogue facts, streams (last result) and its stored check history.
const props = defineProps({ ck: String })
const emit = defineEmits(['close', 'changed'])
const dlg = ref(null)
const d = ref(null), err = ref('')

async function load() {
  if (!props.ck) return
  try { d.value = await api.get('/channel?ck=' + encodeURIComponent(props.ck)); err.value = '' }
  catch (e) { err.value = e.message }
}
watch(() => props.ck, async (ck) => {
  if (ck && dlg.value && !dlg.value.open) {
    d.value = null
    dlg.value.showModal()
    await load()
    await nextTick()
  }
})
const soon = throttle(load, 1200)
useLive({ streams: soon, health: soon })
const onBackdrop = (e) => { if (e.target === dlg.value) dlg.value.close() }

const label = (s) => (s.status === 1 ? 'working' : s.status === 2 ? 'not working' : 'not checked yet')
const tone = (s) => (s.status === 1 ? 'good' : s.status === 2 ? 'bad' : '')
async function recheck() {
  try { const r = await api.post('/health/run', { kind: 'channel', id: props.ck }); toast(`Checking ${r.streams} stream${r.streams === 1 ? '' : 's'}…`); emit('changed') } catch (e) { toast(e.message) }
}
// leave a stream out of health checks, or check it again
async function setExcluded(s, excluded) {
  try {
    await api.post('/health/exclude', { url: s.url, excluded })
    toast(excluded ? `${s.host}: excluded from health checks` : `${s.host}: checked again`)
    emit('changed'); load()
  } catch (e) { toast(e.message) }
}
const code = (c) => statusCode(c.reason)
const facts = computed(() => {
  const i = d.value.info
  const bits = [
    ['Source', i.source],
    ['Country', i.country && `${i.flag || ''} ${i.country}`.trim()],
    ['Categories', i.categories.join(', ')],
    ['Languages', i.languages.join(', ')],
    ['Network', i.network],
    ['Channel ID', i.channel],
    ['Feed', i.feed],
    ['Status', i.closed ? `Closed ${i.closed}` : ''],
    ['Content', i.nsfw ? 'NSFW' : ''],
  ]
  return bits.filter((b) => b[1])
})
const when = (t) => new Date(t * 1000).toLocaleString()
</script>

<template>
  <dialog ref="dlg" aria-labelledby="chh-title" @close="emit('close')" @click="onBackdrop">
    <div class="box">
      <p v-if="err" class="err">{{ err }}</p>
      <template v-else-if="d">
        <div class="head">
          <div>
            <h2 id="chh-title">{{ d.name }}</h2>
            <p class="muted sub">{{ d.ck }}<template v-if="d.profiles.length"> · in </template>
              <RouterLink v-for="p in d.profiles" :key="p.id" class="chip-link" :to="{ name: 'profiles', params: { id: p.id } }" @click="dlg.close()">{{ p.name }}</RouterLink></p>
          </div>
          <button v-if="can('health')" @click="recheck">Re-check this channel</button>
        </div>

        <h3>About</h3>
        <div class="about">
          <img v-if="d.info.logo" class="logo" :src="d.info.logo" alt="" @error="$event.target.style.display = 'none'">
          <dl class="facts">
            <template v-for="f in facts" :key="f[0]"><dt>{{ f[0] }}</dt><dd>{{ f[1] }}</dd></template>
            <template v-if="d.info.website"><dt>Website</dt><dd><a :href="d.info.website" target="_blank" rel="noopener noreferrer">{{ d.info.website }}</a></dd></template>
            <template v-if="d.info.guides.length"><dt>TV guide</dt>
              <dd>{{ d.info.guides.map((g) => g.site + (g.lang ? ' (' + g.lang + ')' : '')).join(', ') }}</dd></template>
          </dl>
        </div>

        <h3>Streams</h3>
        <div class="scroll">
          <table class="t">
            <thead><tr><th>Stream</th><th>Status</th><th class="lastc">Last checked</th></tr></thead>
            <tbody>
              <tr v-for="s in d.streams" :key="s.url">
                <td class="host"><span :title="s.url">{{ s.host }}</span>
                  <span class="muted small">{{ [s.title, s.quality, s.source].filter(Boolean).join(' · ') }}</span>
                  <span class="muted small url">{{ s.url }}</span>
                  <span v-if="s.user_agent" class="muted small">User-Agent: {{ s.user_agent }}</span>
                  <span v-if="s.referrer" class="muted small">Referrer: {{ s.referrer }}</span></td>
                <td><span class="badge" :class="tone(s)">{{ label(s) }}</span>
                  <span v-if="s.excluded" class="badge warn excl" :title="`Excluded ${s.excluded_auto ? 'automatically after failing for days' : 'by hand'} on ${when(s.excluded_at)}`">excluded</span>
                  <template v-if="can('health')">
                    <button v-if="s.excluded" class="small-btn" title="Check this stream again in health checks" @click="setExcluded(s, false)">Include in checks</button>
                    <button v-else class="small-btn" title="Leave this stream out of health checks. It keeps its last result" @click="setExcluded(s, true)">Exclude from checks</button>
                  </template>
                  <span class="muted small lastn">checked {{ s.checked_at ? rel(s.checked_at) : 'never' }}</span></td>
                <td class="nowrap lastc">{{ s.checked_at ? rel(s.checked_at) : 'never' }}</td>
              </tr>
            </tbody>
          </table>
        </div>

        <h3>History <span class="muted small">{{ d.history_kept ? `latest ${d.history_kept} results kept per channel` : 'switched off in Settings' }}</span></h3>
        <p v-if="!d.history.length" class="muted small">{{ d.history_kept ? 'No checks recorded yet.' : 'The history is off. Set “Results to keep per channel” on Settings > Health to record it.' }}</p>
        <div v-else class="scroll hist">
          <table class="t">
            <thead><tr><th>When</th><th>Stream</th><th>Result</th><th>Status code</th><th class="right">Time</th></tr></thead>
            <tbody>
              <tr v-for="(c, i) in d.history" :key="i">
                <td class="nowrap">{{ when(c.at) }}</td>
                <td>{{ c.host }}</td>
                <td><span class="badge" :class="c.ok ? 'good' : 'bad'">{{ c.ok ? 'working' : 'failed' }}</span></td>
                <td>{{ code(c) }}<span v-if="streamFormat(c.reason)" class="muted"> · {{ streamFormat(c.reason) }}</span><span v-if="!code(c) && !c.ok" class="muted">{{ c.reason }}</span></td>
                <td class="right nowrap">{{ c.ms }} ms</td>
              </tr>
            </tbody>
          </table>
        </div>
      </template>
      <p v-else class="muted">Loading…</p>
      <div class="actions"><button @click="dlg.close()">Close</button></div>
    </div>
  </dialog>
</template>

<style scoped>
dialog { border:1px solid var(--line); border-radius:12px; padding:0; background:var(--panel); color:var(--text); width:min(920px, calc(100vw - 24px)); max-width:none; max-height:calc(100vh - 32px); }
dialog::backdrop { background:rgba(0,0,0,.5); }
.box { padding:18px 20px; }
.head { display:flex; justify-content:space-between; align-items:flex-start; gap:12px; flex-wrap:wrap; }
h2 { margin:0; font-size:18px; }
h3 { margin:18px 0 8px; font-size:14px; }
.sub { margin:2px 0 0; font-size:12px; }
.chip-link { margin-left:4px; padding:1px 8px; border:1px solid var(--line); border-radius:999px; font-size:12px; color:var(--muted); background:var(--bg); }
.chip-link:hover { border-color:var(--accent); color:var(--text); }
.about { display:flex; gap:16px; align-items:flex-start; }
.logo { width:88px; height:60px; object-fit:contain; background:#8881; border-radius:6px; flex:none; }
.facts { display:grid; grid-template-columns:max-content 1fr; gap:4px 16px; margin:0; font-size:13px; min-width:0; }
.facts dt { color:var(--muted); }
.facts dd { margin:0; overflow-wrap:anywhere; }
.url { font-family:ui-monospace, monospace; }
.lastn { display:none; margin-top:6px; }
/* phones: the time of the last check moves under the status, so the table needs no sideways scrolling */
@media (max-width:560px) { .about { flex-direction:column; } .host { min-width:110px; } .lastc { display:none; } .lastn { display:block; } }
.scroll { overflow-x:auto; }
.hist { max-height:280px; overflow-y:auto; }
.t { width:100%; border-collapse:collapse; font-size:13px; }
.t th { font-size:12px; font-weight:500; color:var(--muted); position:sticky; top:0; background:var(--panel); }
.t td, .t th { padding:8px 10px 8px 0; vertical-align:top; border-bottom:1px solid var(--line); text-align:left; }
.t td.right, .t th.right { text-align:right; padding-right:0; }
.host { display:flex; flex-direction:column; gap:2px; min-width:160px; overflow-wrap:anywhere; }
.small { font-size:12px; }
.nowrap { white-space:nowrap; }
.badge { margin:0 6px 0 0; }
.badge.excl { display:inline-block; margin-top:4px; }
.small-btn { display:flex; height:30px; padding:0 10px; margin-top:6px; font-size:12px; }
.actions { display:flex; justify-content:flex-end; margin-top:16px; }
</style>
