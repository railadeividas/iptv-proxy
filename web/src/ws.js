import { store } from './store'

// Tiny event bus fed by the server's /ws stream.
//   status   – full status object (sync / EPG / health progress), pushed on change
//   streams  – stream health changed
//   catalog  – channel catalogue or custom sources changed
//   profiles – a profile was created / edited / deleted (possibly in another tab)
//   settings – a setting was changed (possibly in another tab)
//   activity – a viewing session started or ended, or a device used a link (the Stats page)
const handlers = {}
export function on(type, fn) {
  ;(handlers[type] ||= new Set()).add(fn)
  return () => handlers[type].delete(fn)
}

let socket, retry = 0, stopped = true, timer

export function connect() {
  stopped = false
  open()
}
export function disconnect() {
  stopped = true
  clearTimeout(timer)
  socket && socket.close()
  store.wsConnected = false
}

function open() {
  if (stopped) return
  const proto = location.protocol === 'https:' ? 'wss' : 'ws'
  socket = new WebSocket(`${proto}://${location.host}/ws`)
  socket.onopen = () => {
    const reconnected = retry > 0
    retry = 0
    store.wsConnected = true
    // after an outage we may have missed events: tell views to resync
    if (reconnected) for (const t of ['streams', 'catalog', 'profiles', 'settings', 'activity']) emit(t, null)
  }
  socket.onmessage = (e) => {
    try {
      const m = JSON.parse(e.data)
      if (m.type === 'status') store.status = m.data
      emit(m.type, m.data)
    } catch {}
  }
  socket.onclose = () => {
    store.wsConnected = false
    if (stopped) return
    retry++
    timer = setTimeout(open, Math.min(15000, 500 * 2 ** Math.min(retry, 5)))
  }
}

function emit(type, data) {
  for (const fn of handlers[type] || []) fn(data)
}
