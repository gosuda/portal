//const wasm_exec_URL = "https://cdn.jsdelivr.net/gh/golang/go@go1.25.3/lib/wasm/wasm_exec.js";
const BASE_PATH = self.location.origin || "";
let wasmManifest = null;
let wasmManifestPromise = null;

// Debug mode detection (disable verbose logging in production)
const DEBUG_MODE = self.location.hostname === 'localhost' ||
                   self.location.hostname === '127.0.0.1' ||
                   self.location.hostname.endsWith('.localhost');

function debugLog(...args) {
  if (DEBUG_MODE) {
    console.log(...args);
  }
}

// Load manifest from backend (decouples SW from Go template)
async function loadManifest() {
  if (wasmManifest) {
    return wasmManifest;
  }

  if (wasmManifestPromise) {
    return wasmManifestPromise;
  }

  wasmManifestPromise = (async () => {
    try {
      debugLog("[SW] Fetching WASM manifest...");
      const response = await fetch("/frontend/manifest.json", { cache: "no-cache" });

      if (!response.ok) {
        throw new Error(`HTTP ${response.status}: ${response.statusText}`);
      }

      const manifest = await response.json();
      wasmManifest = manifest;

      // Expose bootstrap servers to WASM runtime (service worker global)
      if (manifest.bootstraps) {
        self.__BOOTSTRAP_SERVERS__ = manifest.bootstraps;
        debugLog("[SW] Bootstraps loaded from manifest:", manifest.bootstraps);
      }

      debugLog("[SW] Manifest loaded successfully:", manifest);
      return manifest;
    } catch (error) {
      console.error("[SW] Failed to load WASM manifest:", error);

      // Fallback manifest
      wasmManifest = {
        wasmFile: "main.wasm",
        wasmUrl: null
      };
      console.warn("[SW] Using fallback manifest:", wasmManifest);
      return wasmManifest;
    } finally {
      wasmManifestPromise = null;
    }
  })();

  return wasmManifestPromise;
}

let wasm_exec_URL = BASE_PATH + "/frontend/wasm_exec.js";
try {
  if (new URL(BASE_PATH).protocol === "http:") {
    wasm_exec_URL = "/frontend/wasm_exec.js";
  }
  debugLog("[SW] Loading wasm_exec.js from:", wasm_exec_URL);
  importScripts(wasm_exec_URL);
  debugLog("[SW] wasm_exec.js loaded successfully");
} catch (error) {
  console.error("[SW] Failed to load wasm_exec.js:", error);
  throw new Error(`Failed to load wasm_exec.js from ${wasm_exec_URL}: ${error.message}`);
}

let loading = false;
let initError = null;
let _lastReload = Date.now();
let initPromise = null; // Prevent concurrent initialization

// Service Worker version for debugging
const SW_VERSION = "1.0.0";

debugLog(`[SW] Service Worker v${SW_VERSION} loaded`);

// Service Worker readiness stages
const ReadinessStage = {
  UNINITIALIZED: 0,      // No handlers available
  WASM_LOADING: 1,       // Loading in progress
  WASM_LOADED: 2,        // __go_jshttp available
  READY: 3,              // Both __go_jshttp and __sdk_message_handler available (fully operational)
};

let declaredStage = ReadinessStage.UNINITIALIZED; // What we think the stage is

// Check handler availability
function areHandlersAvailable() {
  return {
    http: typeof __go_jshttp !== "undefined",
    sdk: typeof __sdk_message_handler !== "undefined" || typeof __sdk_handle_message !== "undefined"
  };
}

// Compute actual stage based on runtime state
function getCurrentStage() {
  const { http, sdk } = areHandlersAvailable();

  const sdkHandler = __sdk_message_handler || __sdk_handle_message;
  if (http && sdkHandler) {
    return ReadinessStage.READY;
  } else if (http && !sdkHandler) {
    return ReadinessStage.WASM_LOADED;
  } else if (sdkHandler && !http) {
    return ReadinessStage.WASM_LOADED;
  } else if (loading) {
    return ReadinessStage.WASM_LOADING;
  } else {
    return ReadinessStage.UNINITIALIZED;
  }
}

// Check if error is recoverable (handler missing) or fatal (other errors)
function isRecoverableError(error, currentStage, targetStage) {
  // Handlers missing = recoverable (can retry infinitely)
  if (targetStage === ReadinessStage.READY) {
    const { http, sdk } = areHandlersAvailable();
    if (!http || !sdk) {
      return true;
    }
  }

  // Check for fatal errors that we should not retry infinitely
  const errorMsg = error.message.toLowerCase();

  // Deterministic import/runtime mismatch (e.g. TinyGo WASI imports)
  if (errorMsg.includes('wasi_snapshot_preview1') ||
      errorMsg.includes('linkerror') ||
      errorMsg.includes('unknown import') ||
      errorMsg.includes('import #')) {
    return false;
  }

  // Fatal errors - should throw immediately
  if (errorMsg.includes('out of memory') ||
      errorMsg.includes('rangeerror') ||
      errorMsg.includes('404') ||
      errorMsg.includes('403') ||
      errorMsg.includes('invalid wasm') ||
      errorMsg.includes('bad magic number')) {
    return false;
  }

  // Temporary/recoverable errors - can retry
  if (errorMsg.includes('timeout') ||
      errorMsg.includes('network') ||
      errorMsg.includes('fetch') ||
      errorMsg.includes('offline')) {
    return true;
  }

  // Default: if handlers are missing, it's recoverable
  return currentStage < targetStage;
}

// Simple recovery system: check state → recover if needed → execute
async function ensureReady(targetStage = ReadinessStage.READY) {
  let attempt = 0;

  while (true) {
    // Step 1: Check current state
    const currentStage = getCurrentStage();
    if (currentStage >= targetStage) {
      debugLog(`[SW] Already at stage ${currentStage}, ready`);
      return;
    }

    // Step 2: Recover to desired state
    try {
      if (attempt > 0) {
        const delay = Math.min(100 * Math.pow(2, attempt - 1), 5000);
        debugLog(`[SW] Retry ${attempt + 1} after ${delay}ms...`);
        await new Promise(resolve => setTimeout(resolve, delay));
      }

      // Reset state before recovery
      declaredStage = ReadinessStage.UNINITIALIZED;
      loading = false;
      initError = null;

      // Load WASM
      await ensureStage(targetStage);

      // Verify success
      const finalStage = getCurrentStage();
      if (finalStage >= targetStage) {
        console.log(`[SW] Recovery successful, reached stage ${finalStage}`);
        return;
      }

      throw new Error(`Recovery incomplete: expected ${targetStage}, got ${finalStage}`);
    } catch (error) {
      attempt++;
      console.warn(`[SW] Recovery attempt ${attempt} failed:`, error.message);

      // Check if this is a fatal error
      const currentStageNow = getCurrentStage();
      if (!isRecoverableError(error, currentStageNow, targetStage)) {
        console.error(`[SW] Fatal error, cannot recover:`, error);
        throw error;
      }

      // Continue loop for recoverable errors
    }
  }
}

// Sync declared stage with actual stage
function syncStage() {
  const actualStage = getCurrentStage();
  if (declaredStage !== actualStage) {
    debugLog(`[SW] Stage sync: ${declaredStage} -> ${actualStage}`);
    declaredStage = actualStage;
  }
  return actualStage;
}

// Mobile detection and optimization
const isMobile = /Android|webOS|iPhone|iPad|iPod|BlackBerry|IEMobile|Opera Mini/i.test(navigator.userAgent);
const isIOS = /iPad|iPhone|iPod/.test(navigator.userAgent);
const isAndroid = /Android/.test(navigator.userAgent);
const isSafari = /^((?!chrome|android).)*safari/i.test(navigator.userAgent);

debugLog(`[SW] Platform: ${isMobile ? 'Mobile' : 'Desktop'}, iOS: ${isIOS}, Android: ${isAndroid}, Safari: ${isSafari}`);

// Network utilities with retry logic
async function fetchWithRetry(url, options = {}, maxRetries = 3) {
  let lastError;

  for (let i = 0; i < maxRetries; i++) {
    try {
      console.log(`[SW] Fetching ${url} (attempt ${i + 1}/${maxRetries})`);
      const response = await fetch(url, options);

      if (!response.ok) {
        throw new Error(`HTTP ${response.status}: ${response.statusText}`);
      }

      return response;
    } catch (error) {
      lastError = error;
      console.warn(`[SW] Fetch attempt ${i + 1} failed:`, error.message);

      // Don't retry on certain errors
      if (error.message.includes('404') || error.message.includes('403')) {
        throw error;
      }

      // Wait before retry (exponential backoff)
      if (i < maxRetries - 1) {
        const delay = Math.min(1000 * Math.pow(2, i), 5000);
        console.log(`[SW] Retrying in ${delay}ms...`);
        await new Promise(resolve => setTimeout(resolve, delay));
      }
    }
  }

  throw new Error(`Failed after ${maxRetries} attempts: ${lastError.message}`);
}

// Send error to all clients
async function notifyClientsOfError(error) {
  const clients = await self.clients.matchAll();
  const errorMessage = {
    type: "SW_ERROR",
    error: {
      name: error.name,
      message: error.message,
      stack: error.stack,
    },
  };

  for (const client of clients) {
    client.postMessage(errorMessage);
  }
}

// Stage-based initialization with automatic dependency resolution
async function ensureStage(targetStage) {
  // Sync stage before checking
  const current = syncStage();
  debugLog(`[SW] Ensuring stage: ${targetStage}, current: ${current}`);

  // Already at or past the target stage
  if (current >= targetStage) {
    debugLog(`[SW] Already at stage ${current}, no action needed`);
    return true;
  }

  // Recursive dependency resolution
  switch (targetStage) {
    case ReadinessStage.WASM_LOADED:
      await ensureWASMLoaded();
      break;

    case ReadinessStage.READY:
      // First ensure WASM is loaded
      await ensureStage(ReadinessStage.WASM_LOADED);
      await ensureHandlersRegistered();
      break;
  }

  // Verify we reached the target stage
  const finalStage = syncStage();
  if (finalStage < targetStage) {
    throw new Error(`Failed to reach stage ${targetStage}, stuck at ${finalStage}`);
  }

  return true;
}

// Ensure WASM is loaded
async function ensureWASMLoaded() {
  // Verify current stage
  const current = syncStage();

  // If already loaded, return immediately
  if (current >= ReadinessStage.WASM_LOADED) {
    debugLog("[SW] WASM already loaded (verified by handler check)");
    return true;
  }

  // Prevent concurrent initialization attempts
  if (initPromise) {
    debugLog("[SW] Init already in progress, reusing existing promise");
    await initPromise;
    return syncStage() >= ReadinessStage.WASM_LOADED;
  }

  if (loading) {
    debugLog("[SW] Init already loading, waiting...");
    // Wait for loading to complete
    while (loading && syncStage() < ReadinessStage.WASM_LOADED) {
      await new Promise(resolve => setTimeout(resolve, 100));
    }
    return syncStage() >= ReadinessStage.WASM_LOADED;
  }

  loading = true;
  declaredStage = ReadinessStage.WASM_LOADING;

  initPromise = (async () => {
    try {
      debugLog("[SW] Starting WASM initialization...");

      await runWASM();

      // Stage will be auto-synced based on handler availability
      initError = null;

      // Verify we actually reached the expected stage
      const finalStage = syncStage();
      debugLog(`[SW] WASM initialization complete, stage: ${finalStage}`);
    } catch (error) {
      console.error("[SW] Error initializing WASM:", error);
      initError = error;
      declaredStage = ReadinessStage.UNINITIALIZED;
      loading = false;
      await notifyClientsOfError(error);
      throw error;
    } finally {
      initPromise = null;
      // Don't reset loading here - leave it true until WASM exits
    }
  })();

  await initPromise;
  return syncStage() >= ReadinessStage.WASM_LOADED;
}

// Ensure handlers are registered
async function ensureHandlersRegistered() {
  // Verify current stage
  const current = syncStage();

  // Check if handlers exist
  if (current >= ReadinessStage.READY) {
    debugLog("[SW] Handlers already registered (verified by handler check)");
    return true;
  }

  debugLog("[SW] Handlers not registered, waiting...");

  // Wait for handlers to be registered (max 10 seconds)
  let waitCount = 0;
  const maxWait = 100;

  while (waitCount < maxWait) {
    const stage = syncStage();
    if (stage >= ReadinessStage.READY) {
      debugLog("[SW] Handlers registered successfully");
      return true;
    }

    await new Promise(resolve => setTimeout(resolve, 100));
    waitCount++;
  }

  // If handlers still not available, WASM might have failed
  console.warn("[SW] Handlers not available after waiting, WASM may need reloading");
  declaredStage = ReadinessStage.UNINITIALIZED;
  loading = false;

  // Retry WASM load
  return await ensureWASMLoaded();
}

// Legacy init function for backward compatibility - with infinite retry
async function init() {
  return await ensureReady(ReadinessStage.READY);
}

function resolveWasmURL(manifest) {
  const wasmFile =
    typeof manifest.wasmFile === "string" ? manifest.wasmFile : "";
  let wasm_URL = wasmFile ? `/frontend/${wasmFile}` : "";

  if (manifest.wasmUrl) {
    try {
      const parsed = new URL(manifest.wasmUrl, self.location.origin);
      const sameOrigin = parsed.origin === self.location.origin;
      const isHttps = parsed.protocol === "https:";
      const isHttp = parsed.protocol === "http:";
      const allowHttp = isHttp && self.location.protocol === "http:";

      if (sameOrigin || isHttps || allowHttp) {
        wasm_URL = parsed.toString();
      } else {
        debugLog("[SW] Ignoring manifest.wasmUrl due to mixed content or origin:", parsed.toString());
      }
    } catch (error) {
      console.warn("[SW] Invalid manifest.wasmUrl, falling back to local /frontend path:", error);
    }
  }

  if (!wasm_URL) {
    throw new Error("WASM manifest missing wasmFile/wasmUrl");
  }

  return wasm_URL;
}

async function runWASM() {
  // Check actual runtime state, not just if handler exists
  const currentStage = getCurrentStage();
  if (currentStage >= ReadinessStage.WASM_LOADED) {
    debugLog("[SW] WASM already loaded and verified");
    return;
  }

  try {
    // Ensure manifest is loaded
    const manifest = await loadManifest();

    // Determine WASM URL from manifest
    const wasm_URL = resolveWasmURL(manifest);
    debugLog("[SW] WASM URL:", wasm_URL);

    // Create Go runtime
    const go = new Go();

    // Fetch WASM file with retry logic
    debugLog("[SW] Fetching WASM file...");
    let instance;

    // Set timeout for WASM instantiation (especially important on mobile)
    const instantiateTimeout = isMobile ? 30000 : 15000; // 30s mobile, 15s desktop

    try {
      // Use compileStreaming if available (most efficient)
      if (WebAssembly.compileStreaming) {
        const response = await fetchWithRetry(wasm_URL, {}, isMobile ? 5 : 3);

        // Check Content-Type before streaming
        const contentType = response.headers.get('content-type') || '';
        debugLog("[SW] WASM response Content-Type:", contentType);

        if (contentType.includes('text/html')) {
          throw new Error(
            `Received HTML instead of WASM file. This usually means Service Worker is not properly intercepting requests. ` +
            `Content-Type: ${contentType}, URL: ${wasm_URL}`
          );
        }

        debugLog("[SW] WASM file fetched, size:", response.headers.get('content-length'), "bytes");

        // Use instantiateStreaming for optimal performance
        const instantiatePromise = WebAssembly.instantiateStreaming(
          Promise.resolve(response),
          go.importObject
        );

        const timeoutPromise = new Promise((_, reject) =>
          setTimeout(() => reject(new Error(`WebAssembly instantiation timeout after ${instantiateTimeout}ms`)), instantiateTimeout)
        );

        instance = await Promise.race([instantiatePromise, timeoutPromise]);
        debugLog("[SW] WebAssembly instantiated successfully via streaming");
      }
    } catch (streamError) {
      // Fallback to traditional instantiate
      console.warn("[SW] compileStreaming failed, falling back to traditional method:", streamError.message);

      const response = await fetchWithRetry(wasm_URL, {}, isMobile ? 5 : 3);

      // Check Content-Type to detect if we got HTML instead of WASM
      const contentType = response.headers.get('content-type') || '';
      debugLog("[SW] WASM response Content-Type:", contentType);

      if (contentType.includes('text/html')) {
        throw new Error(
          `Received HTML instead of WASM file. Content-Type: ${contentType}, URL: ${wasm_URL}`
        );
      }

      debugLog("[SW] WASM file fetched, size:", response.headers.get('content-length'), "bytes");

      const wasm_file = await response.arrayBuffer();
      debugLog("[SW] WASM ArrayBuffer size:", wasm_file.byteLength, "bytes");

      // Additional validation: Check WASM magic number (0x00 0x61 0x73 0x6d)
      const magicNumber = new Uint8Array(wasm_file, 0, 4);
      if (magicNumber[0] !== 0x00 || magicNumber[1] !== 0x61 ||
          magicNumber[2] !== 0x73 || magicNumber[3] !== 0x6d) {
        // Try to detect if it's HTML
        const decoder = new TextDecoder();
        const firstBytes = decoder.decode(new Uint8Array(wasm_file, 0, Math.min(100, wasm_file.byteLength)));

        if (firstBytes.includes('<!DOCTYPE') || firstBytes.includes('<html>')) {
          throw new Error(
            `Received HTML document instead of WASM file. ` +
            `This indicates Service Worker is not active or not intercepting requests properly. ` +
            `First bytes: ${firstBytes.substring(0, 50)}...`
          );
        } else {
          throw new Error(
            `Invalid WASM file (bad magic number). ` +
            `Expected: [0x00, 0x61, 0x73, 0x6d], Got: [${Array.from(magicNumber).map(b => '0x' + b.toString(16).padStart(2, '0')).join(', ')}]`
          );
        }
      }
      debugLog("[SW] WASM magic number validated");

      // Instantiate WebAssembly with timeout
      debugLog("[SW] Instantiating WebAssembly...");

      const instantiatePromise = WebAssembly.instantiate(wasm_file, go.importObject);
      const timeoutPromise = new Promise((_, reject) =>
        setTimeout(() => reject(new Error(`WebAssembly instantiation timeout after ${instantiateTimeout}ms`)), instantiateTimeout)
      );

      instance = await Promise.race([instantiatePromise, timeoutPromise]);
      debugLog("[SW] WebAssembly instantiated successfully");
    }

    const onExit = () => {
      console.warn("[SW] Go Program Exited - handlers will be undefined");
      __go_jshttp = undefined;
      __sdk_message_handler = undefined;
      loading = false;
      initError = null;
      syncStage(); // Auto-sync to UNINITIALIZED
    };

    // Run Go program
    debugLog("[SW] Running Go program...");
    go.run(instance.instance)
      .then(onExit)
      .catch((error) => {
        console.error("[SW] Go Program Runtime Error:", error);
        onExit();
      });

    debugLog("[SW] WASM initialization completed successfully");
  } catch (error) {
    console.error("[SW] WASM initialization failed at:", error.stack || error);
    console.error("[SW] Error details:", {
      name: error.name,
      message: error.message,
      stack: error.stack
    });

    // Check for specific error types
    let errorType = "unknown";
    let userMessage = error.message;

    if (error.message.includes("memory") || error.message.includes("RangeError")) {
      errorType = "out_of_memory";
      userMessage = "Not enough memory to load application. Please close other tabs and try again.";
      console.error("[SW] Out of memory error detected");
    } else if (error.message.includes("timeout")) {
      errorType = "timeout";
      userMessage = "Loading timed out. Please check your connection and try again.";
      console.error("[SW] Timeout error detected");
    } else if (error.message.includes("offline") || error.message.includes("Failed to fetch")) {
      errorType = "network";
      userMessage = "Network error. Please check your connection.";
      console.error("[SW] Network error detected");
    } else if (error.message.includes("HTML")) {
      errorType = "service_worker_not_active";
      userMessage = "Service Worker not active. Please refresh the page.";
      console.error("[SW] Service Worker activation issue detected");
    }

    throw new Error(`WASM Initialization (${errorType}): ${userMessage}`);
  }
}

self.addEventListener("install", (e) => {
  debugLog("[SW] Install event triggered");

  e.waitUntil(
    (async () => {
      try {
        await init();
        // Only skipWaiting if initialization succeeded
        // WARNING: skipWaiting() can cause version mismatch issues
        // Consider removing this in production if updates can wait for page reload
        await self.skipWaiting();
        debugLog("[SW] Skipped waiting phase");
      } catch (error) {
        console.error("[SW] Installation failed:", error);
        // Don't skipWaiting on error - let the old SW keep running
        throw error;
      }
    })()
  );
});

self.addEventListener("activate", (e) => {
  debugLog("[SW] Activation event triggered");

  e.waitUntil(
    (async () => {
      try {
        // Delete old caches to free up space (especially important on mobile)
        const cacheKeys = await caches.keys();
        const oldCaches = cacheKeys.filter(key => key.startsWith('portal-') && key !== `portal-v${SW_VERSION}`);
        if (oldCaches.length > 0) {
          console.log(`[SW] Deleting ${oldCaches.length} old caches:`, oldCaches);
          await Promise.all(oldCaches.map(key => caches.delete(key)));
        }

        // Claim clients first to take control immediately
        await self.clients.claim();
        debugLog("[SW] Clients claimed");

        // Safari/iOS specific: Wait a bit before initializing WASM
        if (isSafari || isIOS) {
          debugLog("[SW] Safari/iOS detected, waiting 100ms before WASM init");
          await new Promise(resolve => setTimeout(resolve, 100));
        }

        // Then initialize WASM in background (don't block activation)
        await init();
      } catch (error) {
        console.error("[SW] Activation failed:", error);
        await notifyClientsOfError(error);
      }
    })()
  );
});

// Helper function to broadcast message to all clients
async function broadcastToClients(message) {
  const clients = await self.clients.matchAll();
  clients.forEach((client) => {
    client.postMessage(message);
  });
}

// Expose to WASM
self.__sdk_post_message = broadcastToClients;

// Periodic health check (only in debug mode or when errors occur)
// Adjust interval based on mode: debug = 30s, production = 5min
const healthCheckInterval = DEBUG_MODE ? 30000 : 5 * 60 * 1000;

setInterval(() => {
  const stage = syncStage();
  const handlers = areHandlersAvailable();
  const health = {
    stage: stage,
    stageName: Object.keys(ReadinessStage).find(key => ReadinessStage[key] === stage),
    wasmActive: handlers.http,
    sdkActive: handlers.sdk,
    loading: loading,
    initError: initError ? initError.message : null,
    uptime: Date.now() - _lastReload
  };

  // Only log in debug mode or if there's an issue
  if (DEBUG_MODE || !handlers.http || initError) {
    debugLog("[SW] Health Check:", health);
  }

  // Auto-recovery if stage is too low
  const recoveryStage = syncStage(); // Get actual stage for recovery check
  if (recoveryStage < ReadinessStage.READY && !loading) {
    if (initError && !isRecoverableError(initError, recoveryStage, ReadinessStage.READY)) {
      console.error("[SW] Fatal init error detected, skipping auto-recovery:", initError.message);
      return;
    }
    // Allow recovery even if initError exists (clear it and try again)
    if (initError) {
      console.warn("[SW] Previous init error detected, clearing and retrying...", initError.message);
      initError = null;
    }
    console.warn("[SW] Stage too low, attempting recovery...", health);
    ensureStage(ReadinessStage.READY).catch(err => {
      console.error("[SW] Recovery failed:", err);
      initError = err; // Store new error
    });
  }
}, healthCheckInterval);

// Test hooks (no-op in production)
if (self.__PORTAL_SW_TEST__) {
  self.__PORTAL_SW_TEST__.resolveWasmURL = resolveWasmURL;
  self.__PORTAL_SW_TEST__.isRecoverableError = isRecoverableError;
  self.__PORTAL_SW_TEST__.fetchWithRetry = fetchWithRetry;
  self.__PORTAL_SW_TEST__.ReadinessStage = ReadinessStage;
  self.__PORTAL_SW_TEST__.setHandlers = (httpHandler, sdkHandler) => {
    self.__go_jshttp = httpHandler;
    self.__sdk_message_handler = sdkHandler;
  };
}

self.addEventListener("message", (event) => {
  if (event.data && event.data.type === "CLAIM_CLIENTS") {
    self.clients
      .claim()
      .then(() => {
        self.clients.matchAll().then((clients) => {
          clients.forEach((client) => {
            client.postMessage({ type: "CLAIMED" });
          });
        });
      })
      .catch((error) => {
        console.error("[SW] Manual clients.claim() failed:", error);
      });
    return;
  }

  // Handle SDK messages (SDK_CONNECT, SDK_SEND, SDK_CLOSE)
  if (event.data && event.data.type && event.data.type.startsWith("SDK_")) {
    (async () => {
      try {
        // Centralized recovery: Wait until handlers are ready
        debugLog("[SW] SDK message received, ensuring handlers are ready...");
        await ensureReady(ReadinessStage.READY);

        // Handlers should now be available
        const sdkHandler = __sdk_message_handler || __sdk_handle_message;
        if (typeof sdkHandler === "undefined") {
          throw new Error("SDK message handler still not available after centralized recovery");
        }

        // Call WASM message handler
        sdkHandler(event.data.type, event.data);
      } catch (error) {
        console.error("[SW] SDK message handling failed:", error);
        // Send error back to client
        if (event.data.clientId) {
          await broadcastToClients({
            type: event.data.type.replace("SDK_", "SDK_") + "_ERROR",
            clientId: event.data.clientId,
            error: "Handler unavailable: " + error.message,
          });
        }
      }
    })();
  }
});

self.addEventListener("fetch", (e) => {
  const url = new URL(e.request.url);

  // Skip non-origin requests
  if (url.origin !== self.location.origin) {
    e.respondWith(fetch(e.request));
    return;
  }

  // Skip Service Worker infrastructure files (prevent infinite loop during initialization)
  if (url.pathname.startsWith("/frontend/") ||
      url.pathname === "/service-worker.js") {
    e.respondWith(fetch(e.request));
    return;
  }

  // Health check endpoint - check WASM status
  if (url.pathname === "/e8c2c70c-ec4a-40b2-b8af-d5638264f831") {
    e.respondWith(
      (async () => {
        try {
          // Centralized recovery: Wait until handlers are ready
          await ensureReady(ReadinessStage.READY);

          if (typeof __go_jshttp !== "undefined") {
            return new Response("ACK-e8c2c70c-ec4a-40b2-b8af-d5638264f831", {
              status: 200,
            });
          }
        } catch (error) {
          console.error("[SW] Health check failed:", error);
        }

        return new Response("NAK-e8c2c70c-ec4a-40b2-b8af-d5638264f831", {
          status: 503,
        });
      })()
    );
    return;
  }

  e.respondWith(
    (async () => {
      try {
        // Centralized recovery: Wait until handlers are ready
        debugLog("[SW] Fetch request received, ensuring handlers are ready...");
        await ensureReady(ReadinessStage.READY);

        // Handler should now be available
        if (typeof __go_jshttp === "undefined") {
          throw new Error("__go_jshttp still not available after centralized recovery");
        }

        // Process request
        const resp = await __go_jshttp(e.request);
        return resp;
      } catch (error) {
        console.error("[SW] Request handling failed:", error);

        return new Response(
          "Service temporarily unavailable. Please refresh the page.",
          {
            status: 503,
            statusText: "Service Unavailable",
          }
        );
      }
    })()
  );
});
