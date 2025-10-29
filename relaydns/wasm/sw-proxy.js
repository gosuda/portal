// Service Worker for RelayDNS Network Proxy
// WASM must be loaded during install phase

const CACHE_NAME = 'relaydns-proxy-v1';
let proxyEngine = null;
let wasmReady = false;

// Install event - Load WASM here (only time importScripts is allowed)
self.addEventListener('install', (event) => {
    console.log('[SW-Proxy] Installing...');

    event.waitUntil(
        (async () => {
            try {
                console.log('[SW-Proxy] Loading WASM module during install...');

                // importScripts can ONLY be called during install
                self.importScripts('/pkg/relaydns_wasm_sw.js');
                console.log('[SW-Proxy] ✓ WASM script loaded');

                // Initialize WASM
                console.log('[SW-Proxy] Initializing WASM...');
                await wasm_bindgen('/pkg/relaydns_wasm_sw_bg.wasm');
                console.log('[SW-Proxy] ✓ WASM initialized');

                // Create proxy engine
                console.log('[SW-Proxy] Creating ProxyEngine...');
                proxyEngine = new wasm_bindgen.ProxyEngine('ws://localhost:4017/relay');
                wasmReady = true;
                console.log('[SW-Proxy] ✓ Proxy engine ready');

            } catch (error) {
                console.error('[SW-Proxy] Failed to initialize WASM:', error);
                throw error;
            }

            // Skip waiting to activate immediately
            await self.skipWaiting();
        })()
    );
});

// Activate event
self.addEventListener('activate', (event) => {
    console.log('[SW-Proxy] Activating...');
    event.waitUntil(self.clients.claim());
});

// Check if URL should be proxied
function shouldProxy(url) {
    // Don't proxy same-origin requests (relay server itself)
    if (url.includes('localhost:4017') ||
        url.includes('localhost:8000') ||
        url.includes('/pkg/') ||
        url.includes('/sw-proxy.js') ||
        url.includes('/api/')) {
        return false;
    }

    // Only proxy /peer/* requests
    return url.includes('/peer/');
}

// Handle HTTP request through WASM proxy
async function proxyHttpRequest(request) {
    try {
        if (!wasmReady || !proxyEngine) {
            console.warn('[SW-Proxy] WASM not ready, falling back to direct fetch');
            return fetch(request);
        }

        console.log('[SW-Proxy] Proxying:', request.method, request.url);

        // Extract headers
        const headers = {};
        for (const [key, value] of request.headers.entries()) {
            headers[key] = value;
        }

        // Get body if present
        let body = null;
        if (request.method !== 'GET' && request.method !== 'HEAD') {
            try {
                const arrayBuffer = await request.arrayBuffer();
                body = Array.from(new Uint8Array(arrayBuffer));
            } catch (e) {
                console.warn('[SW-Proxy] Failed to read body:', e);
            }
        }

        // Call WASM ProxyEngine
        console.log('[SW-Proxy] Calling WASM ProxyEngine...');
        const response = await proxyEngine.handleHttpRequest(
            request.method,
            request.url,
            headers,
            body
        );

        console.log('[SW-Proxy] Got response:', response.status);

        // Reconstruct Response object
        const responseHeaders = new Headers();
        for (const [key, value] of Object.entries(response.headers || {})) {
            responseHeaders.set(key, value);
        }

        return new Response(response.body, {
            status: response.status,
            statusText: response.statusText || 'OK',
            headers: responseHeaders
        });

    } catch (error) {
        console.error('[SW-Proxy] Proxy error:', error);
        // Fallback to direct fetch on error
        console.log('[SW-Proxy] Falling back to direct fetch');
        return fetch(request);
    }
}

// Fetch event - main interception point
self.addEventListener('fetch', (event) => {
    const url = event.request.url;

    // Check if request should be proxied
    if (shouldProxy(url)) {
        console.log('[SW-Proxy] Intercepting:', url);
        event.respondWith(proxyHttpRequest(event.request));
    } else {
        // Pass through directly
        event.respondWith(fetch(event.request));
    }
});

// Message handler
self.addEventListener('message', (event) => {
    const { type } = event.data || {};

    switch (type) {
        case 'GET_STATUS':
            event.ports[0]?.postMessage({
                success: true,
                status: {
                    wasmReady,
                    hasEngine: !!proxyEngine
                }
            });
            break;

        case 'PING':
            event.ports[0]?.postMessage({ type: 'PONG', wasmReady });
            break;

        default:
            console.warn('[SW-Proxy] Unknown message:', type);
    }
});

console.log('[SW-Proxy] Service Worker script loaded');
