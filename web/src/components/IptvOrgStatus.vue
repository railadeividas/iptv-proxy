<script setup>
import { computed, onMounted } from 'vue'
import { api } from '../api'
import { store, toast } from '../store'
import { rel } from '../util'
import StackBar from './StackBar.vue'

const s = computed(() => store.status)
onMounted(async () => { if (!store.status) store.status = await api.get('/status') })

async function sync() { try { await api.post('/sync'); toast('Sync started') } catch (e) { toast(e.message) } }
async function grab() { try { await api.post('/epg/run'); toast('Grab started') } catch (e) { toast(e.message) } }
const every = (h) => (!h ? '' : h % 24 === 0 ? (h === 24 ? 'every day' : `every ${h / 24} days`) : `every ${h} h`)
</script>

<template>
  <section v-if="s" class="panel">
    <div class="tiles">
      <div class="tile"><span class="lbl">Channels in the catalogue</span><b>{{ s.channels.toLocaleString() }}</b></div>
      <div class="tile"><span class="lbl">Streams</span><b>{{ s.streams.toLocaleString() }}</b></div>
      <div class="tile warn"><span class="lbl">Skipped on import</span><b>{{ s.skipped.toLocaleString() }}</b>
        <RouterLink class="more" to="/sources">See why on Sources →</RouterLink></div>
    </div>

    <div class="card-row">
      <div class="card">
        <div class="card-head"><div><h2>API sync</h2><p class="muted sub">Channels, feeds, streams and guide sources from iptv-org/api.</p></div>
          <span v-if="s.sync.running" class="state run"><span class="dot pulse" />Syncing</span>
          <span v-else-if="s.sync.error" class="state bad"><span class="dot" />Failed</span>
          <span v-else class="state idle"><span class="dot" />Up to date</span></div>
        <p class="line">Last sync <b>{{ rel(s.sync.last) }}</b><span v-if="s.intervals.sync_hours" class="muted"> · runs {{ every(s.intervals.sync_hours) }}</span></p>
        <p v-if="s.sync.error" class="err">{{ s.sync.error }} (previous data kept)</p>
        <div class="card-foot"><button :disabled="s.sync.running" @click="sync">Sync now</button></div>
      </div>

      <div class="card">
        <div class="card-head"><div><h2>EPG grabber</h2><p class="muted sub">TV guide for the channels in your profiles, from iptv-org/epg.</p></div>
          <span v-if="!s.epg.enabled" class="state off"><span class="dot" />Not installed</span>
          <span v-else-if="s.epg.running" class="state run"><span class="dot pulse" />Grabbing</span>
          <span v-else-if="s.epg.error" class="state bad"><span class="dot" />Failed</span>
          <span v-else class="state idle"><span class="dot" />Idle</span></div>
        <p v-if="!s.epg.enabled" class="muted">Not installed in this image — playlists work, the guide is disabled.</p>
        <template v-else>
          <p class="line">Last guide <b>{{ rel(s.epg.last_ok) }}</b><span v-if="s.intervals.epg_hours" class="muted"> · runs {{ every(s.intervals.epg_hours) }}</span></p>
          <StackBar :parts="[{ value: s.epg.channels, tone: 'ok', label: 'with a guide' }, { value: s.epg.unmapped, tone: 'warn', label: 'without a guide source' }]" />
          <p class="small"><span class="okc">{{ s.epg.channels }} channels mapped</span> · <span :class="s.epg.unmapped ? 'warnc' : 'muted'">{{ s.epg.unmapped }} without a guide source</span></p>
          <p v-if="s.epg.error" class="err">{{ s.epg.error }} (last good guide kept)</p>
          <div class="card-foot"><button :disabled="s.epg.running" @click="grab">Update guide now</button></div>
        </template>
      </div>
    </div>
  </section>
  <p v-else class="muted">Loading…</p>
</template>

<style scoped>
.panel { display:flex; flex-direction:column; gap:16px; }
.panel .card-row { margin:0; }
.intro p { margin:0; max-width:680px; }
.intro a { color:var(--accent); }
.tiles { display:grid; grid-template-columns:repeat(auto-fit, minmax(200px, 1fr)); gap:12px; }
.tile { background:var(--panel); border:1px solid var(--line); border-left:3px solid var(--line); border-radius:10px; padding:12px 14px; }
.tile .lbl { display:block; font-size:12px; color:var(--muted); }
.tile b { display:block; font-size:24px; line-height:1.2; font-variant-numeric:tabular-nums; }
.tile.warn { border-left-color:var(--warn); }
.more { display:inline-block; margin-top:4px; font-size:12px; color:var(--accent); }
.card-head { display:flex; justify-content:space-between; align-items:flex-start; gap:12px; flex-wrap:wrap; margin-bottom:12px; }
.card-head h2 { margin:0; font-size:17px; }
.sub { margin:2px 0 0; font-size:13px; }
.state { display:inline-flex; align-items:center; gap:6px; font-size:12px; padding:3px 10px; border-radius:999px; border:1px solid var(--line); color:var(--muted); background:var(--bg); }
.state .dot { margin:0; width:8px; height:8px; }
.state.idle .dot { background:var(--ok); }
.state.run { color:var(--accent); border-color:var(--accent); } .state.run .dot { background:var(--accent); }
.state.bad { color:var(--bad); border-color:var(--bad); } .state.bad .dot { background:var(--bad); }
.state.off .dot { background:var(--muted); }
.dot.pulse { animation:pulse 1.2s ease-in-out infinite; }
@keyframes pulse { 50% { opacity:.3; } }
@media (prefers-reduced-motion: reduce) { .dot.pulse { animation:none; } }
.okc { color:var(--ok); } .warnc { color:var(--warn); }
.line { margin:0 0 10px; }
.small { font-size:13px; margin:8px 0 12px; }
</style>
