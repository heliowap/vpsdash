const CACHE_NAME = "vpsdash-static-3e055718191a";
const PRECACHE = ["/","/assets/ibm-plex-mono-latin-500-normal-CB9ihrfo.woff","/assets/ibm-plex-mono-latin-500-normal-DSY6xOcd.woff2","/assets/ibm-plex-sans-latin-400-normal-CDDApCn2.woff2","/assets/ibm-plex-sans-latin-400-normal-CYLoc0-x.woff","/assets/ibm-plex-sans-latin-500-normal-6ng42L7E.woff2","/assets/ibm-plex-sans-latin-500-normal-BgVn5rGT.woff","/assets/ibm-plex-sans-latin-600-normal-Cu4Hd6ag.woff","/assets/ibm-plex-sans-latin-600-normal-CuJfVYMP.woff2","/assets/ibm-plex-sans-latin-700-normal-Bth3BMcD.woff","/assets/ibm-plex-sans-latin-700-normal-Bxkt5Cjx.woff2","/assets/index-B5SvW3oF.css","/assets/index-DyPUTK5u.js","/icons/apple-touch-icon.png","/icons/vpsdash-192.png","/icons/vpsdash-512.png","/icons/vpsdash.svg","/manifest.webmanifest"];

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
