<script setup>
import { ref, watch, nextTick } from 'vue'
import { confirmState as c } from '../store'

const dlg = ref(null), cancelBtn = ref(null)
let answer = false

function finish(v) {
  answer = v
  dlg.value?.close()
}
// <dialog> closes on Esc / programmatically; resolve the promise exactly once
function onClose() {
  const r = c.resolve
  c.resolve = null
  c.open = false
  if (r) r(answer)
  answer = false
}
watch(() => c.open, async (open) => {
  if (open && dlg.value && !dlg.value.open) {
    answer = false
    dlg.value.showModal()
    await nextTick()
    cancelBtn.value?.focus() // safe default for destructive actions
  }
})
const onBackdrop = (e) => { if (e.target === dlg.value) finish(false) }
</script>

<template>
  <dialog ref="dlg" aria-labelledby="confirm-title" @close="onClose" @click="onBackdrop">
    <div class="box">
      <h2 id="confirm-title">{{ c.title }}</h2>
      <p v-if="c.message" class="muted msg">{{ c.message }}</p>
      <div class="actions">
        <button ref="cancelBtn" @click="finish(false)">Cancel</button>
        <button :class="c.danger ? 'danger-solid' : 'primary'" @click="finish(true)">{{ c.confirmText }}</button>
      </div>
    </div>
  </dialog>
</template>

<style scoped>
dialog { border:1px solid var(--line); border-radius:12px; padding:0; background:var(--panel); color:var(--text); width:min(420px, calc(100vw - 24px)); }
dialog::backdrop { background:rgba(0,0,0,.5); }
.box { padding:18px 20px; }
h2 { margin:0 0 8px; font-size:17px; }
.msg { margin:0 0 16px; white-space:pre-line; }
.actions { display:flex; justify-content:flex-end; gap:8px; margin-top:16px; }
.danger-solid { background:var(--bad); border-color:var(--bad); color:#fff; }
.danger-solid:hover:not(:disabled) { filter:brightness(1.1); border-color:var(--bad); }
</style>
