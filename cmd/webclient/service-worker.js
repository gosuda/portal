// const wasm_exec_URL = "https://cdn.jsdelivr.net/gh/golang/go@go1.25.3/misc/wasm/wasm_exec.js";
global = {};
const wasm_exec_URL = "/wasm_exec.js";
const wasm_URL = "/main.wasm";
importScripts(wasm_exec_URL);


async function runWASM() {
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

self.addEventListener('activate', (e) => {
    async function Activate() {
        await runWASM();
    }
    e.waitUntil(Activate());
});

self.addEventListener('fetch', async (e) => {
    console.log(e.request);

    if (typeof __go_jshttp == 'undefined') {
        await runWASM();
    }

    if (__go_jshttp) {
        e.respondWith((async () => {    
            try {
                const resp = await __go_jshttp(e.request);
                return resp;
            } catch {
                __go_jshttp = undefined;
                await runWASM();
                const resp = await __go_jshttp(e.request);
                return resp;
            }
        })());
        return;
    }

    e.respondWith(new Response("Sorry, Service Worker failed to process the request. Please refresh the page."));
});