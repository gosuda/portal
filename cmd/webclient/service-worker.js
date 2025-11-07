//const wasm_exec_URL = "https://cdn.jsdelivr.net/gh/golang/go@go1.25.3/lib/wasm/wasm_exec.js";
let BASE_PATH = "<PORTAL_UI_URL>";
let wasmManifestString = '"<WASM_MANIFEST>"';
let wasmManifest = JSON.parse(wasmManifestString);

let wasm_exec_URL = BASE_PATH + "/frontend/wasm_exec.js";
if (new URL(BASE_PATH).protocol === "http:") {
  wasm_exec_URL = "/frontend/wasm_exec.js";
}
importScripts(wasm_exec_URL);

let loading = false;
let initError = null;
let _lastReload = Date.now();

// Fetch manifest to get current WASM filename
async function fetchManifest() {
  return wasmManifest;
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
    // loading = false;
  }
}

async function runWASM() {
  if (typeof __go_jshttp !== "undefined") {
    return;
  }

  try {
    const manifest = await fetchManifest();
    // Use unified cache path from manifest (full URL)
    let wasm_URL;
    if (manifest.wasmUrl && new URL(manifest.wasmUrl).protocol !== "http:") {
      wasm_URL = manifest.wasmUrl;
    } else {
      wasm_URL = `/frontend/${manifest.wasmFile}`;
    }

    const go = new Go();

    const response = await fetch(wasm_URL);
    if (!response.ok) {
      throw new Error(
        `Failed to fetch WASM: ${response.status} ${response.statusText}`
      );
    }
    const wasm_file = await response.arrayBuffer();

    const instance = await WebAssembly.instantiate(wasm_file, go.importObject);

    const onExit = () => {
      console.log("[SW] Go Program Exited");
      __go_jshttp = undefined;
      loading = false;
    };

    go.run(instance.instance)
      .then(onExit)
      .catch((error) => {
        console.error("[SW] Go Program Error:", error);
        onExit();
      });
  } catch (error) {
    console.error("[SW] WASM initialization failed:", error);
    throw new Error(`WASM Initialization: ${error.message}`);
  }
}

self.addEventListener("install", (e) => {
  e.waitUntil(init());
  self.skipWaiting();
});

self.addEventListener("activate", (e) => {
  e.waitUntil(
    (async () => {
      try {
        // Claim clients first to take control immediately
        await self.clients.claim();

        // Then initialize WASM in background (don't block activation)
        init().catch((error) => {
          console.error(
            "[SW] WASM initialization failed after activation:",
            error
          );
          notifyClientsOfError(error);
        });
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
    if (typeof __sdk_message_handler === "undefined") {
      console.error("[SW] SDK message handler not available");
      return;
    }

    // Call WASM message handler
    __sdk_message_handler(event.data.type, event.data);
  }
});

self.addEventListener("fetch", (e) => {
  const url = new URL(e.request.url);

  // Skip non-origin requests
  if (url.origin !== self.location.origin) {
    e.respondWith(fetch(e.request));
    return;
  }

  // Health check endpoint - check WASM status
  if (url.pathname === "/e8c2c70c-ec4a-40b2-b8af-d5638264f831") {
    e.respondWith(
      (async () => {
        // Try to initialize if not ready
        if (typeof __go_jshttp === "undefined" && !loading) {
          try {
            await init();
          } catch (error) {
            console.error("[SW] Health check init failed:", error);
          }
        }

        // Return status based on WASM availability
        if (typeof __go_jshttp !== "undefined") {
          return new Response("ACK-e8c2c70c-ec4a-40b2-b8af-d5638264f831", {
            status: 200,
          });
        } else {
          return new Response("NAK-e8c2c70c-ec4a-40b2-b8af-d5638264f831", {
            status: 503,
          });
        }
      })()
    );
    return;
  }

  e.respondWith(
    (async () => {
      if (typeof __go_jshttp === "undefined" && !loading) {
        try {
          await init();
        } catch (error) {
          console.error("[SW] Init failed:", error);
          return new Response(
            "WASM initialization failed. Please refresh the page.",
            {
              status: 503,
              statusText: "Service Unavailable",
            }
          );
        }
      }

      // Wait for WASM to be ready (increased timeout for Safari)
      let waitCount = 0;
      const maxWait = 100; // 10 seconds (100 × 100ms)
      while (typeof __go_jshttp === "undefined" && waitCount < maxWait) {
        await new Promise((resolve) => setTimeout(resolve, 100));
        waitCount++;
      }

      // If still not ready after timeout, return error
      if (typeof __go_jshttp === "undefined") {
        console.error("[SW] WASM not ready after timeout");
        return new Response(
          "WASM initialization timeout. Please refresh the page.",
          {
            status: 503,
            statusText: "Service Unavailable",
          }
        );
      }

      try {
        const resp = await __go_jshttp(e.request);
        return resp;
      } catch (error) {
        console.error("[SW] Request handling error:", error);
        __go_jshttp = undefined;
        await init();

        // Wait again after reinit
        waitCount = 0;
        while (typeof __go_jshttp === "undefined" && waitCount < maxWait) {
          await new Promise((resolve) => setTimeout(resolve, 100));
          waitCount++;
        }

        if (typeof __go_jshttp === "undefined") {
          throw new Error("WASM reinitialization failed");
        }

        const resp = await __go_jshttp(e.request);
        return resp;
      }
    })()
  );
});
