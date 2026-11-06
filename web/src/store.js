import { reactive } from 'vue'

// Shared live state. `status` is pushed over the WebSocket.
export const store = reactive({
  user: undefined, // undefined = not asked yet, null = signed out, else { id, username, role, perms }
  setupNeeded: false, // true on the first start: no user exists yet
  version: '', // of the running app, from /api/session
  status: null,
  wsConnected: false,
  toast: '',
})

// Does the signed-in user have a permission ('sources', 'health', 'settings', 'users', 'profiles.all')?
// The server enforces the same list; this only decides what to show.
export const can = (perm) => !!store.user?.perms?.includes(perm)

let timer
export function toast(msg) {
  store.toast = msg
  clearTimeout(timer)
  timer = setTimeout(() => (store.toast = ''), 2600)
}

// Promise-based confirmation dialog (rendered by <ConfirmDialog> in App.vue).
export const confirmState = reactive({ open: false, title: '', message: '', confirmText: 'Confirm', danger: false, resolve: null })

export function ask({ title, message = '', confirmText = 'Confirm', danger = false }) {
  if (confirmState.resolve) confirmState.resolve(false)
  return new Promise((resolve) => {
    Object.assign(confirmState, { open: true, title, message, confirmText, danger, resolve })
  })
}
