import {openContent} from './content.js';
import {openCustom} from './custom.js';
import {app, onState, request, setState} from './state.js';
import {$, closable, element, number, since, toast} from './util.js';

let kind = 'url', editingSource = null;
const toHours = seconds => Math.max(1, Math.round(seconds / 3600));
const SHARE_KEY = 'iptv-share-host';

// ---- playlist address -----------------------------------------------------------
function savedHost() {
  try { return localStorage.getItem(SHARE_KEY) || ''; } catch { return ''; }
}
function shareBase(value) {
  value = value.trim().replace(/\/+$/, '');
  if (!value) return '';
  return /^https?:\/\//i.test(value) ? value : 'http://' + value;
}
function playlistUrl() {
  const base = shareBase(savedHost());
  return base ? base + '/playlist.m3u' : app.state.playlist_url;
}
function renderHeader() {
  const {state} = app;
  $('#playlist-url').textContent = playlistUrl();
  const enabled = state.sources.filter(source => source.enabled).length;
  $('#playlist-channel-count').textContent = `${number(state.channels)} ${state.channels === 1 ? 'channel' : 'channels'} from ${number(enabled)} ${enabled === 1 ? 'source' : 'sources'}`;
  $('#source-count').textContent = state.sources.length;
  $('#logout').hidden = !state.password_protected;
  // Release builds are numeric (1.2.3); show those with a leading v and anything else as-is.
  // Release builds are numeric (1.2.3): show them as v1.2.3 and link to their release notes.
  // Other builds (dev, ci) show as-is without a link.
  const release = /^\d/.test(state.version ?? '');
  for (const node of document.querySelectorAll('#footer-version')) {
    node.textContent = release ? 'v' + state.version : (state.version ?? '');
    if (release) node.href = `https://github.com/railadeividas/iptv-proxy/releases/tag/v${state.version}`;
    else node.removeAttribute('href');
  }
  $('#empty').hidden = state.sources.length !== 0;
}

// ---- source cards -----------------------------------------------------------------
function describe(source) {
  if (source.kind === 'url') return source.location;
  if (source.kind === 'custom') return 'Custom M3U playlist';
  if (source.kind === 'upload') return 'Uploaded playlist';
  return 'Local playlist · ' + source.location.split('/').pop();
}

function statusOf({enabled, error, updated, interval}) {
  if (!enabled) return ['off', 'Disabled'];
  if (error) return ['error', 'Last refresh failed'];
  if (!updated) return ['pending', 'Waiting for first load'];
  const age = Date.now() / 1000 - updated;
  if (interval > 0 && age > interval * 3 + 60) return ['stale', 'Not refreshed recently'];
  return ['ok', 'Up to date'];
}

function statusDot(source) {
  const dot = element('span', 'status-dot');
  Object.assign(dot.dataset, {enabled: source.enabled ? '1' : '', error: source.error ? '1' : '', updated: source.updated ?? '', interval: source.refresh_seconds});
  paintStatus(dot);
  return dot;
}

function paintStatus(dot) {
  const [status, label] = statusOf({enabled: !!dot.dataset.enabled, error: !!dot.dataset.error,
    updated: Number(dot.dataset.updated) || null, interval: Number(dot.dataset.interval)});
  dot.className = `status-dot is-${status}`;
  dot.title = label;
  dot.setAttribute('role', 'img');
  dot.setAttribute('aria-label', `Status: ${label}`);
}

function meter(source) {
  const share = source.channels ? Math.round(source.included / source.channels * 100) : 0;
  const bar = element('div', 'meter');
  bar.setAttribute('role', 'img');
  bar.setAttribute('aria-label', `${share}% of channels included`);
  const fill = element('span');
  fill.style.width = share + '%';
  bar.append(fill);
  return bar;
}

function card(source) {
  const node = element('article', 'source');
  node.classList.toggle('is-disabled', !source.enabled);
  const symbol = element('span', 'source-symbol');
  symbol.append(statusDot(source));
  const title = element('div', 'source-title');
  title.append(element('h3', '', source.name));
  const text = element('div', 'source-text');
  text.append(title, element('div', 'source-location', describe(source)));
  const toggle = element('button', 'switch');
  toggle.type = 'button';
  toggle.setAttribute('role', 'switch');
  toggle.setAttribute('aria-checked', source.enabled);
  toggle.setAttribute('aria-label', source.name);
  toggle.title = source.enabled ? 'Enabled: channels are exported' : 'Disabled: nothing is exported';
  toggle.onclick = () => action('/api/sources/toggle', {id: source.id}, toggle);
  const main = element('div', 'source-main');
  main.append(symbol, text, toggle);

  const stats = element('div', 'source-stats');
  const count = element('div', 'source-count');
  count.append(element('strong', '', `${number(source.included)} of ${number(source.channels)} channels included`), meter(source));
  stats.append(count);
  if (source.skipped) {
    const skipped = element('button', 'link-button', `${number(source.skipped)} unsupported ${source.skipped === 1 ? 'entry' : 'entries'} skipped`);
    skipped.type = 'button';
    skipped.title = 'See which entries were skipped and why';
    skipped.onclick = () => openContent(source, {showSkipped: true});
    stats.append(skipped);
  }
  const updated = element('span', '', since(source.updated));
  updated.dataset.updated = source.updated ?? '';
  stats.append(updated);

  const button = (label, handler, className = '') => {
    const item = element('button', className, label);
    item.type = 'button';
    item.onclick = handler;
    return item;
  };
  const needsSelection = source.enabled && source.channels > 0 && source.included === 0;
  const manage = button('Manage content', () => openContent(source), needsSelection ? 'manage attention' : 'manage');
  const menu = element('details', 'more-menu');
  const trigger = element('summary', '', '⋯');
  trigger.setAttribute('aria-label', `More actions for ${source.name}`);
  const list = element('div', 'more-list');
  list.append(
    button('✎ Edit', () => { menu.removeAttribute('open'); openEdit(source); }, 'edit'),
    button('✕ Remove', () => { menu.removeAttribute('open'); confirmRemove(source); }, 'remove'));
  menu.append(trigger, list);
  const actions = element('div', 'source-actions');
  actions.append(
    manage,
    source.kind === 'custom'
      ? button('✎ Edit playlist', () => openCustom(source), 'edit')
      : button('↻ Refresh', event => action('/api/sources/refresh', {id: source.id}, event.currentTarget)),
    menu);
  const bottom = element('div', 'source-bottom');
  bottom.append(stats, actions);
  node.append(main, bottom);
  if (needsSelection) node.append(element('p', 'source-hint', 'Nothing is exported yet. Choose the groups and channels to include with Manage content.'));
  if (source.error) {
    const banner = element('p', 'source-error');
    banner.setAttribute('role', 'alert');
    banner.append(element('strong', '', 'Refresh failed. '), document.createTextNode(source.error + (source.updated ? ' The last successful copy is still in use.' : '')));
    node.append(banner);
  }
  return node;
}

function render() {
  renderHeader();
  $('#sources').replaceChildren(...app.state.sources.map(card));
}

export function tickTimes() {
  document.querySelectorAll('.source-stats [data-updated]').forEach(node => { node.textContent = since(Number(node.dataset.updated) || null); });
  document.querySelectorAll('.status-dot').forEach(paintStatus);
}

async function action(path, payload, button) {
  if (app.busy) return false;
  app.busy = true;
  const label = button?.textContent;
  if (button) { button.disabled = true; if (!button.classList.contains('switch')) button.textContent = 'Working…'; }
  try {
    setState(await request(path, payload));
    return true;
  } catch (error) {
    toast(error.message, 'error');
    return false;
  } finally {
    app.busy = false;
    if (button?.isConnected) { button.disabled = false; button.textContent = label; }
  }
}

function confirmRemove(source) {
  if (app.busy) return;
  $('#delete-description').textContent = `“${source.name}” and its channels will be removed from the combined playlist. Your original source will not be changed.`;
  const dialog = $('#delete-dialog');
  dialog.returnValue = '';
  dialog.showModal();
  dialog.addEventListener('close', () => {
    if (dialog.returnValue === 'remove') action('/api/sources/delete', {id: source.id});
  }, {once: true});
}

// ---- add / edit dialog --------------------------------------------------------------
function selectKind(value) {
  kind = value;
  for (const name of ['url', 'upload', 'custom']) {
    $(`#${name}-fields`).hidden = kind !== name;
    $(`#${name}-tab`).classList.toggle('selected', kind === name);
    $(`#${name}-tab`).setAttribute('aria-pressed', kind === name);
  }
  $('#source-url').required = kind === 'url';
  $('#file').required = kind === 'upload' && (!editingSource || editingSource.kind === 'url');
  updateRefreshFields();
}

function updateRefreshFields() {
  // A hand-built playlist is edited locally and has nothing to refresh.
  const custom = kind === 'custom';
  $('#refresh-settings').hidden = custom;
  const enabled = $('#auto-refresh').checked;
  $('#interval-fields').hidden = !enabled;
  $('#refresh-interval').disabled = custom || !enabled;
  $('#interval-hint').textContent = enabled ? 'Manual Refresh is always available.' : 'Loaded when the app starts. After that, refresh manually whenever you need.';
}

function openForm(title, submit, description) {
  $('#source-dialog-title').textContent = title;
  $('#save').textContent = submit;
  $('#source-dialog-description').textContent = description;
  $('#form-error').hidden = true;
  $('#dialog').showModal();
  $('#name').focus();
}

function openAdd() {
  if (app.busy) return;
  editingSource = null;
  $('#source-form').reset();
  selectKind('url');
  $('#refresh-interval').value = toHours(app.state?.refresh_seconds || 86400);
  updateRefreshFields();
  openForm('Add playlist', 'Add playlist', 'Add a playlist link, file, or build an M3U playlist. Use Manage content to select channels.');
}

function openEdit(source) {
  if (app.busy) return;
  editingSource = source;
  $('#source-form').reset();
  $('#name').value = source.name;
  $('#source-url').value = source.kind === 'url' ? source.location : '';
  selectKind(source.kind === 'url' || source.kind === 'custom' ? source.kind : 'upload');
  $('#auto-refresh').checked = source.refresh_seconds !== 0;
  $('#refresh-interval').value = toHours(source.refresh_seconds || app.state.refresh_seconds || 86400);
  updateRefreshFields();
  openForm('Edit playlist', 'Save changes', 'Update the name or source. Your saved content selections will be kept.');
}

async function submitForm(event) {
  event.preventDefault();
  if (app.busy) return;
  app.busy = true;
  $('#save').disabled = true;
  $('#save').textContent = editingSource ? 'Saving…' : 'Importing…';
  $('#form-error').hidden = true;
  try {
    const payload = {name: $('#name').value.trim(), kind, refresh_seconds: kind === 'custom' || !$('#auto-refresh').checked ? 0 : Math.round(Number($('#refresh-interval').value) * 3600)};
    if (editingSource) payload.id = editingSource.id;
    if (kind === 'url') payload.url = $('#source-url').value.trim();
    else if (kind === 'custom') { if (!editingSource || editingSource.kind !== 'custom') payload.content = '#EXTM3U\n'; }
    else {
      const file = $('#file').files[0];
      if (!file && (!editingSource || editingSource.kind === 'url')) throw new Error('Choose a playlist file.');
      if (file?.size > 16 * 1024 * 1024) throw new Error('Choose a playlist file smaller than 16 MB.');
      if (file) payload.content = await file.text();
    }
    setState(await request(editingSource ? '/api/sources/edit' : '/api/sources/add', payload));
    $('#dialog').close();
    toast(editingSource ? 'Playlist updated' : 'Playlist added. Use Manage content to select channels.');
  } catch (error) {
    $('#form-error').textContent = error.message;
    $('#form-error').hidden = false;
  } finally {
    app.busy = false;
    $('#save').disabled = false;
    $('#save').textContent = editingSource ? 'Save changes' : 'Add playlist';
  }
}

let copiedTimer;
async function copyUrl() {
  if (!app.state) return;
  const button = $('#copy');
  try {
    await navigator.clipboard.writeText(playlistUrl());
    button.textContent = 'Copied ✓';
    clearTimeout(copiedTimer);
    copiedTimer = setTimeout(() => { button.textContent = 'Copy URL'; }, 2000);
  } catch {
    const range = document.createRange();
    range.selectNodeContents($('#playlist-url'));
    getSelection().removeAllRanges();
    getSelection().addRange(range);
    toast('Select and copy the playlist URL');
  }
}

export function initSources() {
  onState(render);
  $('#add').onclick = $('#empty-add').onclick = openAdd;
  closable($('#dialog'), [$('#close'), $('#cancel')]);
  for (const name of ['url', 'upload', 'custom']) $(`#${name}-tab`).onclick = () => selectKind(name);
  $('#auto-refresh').onchange = updateRefreshFields;
  $('#source-form').onsubmit = submitForm;
  $('#copy').onclick = copyUrl;
  $('#logout').onclick = async () => {
    await fetch('/api/logout', {method: 'POST'});
    location.reload();
  };
  // Close an open "more actions" menu when clicking elsewhere or pressing Escape.
  const closeMenus = except => document.querySelectorAll('.more-menu[open]').forEach(menu => { if (!except || !menu.contains(except)) menu.removeAttribute('open'); });
  document.addEventListener('click', event => closeMenus(event.target));
  document.addEventListener('keydown', event => { if (event.key === 'Escape') closeMenus(); });
  $('#share-host').value = savedHost();
  $('#share-host').oninput = event => {
    try { localStorage.setItem(SHARE_KEY, event.target.value.trim()); } catch { /* Address is kept for this page only. */ }
    if (app.state) renderHeader();
  };
}
