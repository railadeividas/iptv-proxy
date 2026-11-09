<script setup>
import { ref, computed, onMounted, onUnmounted } from 'vue'

// A small SVG chart: stacked bars, stacked area or lines over a row per x position, with a crosshair and a tooltip.
// rows: [{ l: axis label, tt: tooltip title, v: { seriesKey: number|null } }]. Colours come from CSS variables.
const props = defineProps({
  rows: { type: Array, default: () => [] },
  keys: { type: Array, required: true },
  names: { type: Object, required: true },
  colors: { type: Object, required: true },
  type: { type: String, default: 'bars' }, // bars | area | line
  fmt: { type: Function, required: true }, // a value for people (tooltip)
  axis: { type: Function, default: null }, // a value on the y axis (defaults to fmt)
  scale: { type: Number, default: 1 }, // the unit the grid counts in (1024 for KB…): grid lines land on round numbers
  max: { type: Number, default: 0 }, // fixed top of the axis (percentages)
  int: Boolean, // whole numbers on the axis
  prev: { type: Array, default: null }, // previous period, one number per row, drawn as a dashed line
  height: { type: Number, default: 0 },
  label: { type: String, default: 'Chart' },
  maxBar: { type: Number, default: 36 },
  labelWidth: { type: Number, default: 62 },
  sparse: Boolean, // the rows carry labels only where wanted: show every label there is
})

const box = ref(null)
const w = ref(0)
let ro
onMounted(() => {
  w.value = box.value.clientWidth
  ro = new ResizeObserver(() => { w.value = box.value?.clientWidth || w.value })
  ro.observe(box.value)
})
onUnmounted(() => ro?.disconnect())

const W = computed(() => Math.max(240, w.value))
const H = computed(() => props.height || (W.value < 500 ? 200 : 240))
const M = { l: 54, r: 10, t: 8, b: 24 }
const iw = computed(() => W.value - M.l - M.r)
const ih = computed(() => H.value - M.t - M.b)
const n = computed(() => props.rows.length)
const stacked = computed(() => props.type !== 'line')
const val = (r, k) => r.v[k] || 0
const total = (r) => props.keys.reduce((a, k) => a + val(r, k), 0)

function niceMax(v) {
  if (v <= 0) return 1
  const e = 10 ** Math.floor(Math.log10(v))
  for (const m of [1, 2, 2.5, 5, 10]) if (m * e >= v) return m * e
  return 10 * e
}
const top = computed(() => {
  if (props.max) return props.max
  let mx = Math.max(0, ...props.rows.map((r) => (stacked.value ? total(r) : Math.max(0, ...props.keys.map((k) => val(r, k))))))
  if (props.prev) mx = Math.max(mx, ...props.prev)
  const t = mx > 0 ? niceMax((mx / props.scale) * 1.05) * props.scale : 4 * props.scale // nothing to draw: a plain 0 to 4 in the axis unit
  if (!props.int) return t
  // whole numbers: four equal steps that are themselves whole (and round when the numbers are big)
  if (t < 20) return Math.max(4, Math.ceil(t / 4) * 4)
  const step = t / 4, half = 10 ** Math.floor(Math.log10(step)) / 2
  return Math.ceil(step / half) * half * 4
})
const y = (v) => M.t + ih.value - (v / top.value) * ih.value
const band = computed(() => iw.value / Math.max(1, n.value))
const x = (i) => M.l + band.value * (i + 0.5)
const ticks = computed(() => [0, 1, 2, 3, 4].map((i) => ({ v: (top.value * i) / 4, y: y((top.value * i) / 4) })))
const fmtAxis = (v) => (props.axis || props.fmt)(v)
const xlabels = computed(() => {
  const widest = Math.max(props.labelWidth, ...props.rows.map((r) => (r.l || '').length * 6.4 + 18))
  const step = props.sparse ? 1 : Math.ceil(n.value / Math.max(2, Math.floor(iw.value / widest)))
  return props.rows.map((r, i) => ({ i, l: r.l })).filter((r) => r.i % step === 0 && r.l)
})

// bars: one path per segment; the top segment of a stack gets rounded corners
const bars = computed(() => {
  if (props.type !== 'bars') return []
  const bw = Math.max(2, Math.min(props.maxBar, band.value * 0.72))
  const out = []
  props.rows.forEach((r, i) => {
    let acc = 0
    props.keys.forEach((k, ki) => {
      const v = val(r, k)
      if (!v) return
      const y0 = y(acc), y1 = y(acc + v)
      acc += v
      const last = props.keys.slice(ki + 1).every((kk) => !val(r, kk))
      const h = Math.max(1, y0 - y1)
      const rx = last ? Math.min(4, bw / 2, h) : 0
      const l = x(i) - bw / 2, rr = x(i) + bw / 2
      const d = rx > 0
        ? `M${l},${y0} V${y1 + rx} a${rx},${rx} 0 0 1 ${rx},${-rx} H${rr - rx} a${rx},${rx} 0 0 1 ${rx},${rx} V${y0} Z`
        : `M${l},${y0} V${y0 - h} H${rr} V${y0} Z`
      out.push({ key: `${i}-${k}`, d, color: props.colors[k] })
    })
  })
  return out
})
const areas = computed(() => {
  if (props.type !== 'area' || !n.value) return []
  const acc = props.rows.map(() => 0)
  return props.keys.map((k) => {
    const lo = acc.slice()
    props.rows.forEach((r, i) => { acc[i] += val(r, k) })
    const up = props.rows.map((_, i) => `${x(i)},${y(acc[i])}`).join(' L')
    const dn = props.rows.map((_, i) => `${x(n.value - 1 - i)},${y(lo[n.value - 1 - i])}`).join(' L')
    return { key: k, fill: `M${up} L${dn} Z`, edge: `M${up}`, color: props.colors[k] }
  })
})
// lines: a gap (null) breaks the line
function segments(k) {
  const out = []
  let cur = []
  props.rows.forEach((r, i) => {
    if (r.v[k] == null) { if (cur.length) out.push(cur); cur = []; return }
    cur.push(`${x(i)},${y(r.v[k])}`)
  })
  if (cur.length) out.push(cur)
  return out.map((s) => 'M' + s.join(' L'))
}
const lines = computed(() => (props.type === 'line' ? props.keys.map((k) => ({
  key: k, color: props.colors[k], paths: segments(k),
  dots: n.value <= 31 ? props.rows.flatMap((r, i) => (r.v[k] == null ? [] : [{ i, cx: x(i), cy: y(r.v[k]) }])) : [],
})) : []))
const prevPath = computed(() => (props.prev && props.prev.length ? 'M' + props.prev.map((v, i) => `${x(i)},${y(v)}`).join(' L') : ''))

// hover
const hover = ref(-1)
const mx = ref(0)
function move(e) {
  if (!n.value) return
  const rc = e.currentTarget.getBoundingClientRect()
  hover.value = Math.max(0, Math.min(n.value - 1, Math.floor(((e.clientX - rc.left) / rc.width) * n.value)))
  mx.value = e.clientX - box.value.getBoundingClientRect().left
}
const row = computed(() => props.rows[hover.value])
const tipStyle = computed(() => (mx.value > W.value / 2 ? { right: W.value - mx.value + 14 + 'px' } : { left: mx.value + 14 + 'px' }))
</script>

<template>
  <div ref="box" class="chart">
    <svg v-if="w" :viewBox="`0 0 ${W} ${H}`" :height="H" role="img" :aria-label="label">
      <g v-for="(t, i) in ticks" :key="i">
        <line :x1="M.l" :x2="W - M.r" :y1="t.y" :y2="t.y" class="grid" :class="{ base: i === 0 }" />
        <text :x="M.l - 8" :y="t.y + 4" text-anchor="end">{{ fmtAxis(t.v) }}</text>
      </g>
      <text v-for="l in xlabels" :key="l.i" :x="x(l.i)" :y="H - 6" text-anchor="middle">{{ l.l }}</text>
      <path v-for="b in bars" :key="b.key" :d="b.d" :fill="b.color" class="seg" />
      <g v-for="a in areas" :key="a.key">
        <path :d="a.fill" :fill="a.color" fill-opacity=".78" class="seg" />
        <path :d="a.edge" fill="none" :stroke="a.color" stroke-width="2" />
      </g>
      <g v-for="l in lines" :key="l.key">
        <path v-for="(d, i) in l.paths" :key="i" :d="d" fill="none" :stroke="l.color" stroke-width="2" stroke-linejoin="round" />
        <circle v-for="p in l.dots" :key="p.i" :cx="p.cx" :cy="p.cy" r="3" :fill="l.color" class="dot" />
      </g>
      <path v-if="prevPath" :d="prevPath" fill="none" class="prev" />
      <line v-if="hover >= 0" :x1="x(hover)" :x2="x(hover)" :y1="M.t" :y2="M.t + ih" class="cross" />
      <rect :x="M.l" :y="M.t" :width="iw" :height="ih" fill="transparent" class="hit" @pointermove="move" @pointerleave="hover = -1" />
    </svg>
    <div v-if="row" class="tip" :style="tipStyle">
      <div class="t">{{ row.tt || row.l }}</div>
      <div v-for="k in keys" :key="k" class="r"><span><i :style="{ background: colors[k] }"></i>{{ names[k] }}</span><b>{{ row.v[k] == null ? '–' : fmt(row.v[k]) }}</b></div>
      <div v-if="keys.length > 1 && stacked" class="r muted tot"><span>Total</span><b>{{ fmt(total(row)) }}</b></div>
      <div v-if="prev && prev[hover] != null" class="r muted"><span>Previous period</span><span>{{ fmt(prev[hover]) }}</span></div>
    </div>
  </div>
</template>

<style scoped>
.chart { position:relative; width:100%; }
svg { display:block; width:100%; }
text { fill:var(--muted); font-size:11px; font-family:inherit; }
.grid { stroke:var(--line); stroke-dasharray:2 3; }
.grid.base { stroke-dasharray:none; }
.seg { stroke:var(--panel); stroke-width:1.5; }
.dot { stroke:var(--panel); stroke-width:2; }
.prev { stroke:var(--muted); stroke-width:1.5; stroke-dasharray:5 4; }
.cross { stroke:var(--muted); stroke-width:1; pointer-events:none; }
.hit { touch-action:pan-y; }
.small { font-size:12px; }
.tip { position:absolute; top:0; z-index:5; pointer-events:none; background:var(--panel); border:1px solid var(--line); border-radius:8px; padding:8px 10px; box-shadow:0 6px 24px rgba(0,0,0,.2); font-size:12px; min-width:140px; }
.tip .t { font-weight:600; margin-bottom:4px; }
.tip .r { display:flex; align-items:center; gap:12px; justify-content:space-between; }
.tip .r span:first-child { display:flex; align-items:center; gap:6px; }
.tip .tot { margin-top:3px; }
.tip i { width:8px; height:8px; border-radius:2px; display:inline-block; flex:none; }
</style>
