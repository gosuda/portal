// Service Worker for RelayDNS WASM Client
const CACHE_NAME = 'relaydns-wasm-v1';

// Files to cache
const urlsToCache = [
    '/pkg/relaydns_wasm.js',
    '/pkg/relaydns_wasm_bg.wasm',
    '/example.html',
    '/adapter-test.html'
];

// Install event - cache files
self.addEventListener('install', (event) => {
    console.log('[SW] Installing Service Worker...');
    event.waitUntil(
        caches.open(CACHE_NAME)
            .then((cache) => {
                console.log('[SW] Caching WASM files');
                return cache.addAll(urlsToCache);
            })
            .then(() => {
                console.log('[SW] All files cached successfully');
                return self.skipWaiting(); // Activate immediately
            })
    );
});

// Activate event - clean up old caches
self.addEventListener('activate', (event) => {
    console.log('[SW] Activating Service Worker...');
    event.waitUntil(
        caches.keys().then((cacheNames) => {
            return Promise.all(
                cacheNames.map((cacheName) => {
                    if (cacheName !== CACHE_NAME) {
                        console.log('[SW] Deleting old cache:', cacheName);
                        return caches.delete(cacheName);
                    }
                })
            );
        }).then(() => {
            console.log('[SW] Service Worker activated');
            return self.clients.claim(); // Take control immediately
        })
    );
});

// Fetch event - serve from cache or network
self.addEventListener('fetch', (event) => {
    const url = new URL(event.request.url);

    // Handle WASM files with special headers
    if (url.pathname.endsWith('.wasm')) {
        event.respondWith(
            caches.match(event.request)
                .then((response) => {
                    if (response) {
                        console.log('[SW] Serving WASM from cache:', url.pathname);
                        return response;
                    }

                    console.log('[SW] Fetching WASM from network:', url.pathname);
                    return fetch(event.request)
                        .then((networkResponse) => {
                            // Clone the response
                            const responseToCache = networkResponse.clone();

                            // Cache the fetched response
                            caches.open(CACHE_NAME)
                                .then((cache) => {
                                    cache.put(event.request, responseToCache);
                                });

                            return networkResponse;
                        });
                })
        );
    }
    // Handle JS files
    else if (url.pathname.endsWith('relaydns_wasm.js')) {
        event.respondWith(
            caches.match(event.request)
                .then((response) => {
                    if (response) {
                        console.log('[SW] Serving JS from cache:', url.pathname);
                        return response;
                    }

                    return fetch(event.request)
                        .then((networkResponse) => {
                            const responseToCache = networkResponse.clone();
                            caches.open(CACHE_NAME)
                                .then((cache) => {
                                    cache.put(event.request, responseToCache);
                                });
                            return networkResponse;
                        });
                })
        );
    }
    // All other requests - network first, fallback to cache
    else {
        event.respondWith(
            fetch(event.request)
                .catch(() => {
                    return caches.match(event.request);
                })
        );
    }
});

// Message handler
self.addEventListener('message', (event) => {
    if (event.data && event.data.type === 'SKIP_WAITING') {
        console.log('[SW] Received SKIP_WAITING message');
        self.skipWaiting();
    }
});

console.log('[SW] Service Worker loaded');
