import {initContent} from './content.js';
import {initCustom} from './custom.js';
import {initSources, tickTimes} from './sources.js';
import {app, request, setState} from './state.js';
import {$} from './util.js';

let offline = false;

async function load() {
  // Skip while an action is running or the tab is hidden; the next tick catches up.
  if (app.busy || document.hidden) return;
  try {
    setState(await request('/api/sources'), offline);
    offline = false;
  } catch {
    offline = true;
    $('#playlist-channel-count').textContent = 'Unable to connect. Retrying…';
  }
}

initSources();
initContent();
initCustom();
load();
setInterval(load, 10000);
setInterval(tickTimes, 10000);
document.addEventListener('visibilitychange', () => { if (!document.hidden) load(); });
