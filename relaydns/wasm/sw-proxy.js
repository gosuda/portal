// Service Worker for RelayDNS Network Proxy
// WASM must be loaded during install phase

const CACHE_NAME = 'relaydns-proxy-v1';
let proxyEngine = null;
let wasmReady = false;
let initializationPromise = null;

// Dynamic WASM initialization (can be called anytime, not just install)
async function initializeProxyEngine() {
    // If already initializing, wait for it
    if (initializationPromise) {
        return initializationPromise;
    }

    // If already initialized, return immediately
    if (proxyEngine && wasmReady) {
        return proxyEngine;
    }

    initializationPromise = (async () => {
        try {
            console.log('[SW-Proxy] Dynamically initializing ProxyEngine...');

            // Check if wasm_bindgen is available (loaded during install)
            if (typeof wasm_bindgen === 'undefined') {
                console.log('[SW-Proxy] wasm_bindgen not available, loading via dynamic import...');

                // Use dynamic import for ES6 modules
                const wasmModule = await import('/pkg/relaydns_wasm.js');

                // Make wasm_bindgen available globally
                self.wasm_bindgen = wasmModule;

                console.log('[SW-Proxy] ✓ WASM JS module loaded');
            }

            // Check if SecureWebSocket SW module is loaded
            if (typeof ServiceWorkerWebSocketTunnel === 'undefined') {
                console.log('[SW-Proxy] SecureWebSocket SW module not available, loading via fetch...');

                const swWsResponse = await fetch('/secure-websocket-sw.js');
                const swWsCode = await swWsResponse.text();

                // Use indirect eval to execute in global scope
                (1, eval)(swWsCode);
                console.log('[SW-Proxy] ✓ SecureWebSocket SW module loaded');
            }

            // Initialize WASM module if not already done
            if (!wasmReady) {
                console.log('[SW-Proxy] Initializing WASM module...');

                // wasm_bindgen is now the module object from dynamic import
                const initWasm = self.wasm_bindgen.default || self.wasm_bindgen;
                await initWasm('/pkg/relaydns_wasm_bg.wasm');

                console.log('[SW-Proxy] ✓ WASM module initialized');
            }

            // Get relay URL from API
            let relayUrl = 'ws://localhost:4017/relay'; // Default fallback
            try {
                const relayInfoResponse = await fetch('/api/relay-info');
                if (relayInfoResponse.ok) {
                    const relayInfo = await relayInfoResponse.json();
                    if (relayInfo.relayUrl) {
                        relayUrl = relayInfo.relayUrl;
                        console.log('[SW-Proxy] Got relay URL from server:', relayUrl);
                    }
                }
            } catch (e) {
                console.warn('[SW-Proxy] Failed to fetch relay URL, using default:', e.message);
            }

            // Create ProxyEngine
            console.log('[SW-Proxy] Creating ProxyEngine with URL:', relayUrl);
            const ProxyEngine = self.wasm_bindgen.ProxyEngine;
            proxyEngine = new ProxyEngine(relayUrl);
            wasmReady = true;

            console.log('[SW-Proxy] ✓ ProxyEngine ready');
            return proxyEngine;

        } catch (error) {
            console.error('[SW-Proxy] Failed to initialize ProxyEngine:', error);
            initializationPromise = null; // Reset so we can retry
            wasmReady = false;
            throw error;
        }
    })();

    return initializationPromise;
}

// Install event - Skip importScripts since WASM is ES6 module
self.addEventListener('install', (event) => {
    console.log('[SW-Proxy] Installing...');

    event.waitUntil(
        (async () => {
            try {
                console.log('[SW-Proxy] Service Worker installing...');

                // Note: We cannot use importScripts with ES6 modules
                // WASM will be loaded dynamically on first request via fetch + dynamic import
                console.log('[SW-Proxy] WASM will be loaded dynamically on first request');

            } catch (error) {
                console.error('[SW-Proxy] Install error:', error);
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
        // Ensure ProxyEngine is initialized (lazy init if needed)
        if (!wasmReady || !proxyEngine) {
            console.log('[SW-Proxy] ProxyEngine not ready, attempting lazy initialization...');
            try {
                await initializeProxyEngine();
            } catch (initError) {
                // Security: Never fallback to direct fetch - this would bypass E2EE!
                console.error('[SW-Proxy] Failed to initialize ProxyEngine:', initError);
                return new Response(
                    JSON.stringify({
                        error: 'E2EE ProxyEngine Initialization Failed',
                        message: 'Could not initialize secure proxy. Please refresh the page.',
                        code: 'PROXY_ENGINE_INIT_FAILED',
                        details: initError.message
                    }),
                    {
                        status: 503,
                        statusText: 'Service Unavailable',
                        headers: {
                            'Content-Type': 'application/json',
                            'X-E2EE-Status': 'init-failed'
                        }
                    }
                );
            }
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
        // Security: Never fallback to direct fetch - return error instead
        return new Response(
            JSON.stringify({
                error: 'E2EE Proxy Error',
                message: error.message || 'Failed to proxy request through E2EE tunnel',
                code: 'PROXY_ERROR'
            }),
            {
                status: 502,
                statusText: 'Bad Gateway',
                headers: {
                    'Content-Type': 'application/json',
                    'X-E2EE-Status': 'error'
                }
            }
        );
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

    // Try WebSocket handler first
    if (typeof handleWebSocketMessage === 'function') {
        const handled = handleWebSocketMessage(event);
        if (handled instanceof Promise) {
            // Async handler
            return;
        } else if (handled) {
            // Synchronously handled
            return;
        }
    }

    // Standard message handling
    switch (type) {
        case 'GET_STATUS':
            event.ports[0]?.postMessage({
                success: true,
                status: {
                    wasmReady,
                    hasEngine: !!proxyEngine,
                    hasWebSocket: typeof handleWebSocketMessage === 'function'
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
