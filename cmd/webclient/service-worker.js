// const wasm_exec_URL = "https://cdn.jsdelivr.net/gh/golang/go@go1.25.3/misc/wasm/wasm_exec.js";
const wasm_exec_URL = "/wasm_exec.js";
const wasm_URL = "/main.wasm";
importScripts(wasm_exec_URL);

let loading = false;

async function init() {
    if (loading) return;
    loading = true;
    try {
        await runWASM();
    } catch (error) {
        console.error("Error initializing WASM:", error);
    }
    loading = false;
}

async function runWASM() {
    if (typeof __go_jshttp !== 'undefined') return;

    const go = new Go();
    const cache = await caches.open("WASM_Cache_v1");
    let wasm_file;
    const cache_wasm = await cache.match(wasm_URL);
    if (cache_wasm) {
        wasm_file = await cache_wasm.arrayBuffer();
    } else {
        wasm_file = await (await fetch(wasm_URL)).arrayBuffer();
    }
    const instance = await WebAssembly.instantiate(wasm_file, go.importObject);
    go.run(instance.instance);
}

self.addEventListener('install', (e) => {
    self.skipWaiting();
    async function LoadCache() {
        const cache = await caches.open("WASM_Cache_v1");
        await cache.addAll([
            wasm_URL,
            wasm_exec_URL,
        ]);
    }
    e.waitUntil(LoadCache());
});

self.addEventListener('activate', async (e) => {
    await init();
    await self.clients.claim();
});

self.addEventListener('fetch', async (e) => {
    console.log(e.request);
    const url = new URL(e.request.url);

    if (url.origin !== self.location.origin) {
        e.respondWith(fetch(e.request));
        return;
    }

    if (typeof __go_jshttp == 'undefined') {
        await init();
    }

    if (url.pathname === '/e8c2c70c-ec4a-40b2-b8af-d5638264f831') {
        if (typeof __go_jshttp == 'undefined') {
            e.respondWith(new Response("NAK-e8c2c70c-ec4a-40b2-b8af-d5638264f831", { status: 500 }))
            return;
        }
        e.respondWith(new Response("ACK-e8c2c70c-ec4a-40b2-b8af-d5638264f831", { status: 200 }));
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