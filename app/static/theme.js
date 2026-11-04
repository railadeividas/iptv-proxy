(() => {
  const key = 'iptv-theme';
  const choices = ['light', 'dark', 'system'];
  const system = window.matchMedia('(prefers-color-scheme: dark)');
  let choice = 'system';
  try {
    const saved = localStorage.getItem(key);
    if (choices.includes(saved)) choice = saved;
  } catch { /* Theme selection still works when browser storage is unavailable. */ }

  function apply() {
    document.documentElement.dataset.theme = choice === 'system' ? (system.matches ? 'dark' : 'light') : choice;
    document.querySelectorAll('[data-theme-choice]').forEach(button => {
      button.setAttribute('aria-pressed', String(button.dataset.themeChoice === choice));
    });
  }
  // Run before the stylesheet is loaded to avoid a flash of the wrong theme.
  apply();
  system.addEventListener('change', () => { if (choice === 'system') apply(); });
  window.addEventListener('storage', event => {
    if (event.key === key) { choice = choices.includes(event.newValue) ? event.newValue : 'system'; apply(); }
  });
  document.addEventListener('DOMContentLoaded', () => {
    document.querySelectorAll('[data-theme-choice]').forEach(button => {
      button.addEventListener('click', () => {
        choice = button.dataset.themeChoice;
        try { localStorage.setItem(key, choice); } catch { /* Session-only choice. */ }
        apply();
      });
    });
    apply();
  });
})();
