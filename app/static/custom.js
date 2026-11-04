import {ChannelError, duplicateWarnings, parseCustom, serializeCustom} from './custom-m3u.js';
import {app, request, setState} from './state.js';
import {$, closable, element, number, toast} from './util.js';

let source = null, channels = [], initial = '', removing = null, openIndex = -1;
const FIELDS = [
  ['name', 'Channel name', 'Name', 'text'], ['group', 'Group (optional)', 'News', 'text'],
  ['url', 'Stream URL', 'https://example.com/live.m3u8', 'url'],
  ['id', 'Guide ID (optional)', 'tvg-id', 'text'], ['logo', 'Logo URL (optional)', 'https://example.com/logo.png', 'text'],
];

const snapshot = () => JSON.stringify(channels);

function updateWarnings() {
  const warnings = duplicateWarnings(channels);
  document.querySelectorAll('#custom-channel-list .custom-channel').forEach(card => {
    const message = warnings.get(Number(card.dataset.index)) ?? '';
    const badge = card.querySelector('.custom-badge');
    badge.hidden = !message;
    badge.title = message;
    const note = card.querySelector('.custom-warning');
    if (note) { note.textContent = message; note.hidden = !message; }
  });
}

function matches(channel, query) {
  return !query || [channel.name, channel.group, channel.url].some(value => (value ?? '').toLocaleLowerCase().includes(query));
}

function summaryText(channel) {
  return [channel.group, channel.url].filter(value => value?.trim()).join(' · ');
}

function channelCard(channel, index) {
  const open = index === openIndex;
  const card = element('section', 'custom-channel');
  card.classList.toggle('is-open', open);
  card.dataset.index = index;
  card.setAttribute('role', 'group');
  card.setAttribute('aria-labelledby', `custom-heading-${index}`);

  const heading = element('div', 'custom-channel-heading');
  const toggle = element('button', 'custom-channel-toggle');
  toggle.type = 'button';
  toggle.setAttribute('aria-expanded', open);
  toggle.setAttribute('aria-controls', `custom-body-${index}`);
  const title = element('strong', '', channel.name?.trim() || `Channel ${index + 1}`);
  title.id = `custom-heading-${index}`;
  const summary = element('span', 'custom-channel-summary', summaryText(channel));
  const badge = element('span', 'custom-badge', 'Duplicate');
  badge.hidden = true;
  toggle.append(element('span', 'custom-chevron'), element('span', 'custom-channel-number', String(index + 1)), title, summary, badge);
  toggle.onclick = () => {
    openIndex = open ? -1 : index;
    renderChannels();
    const next = $('#custom-channel-list').querySelector(`[data-index="${index}"]`);
    next?.scrollIntoView({block: 'nearest'});
    next?.querySelector('.custom-channel-toggle').focus();
  };

  const remove = element('button', 'text-button', 'Remove');
  remove.type = 'button';
  remove.onclick = () => {
    // Two clicks: the first arms the button so a stray click cannot delete a channel.
    if (removing?.button === remove) {
      clearTimeout(removing.timer);
      removing = null;
      channels.splice(index, 1);
      if (openIndex === index) openIndex = -1;
      else if (openIndex > index) openIndex--;
      renderChannels();
      return;
    }
    if (removing) { clearTimeout(removing.timer); removing.reset(); }
    remove.textContent = 'Click again to remove';
    remove.classList.add('armed');
    removing = {button: remove, reset: () => { remove.textContent = 'Remove'; remove.classList.remove('armed'); },
      timer: setTimeout(() => { removing.reset(); removing = null; }, 3000)};
  };
  heading.append(toggle, remove);
  card.append(heading);
  if (!open) return card;

  const body = element('div', 'custom-channel-body');
  body.id = `custom-body-${index}`;
  for (const [key, label, placeholder, type] of FIELDS) {
    const wrap = element('div', 'custom-channel-field');
    const caption = element('label', '', label);
    const input = element('input');
    input.type = type;
    input.id = `custom-channel-${index}-${key}`;
    caption.htmlFor = input.id;
    input.dataset.key = key;
    input.placeholder = placeholder;
    input.value = channel[key] || '';
    input.required = key === 'name' || key === 'url';
    input.oninput = () => {
      channel[key] = input.value;
      input.removeAttribute('aria-invalid');
      title.textContent = channel.name?.trim() || `Channel ${index + 1}`;
      summary.textContent = summaryText(channel);
      if (key === 'url') updateWarnings();
    };
    wrap.append(caption, input);
    body.append(wrap);
  }
  const warning = element('p', 'custom-warning');
  warning.hidden = true;
  body.append(warning);
  card.append(body);
  return card;
}

function renderChannels() {
  const query = $('#custom-filter').value.trim().toLocaleLowerCase();
  const visible = channels.map((channel, index) => [channel, index]).filter(([channel]) => matches(channel, query));
  $('#custom-channel-count').textContent = query
    ? `${number(visible.length)} of ${number(channels.length)} channels`
    : `${number(channels.length)} ${channels.length === 1 ? 'channel' : 'channels'}`;
  const list = $('#custom-channel-list');
  const scroll = list.scrollTop;
  list.replaceChildren(...visible.map(([channel, index]) => channelCard(channel, index)));
  if (!visible.length) list.append(element('p', 'content-empty', channels.length ? 'No channels match your search.' : 'No channels yet. Add a channel or paste an M3U list to start building your playlist.'));
  list.scrollTop = scroll;
  updateWarnings();
}

export async function openCustom(target) {
  if (app.busy) return;
  app.busy = true;
  try {
    const {content} = await request('/api/custom?source=' + encodeURIComponent(target.id));
    source = target;
    channels = parseCustom(content);
    initial = snapshot();
    openIndex = channels.length === 1 ? 0 : -1;
    $('#custom-source').textContent = target.name;
    $('#custom-error').hidden = true;
    $('#custom-filter').value = '';
    $('#custom-paste-area').hidden = true;
    renderChannels();
    $('#custom-dialog').showModal();
  } catch (error) {
    toast(error.message, 'error');
  } finally {
    app.busy = false;
  }
}

function addChannel() {
  $('#custom-filter').value = '';
  channels.push({name: '', url: '', group: '', id: '', logo: '', attrs: {}, extra: [], options: ''});
  openIndex = channels.length - 1;
  renderChannels();
  const card = $('#custom-channel-list').querySelector(`[data-index="${openIndex}"]`);
  card.scrollIntoView({block: 'nearest'});
  card.querySelector('input').focus();
}

function pasteChannels() {
  const added = parseCustom($('#custom-paste').value);
  if (!added.length) {
    $('#custom-error').textContent = 'No channels found. Paste M3U text with #EXTINF lines followed by stream URLs.';
    $('#custom-error').hidden = false;
    return;
  }
  $('#custom-error').hidden = true;
  openIndex = channels.length;
  channels.push(...added);
  $('#custom-paste').value = '';
  $('#custom-paste-area').hidden = true;
  renderChannels();
  toast(`${number(added.length)} ${added.length === 1 ? 'channel' : 'channels'} added`);
}

async function save(event) {
  event.preventDefault();
  if (app.busy) return;
  let content;
  try {
    content = serializeCustom(channels);
  } catch (error) {
    $('#custom-error').textContent = error.message;
    $('#custom-error').hidden = false;
    if (error instanceof ChannelError) {
      $('#custom-filter').value = '';
      openIndex = error.index;
      renderChannels();
      const card = $('#custom-channel-list').querySelector(`[data-index="${error.index}"]`);
      const key = channels[error.index].name?.trim() ? 'url' : 'name';
      const input = card?.querySelector(`input[data-key="${key}"]`);
      card?.scrollIntoView({block: 'nearest'});
      input?.setAttribute('aria-invalid', 'true');
      input?.focus();
    }
    return;
  }
  app.busy = true;
  try {
    const current = app.state.sources.find(item => item.id === source.id) ?? source;
    setState(await request('/api/sources/edit', {id: source.id, name: current.name, kind: 'custom', refresh_seconds: current.refresh_seconds, content}));
    $('#custom-dialog').close();
    toast('Playlist updated');
  } catch (error) {
    $('#custom-error').textContent = error.message;
    $('#custom-error').hidden = false;
  } finally {
    app.busy = false;
  }
}

export function initCustom() {
  closable($('#custom-dialog'), [$('#custom-close'), $('#custom-cancel')], () => snapshot() !== initial);
  $('#custom-add').onclick = addChannel;
  $('#custom-paste-toggle').onclick = () => { $('#custom-paste-area').hidden = !$('#custom-paste-area').hidden; if (!$('#custom-paste-area').hidden) $('#custom-paste').focus(); };
  $('#custom-paste-add').onclick = pasteChannels;
  $('#custom-filter').oninput = renderChannels;
  $('#custom-form').onsubmit = save;
}
