// 1. Import WASM execution environment
// (You can use CDN or local path)
// const wasm_exec_URL = "https://cdn.jsdelivr.net/gh/golang/go@go1.19/misc/wasm/wasm_exec.js";
const wasm_exec_URL = "/wasm_exec.js";
importScripts(wasm_exec_URL);

// --- Global constants and variables ---

const wasm_URL = "/main.wasm";
// Path matching with importScripts
const CACHE_NAME = "WASM_Cache_v1";

// Promise to manage WASM loading state (prevents duplicate loading)
let wasmReadyPromise = null;

/**
 * Loads and executes Go WASM.
 */
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

/**
 * Wrapper function that ensures runWASM() is executed only once.
 * @returns {Promise<void>} Promise that resolves when WASM is ready
 */
function getWasmReady() {
    if (!wasmReadyPromise) {
        console.log("Service Worker: Starting WASM loading...");
        wasmReadyPromise = runWASM().catch(err => {
            console.error("Service Worker: WASM execution failed:", err);
            wasmReadyPromise = null; // Allow retry on next request if failed
            throw err; // Propagate error to caller (fetch handler)
        });
    }
    return wasmReadyPromise;
}


// --- 1. Install event listener ---
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
      await getWasmReady();
      console.log('Service Worker: Go WASM preloading complete.');
    })()
  );
});


// --- 3. Fetch event listener ---
// Pass all requests to Go handler.
self.addEventListener('fetch', (event) => {
  const url = new URL(event.request.url);
  console.log(`Service Worker: Forwarding request to Portal Proxy handler: ${url.pathname}`);
  
  event.respondWith((async () => {
    try {
      // Wait until WASM is ready
      await getWasmReady();

      if (typeof _portal_proxy !== 'undefined') {
        // WASM is ready and handler function exists
        const resp = await _portal_proxy(event.request);
        return resp;
      } else {
        // Abnormal situation where function doesn't exist even though getWasmReady() succeeded
        console.error("Service Worker: WASM loading succeeded but _portal_proxy is not defined.");
        return new Response("WASM handler is not available.", { status: 500 });
      }
    } catch (err) {
      // 1. getWasmReady() failure (WASM load/execution failure)
      // 2. _portal_http(event.request) failure (Go handler internal error)
      console.error(`Service Worker: Portal Proxy handler processing failed (falling back to network): ${err}`, event.request.url);
      
      // Fallback to network when WASM handler fails
      return fetch(event.request);
    }
  })());
});