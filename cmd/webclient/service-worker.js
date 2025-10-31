// const wasm_exec_URL = "https://cdn.jsdelivr.net/gh/golang/go@go1.25.3/misc/wasm/wasm_exec.js";
const wasm_exec_URL = "/wasm_exec.js";
importScripts(wasm_exec_URL);

let _portal_proxy;
const wasm_URL = "/main.wasm";
const CACHE_NAME = "WASM_Cache_v1";

async function runWASM() {
  const go = new Go();
  const cache = await caches.open(CACHE_NAME);
  let wasm_file;

  const cache_wasm = await cache.match(wasm_URL);

  if (cache_wasm) {
    console.log("Service Worker: Loading WASM from cache...");
    wasm_file = await cache_wasm.arrayBuffer();
  } else {
    console.warn("Service Worker: WASM not in cache. Fetching from network...");
    const resp = await fetch(wasm_URL);
    wasm_file = await resp.arrayBuffer();
    await cache.put(wasm_URL, new Response(wasm_file.slice(0)));
  }

  console.log("Service Worker: Instantiating WebAssembly...");
  const { instance } = await WebAssembly.instantiate(wasm_file, go.importObject);

  // go.run() executes Go's main() and returns
  // when _portal_proxy callback is registered
  go.run(instance);
  console.log("Service Worker: Go WASM execution complete. _portal_proxy is ready.");
}

self.addEventListener('install', (event) => {
  console.log('Service Worker: Installing...');

  event.waitUntil(
    (async () => {
      const cache = await caches.open(CACHE_NAME);
      console.log('Service Worker: Caching essential assets...');
      await cache.addAll([
        wasm_URL,
        wasm_exec_URL,
      ]);
      await self.skipWaiting();
    })()
  );
});

// --- 2. Activate event listener ---
self.addEventListener('activate', (event) => {
  console.log('Service Worker: Activated.');

  event.waitUntil(
    (async () => {
      await self.clients.claim();
      // Preload WASM to prepare for next fetch requests
      console.log('Service Worker: Starting Go WASM preloading...');
      await runWASM();
      console.log('Service Worker: Go WASM preloading complete.');
    })()
  );
});

async function proxy_handler(event) {
  console.log('Service Worker: Fetch event:', event.request);
  if (typeof _portal_proxy != 'undefined') {
    event.respondWith((async () => {
      try {
        const resp = await _portal_proxy(event.request);
        return resp;
      } catch {
        _portal_proxy = undefined;
        await runWASM();
        const resp = await _portal_proxy(event.request);
        return resp;
      }
    })());
    return;
  }

  console.log('Service Worker: _portal_proxy is not defined. Fetch event ignored.');
  event.respondWith(fetch(event.request));
}

self.addEventListener('fetch', proxy_handler);
