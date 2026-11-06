import { ref, watch } from 'vue'

// 'system' (default) follows the OS via prefers-color-scheme; no attribute is set.
const saved = (() => { try { return localStorage.getItem('theme') } catch { return null } })()
export const theme = ref(saved === 'light' || saved === 'dark' ? saved : 'system')

watch(theme, (t) => {
  const el = document.documentElement
  if (t === 'system') el.removeAttribute('data-theme')
  else el.setAttribute('data-theme', t)
  try {
    if (t === 'system') localStorage.removeItem('theme')
    else localStorage.setItem('theme', t)
  } catch {}
})
