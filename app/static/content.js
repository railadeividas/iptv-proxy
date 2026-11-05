import {app, request, setState} from './state.js';
import {$, closable, element, number, toast} from './util.js';

let content = null, rules = null, initial = '', channels = [];
let expanded = new Set(), pageSizes = new Map(), searchTimer;

const included = channel => rules.channels[channel.id] ?? rules.groups[channel.group] ?? rules.default;

// Channel choices that merely repeat what their group would give are redundant; drop them.
function normalized() {
  const overrides = {};
  const known = new Map(channels.map(channel => [channel.id, channel]));
  for (const [id, value] of Object.entries(rules.channels)) {
    const channel = known.get(id);
    // Keep choices for channels that are not in the list right now (a provider may be briefly empty).
    if (channel && value === (rules.groups[channel.group] ?? rules.default)) continue;
    overrides[id] = value;
  }
  const sorted = object => Object.fromEntries(Object.entries(object).sort(([a], [b]) => a.localeCompare(b)));
  return {default: rules.default, groups: sorted(rules.groups), channels: sorted(overrides)};
}
const snapshot = () => JSON.stringify(normalized());

function matchingGroups() {
  const query = $('#content-search').value.trim().toLocaleLowerCase();
  const onlyIncluded = $('#only-included').checked;
  return content.groups.map(group => {
    const groupMatches = !query || group.name.toLocaleLowerCase().includes(query);
    const matches = group.channels.filter(channel =>
      (groupMatches || channel.name.toLocaleLowerCase().includes(query) || (channel.tvg_id ?? '').toLocaleLowerCase().includes(query)) && (!onlyIncluded || included(channel)));
    return {...group, matches};
  }).filter(group => group.matches.length);
}

function selectionCount() {
  $('#selection-count').textContent = `${number(channels.filter(included).length)} / ${number(channels.length)} included`;
}

function renderGroup(group, query) {
  const details = element('details', 'content-group');
  details.open = expanded.has(group.name) || (!!query && group.matches.length <= 100);
  const summary = element('summary');
  const checkbox = element('input');
  checkbox.type = 'checkbox';
  checkbox.setAttribute('aria-label', `Include group ${group.name}`);
  checkbox.onclick = event => event.stopPropagation();
  const count = element('span', 'group-count');
  const syncGroup = () => {
    const selected = group.channels.filter(included).length;
    checkbox.checked = selected === group.channels.length;
    checkbox.indeterminate = selected > 0 && selected < group.channels.length;
    count.textContent = `${number(selected)} / ${number(group.channels.length)}`;
  };
  syncGroup();
  checkbox.onchange = () => {
    rules.groups[group.name] = checkbox.checked;
    for (const channel of group.channels) delete rules.channels[channel.id];
    renderContent();
  };
  summary.append(checkbox, element('span', 'group-name', group.name), count);

  const rows = element('div', 'channel-rows');
  const renderRows = () => {
    rows.replaceChildren();
    const limit = pageSizes.get(group.name) || 100;
    for (const channel of group.matches.slice(0, limit)) {
      const label = element('label', 'channel-row');
      const check = element('input');
      check.type = 'checkbox';
      check.checked = included(channel);
      check.onchange = () => { rules.channels[channel.id] = check.checked; selectionCount(); syncGroup(); };
      label.append(check, element('span', '', channel.name));
      if (channel.tvg_id) label.append(Object.assign(element('code', 'tvg-id', channel.tvg_id), {title: 'TVG ID, the channel id used to match a program guide (EPG)'}));
      rows.append(label);
    }
    if (group.matches.length > limit) {
      const more = element('button', 'show-more', `Show more (${number(group.matches.length - limit)} remaining)`);
      more.type = 'button';
      more.onclick = () => { pageSizes.set(group.name, limit + 100); renderRows(); };
      rows.append(more);
    }
  };
  if (details.open) renderRows();
  details.addEventListener('toggle', () => {
    if (details.open) { expanded.add(group.name); if (!rows.childNodes.length) renderRows(); }
    else expanded.delete(group.name);
  });
  details.append(summary, rows);
  return details;
}

function renderContent() {
  selectionCount();
  const query = $('#content-search').value.trim();
  $('#search-actions').hidden = !query;
  const groups = matchingGroups();
  const tree = $('#content-tree');
  if (!groups.length) {
    const message = $('#only-included').checked && !query ? 'No channels are included yet.'
      : content.groups.length ? 'No groups or channels match your search.'
      : content.error || 'No channels loaded. Refresh this source and try again.';
    tree.replaceChildren(element('p', 'content-empty', message));
    return;
  }
  tree.replaceChildren(...groups.map(group => renderGroup(group, query)));
}

function renderSkipped(open) {
  const box = $('#skipped'), items = content.skipped ?? [];
  box.hidden = !items.length;
  if (!items.length) return;
  const total = content.skipped_total || items.length;
  $('#skipped-summary').textContent = `${number(total)} unsupported ${total === 1 ? 'entry' : 'entries'} skipped (never exported)`;
  const rows = items.map(item => {
    const row = element('div', 'skipped-row');
    row.append(element('strong', '', item.name), element('span', 'skipped-reason', item.reason), element('code', '', item.url));
    return row;
  });
  if (total > items.length) rows.push(element('p', 'hint', `…and ${number(total - items.length)} more not shown.`));
  $('#skipped-list').replaceChildren(...rows);
  box.open = open;
}

export async function openContent(source, {showSkipped = false} = {}) {
  if (app.busy) return;
  app.busy = true;
  try {
    content = await request('/api/content?source=' + encodeURIComponent(source.id));
    rules = structuredClone(content.rules);
    channels = content.groups.flatMap(group => group.channels);
    initial = snapshot();
    expanded = new Set();
    pageSizes = new Map();
    $('#content-source').textContent = content.name + (content.enabled ? '' : ' · Playlist disabled');
    $('#content-search').value = '';
    $('#only-included').checked = false;
    $('#content-error').hidden = true;
    renderContent();
    renderSkipped(showSkipped);
    $('#content-dialog').showModal();
    if (showSkipped) $('#skipped').scrollIntoView({block: 'nearest'});
    else $('#content-search').focus();
  } catch (error) {
    toast(error.message, 'error');
  } finally {
    app.busy = false;
  }
}

function selectAll(value) {
  rules = {default: value, groups: {}, channels: {}};
  renderContent();
}

function selectResults(value) {
  for (const group of matchingGroups()) for (const channel of group.matches) rules.channels[channel.id] = value;
  renderContent();
}

async function save() {
  if (app.busy || !content) return;
  app.busy = true;
  $('#content-save').disabled = true;
  $('#content-save').textContent = 'Saving…';
  $('#content-error').hidden = true;
  try {
    setState(await request('/api/content/save', {id: content.source_id, rules: normalized()}));
    initial = snapshot();
    $('#content-dialog').close();
    toast('Selection saved. Reload the playlist in your player to update its channels.');
  } catch (error) {
    $('#content-error').textContent = error.message;
    $('#content-error').hidden = false;
  } finally {
    app.busy = false;
    $('#content-save').disabled = false;
    $('#content-save').textContent = 'Save selection';
  }
}

export function initContent() {
  closable($('#content-dialog'), [$('#content-close'), $('#content-cancel')], () => content && snapshot() !== initial);
  // Searching thousands of channels on every keystroke is wasteful; wait for a pause.
  $('#content-search').oninput = () => { clearTimeout(searchTimer); searchTimer = setTimeout(renderContent, 150); };
  $('#only-included').onchange = renderContent;
  $('#include-all').onclick = () => selectAll(true);
  $('#exclude-all').onclick = () => selectAll(false);
  $('#include-results').onclick = () => selectResults(true);
  $('#exclude-results').onclick = () => selectResults(false);
  $('#content-save').onclick = save;
}
