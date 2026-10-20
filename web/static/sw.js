// Taper service worker. The server fills in the cache name and the files to keep.
//
// Pages always come from the network, because they hold personal,
// up-to-the-minute information. Only when there's no connection does the
// worker answer with the offline page. Static files carry a content hash in
// their address, so they're served from the cache once fetched.
'use strict';

const CACHE = '__CACHE__';
const PRECACHE = __PRECACHE__.concat(['/offline']);

self.addEventListener('install', (event) => {
  event.waitUntil(
    caches.open(CACHE).then((cache) => cache.addAll(PRECACHE)).then(() => self.skipWaiting())
  );
});

self.addEventListener('activate', (event) => {
  event.waitUntil(
    caches.keys()
      .then((keys) => Promise.all(keys.filter((k) => k.startsWith('taper-') && k !== CACHE).map((k) => caches.delete(k))))
      .then(() => self.clients.claim())
  );
});

self.addEventListener('fetch', (event) => {
  const req = event.request;
  if (req.method !== 'GET') return;
  const url = new URL(req.url);
  if (url.origin !== self.location.origin) return;

  if (req.mode === 'navigate') {
    event.respondWith(fetch(req).catch(() => caches.match('/offline')));
    return;
  }
  if (url.pathname.startsWith('/static/') && url.searchParams.has('v')) {
    event.respondWith(
      caches.open(CACHE).then((cache) =>
        cache.match(req).then((hit) => hit || fetch(req).then((res) => {
          if (res.ok) cache.put(req, res.clone());
          return res;
        }))
      )
    );
  }
});

// ------------------------------------------------------------- notifications

// A push from Taper: show it, replacing any earlier one about the same thing
// (like "3 new messages in Latin I"), and update the badge on the app's icon.
self.addEventListener('push', (event) => {
  let d = {};
  try { d = event.data ? event.data.json() : {}; } catch (e) { d = { body: event.data && event.data.text() }; }
  const shown = self.registration.showNotification(d.title || 'Taper', {
    body: d.body || '',
    tag: d.tag || undefined,
    icon: d.icon || undefined,
    data: { url: d.url || '/notifications' },
  });
  const badge = (typeof d.unread === 'number' && self.navigator.setAppBadge)
    ? (d.unread > 0 ? self.navigator.setAppBadge(d.unread) : self.navigator.clearAppBadge()).catch(() => {})
    : Promise.resolve();
  event.waitUntil(Promise.all([shown, badge]));
});

// Tapping a notification opens what it's about, in an open Taper window if
// there is one.
self.addEventListener('notificationclick', (event) => {
  event.notification.close();
  const target = new URL((event.notification.data && event.notification.data.url) || '/notifications', self.location.origin);
  if (target.origin !== self.location.origin) return;
  event.waitUntil(
    self.clients.matchAll({ type: 'window', includeUncontrolled: true }).then((list) => {
      for (const c of list) {
        if (new URL(c.url).origin === self.location.origin && 'navigate' in c) {
          return c.focus().then(() => c.navigate(target.href));
        }
      }
      return self.clients.openWindow(target.href);
    })
  );
});
