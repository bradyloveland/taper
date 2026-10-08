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
    // Close the menus when tapping outside them.
    const toggle = document.getElementById('nav-toggle');
    if (toggle && toggle.checked && !e.target.closest('.nav, .nav-button, .nav-toggle')) toggle.checked = false;
    const menu = document.querySelector('[data-menu]');
    if (menu && menu.open && !e.target.closest('[data-menu]')) menu.open = false;
  });

  document.addEventListener('keydown', (e) => {
    if (e.key === 'Escape') {
      const toggle = document.getElementById('nav-toggle');
      if (toggle) toggle.checked = false;
      const menu = document.querySelector('[data-menu]');
      if (menu && menu.open) { menu.open = false; menu.querySelector('summary').focus(); }
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

  // Opening one header menu closes the other.
  const acct = document.querySelector('[data-menu]');
  const navToggle = document.getElementById('nav-toggle');
  if (acct && navToggle) {
    acct.addEventListener('toggle', () => { if (acct.open) navToggle.checked = false; });
    navToggle.addEventListener('change', () => { if (navToggle.checked) acct.open = false; });
  }

  // On narrow screens, scroll the settings tabs so the current one shows.
  const tabs = document.querySelector('.admin-tabs');
  const tab = tabs && tabs.querySelector('[aria-current="page"]');
  if (tab && tabs.scrollWidth > tabs.clientWidth) {
    tabs.scrollLeft = tab.offsetLeft - (tabs.clientWidth - tab.offsetWidth) / 2;
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
    // Moving the start moves the end with it, keeping the event's length
    // (a 9–10 AM event moved to 6 PM becomes 6–7 PM).
    const sd = eventForm.querySelector('#start_date'), st = eventForm.querySelector('#start_time');
    const ed = eventForm.querySelector('#end_date'), et = eventForm.querySelector('#end_time');
    const pad = (n) => String(n).padStart(2, '0');
    const at = (d, t) => (d ? new Date(d + 'T' + (allDay.checked || !t ? '00:00' : t)) : null);
    const ok = (x) => x && !isNaN(x);
    const day = (x) => x.getFullYear() + '-' + pad(x.getMonth() + 1) + '-' + pad(x.getDate());
    const clock = (x) => pad(x.getHours()) + ':' + pad(x.getMinutes());
    let before = at(sd.value, st.value);
    const follow = () => {
      const start = at(sd.value, st.value), end = at(ed.value, et.value);
      if (ok(start) && ok(before)) {
        let length = ok(end) ? end - before : 0;
        if (!(length > 0) && !allDay.checked) length = 60 * 60 * 1000;
        if (length < 0) length = 0;
        const moved = new Date(start.getTime() + length);
        ed.value = day(moved);
        if (!allDay.checked) et.value = clock(moved);
      }
      before = start;
      check();
    };
    // An end before the start can't be saved; say so on the field itself.
    const check = () => {
      const start = at(sd.value, st.value), end = at(ed.value, et.value);
      let msg = '';
      if (ok(start) && ok(end)) {
        if (allDay.checked ? end < start : end <= start) {
          msg = allDay.checked
            ? 'The last day is before the first day.'
            : 'Event end time is before it starts. Choose a later end time, or a later end date if it goes past midnight.';
        }
      }
      const field = allDay.checked ? ed : et;
      [ed, et].forEach((f) => { f.setCustomValidity(''); f.classList.remove('invalid'); f.removeAttribute('aria-invalid'); });
      if (msg) {
        field.setCustomValidity(msg);
        field.classList.add('invalid');
        field.setAttribute('aria-invalid', 'true');
      }
    };
    sd.addEventListener('change', follow);
    st.addEventListener('change', follow);
    [ed, et, allDay].forEach((f) => f.addEventListener('change', check));
    [ed, et].forEach((f) => f.addEventListener('input', check));
    check();
  }

  // Assignment form: the publish day and time show only for "Later".
  const publish = document.querySelector('[data-publish]');
  if (publish) {
    const later = publish.querySelector('[data-publish-later]');
    const update = () => {
      const on = publish.querySelector('input[name=publish]:checked');
      later.hidden = !(on && on.value === 'later');
    };
    publish.addEventListener('change', update);
    update();
  }

  // Scholars' writing saves itself a few seconds after they stop typing,
  // and at least every 20 seconds while they type.
  const work = document.querySelector('form[data-autosave]');
  if (work) {
    const box = work.querySelector('textarea[name=body]');
    const status = work.querySelector('[data-save-status]');
    const url = work.getAttribute('data-autosave');
    let saved = box.value, timer = null, first = 0, busy = false;
    const say = (t) => { if (status) status.textContent = t; };
    const payload = () => {
      const fd = new FormData();
      fd.append('csrf', work.querySelector('input[name=csrf]').value);
      fd.append('body', box.value);
      return fd;
    };
    const save = () => {
      clearTimeout(timer);
      timer = null;
      first = 0;
      if (busy || box.value === saved) return;
      busy = true;
      const text = box.value;
      say('Saving…');
      fetch(url, { method: 'POST', body: payload(), credentials: 'same-origin' })
        .then((r) => r.json().then((j) => ({ ok: r.ok, j })))
        .then(({ ok, j }) => {
          if (ok) { saved = text; say('Saved at ' + j.saved + '. Not turned in yet.'); }
          else say('Not saved: ' + (j.error || 'try the Save button.'));
        })
        .catch(() => say('Not saved: you may be offline. Keep this page open and try again.'))
        .finally(() => { busy = false; if (box.value !== saved && !timer) timer = setTimeout(save, 3000); });
    };
    box.addEventListener('input', () => {
      say('');
      const now = Date.now();
      if (!first) first = now;
      clearTimeout(timer);
      timer = setTimeout(save, now - first > 20000 ? 0 : 3000);
    });
    work.addEventListener('submit', () => { clearTimeout(timer); saved = box.value; });
    window.addEventListener('pagehide', () => {
      if (box.value !== saved && navigator.sendBeacon) navigator.sendBeacon(url, payload());
    });
  }

  // ------------------------------------------------------------------ chat

  const badge = document.querySelector('[data-chat-badge]');
  const dot = document.querySelector('[data-chat-dot]');
  const baseTitle = document.title;
  const showUnread = (n) => {
    if (badge) { badge.textContent = n; badge.hidden = !(n > 0); badge.setAttribute('aria-label', n + ' unread'); }
    if (dot) dot.hidden = !(n > 0);
  };

  const chat = document.querySelector('[data-chat]');
  if (chat) {
    const id = chat.getAttribute('data-chat');
    const me = chat.getAttribute('data-me');
    const log = chat.querySelector('#chat-log');
    const scroller = chat.querySelector('[data-chat-scroll]');
    const jump = chat.querySelector('[data-chat-jump]');
    const form = chat.querySelector('[data-chat-form]');
    const box = form && form.querySelector('textarea');
    const errBox = form && form.querySelector('[data-chat-error]');
    const csrf = document.querySelector('input[name=csrf]');
    const history = chat.hasAttribute('data-history');
    let last = Number(chat.getAttribute('data-last')) || 0;
    let hiddenNew = 0;

    const nearBottom = () => scroller.scrollHeight - scroller.scrollTop - scroller.clientHeight < 80;
    const toBottom = () => { scroller.scrollTop = scroller.scrollHeight; jump.hidden = true; };
    if (!history && !location.hash) toBottom();
    if (location.hash) { const t = document.querySelector(location.hash); if (t) t.scrollIntoView({ block: 'center' }); }
    scroller.addEventListener('scroll', () => { if (nearBottom()) jump.hidden = true; });
    jump.addEventListener('click', toBottom);

    // Server HTML for one message, parsed into an element.
    const parse = (html) => {
      const t = document.createElement('template');
      t.innerHTML = html.trim();
      return t.content.firstElementChild;
    };
    // Put a message in the log: replace it if it's there, otherwise add it
    // with a day divider and grouping like the server does.
    const place = (el) => {
      const old = document.getElementById(el.id);
      if (old) { el.className = old.className.includes('cont') && !el.classList.contains('removed') ? el.className + ' cont' : el.className; old.replaceWith(el); return false; }
      const msgs = log.querySelectorAll('.msg');
      const prev = msgs[msgs.length - 1];
      if (!prev || prev.dataset.day !== el.dataset.day) {
        const li = document.createElement('li');
        li.className = 'chat-day';
        const span = document.createElement('span');
        span.textContent = el.dataset.dayLabel;
        li.appendChild(span);
        log.appendChild(li);
      } else if (prev.dataset.author === el.dataset.author && !prev.classList.contains('removed') &&
                 !el.classList.contains('removed') && Number(el.dataset.at) - Number(prev.dataset.at) < 300) {
        el.classList.add('cont');
      }
      log.appendChild(el);
      const empty = chat.querySelector('[data-chat-empty]');
      if (empty) empty.remove();
      return true;
    };

    let readTimer = null;
    const readForm = () => {
      const fd = new FormData();
      fd.append('csrf', csrf ? csrf.value : '');
      fd.append('last', String(last));
      return fd;
    };
    const markRead = () => {
      clearTimeout(readTimer);
      readTimer = setTimeout(() => {
        readTimer = null;
        if (document.hidden || !last) return;
        fetch('/chat/' + id + '/read', { method: 'POST', body: readForm(), credentials: 'same-origin', headers: { Accept: 'application/json' } })
          .then((r) => r.json()).then((j) => showUnread(j.total)).catch(() => {});
      }, 400);
    };
    // Leaving straight after a message arrived: still count it as read.
    window.addEventListener('pagehide', () => {
      if (readTimer && navigator.sendBeacon) { clearTimeout(readTimer); navigator.sendBeacon('/chat/' + id + '/read', readForm()); }
    });
    document.addEventListener('visibilitychange', () => {
      if (!document.hidden) { hiddenNew = 0; document.title = baseTitle; markRead(); }
    });

    const receive = (data) => {
      const el = parse(data.html);
      if (!el) return;
      const stick = nearBottom() || data.mine;
      const added = place(el);
      if (added) {
        last = Math.max(last, data.id);
        if (!data.mine) {
          el.classList.add('flash-new');
          if (document.hidden) { hiddenNew++; document.title = '(' + hiddenNew + ') ' + baseTitle; }
        }
        if (stick) toBottom(); else if (!data.mine) jump.hidden = false;
        markRead();
      }
    };

    if (!history && window.EventSource) {
      const es = new EventSource('/chat/' + id + '/events?after=' + last);
      es.addEventListener('message', (e) => { try { receive(JSON.parse(e.data)); } catch (_) {} });
      window.addEventListener('pagehide', () => es.close());
    }

    if (form) {
      const grow = () => { box.style.height = 'auto'; box.style.height = Math.min(box.scrollHeight, window.innerHeight * 0.4) + 'px'; };
      box.addEventListener('input', grow);
      // Enter sends; Shift+Enter adds a line. On touchscreens Enter adds a line.
      box.addEventListener('keydown', (e) => {
        if (e.key === 'Enter' && !e.shiftKey && !e.isComposing && !window.matchMedia('(pointer: coarse)').matches) {
          e.preventDefault();
          form.requestSubmit();
        }
      });
      let sending = false;
      form.addEventListener('submit', (e) => {
        e.preventDefault();
        if (sending || !box.value.trim()) return;
        sending = true;
        const fd = new FormData(form);
        fetch(form.action, { method: 'POST', body: fd, credentials: 'same-origin', headers: { Accept: 'application/json' } })
          .then((r) => r.json().then((j) => ({ ok: r.ok, j })))
          .then(({ ok, j }) => {
            if (!ok) { errBox.textContent = j.error || 'Not sent. Try again.'; errBox.hidden = false; return; }
            errBox.hidden = true;
            box.value = '';
            grow();
            receive({ id: j.id, html: j.html, mine: true });
          })
          .catch(() => { errBox.textContent = "Not sent: you may be offline. Your message is still in the box; try again."; errBox.hidden = false; })
          .finally(() => { sending = false; box.focus(); });
      });
    }

    // Removing a message updates it in place.
    chat.addEventListener('submit', (e) => {
      const f = e.target;
      if (e.defaultPrevented || !f.hasAttribute('data-chat-action')) return;
      e.preventDefault();
      fetch(f.action, { method: 'POST', body: new FormData(f), credentials: 'same-origin', headers: { Accept: 'application/json' } })
        .then((r) => r.json())
        .then((j) => { if (j.html) place(parse(j.html)); })
        .catch(() => f.submit());
    });
    // Close message menus when tapping elsewhere.
    document.addEventListener('click', (e) => {
      document.querySelectorAll('.msg-menu[open], .chat-tools[open]').forEach((d) => { if (!d.contains(e.target)) d.open = false; });
    });
  } else if (badge) {
    // Elsewhere, check for new messages now and then.
    const poll = () => {
      if (document.hidden) return;
      fetch('/chat/unread', { credentials: 'same-origin', headers: { Accept: 'application/json' } })
        .then((r) => (r.ok ? r.json() : null)).then((j) => { if (j) showUnread(j.total); }).catch(() => {});
    };
    setInterval(poll, 60000);
    document.addEventListener('visibilitychange', poll);
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
