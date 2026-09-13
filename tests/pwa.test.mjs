import test from 'node:test';
import assert from 'node:assert/strict';
import vm from 'node:vm';
import { readFile } from 'node:fs/promises';
const assets = new URL('../internal/web/assets/', import.meta.url);
const origin = 'https://dairy.example';

async function workerHarness({ legacy = false, failPath = "" } = {}) {
  const listeners = {}, cachesByName = new Map();
  let online = true, fetches = 0, skipped = false, claimed = false;
  const network = async request => {
    fetches++;
    if (!online) throw new TypeError('offline');
    const path = new URL(typeof request === 'string' ? request : request.url, origin).pathname;
    if (path === failPath) throw new Error('Failed to precache ' + path);
    return new Response(await readFile(new URL(path === '/' ? 'index.html' : path.slice(1), assets)), {
      headers: { Vary: 'Accept-Encoding' },
    });
  };
  const caches = {
    async open(name) {
      if (!cachesByName.has(name)) {
        const entries = new Map();
        cachesByName.set(name, {
          async addAll(paths) { for (const path of paths) entries.set(path, { response: await network(path), encoding: 'gzip' }); },
          async match(path, options = {}) {
            const entry = entries.get(typeof path === 'string' ? path : new URL(path.url).pathname);
            // Compression request metadata may differ after a process restart.
            // Stored bodies have already been decompressed by Fetch.
            if (!entry || (!options.ignoreVary && entry.encoding !== (path.headers?.get('Accept-Encoding') || ''))) return;
            return entry.response.clone();
          },
          async put(path, response) { entries.set(path, { response: response.clone(), encoding: '' }); },
        });
      }
      return cachesByName.get(name);
    },
    async keys() { return [...cachesByName.keys()]; },
    async delete(name) { return cachesByName.delete(name); },
  };
  if (legacy) {
    await (await caches.open('mp-shell-v3')).put('/app.js', new Response('old app'));
    await (await caches.open('mp-shell-v19')).put('/app.js', new Response('waiting app'));
    await (await caches.open('unrelated-cache')).put('/other', new Response('keep'));
  }
  const source = await readFile(new URL('sw.js', assets), 'utf8');
  const createWorker = () => vm.runInNewContext(source, { URL, caches, fetch: network,
    self: { location: { origin }, skipWaiting: async () => { skipped = true; }, clients: { claim: async () => { claimed = true; } }, addEventListener: (name, fn) => listeners[name] = fn },
  });
  createWorker();
  let install;
  listeners.install({ waitUntil: p => install = p });
  let installError;
  try { await install; } catch (error) { installError = error; }
  return {
    installError,
    skipped: () => skipped,
    claimed: () => claimed,
    names: () => caches.keys(),
    async activate() {
      let activation;
      listeners.activate({ waitUntil: promise => activation = promise });
      await activation;
    },
    version() {
      let value;
      listeners.message({ data: { type: "MP_VERSION" }, ports: [{ postMessage: data => value = data.cache }] });
      return value;
    },
    restart() { createWorker(); },
    offline() { online = false; },
    fetches: () => fetches,
    cache: async () => caches.open((await caches.keys()).find(name => name === 'mp-shell-v31')),
    async get(path) {
      let result;
      listeners.fetch({ request: new Request(origin + path), respondWith: p => result = p });
      return result ? result : null;
    },
  };
}

test('cold worker serves HTML and the entire module graph offline despite compression Vary', async () => {
  const worker = await workerHarness();
  worker.offline();
  worker.restart(); // no running page, no in-memory modules or worker state
  const before = worker.fetches();
  assert.match(await (await worker.get('/?group=545&subgroup=707')).text(), /type="module"/);
  const queue = ['app.js', 'theme.js'], seen = new Set();
  while (queue.length) {
    const path = queue.pop();
    if (seen.has(path)) continue;
    seen.add(path);
    const response = await worker.get('/' + path);
    assert.ok(response, path + ' must be precached');
    const source = await response.text();
    for (const match of source.matchAll(/(?:from\s*|import\s*\()\s*["']\.\/([^"']+)["']/g)) queue.push(match[1]);
  }
  for (const path of ['/app.css', '/manifest.webmanifest', '/manifest-night.webmanifest', '/manifest-dark.webmanifest', '/icon-192.png']) assert.ok(await worker.get(path), path);
  assert.equal(worker.fetches(), before, 'offline startup never attempts network for shell files');
  assert.equal(await worker.get('/api/schedule/week?group=545'), null, 'API cache remains explicitly managed by the app');
});

test('an active shell never mixes freshly downloaded modules into the installed version', async () => {
  const worker = await workerHarness(), cache = await worker.cache();
  await cache.put('/app.js', new Response('installed version'));
  const before = worker.fetches();
  assert.equal(await (await worker.get('/app.js')).text(), 'installed version');
  assert.equal(worker.fetches(), before);
});

test('all install manifests share identity, and night/dark chrome matches their page theme', async () => {
  for (const [name, color] of [['manifest.webmanifest', '#f6f7f4'], ['manifest-night.webmanifest', '#121525'], ['manifest-dark.webmanifest', '#202620']]) {
    const manifest = JSON.parse(await readFile(new URL(name, assets), 'utf8'));
    assert.equal(manifest.id, '/');
    assert.equal(manifest.start_url, '/');
    assert.equal(manifest.theme_color, color);
    assert.equal(manifest.background_color, color);
  }
});

async function startupHarness({ hasBody = true, cacheError = false, controlled = true } = {}) {
  const events = {}, swEvents = {}, timers = [], saved = new Map([['mp.preferences.v1', '{"theme":"night"}']]);
  const element = tagName => ({ tagName, children: [], textContent: '', dataset: {}, append(...items) { this.children.push(...items); } });
  const boot = element('MAIN'), paragraph = element('P');
  boot.querySelector = () => paragraph;
  let body = hasBody;
  const root = element('HTML'), meta = {}, manifest = {};
  const window = { isSecureContext: true, addEventListener: (name, handler) => events[name] = handler };
  const document = {
    documentElement: root,
    querySelector: selector => ({ '.boot': body ? boot : null, 'meta[name="app-config"]': { content: '{"demo":false}' }, 'meta[name="theme-color"]': meta, 'link[rel="manifest"]': manifest })[selector],
    createElement: element,
    addEventListener: (name, handler) => events[name] = handler,
  };
  let reloads = 0;
  const context = { window, document, URL, navigator: { onLine: false, userAgent: 'Firefox test', serviceWorker: { controller: controlled ? { state: 'activated' } : null, register: async () => ({}), addEventListener: (name, handler) => swEvents[name] = handler } },
    localStorage: { getItem: key => saved.get(key) }, matchMedia: () => ({ matches: false }),
    caches: { keys: async () => { if (cacheError) throw new Error('Storage unavailable'); return []; } },
    setTimeout: handler => timers.push(handler), location: { reload() { reloads++; } },
  };
  new vm.Script(await readFile(new URL('theme.js', assets), 'utf8')).runInNewContext(context);
  const output = () => {
    const details = boot.children.find(el => el.tagName === 'details');
    return { message: paragraph.textContent, details: details?.children.find(el => el.tagName === 'pre')?.textContent || '' };
  };
  return { window, events, swEvents, navigator: context.navigator, reloads: () => reloads, timers, saved, boot, output, addBody() { body = true; events.DOMContentLoaded(); } };
}

test('startup exposes an import failure received before body parsing, without changing saved preferences', async () => {
  const h = await startupHarness({ hasBody: false });
  h.events.error({ target: { tagName: 'SCRIPT', src: 'https://dairy.example/app.js?secret=omit#private' } });
  assert.equal(h.boot.children.length, 0);
  h.addBody();
  await new Promise(resolve => setImmediate(resolve));
  assert.match(h.output().details, /Не загружен скрипт: \/app.js/);
  assert.match(h.output().details, /Этап: modules/);
  assert.match(h.output().details, /Кеш: отсутствует/);
  assert.doesNotMatch(h.output().details, /secret|private|https:/);
  assert.equal(h.saved.get('mp.preferences.v1'), '{"theme":"night"}');
  h.timers[0]();
  assert.equal(h.boot.children.length, 2, 'timeout cannot duplicate the retry and diagnostic controls');
});

test('startup distinguishes evaluation errors and storage failure from missing connectivity', async () => {
  const h = await startupHarness({ cacheError: true });
  h.window.mpBoot.stage = 'initializing';
  h.events.unhandledrejection({ reason: new TypeError('Unsupported startup API') });
  await new Promise(resolve => setImmediate(resolve));
  assert.match(h.output().details, /TypeError: Unsupported startup API/);
  assert.match(h.output().details, /Этап: initializing/);
  assert.match(h.output().details, /Storage unavailable/);
  assert.doesNotMatch(h.output().message, /Подключитесь к сети/);
});

test('startup diagnostics ignore errors after the application is ready', async () => {
  const h = await startupHarness();
  h.window.mpBoot.stage = 'ready';
  h.events.error({ target: h.window, message: 'Unrelated later error' });
  h.events.unhandledrejection({ reason: new Error('Unrelated later rejection') });
  assert.equal(h.boot.children.length, 0);
  assert.equal(h.window.mpBoot.errors.length, 0);
});


test('a fully installed update replaces a retained old controller and removes only old shell caches', async () => {
  const worker = await workerHarness({ legacy: true });
  assert.equal(worker.installError, undefined);
  assert.equal(worker.skipped(), true, 'old live clients must not pin the obsolete worker');
  assert.equal(worker.claimed(), false, 'installation alone does not claim clients');
  await worker.activate();
  assert.equal(worker.claimed(), true);
  assert.deepEqual((await worker.names()).sort(), ['mp-shell-v31', 'unrelated-cache']);
  assert.equal(worker.version(), 'mp-shell-v31');
  worker.offline();
  worker.restart();
  for (const path of ['/app.js', '/ux.mjs', '/revisions.mjs', '/icon-192.png', '/icon-512.png', '/manifest-night.webmanifest']) {
    assert.ok((await worker.get(path))?.ok, path + ' is available on a cold start after the upgrade');
  }
});

test('a failed shell install cannot take over from or delete the working offline version', async () => {
  const worker = await workerHarness({ legacy: true, failPath: '/ux.mjs' });
  assert.match(worker.installError.message, /Failed to precache/);
  assert.equal(worker.skipped(), false);
  assert.equal(worker.claimed(), false);
  assert.ok((await worker.names()).includes('mp-shell-v3'));
  assert.ok((await worker.names()).includes('mp-shell-v19'));
});

test('controller replacement reloads the page once; initial installation does not', async () => {
  const existing = await startupHarness();
  existing.swEvents.controllerchange();
  existing.swEvents.controllerchange();
  assert.equal(existing.reloads(), 1);
  const fresh = await startupHarness({ controlled: false });
  fresh.navigator.serviceWorker.controller = { state: 'activated' };
  fresh.swEvents.controllerchange();
  assert.equal(fresh.reloads(), 0);
  fresh.swEvents.controllerchange();
  assert.equal(fresh.reloads(), 1);
});

test('every theme offers explicit raster installation icons that match their declared PNG sizes', async () => {
  for (const name of ['manifest.webmanifest', 'manifest-night.webmanifest', 'manifest-dark.webmanifest']) {
    const manifest = JSON.parse(await readFile(new URL(name, assets), 'utf8'));
    assert.deepEqual(manifest.icons.map(icon => icon.sizes), ['192x192', '512x512']);
    for (const icon of manifest.icons) {
      assert.equal(icon.type, 'image/png');
      assert.match(icon.purpose, /\bany\b/);
      assert.ok(icon.src.startsWith('data:image/png;base64,'), 'the installer can decode the icon without a separate network request');
      const bytes = Buffer.from(icon.src.split(',')[1], 'base64');
      assert.deepEqual(bytes, await readFile(new URL('app-icon-' + icon.sizes.split('x')[0] + '.png', assets)));
      assert.equal(bytes[24], 8, 'use conventional 8-bit PNG for the native launcher decoder');
      assert.equal(bytes.subarray(1, 4).toString(), 'PNG');
      assert.equal(`${bytes.readUInt32BE(16)}x${bytes.readUInt32BE(20)}`, icon.sizes);
    }
  }
});

test('closed beta worker removes legacy shells and never serves cached pages', async () => {
  const handlers = {}, removed = [];
  let claimed = false, skipped = false;
  vm.runInNewContext(await readFile(new URL('beta-sw.js', assets), 'utf8'), {
    caches: { keys: async () => ['mp-shell-v31', 'unrelated-cache'], delete: async k => removed.push(k) },
    self: { skipWaiting: async () => { skipped = true; }, clients: { claim: async () => { claimed = true; } }, addEventListener: (name, fn) => handlers[name] = fn },
  });
  let work;
  handlers.install({ waitUntil: p => work = p }); await work;
  handlers.activate({ waitUntil: p => work = p }); await work;
  assert.equal(skipped, true); assert.equal(claimed, true);
  assert.deepEqual(removed, ['mp-shell-v31']); assert.equal(handlers.fetch, undefined);
});
