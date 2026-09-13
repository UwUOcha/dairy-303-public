// Closed beta uses network authorization on every navigation. Remove old public
// shell snapshots; previously downloaded content cannot be remotely unlearned.
self.addEventListener('install', event => event.waitUntil(self.skipWaiting()));
self.addEventListener('activate', event => event.waitUntil(caches.keys().then(keys => Promise.all(keys.filter(k => k.startsWith('mp-shell-')).map(k => caches.delete(k)))).then(() => self.clients.claim())));
self.addEventListener('message', event => { if(event.data?.type === 'MP_VERSION') event.ports?.[0]?.postMessage({cache:'mp-beta-network-v1'}); });
