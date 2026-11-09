<script setup>
import { ref, computed, onUnmounted } from 'vue'
import { useStatsQuery, profileColor } from '../../stats'

// Which profiles the page is about: all of them, one, or any selection. The ids go in the URL (profile=1,3).
const props = defineProps({ profiles: { type: Array, default: () => [] }, many: Boolean })
const q = useStatsQuery()
const open = ref(false), root = ref(null), trigger = ref(null), find = ref('')

const away = (e) => { if (!root.value?.contains(e.target)) close() }
const key = (e) => { if (e.key === 'Escape') { close(); trigger.value?.focus() } }
function show() { find.value = ''; open.value = true; document.addEventListener('mousedown', away); document.addEventListener('keydown', key) }
function close() { open.value = false; document.removeEventListener('mousedown', away); document.removeEventListener('keydown', key) }
onUnmounted(close)

const chosen = computed(() => props.profiles.filter((p) => q.selected.value.includes(p.profile_id)))
const label = computed(() => {
  const c = chosen.value
  if (!c.length) return props.many ? 'All profiles' : 'All my profiles'
  if (c.length === 1) return c[0].profile
  return c.length <= 2 ? c.map((p) => p.profile).join(', ') : `${c.length} profiles`
})
const visible = computed(() => {
  const s = find.value.trim().toLowerCase()
  return s ? props.profiles.filter((p) => p.profile.toLowerCase().includes(s) || (p.owner || '').toLowerCase().includes(s)) : props.profiles
})
function set(ids) { q.setQuery({ profile: ids.join(','), page: '' }) }
function toggle(id) {
  const now = new Set(q.selected.value)
  now.has(id) ? now.delete(id) : now.add(id)
  set([...now].sort((a, b) => a - b))
}
const all = () => set([])
const only = (id) => { set([id]); close() }
</script>

<template>
  <div ref="root" class="picker">
    <button ref="trigger" type="button" class="trigger" aria-haspopup="listbox" :aria-expanded="open" :title="chosen.map((p) => p.profile).join(', ')" @click="open ? close() : show()">
      <span class="lbl">{{ label }}</span>
      <svg viewBox="0 0 10 6" width="10" height="6" aria-hidden="true"><path d="M1 1l4 4 4-4" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" /></svg>
    </button>
    <div v-if="open" class="pop" role="listbox" aria-multiselectable="true" aria-label="Profiles">
      <input v-if="profiles.length > 7" v-model="find" type="search" placeholder="Search profiles" aria-label="Search profiles">
      <div class="top">
        <button type="button" class="all" :class="{ on: !chosen.length }" @click="all">{{ many ? 'All profiles' : 'All my profiles' }}</button>
        <span v-if="chosen.length" class="muted small">{{ chosen.length }} selected</span>
      </div>
      <ul>
        <li v-for="p in visible" :key="p.profile_id">
          <label><input type="checkbox" :checked="q.selected.value.includes(p.profile_id)" @change="toggle(p.profile_id)">
            <i class="dot" :style="{ background: profileColor(p.profile_id, profiles) }"></i>
            <span class="nm">{{ p.profile }}<span v-if="many && p.owner" class="muted small"> · {{ p.owner }}</span></span></label>
          <button type="button" class="just" title="Only this profile" @click="only(p.profile_id)">only</button>
        </li>
        <li v-if="!visible.length" class="muted small none">No profile matches.</li>
      </ul>
    </div>
  </div>
</template>

<style scoped>
.picker { position:relative; min-width:0; max-width:100%; }
.trigger { gap:8px; padding:0 12px; max-width:100%; min-width:160px; justify-content:space-between; }
.trigger .lbl { overflow:hidden; text-overflow:ellipsis; }
.pop { position:absolute; z-index:30; top:calc(100% + 6px); left:0; width:max(100%, min(320px, calc(100vw - 32px))); min-width:240px; background:var(--panel); border:1px solid var(--line); border-radius:10px; box-shadow:0 10px 32px rgba(0,0,0,.22); padding:10px; display:flex; flex-direction:column; gap:8px; }
.pop > input[type=search] { flex:none; width:100%; margin:0; } /* a flex item would be squeezed to its text height */
.pop > * { flex-shrink:0; }
.top { display:flex; align-items:center; justify-content:space-between; gap:8px; }
.all { height:auto; border:0; background:none; padding:4px 6px; border-radius:6px; font-weight:500; }
.all:hover { background:color-mix(in srgb, var(--accent) 10%, transparent); }
.all.on { color:var(--accent); }
ul { list-style:none; margin:0; padding:0; max-height:300px; overflow-y:auto; overflow-x:hidden; }
li { display:flex; align-items:center; gap:6px; border-radius:6px; }
li:hover { background:color-mix(in srgb, var(--accent) 8%, transparent); }
li label { flex:1; min-width:0; display:flex; align-items:center; gap:8px; padding:6px 6px; cursor:pointer; }
li label input { width:auto; margin:0; flex:none; }
.dot { flex:none; width:10px; height:10px; border-radius:3px; }
.nm { overflow:hidden; text-overflow:ellipsis; white-space:nowrap; }
.small { font-size:12px; }
.just { height:auto; border:0; background:none; color:var(--muted); font-size:12px; padding:2px 8px; margin-right:4px; opacity:0; }
li:hover .just, .just:focus-visible { opacity:1; }
.just:hover { color:var(--accent); }
@media (hover:none) { .just { opacity:1; } }
.none { padding:8px 6px; }
</style>
