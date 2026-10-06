// Small enhancements for Taper's pages. Every page works without this file.
'use strict';

(function () {
  if ('serviceWorker' in navigator && window.isSecureContext) {
    window.addEventListener('load', () => {
      navigator.serviceWorker.register('/sw.js').catch(() => {});
    });
  }

  // Forms with data-confirm ask before submitting.
  document.addEventListener('submit', (e) => {
    const msg = e.target.getAttribute && e.target.getAttribute('data-confirm');
    if (msg && !window.confirm(msg)) e.preventDefault();
  });

  document.addEventListener('click', (e) => {
    const t = e.target.closest ? e.target.closest('[data-copy],[data-print],[data-reload]') : null;
    if (t) {
      if (t.hasAttribute('data-copy')) {
        const src = document.getElementById(t.getAttribute('data-copy'));
        if (src && navigator.clipboard) {
          navigator.clipboard.writeText(src.textContent.trim()).then(() => {
            const old = t.textContent;
            t.textContent = 'Copied';
            setTimeout(() => { t.textContent = old; }, 1500);
          });
        }
      } else if (t.hasAttribute('data-print')) {
        window.print();
      } else if (t.hasAttribute('data-reload')) {
        e.preventDefault();
        window.location.reload();
      }
    }
    // Close the menu when tapping outside it.
    const toggle = document.getElementById('nav-toggle');
    if (toggle && toggle.checked && !e.target.closest('.nav, .nav-button, .nav-toggle')) toggle.checked = false;
  });

  document.addEventListener('keydown', (e) => {
    if (e.key === 'Escape') {
      const toggle = document.getElementById('nav-toggle');
      if (toggle) toggle.checked = false;
    }
  });

  // After installing an update, wait for the new version to answer, then go on.
  const wait = document.querySelector('[data-wait-version]');
  if (wait) {
    const target = wait.getAttribute('data-wait-version');
    const started = Date.now();
    let sawDown = false;
    const poll = () => {
      fetch('/healthz', { cache: 'no-store' })
        .then((r) => r.text())
        .then((t) => {
          if (t.trim() === 'ok ' + target && (sawDown || Date.now() - started > 8000)) {
            window.location.href = '/admin/updates';
            return;
          }
          setTimeout(poll, 2000);
        })
        .catch(() => { sawDown = true; setTimeout(poll, 2000); });
      if (Date.now() - started > 120000) {
        const slow = wait.querySelector('[data-wait-slow]');
        if (slow) slow.hidden = false;
      }
    };
    setTimeout(poll, 2500);
  }

  // Suggest a username from the name while adding a person.
  const name = document.querySelector('[data-username-source]');
  const user = document.querySelector('[data-username-target]');
  if (name && user) {
    let touched = user.value !== '';
    user.addEventListener('input', () => { touched = true; });
    name.addEventListener('input', () => {
      if (touched) return;
      user.value = name.value
        .normalize('NFD').replace(/[̀-ͯ]/g, '')
        .toLowerCase().trim()
        .replace(/[^a-z0-9]+/g, '.')
        .replace(/^\.+|\.+$/g, '')
        .slice(0, 40);
    });
  }
})();
