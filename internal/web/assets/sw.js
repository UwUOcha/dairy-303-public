// A versioned app shell only. API responses use the explicit, bounded device
// cache in data.mjs, with an offline label; they never masquerade as fresh.
const CACHE = "mp-shell-v26";
const FILES = [
  "/",
  "/app.css",
  "/theme.js",
  "/themes.mjs",
  "/stars.svg",
  "/app.js",
  "/model.mjs",
  "/config.mjs",
  "/profile.css",
  "/ux.mjs",
  "/changes.mjs",
  "/revisions.mjs",
  "/meetings.mjs",
  "/links.mjs",
  "/data.mjs",
  "/platform.mjs",
  "/demo.mjs",
  "/icon.svg",
  "/app-icon-192.png",
  "/app-icon-512.png",
  "/icon-192.png",
  "/icon-512.png",
  "/manifest.webmanifest",
  "/manifest-night.webmanifest",
  "/manifest-dark.webmanifest",
];
self.addEventListener("install", (event) =>
  // Activate only once every required file is stored. Old installed Android
  // clients may survive closing the PWA, otherwise pinning a worker forever.
  event.waitUntil(caches.open(CACHE).then((cache) => cache.addAll(FILES)).then(() => self.skipWaiting())),
);
self.addEventListener("activate", (event) =>
  event.waitUntil(
    caches
      .keys()
      .then((keys) =>
        Promise.all(
          keys
            .filter((k) => k.startsWith("mp-shell-") && k !== CACHE)
            .map((k) => caches.delete(k)),
        ),
      )
      .then(() => self.clients.claim()),
  ),
);
// Each installed shell is a complete version. Never overwrite individual
// modules in the active cache: that can mix new imports with old dependencies.
// A new version is precached before activation; pages reload on controller change.
self.addEventListener("fetch", (event) => {
  const url = new URL(event.request.url);
  if (event.request.method !== "GET" || url.origin !== self.location.origin ||
      !FILES.includes(url.pathname)) return;
  event.respondWith(caches.open(CACHE).then(async cache => {
    // Caddy varies responses by Accept-Encoding. Cache API bodies are already
    // decoded; the same shell bytes are valid for every compression variant.
    const hit = await cache.match(url.pathname, { ignoreVary: true });
    if (hit) return hit;
    return fetch(event.request);
  }));
});

// Identify the actual controller, rather than guessing from cache names: caches
// can belong to workers that installed successfully but never became active.
self.addEventListener("message", (event) => {
  if (event.data?.type === "MP_VERSION") event.ports?.[0]?.postMessage({ cache: CACHE });
});
