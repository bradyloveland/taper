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
    const ask = e.target.closest ? e.target.closest('[data-confirm-click]') : null;
    if (ask && !window.confirm(ask.getAttribute('data-confirm-click'))) {
      e.preventDefault();
      return;
    }
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

  // Selects that change the page straight away.
  document.querySelectorAll('select[data-autosubmit]').forEach((sel) => {
    sel.addEventListener('change', () => sel.form.submit());
  });

  // The event form shows only the fields that apply.
  const eventForm = document.querySelector('.event-form');
  if (eventForm) {
    const allDay = eventForm.querySelector('[data-allday]');
    const repeat = eventForm.querySelector('[data-repeat]');
    const cal = eventForm.querySelector('[data-closed-toggle]');
    const show = (els, on) => els.forEach((el) => { el.hidden = !on; });
    const update = () => {
      show(eventForm.querySelectorAll('.time-field'), !allDay.checked);
      show(eventForm.querySelectorAll('[data-weekly]'), repeat.value === 'weekly');
      show(eventForm.querySelectorAll('[data-repeating]'), repeat.value !== '');
      if (cal) show(eventForm.querySelectorAll('[data-closed-field]'), cal.value === 'school');
    };
    [allDay, repeat, cal].forEach((el) => el && el.addEventListener('change', update));
    update();
    // Moving the start date moves the end date with it, keeping the length.
    const startDate = eventForm.querySelector('#start_date');
    const endDate = eventForm.querySelector('#end_date');
    let previous = startDate.value;
    startDate.addEventListener('change', () => {
      const from = Date.parse(previous), to = Date.parse(startDate.value), end = Date.parse(endDate.value);
      if (!isNaN(from) && !isNaN(to) && !isNaN(end)) {
        endDate.value = new Date(end + (to - from)).toISOString().slice(0, 10);
      } else if (!isNaN(to)) {
        endDate.value = startDate.value;
      }
      previous = startDate.value;
    });
  }

  // Filter boxes for long lists of people.
  document.querySelectorAll('[data-filter]').forEach((box) => {
    const list = document.getElementById(box.getAttribute('data-filter'));
    if (!list) return;
    box.addEventListener('input', () => {
      const q = box.value.toLowerCase().trim();
      list.querySelectorAll('[data-search]').forEach((li) => {
        li.hidden = q !== '' && !li.getAttribute('data-search').includes(q);
      });
    });
  });

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
