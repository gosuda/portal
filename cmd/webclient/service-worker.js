// const wasm_exec_URL = "https://cdn.jsdelivr.net/gh/golang/go@go1.25.3/lib/wasm/wasm_exec.js";
const wasm_exec_URL = "/wasm_exec.js";
const manifest_URL = "/manifest.json";

importScripts(wasm_exec_URL);

let loading = false;
let initError = null;
let wasmManifest = null;
let currentCacheVersion = null;

// Fetch manifest to get current WASM filename
async function fetchManifest() {
    if (wasmManifest) return wasmManifest;

    try {
        const response = await fetch(manifest_URL);
        if (!response.ok) {
            throw new Error(`Failed to fetch manifest: ${response.status}`);
        }
        wasmManifest = await response.json();
        currentCacheVersion = `WASM_Cache_${wasmManifest.hash}`;
        return wasmManifest;
    } catch (error) {
        console.error('[SW] Failed to fetch manifest:', error);
        throw error;
    }
}

// Send error to all clients
async function notifyClientsOfError(error) {
    const clients = await self.clients.matchAll();
    const errorMessage = {
        type: 'SW_ERROR',
        error: {
            name: error.name,
            message: error.message,
            stack: error.stack
        }
    };

    for (const client of clients) {
        client.postMessage(errorMessage);
    }
}

async function init() {
    if (loading) return;
    loading = true;
    try {
        await runWASM();
        initError = null;
    } catch (error) {
        console.error("[SW] Error initializing WASM:", error);
        initError = error;
        await notifyClientsOfError(error);
        throw error; // Re-throw to prevent further processing
    } finally {
        loading = false;
    }
}

async function runWASM() {
    if (typeof __go_jshttp !== 'undefined') {
        return;
    }

    try {
        const manifest = await fetchManifest();
        // Use content-addressed path: /static/<sha256>.wasm
        const wasm_URL = `/static/${manifest.wasmFile}`;

        const go = new Go();

        const cache = await caches.open(currentCacheVersion);

        let wasm_file;
        const cache_wasm = await cache.match(wasm_URL);

        if (cache_wasm) {
            wasm_file = await cache_wasm.arrayBuffer();
        } else {
            const response = await fetch(wasm_URL);
            if (!response.ok) {
                throw new Error(`Failed to fetch WASM: ${response.status} ${response.statusText}`);
            }
            wasm_file = await response.arrayBuffer();
        }

        const instance = await WebAssembly.instantiate(wasm_file, go.importObject);

        go.run(instance.instance);

    } catch (error) {
        console.error('[SW] WASM initialization failed:', error);
        throw new Error(`WASM Initialization: ${error.message}`);
    }
}

self.addEventListener('install', (e) => {
    self.skipWaiting();
    async function LoadCache() {
        try {
            const manifest = await fetchManifest();
            // Use content-addressed path: /static/<sha256>.wasm
            const wasm_URL = `/static/${manifest.wasmFile}`;
            const cache = await caches.open(currentCacheVersion);

            await cache.addAll([
                wasm_URL,
                wasm_exec_URL,
                manifest_URL,
            ]);
        } catch (error) {
            console.error('[SW] Cache loading failed:', error);
            throw new Error(`Cache Loading: ${error.message}`);
        }
    }
    e.waitUntil(LoadCache());
});

self.addEventListener('activate', (e) => {
    e.waitUntil((async () => {
        try {
            // Delete old caches
            const manifest = await fetchManifest();
            const cacheNames = await caches.keys();
            await Promise.all(
                cacheNames.map(cacheName => {
                    if (cacheName !== currentCacheVersion && cacheName.startsWith('WASM_Cache_')) {
                        console.log('[SW] Deleting old cache:', cacheName);
                        return caches.delete(cacheName);
                    }
                })
            );

            // Claim clients first to take control immediately
            await self.clients.claim();

            // Then initialize WASM in background (don't block activation)
            init().catch(error => {
                console.error('[SW] WASM initialization failed after activation:', error);
                notifyClientsOfError(error);
            });

        } catch (error) {
            console.error('[SW] Activation failed:', error);
            await notifyClientsOfError(error);
        }
    })());
});

self.addEventListener('fetch', (e) => {
    const url = new URL(e.request.url);

    // Skip non-origin requests
    if (url.origin !== self.location.origin) {
        e.respondWith(fetch(e.request));
        return;
    }

    // Health check endpoint - check WASM status
    if (url.pathname === '/e8c2c70c-ec4a-40b2-b8af-d5638264f831') {
        e.respondWith((async () => {

            // Try to initialize if not ready
            if (typeof __go_jshttp === 'undefined' && !loading) {
                try {
                    await init();
                } catch (error) {
                    console.error('[SW] Health check init failed:', error);
                }
            }

            // Return status based on WASM availability
            if (typeof __go_jshttp !== 'undefined') {
                return new Response("ACK-e8c2c70c-ec4a-40b2-b8af-d5638264f831", { status: 200 });
            } else {
                return new Response("NAK-e8c2c70c-ec4a-40b2-b8af-d5638264f831", { status: 503 });
            }
        })());
        return;
    }

    // Serve portal.mp4 from cache or fetch from origin
    if (url.pathname === '/portal.mp4') {
        e.respondWith((async () => {
            try {
                await fetchManifest();
                // Try to get from cache first
                const cache = await caches.open(currentCacheVersion);
                const cachedResponse = await cache.match('/portal.mp4');
                if (cachedResponse) {
                    return cachedResponse;
                }

                // Fetch from network and cache it
                const response = await fetch(e.request);
                if (response.ok) {
                    cache.put('/portal.mp4', response.clone());
                }
                return response;
            } catch (error) {
                console.error('Failed to fetch portal.mp4:', error);
                return new Response('Not Found', { status: 404 });
            }
        })());
        return;
    }

    if (__go_jshttp) {
        e.respondWith((async () => {
            try {
                const resp = await __go_jshttp(e.request);
                return resp;
            } catch {
                __go_jshttp = undefined;
                await init();
                const resp = await __go_jshttp(e.request);
                return resp;
            }
        })());
        return;
    }

    e.respondWith(new Response("Sorry, Service Worker failed to process the request. Please refresh the page."));
});