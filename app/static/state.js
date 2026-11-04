// Shared UI state and the API client.
export const app = {state: null, busy: false};

const listeners = [];
let lastKey = '';

export const onState = listener => listeners.push(listener);

export function setState(data, force = false) {
  const token = data.token ?? app.state?.token;
  app.state = {...data, token};
  // Re-render only when something changed, so focus and screen-reader output are left alone.
  const key = JSON.stringify({...data, token: undefined});
  if (!force && key === lastKey) return;
  lastKey = key;
  listeners.forEach(listener => listener(app.state));
}

export async function request(path, payload, retried = false) {
  const options = payload === undefined ? {} : {
    method: 'POST',
    headers: {'Content-Type': 'application/json', 'X-Management-Token': app.state?.token ?? ''},
    body: JSON.stringify(payload),
  };
  const response = await fetch(path, options);
  let data = {};
  try { data = await response.json(); } catch { /* Non-JSON error body. */ }
  if (response.status === 403 && payload !== undefined && !retried) {
    // The token changes when the proxy restarts; fetch a fresh one and retry once.
    setState(await request('/api/sources'));
    return request(path, payload, true);
  }
  if (response.status === 401) {
    location.reload();  // the session ended; the same address now shows the sign-in page
    throw new Error('Sign in required.');
  }
  if (!response.ok) throw new Error(data.error || (response.status === 401 ? 'Authentication required.' : 'The request failed. Please try again.'));
  return data;
}
