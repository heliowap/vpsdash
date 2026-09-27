import { createHash } from 'node:crypto'
import { readFileSync, readdirSync, writeFileSync } from 'node:fs'
import { dirname, join, relative, resolve, sep } from 'node:path'
import { fileURLToPath } from 'node:url'
import { Resvg } from '@resvg/resvg-js'

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const publicIcons = join(root, 'public', 'icons')
const dist = join(root, '..', 'internal', 'web', 'dist')

function renderIcons() {
  const svg = readFileSync(join(publicIcons, 'vpsdash.svg'))
  for (const [size, name] of [[192, 'vpsdash-192.png'], [512, 'vpsdash-512.png'], [180, 'apple-touch-icon.png']]) {
    const png = new Resvg(svg, { fitTo: { mode: 'width', value: size } }).render().asPng()
    writeFileSync(join(publicIcons, name), png)
  }
}

function filesIn(dir) {
  return readdirSync(dir, { withFileTypes: true }).flatMap(entry => {
    const path = join(dir, entry.name)
    return entry.isDirectory() ? filesIn(path) : [path]
  })
}

function renderWorker() {
  const files = filesIn(dist).filter(path => !['index.html', 'sw.js'].includes(relative(dist, path)))
  const precache = ['/', ...files.map(path => '/' + relative(dist, path).split(sep).join('/'))].sort()
  const hash = createHash('sha256')
  hash.update(readFileSync(join(dist, 'index.html')))
  for (const file of files) {
    hash.update(relative(dist, file))
    hash.update(readFileSync(file))
  }
  const cacheName = 'vpsdash-static-' + hash.digest('hex').slice(0, 12)
  writeFileSync(join(dist, 'sw.js'), `const CACHE_NAME = ${JSON.stringify(cacheName)};
const PRECACHE = ${JSON.stringify(precache)};

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
`)
}

if (process.argv[2] === 'icons') renderIcons()
else if (process.argv[2] === 'worker') renderWorker()
else throw new Error('usage: node scripts/pwa.mjs icons|worker')
