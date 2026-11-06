<script setup>
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { api } from '../api'
import { store, can } from '../store'
import { rel, dur, throttle, useLive } from '../util'
import StackBar from '../components/StackBar.vue'
import HealthControls from '../components/HealthControls.vue'
import HealthAttention from '../components/HealthAttention.vue'
import HealthHistory from '../components/HealthHistory.vue'
import HealthExcluded from '../components/HealthExcluded.vue'

const s = computed(() => store.status)
const h = ref(null)                // detailed health numbers
const now = ref(Date.now() / 1000)

async function loadHealth() { try { h.value = await api.get('/health') } catch {} }
// refresh at most every 1.5 s while probes report in
const soon = throttle(loadHealth, 1500)
let tick
onMounted(async () => {
  tick = setInterval(() => { now.value = Date.now() / 1000 }, 15000)
  loadHealth()
  if (!store.status) store.status = await api.get('/status')
})
onUnmounted(() => clearInterval(tick))
useLive({ streams: soon, catalog: soon, profiles: soon, health: soon, settings: soon })

const live = computed(() => s.value?.health)                       // pushed every 2 s
const running = computed(() => live.value?.running ?? h.value?.running ?? false)
const done = computed(() => (running.value ? live.value?.done ?? h.value?.done : 0) || 0)
const total = computed(() => (running.value ? live.value?.total ?? h.value?.total : 0) || 0)
const pct = computed(() => (total.value ? Math.round((done.value / total.value) * 100) : 0))

const parts = (c) => [
  { value: c.ok, tone: 'ok', label: 'working' },
  { value: c.bad, tone: 'bad', label: 'not working' },
  { value: c.unchecked, tone: 'idle', label: 'not checked yet' },
  { value: c.excluded, tone: 'excl', label: 'excluded from checks' },
]
const scopeText = computed(() => ({ all: 'Every stream', selected: 'Streams of channels in profiles', off: 'Off' })[h.value?.scope] || h.value?.scope)
const schedule = computed(() => {
  const x = h.value
  if (!x) return ''
  if (x.paused) return 'Automatic checks are paused. Resume them with the button above.'
  if (x.scope === 'off') return 'Automatic checks are off. You can still check by hand.'
  const base = `Channels in profiles: every ${x.selected_interval_hours} h.`
  return x.scope === 'all' ? `${base} Every other stream: every ${x.interval_hours} h.` : base
})
const quietText = computed(() => {
  const x = h.value
  if (!x?.quiet_start || !x?.quiet_end || x.quiet_start === x.quiet_end) return 'None'
  return `${x.quiet_start}–${x.quiet_end}${x.time_zone ? ' (' + x.time_zone + ')' : ''}${x.quiet ? ', on now' : ''}`
})
const downText = computed(() => {
  const n = h.value?.failures_before_down
  return n === 1 ? 'At the first failed check' : `After ${n} failed checks in a row`
})

// the people-first numbers: streams of channels in profiles. Without profiles, the whole catalogue.
const prof = computed(() => h.value?.profiles)
const hasProfiles = computed(() => !!prof.value?.channels)
// Channels, not streams: every channel is in exactly one of the five, so they add up to all channels.
const tiles = computed(() => {
  const c = h.value?.channel_states
  if (!c) return []
  return [
    ['Working', c.working, 'ok', 'Channels with a working stream and none failing'],
    ['Partly working', c.partly, 'warn', 'Channels with working and failing streams'],
    ['Not working', c.not_working, 'bad', 'Channels where every stream fails'],
    ['Pending', c.pending, 'idle', 'No result yet. The automatic checks will look at these channels'],
    ['Not checked', c.not_checked, 'none', 'No result, and the automatic checks do not cover these channels (see “Streams to check” in Settings)'],
  ]
})
// streams, not channels, so it is not part of the four above
const excludedTile = computed(() => h.value?.excluded ?? 0)
const toExcluded = () => document.getElementById('excluded')?.scrollIntoView({ behavior: 'smooth', block: 'start' })
const nextText = computed(() => {
  const x = h.value
  if (!x) return ''
  if (x.scope === 'off') return 'Automatic checks are off'
  if (x.paused) return 'Paused'
  if (x.quiet) return 'After the quiet hours'
  if (!x.next) return 'Nothing planned'
  const d = x.next - now.value
  if (d < 60) return 'Within a minute'
  if (d < 3600) return `In ${Math.round(d / 60)} min`
  return `In ${Math.floor(d / 3600)} h ${Math.round((d % 3600) / 60)} min`
})
const nextTitle = computed(() => (h.value?.next ? new Date(h.value.next * 1000).toLocaleString() : ''))
const lastWhat = computed(() => {
  const l = h.value?.last
  if (!l?.finished) return ''
  return l.kind === 'manual' ? `${l.label}, started by hand` : 'Automatic round: only the streams that were due, such as retries of failed ones'
})
const fresh = computed(() => {
  const f = h.value?.fresh
  return f ? [
    { label: 'Checked in the last hour', value: f.hour },
    { label: 'Checked today', value: f.day },
    { label: 'Checked earlier', value: f.older },
    { label: 'Never checked', value: f.never },
  ] : []
})
const lastDuration = computed(() => (h.value?.last.finished ? h.value.last.finished - h.value.last.started : 0))

</script>

<template>
  <section v-if="s" class="health-page">
    <div v-if="h" class="tiles-wrap">
      <div class="tiles">
        <div v-for="t in tiles" :key="t[0]" class="tile" :class="t[2]" :title="t[3]"><span class="lbl">{{ t[0] }}</span><b>{{ t[1].toLocaleString() }}</b></div>
        <button type="button" class="tile excl" title="Streams left out of health checks, by hand or after failing too often. Show the list" @click="toExcluded">
          <span class="lbl">Excluded streams</span><b>{{ excludedTile.toLocaleString() }}</b></button>
      </div>
    </div>

    <div class="card health">
      <div class="card-head">
        <div>
          <h2>Health checker</h2>
          <p class="muted sub">Probes each stream and marks it working or not working.</p>
        </div>
        <HealthControls :h="h" :running="running" @changed="loadHealth" />
      </div>

      <div v-if="running" class="run-box">
        <div class="run-line"><b v-if="h?.label">{{ h.label }}: </b><b>{{ done.toLocaleString() }} / {{ total.toLocaleString() }}</b> streams probed ({{ pct }}%)
          <span v-if="h" class="muted"> · <span class="okc">{{ h.pass_ok.toLocaleString() }} working</span>, <span class="badc">{{ h.pass_bad.toLocaleString() }} not working</span><template v-if="h.started"> · started {{ rel(h.started) }}</template></span></div>
        <div class="progress"><div :style="{ width: pct + '%' }" /></div>
      </div>

      <template v-if="h">
        <div class="keyfacts">
          <div>
            <span class="k">Last check</span>
            <b v-if="h.last.finished" :title="lastWhat">{{ rel(h.last.finished) }}</b><b v-else class="muted">Not yet</b>
            <span v-if="h.last.finished" class="muted small">{{ h.last.count.toLocaleString() }} streams: <span class="okc">{{ h.last.ok.toLocaleString() }} working</span>, <span class="badc">{{ h.last.bad.toLocaleString() }} not working</span> · took {{ dur(lastDuration) }}</span>
            <span v-if="h.last.finished" class="muted small">{{ lastWhat }}</span>
          </div>
          <div>
            <span class="k">Next check</span>
            <b :title="nextTitle">{{ nextText }}</b>
            <span class="muted small">{{ h.due.toLocaleString() }} stream{{ h.due === 1 ? '' : 's' }} waiting now</span>
          </div>
        </div>

        <div class="block">
          <div class="block-head"><span>Streams in your profiles</span><span class="muted">{{ hasProfiles ? prof.streams.toLocaleString() + ' total' : '' }}</span></div>
          <template v-if="hasProfiles">
            <StackBar :parts="parts(prof)" :height="12" />
            <ul class="legend">
              <li><i class="sw ok" />Working <b>{{ prof.ok.toLocaleString() }}</b></li>
              <li><i class="sw bad" />Not working <b>{{ prof.bad.toLocaleString() }}</b></li>
              <li><i class="sw idle" />Not checked yet <b>{{ prof.unchecked.toLocaleString() }}</b></li>
              <li v-if="prof.excluded"><i class="sw excl-fill" />Excluded <b>{{ prof.excluded.toLocaleString() }}</b></li>
            </ul>
          </template>
          <p v-else class="muted small">No channels in any profile yet.</p>
        </div>

        <div v-if="h.scope === 'all'" class="block">
          <div class="block-head"><span>Whole catalogue</span><span class="muted">{{ h.totals.streams.toLocaleString() }} streams</span></div>
          <StackBar :parts="parts(h.totals)" :height="8" />
          <ul class="legend">
            <li><i class="sw ok" />Working <b>{{ h.totals.ok.toLocaleString() }}</b></li>
            <li><i class="sw bad" />Not working <b>{{ h.totals.bad.toLocaleString() }}</b></li>
            <li><i class="sw idle" />Not checked yet <b>{{ h.totals.unchecked.toLocaleString() }}</b></li>
            <li v-if="h.totals.excluded"><i class="sw excl-fill" />Excluded <b>{{ h.totals.excluded.toLocaleString() }}</b></li>
          </ul>
        </div>

        <div class="cols">
          <div class="block">
            <div class="block-head"><span>How recent are the checks</span></div>
            <ul class="recent">
              <li v-for="f in fresh" :key="f.label"><span>{{ f.label }}</span><b>{{ f.value.toLocaleString() }}</b></li>
            </ul>
          </div>

          <div v-if="h.sources.length > 1" class="block">
            <div class="block-head"><span>By source</span></div>
            <div v-for="r in h.sources" :key="r.id" class="srow">
              <span class="sname"><span class="sn" :title="r.name">{{ r.name }}</span><span v-if="r.kind === 'custom'" class="badge">custom</span></span>
              <StackBar :parts="parts(r)" />
              <span class="snums"><span class="okc">{{ r.ok.toLocaleString() }} working</span> · <span class="badc">{{ r.bad.toLocaleString() }} not working</span> · <span class="muted">{{ r.unchecked.toLocaleString() }} not checked yet</span><template v-if="r.excluded"> · <span class="muted">{{ r.excluded.toLocaleString() }} excluded</span></template></span>
            </div>
          </div>
        </div>

        <div class="cfg">
          <div class="cfg-head"><b>How it is set up</b>
            <RouterLink v-if="can('settings')" :to="{ name: 'settings-group', params: { group: 'health' } }">Edit in Settings</RouterLink></div>
          <dl class="facts">
            <div><dt>Scope</dt><dd>{{ scopeText }}</dd></div>
            <div><dt>Schedule</dt><dd>{{ schedule }}</dd></div>
            <div><dt>Parallel checks</dt><dd>{{ h.concurrency }}</dd></div>
            <div><dt>Marked not working</dt><dd>{{ downText }}</dd></div>
            <div><dt>Quiet hours</dt><dd>{{ quietText }}</dd></div>
          </dl>
        </div>
      </template>
      <p v-else class="muted">Loading health details…</p>
    </div>

    <div id="attention" class="card">
      <div class="card-head">
        <div>
          <h2>Needs attention</h2>
          <p class="muted sub">Channels in your profiles where every stream fails{{ h?.profiles.channels_down ? ` (${h.profiles.channels_down.toLocaleString()})` : '' }}.</p>
        </div>
      </div>
      <HealthAttention @changed="loadHealth" />
    </div>

    <div class="card">
      <div class="card-head">
        <div>
          <h2>History</h2>
          <p class="muted sub">Every round of checks, and how the streams developed.</p>
        </div>
      </div>
      <HealthHistory :h="h" />
    </div>

    <div id="excluded" class="card">
      <div class="card-head">
        <div>
          <h2>Excluded streams</h2>
          <p class="muted sub">Streams left out of health checks{{ h?.excluded ? ` (${h.excluded.toLocaleString()})` : '' }}.</p>
        </div>
      </div>
      <HealthExcluded :h="h" @changed="loadHealth" />
    </div>
  </section>
  <p v-else class="muted">Loading…</p>
</template>

<style scoped>
.health-page { display:flex; flex-direction:column; gap:16px; }
.health-page .card { margin:0; }
.tiles { display:grid; grid-template-columns:repeat(auto-fit, minmax(150px, 1fr)); gap:12px; }
.tile { background:var(--panel); border:1px solid var(--line); border-left:3px solid var(--line); border-radius:10px; padding:12px 14px; }
.tile .lbl { display:block; font-size:12px; color:var(--muted); }
.tile.none { border-left-color:var(--line); }
.tile b { display:block; font-size:24px; line-height:1.2; font-variant-numeric:tabular-nums; }
.tile.ok { border-left-color:var(--ok); } .tile.bad { border-left-color:var(--bad); } .tile.warn { border-left-color:var(--warn); }
.tile.excl { display:block; border-left-style:dashed; text-align:left; white-space:normal; cursor:pointer; height:auto; width:100%; padding:12px 14px; border-radius:10px; }
.tile.excl:hover { border-color:var(--accent); }
.tile.idle { border-left-color:color-mix(in srgb, var(--muted) 55%, transparent); }

.card-head { display:flex; justify-content:space-between; align-items:flex-start; gap:12px; flex-wrap:wrap; margin-bottom:12px; }
.card-head h2 { margin:0; font-size:17px; }
.sub { margin:2px 0 0; font-size:13px; }
.run-box { background:var(--bg); border:1px solid var(--line); border-radius:8px; padding:12px; margin-bottom:16px; }
.run-line { margin-bottom:8px; }
.okc { color:var(--ok); } .badc { color:var(--bad); } .warnc { color:var(--warn); }

.block { margin-top:18px; }
.block-head { display:flex; justify-content:space-between; gap:8px; font-weight:600; font-size:13px; margin-bottom:8px; }
.block-head .muted { font-weight:400; }
.legend { list-style:none; display:flex; gap:6px 22px; flex-wrap:wrap; margin:10px 0 0; padding:0; font-size:13px; }
.legend li { display:flex; align-items:center; gap:6px; }
.sw { width:10px; height:10px; border-radius:3px; display:inline-block; }
.sw.ok { background:var(--ok); } .sw.bad { background:var(--bad); } .sw.idle { background:color-mix(in srgb, var(--muted) 45%, transparent); }

.keyfacts { display:grid; grid-template-columns:repeat(auto-fit, minmax(260px, 1fr)); gap:16px 32px; margin-top:4px; }
.keyfacts > div { display:flex; flex-direction:column; gap:2px; }
.keyfacts .k { font-size:12px; color:var(--muted); }
.keyfacts b { font-size:20px; line-height:1.25; }
.keyfacts .small { margin:0; }
.recent { list-style:none; margin:0; padding:0; font-size:13px; }
.recent li { display:flex; justify-content:space-between; gap:12px; padding:5px 0; border-bottom:1px solid var(--line); }
.recent li:last-child { border-bottom:0; }
.cfg { margin-top:22px; padding-top:14px; border-top:1px solid var(--line); }
.cfg-head { display:flex; justify-content:space-between; gap:8px; font-size:13px; margin-bottom:8px; }
.cfg-head a { color:var(--accent); font-size:13px; }
.cfg .facts { margin:0; padding:0; border:0; }
.cfg .facts dd { font-size:13px; }
.facts { display:grid; grid-template-columns:repeat(auto-fit, minmax(210px, 1fr)); gap:12px 24px; margin:20px 0 0; padding:14px 0 0; border-top:1px solid var(--line); }
.facts dt { font-size:12px; color:var(--muted); margin-bottom:2px; }
.facts dd { margin:0; font-size:14px; }
.facts .wide { grid-column:1 / -1; }

.cols { display:grid; grid-template-columns:1fr 1fr; gap:24px; }
.small { font-size:13px; margin:8px 0 0; } .tiny { font-size:12px; } .tiny a { color:var(--accent); }
.srow { display:grid; grid-template-columns:minmax(120px, 200px) 1fr minmax(180px, auto); align-items:center; gap:12px; margin-bottom:8px; font-size:13px; }
.sname { display:flex; align-items:center; gap:6px; min-width:0; }
.sn { min-width:0; overflow:hidden; text-overflow:ellipsis; white-space:nowrap; }
.sname .badge { flex:none; margin:0; }
.snums { text-align:right; font-variant-numeric:tabular-nums; }

@media (max-width:860px) { .cols { grid-template-columns:1fr; } .srow { grid-template-columns:1fr; gap:4px; } .snums { text-align:left; } }
</style>
