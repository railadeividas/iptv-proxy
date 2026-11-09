<script setup>
import MeasureSwitch from './MeasureSwitch.vue'
import Scope from './Scope.vue'
import { ref, computed } from 'vue'
import { api } from '../../api'
import { rel } from '../../util'
import { useStatsQuery, useLoader, usePaging, bucketWord, METRICS, numberRows, playerName, networkName } from '../../stats'
import BarList from './BarList.vue'
import Tiles from './Tiles.vue'
import StatChart from './StatChart.vue'
import Pager from '../Pager.vue'

// Who watches with what: players, networks, active devices over time and every device that used a link.
const props = defineProps({ o: Object })
const q = useStatsQuery()
const pg = usePaging()
const M = computed(() => METRICS[q.metric.value])
const players = ref([]), networks = ref([]), playerTotal = ref(0), networkTotal = ref(0), t = ref(null), list = ref(null)

useLoader(async (cur) => {
  const [pl, nw, tl, ls] = await Promise.all([
    api.get('/stats/breakdown?' + q.params({ by: 'player', sort: q.metric.value, limit: 6 })),
    api.get('/stats/breakdown?' + q.params({ by: 'network', sort: q.metric.value, limit: 6 })),
    api.get('/stats/timeline?' + q.params()),
    api.get('/stats/clients?' + q.params({ limit: pg.size.value, offset: pg.page.value * pg.size.value })),
  ])
  if (!cur()) return
  players.value = pl.items; playerTotal.value = pl.total; networks.value = nw.items; networkTotal.value = nw.total; t.value = tl; list.value = ls
}, { deps: () => [q.rangeKey.value, q.stepKey.value, q.profile.value, q.metric.value, pg.page.value, pg.size.value] })

const playerRows = computed(() => players.value.map((x) => ({ key: x.key, name: playerName(x.key), v: M.value.pick(x) })))
const networkRows = computed(() => networks.value.map((x) => ({ key: x.key, name: networkName(x.key), v: M.value.pick(x) })))
const activeRows = computed(() => (t.value ? numberRows(t.value, t.value.devices) : []))
const tiles = computed(() => [
  { label: 'Devices', scope: 'period', value: (props.o?.totals?.clients || 0).toLocaleString(), title: 'Different devices (address and player) that used a link in this period' },
  { label: 'Players', scope: 'period', value: playerTotal.value.toLocaleString(), title: 'Different players (apps or devices) that watched' },
  { label: 'Networks', scope: 'period', value: networkTotal.value.toLocaleString(), title: 'Different addresses, as recorded (Settings > Stats)' },
])
const many = computed(() => !!props.o?.all_profiles)
const pages = computed(() => Math.max(1, Math.ceil((list.value?.total || 0) / pg.size.value)))
const addr = (x) => x.ip || 'address not recorded'
const agent = (x) => x.ua || 'player not recorded'
const privacy = computed(() => ({ masked: 'Addresses are masked: devices on the same network look alike.', none: 'Addresses are not recorded.' })[props.o?.ip_mode] || '')
</script>

<template>
  <Tiles :items="tiles" :cols="3" />
  <p v-if="privacy" class="muted small">{{ privacy }}</p>
  <div class="card-row">
    <div class="card"><div class="chead"><h2>Players<Scope /></h2><MeasureSwitch /></div><p class="muted small">By {{ M.unit }}, the app or device that played</p><BarList :rows="playerRows" :fmt="M.fmt" /></div>
    <div class="card"><div class="chead"><h2>Networks<Scope /></h2><MeasureSwitch /></div><p class="muted small">By {{ M.unit }}, as recorded (Settings > Stats)</p><BarList :rows="networkRows" :fmt="M.fmt" color="var(--s5)" /></div>
  </div>
  <div class="card"><h2>Active devices<Scope /></h2><p class="muted small">Different devices that played, per {{ bucketWord(t?.bucket) }}</p>
    <StatChart :rows="activeRows" :keys="['n']" :names="{ n: 'Active devices' }" :colors="{ n: 'var(--s3)' }" type="line" int :fmt="(v) => v.toLocaleString()" :height="200" label="Active devices" /></div>
  <div class="card">
    <h2>Devices<Scope text="Seen in the period" /></h2>
    <p v-if="list && !list.total" class="muted small">No device used a link in this period.</p>
    <p v-else-if="!list" class="muted small">Loading…</p>
    <div v-else class="scroll">
      <table class="t">
        <thead><tr><th>Device</th><th>Profile</th><th>First seen</th><th>Last seen</th><th class="r">Playlist</th><th class="r">Guide</th><th class="r">Plays</th></tr></thead>
        <tbody>
          <tr v-for="x in list.items" :key="x.id">
            <td><b>{{ addr(x) }}</b><span class="muted small block">{{ agent(x) }}</span></td>
            <td>{{ x.profile }}<span v-if="many && x.owner" class="muted small block">{{ x.owner }}</span></td>
            <td class="nowrap" :title="new Date(x.first_seen * 1000).toLocaleString()">{{ rel(x.first_seen) }}</td>
            <td class="nowrap" :title="new Date(x.last_seen * 1000).toLocaleString()">{{ rel(x.last_seen) }}</td>
            <td class="r">{{ x.playlists.toLocaleString() }}</td>
            <td class="r">{{ x.guides.toLocaleString() }}</td>
            <td class="r">{{ x.plays.toLocaleString() }}</td>
          </tr>
        </tbody>
      </table>
    </div>
    <Pager v-if="list?.total" :page="pg.page.value" :pages="pages" :total="list.total" :size="pg.size.value" :count="list.items.length" :sizes="pg.SIZES" @go="pg.goPage" @size="pg.setSize" />
  </div>
</template>

<style scoped>
.small { font-size:13px; margin:0 0 10px; }
.scroll { overflow-x:auto; overflow-y:hidden; }
.t { width:100%; border-collapse:collapse; font-size:13px; }
.t th { font-size:12px; font-weight:500; color:var(--muted); }
.t td, .t th { padding:8px 10px 8px 0; vertical-align:top; border-bottom:1px solid var(--line); text-align:left; }
.t td.r, .t th.r { text-align:right; }
.t td .small { margin:2px 0 0; }
.block { display:block; }
.nowrap { white-space:nowrap; }
</style>
