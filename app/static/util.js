import {app} from './state.js';

export const $ = selector => document.querySelector(selector);
export const number = value => Number(value).toLocaleString();

export function element(tag, className, text) {
  const node = document.createElement(tag);
  if (className) node.className = className;
  if (text !== undefined) node.textContent = text;
  return node;
}

let toastTimer;
export function toast(message, kind = 'info') {
  $('#toast').textContent = message;
  $('#toast').classList.toggle('error', kind === 'error');
  $('#toast').hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { $('#toast').hidden = true; }, 4500);
}

export function since(timestamp) {
  if (!timestamp) return 'Not refreshed yet';
  const seconds = Math.max(0, Math.floor(Date.now() / 1000 - timestamp));
  if (seconds < 60) return 'Updated just now';
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `Updated ${minutes}m ago`;
  const hours = Math.floor(minutes / 60);
  return hours < 48 ? `Updated ${hours}h ago` : `Updated ${Math.floor(hours / 24)}d ago`;
}

// Close buttons and Esc share one path that asks before discarding unsaved edits.
export function closable(dialog, buttons, isDirty = () => false) {
  const attempt = () => {
    if (app.busy) return;
    if (isDirty() && !confirm('Discard your unsaved changes?')) return;
    dialog.close();
  };
  buttons.forEach(button => { button.onclick = attempt; });
  dialog.addEventListener('cancel', event => { event.preventDefault(); attempt(); });
}
