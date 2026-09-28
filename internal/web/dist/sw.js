const CACHE_NAME = "vpsdash-static-8d5e5e6adbed";
const PRECACHE = ["/","/assets/addon-fit-DIOBYJe3.js","/assets/ibm-plex-mono-latin-500-normal-CB9ihrfo.woff","/assets/ibm-plex-mono-latin-500-normal-DSY6xOcd.woff2","/assets/ibm-plex-sans-latin-400-normal-CDDApCn2.woff2","/assets/ibm-plex-sans-latin-400-normal-CYLoc0-x.woff","/assets/ibm-plex-sans-latin-500-normal-6ng42L7E.woff2","/assets/ibm-plex-sans-latin-500-normal-BgVn5rGT.woff","/assets/ibm-plex-sans-latin-600-normal-Cu4Hd6ag.woff","/assets/ibm-plex-sans-latin-600-normal-CuJfVYMP.woff2","/assets/ibm-plex-sans-latin-700-normal-Bth3BMcD.woff","/assets/ibm-plex-sans-latin-700-normal-Bxkt5Cjx.woff2","/assets/index-B7wnH8IZ.js","/assets/index-BwlOxufD.css","/assets/xterm-ScmBfeDI.js","/icons/apple-touch-icon.png","/icons/vpsdash-192.png","/icons/vpsdash-512.png","/icons/vpsdash.svg","/manifest.webmanifest"];

self.addEventListener('install', event => {
  event.waitUntil(caches.open(CACHE_NAME).then(cache => cache.addAll(PRECACHE)).then(() => self.skipWaiting()));
});

self.addEventListener('activate', event => {
  event.waitUntil(caches.keys().then(names => Promise.all(names.filter(name => name.startsWith('vpsdash-static-') && name !== CACHE_NAME).map(name => caches.delete(name)))).then(() => self.clients.claim()));
});

self.addEventListener('fetch', event => {
  const request = event.request;
  const url = new URL(request.url);
  if (request.method !== 'GET' || url.origin !== self.location.origin || url.pathname.startsWith('/api/') || url.pathname === '/healthz') return;

  if (request.mode === 'navigate') {
    event.respondWith(fetch(request).then(response => {
      if (response.ok && response.headers.get('Content-Type')?.includes('text/html')) {
        const copy = response.clone();
        event.waitUntil(caches.open(CACHE_NAME).then(cache => cache.put('/', copy)));
      }
      return response;
    }).catch(() => caches.match('/')));
    return;
  }

  if (!PRECACHE.includes(url.pathname)) return;
  event.respondWith(caches.match(url.pathname).then(cached => cached || fetch(request)));
});

function sameOriginURL(value) {
  try {
    const url = new URL(value || '/', self.location.origin);
    return url.origin === self.location.origin ? url.pathname + url.hash : '/';
  } catch {
    return '/';
  }
}

self.addEventListener('push', event => {
  let message = {};
  try {
    message = event.data ? event.data.json() : {};
  } catch {
    message = { body: event.data ? event.data.text() : '' };
  }
  const title = typeof message.title === 'string' && message.title ? message.title : 'vpsdash';
  event.waitUntil(self.registration.showNotification(title, {
    body: typeof message.body === 'string' ? message.body : '',
    tag: typeof message.tag === 'string' ? message.tag : undefined,
    icon: '/icons/vpsdash-192.png',
    badge: '/icons/vpsdash-192.png',
    lang: 'pt-BR',
    data: { url: sameOriginURL(message.url) }
  }));
});

self.addEventListener('notificationclick', event => {
  event.notification.close();
  const target = sameOriginURL(event.notification.data && event.notification.data.url);
  event.waitUntil(self.clients.matchAll({ type: 'window', includeUncontrolled: true }).then(windows => {
    const open = windows.find(client => new URL(client.url).origin === self.location.origin);
    if (open) {
      open.postMessage({ type: 'vpsdash:navigate', url: target });
      return open.focus();
    }
    return self.clients.openWindow(target);
  }));
});
