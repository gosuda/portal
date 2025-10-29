/**
 * SecureWebSocket - E2EE WebSocket Polyfill using WASM ProxyEngine
 *
 * Provides transparent end-to-end encryption for WebSocket connections
 * through the Portal WASM ProxyEngine.
 */

// Global WASM instance cache
let wasmInstance = null;
let wasmInitPromise = null;

/**
 * Get Relay server URL from server or config
 * @returns {Promise<string>} Relay server WebSocket URL
 */
async function getRelayUrl() {
  // 1. Check if manually configured
  if (window.PORTAL_RELAY_URL) {
    console.log('[SecureWebSocket] Using configured relay URL:', window.PORTAL_RELAY_URL);
    return window.PORTAL_RELAY_URL;
  }

  // 2. Try to get from server API
  try {
    const response = await fetch('/api/relay-info');
    if (response.ok) {
      const data = await response.json();
      if (data.relayUrl) {
        console.log('[SecureWebSocket] Got relay URL from server:', data.relayUrl);
        return data.relayUrl;
      }
    }
  } catch (error) {
    console.warn('[SecureWebSocket] Failed to fetch relay info from server:', error.message);
  }

  // 3. Auto-detect from current location
  const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
  const host = window.location.host;
  const autoUrl = `${protocol}//${host}/relay`;

  console.log('[SecureWebSocket] Auto-detected relay URL:', autoUrl);
  return autoUrl;
}

/**
 * Check if Service Worker is available and has WebSocket support
 * @returns {Promise<boolean>}
 */
async function hasServiceWorkerWebSocket() {
  if (!navigator.serviceWorker || !navigator.serviceWorker.controller) {
    return false;
  }

  try {
    const channel = new MessageChannel();
    const response = await new Promise((resolve) => {
      channel.port1.onmessage = (event) => resolve(event.data);
      navigator.serviceWorker.controller.postMessage(
        { type: 'GET_STATUS' },
        [channel.port2]
      );
      setTimeout(() => resolve({ success: false }), 1000);
    });

    return response.success && response.status?.hasWebSocket;
  } catch {
    return false;
  }
}

/**
 * Initialize and get the WASM ProxyEngine instance
 * @returns {Promise<Object>} WASM module with ProxyEngine
 */
async function getProxyEngine() {
  if (wasmInstance) {
    return wasmInstance;
  }

  if (wasmInitPromise) {
    return wasmInitPromise;
  }

  wasmInitPromise = (async () => {
    try {
      // Check if we should use Service Worker
      const useServiceWorker = await hasServiceWorkerWebSocket();

      if (useServiceWorker) {
        console.log('[SecureWebSocket] Using Service Worker for WebSocket');
        wasmInstance = {
          engine: null,
          wasm: null,
          useServiceWorker: true
        };
        return wasmInstance;
      }

      // Fallback to direct WASM
      console.log('[SecureWebSocket] Using direct WASM');

      // Load WASM module
      if (typeof wasm_bindgen === 'undefined') {
        throw new Error('WASM module not loaded. Include portal_wasm.js first.');
      }

      // Initialize WASM
      await wasm_bindgen('/pkg/portal_wasm_bg.wasm');

      // Get relay server URL
      const relayUrl = await getRelayUrl();

      // Create ProxyEngine instance
      const engine = new wasm_bindgen.ProxyEngine(relayUrl);

      console.log('[SecureWebSocket] WASM ProxyEngine initialized:', relayUrl);

      wasmInstance = {
        engine,
        wasm: wasm_bindgen,
        useServiceWorker: false
      };

      return wasmInstance;

    } catch (error) {
      console.error('[SecureWebSocket] Failed to initialize WASM:', error);
      wasmInitPromise = null;
      throw error;
    }
  })();

  return wasmInitPromise;
}

/**
 * SecureWebSocket - Drop-in replacement for native WebSocket with E2EE
 */
class SecureWebSocket extends EventTarget {
  /**
   * @param {string} url - WebSocket URL
   * @param {string|string[]} protocols - Optional subprotocols
   */
  constructor(url, protocols = []) {
    super();

    // Normalize protocols
    if (typeof protocols === 'string') {
      protocols = [protocols];
    }

    // Public properties (read-only)
    Object.defineProperties(this, {
      url: { value: url, writable: false, enumerable: true },
      protocols: { value: protocols, writable: false, enumerable: true },
    });

    // Internal state
    this._readyState = WebSocket.CONNECTING;
    this._protocol = '';
    this._tunnelId = null;
    this._bufferedAmount = 0;
    this._extensions = '';
    this._binaryType = 'blob';

    // Event handlers (nullable)
    this.onopen = null;
    this.onmessage = null;
    this.onerror = null;
    this.onclose = null;

    // Start connection
    this._connect();
  }

  // Public properties with getters
  get readyState() { return this._readyState; }
  get protocol() { return this._protocol; }
  get bufferedAmount() { return this._bufferedAmount; }
  get extensions() { return this._extensions; }
  get binaryType() { return this._binaryType; }
  set binaryType(value) {
    if (value === 'blob' || value === 'arraybuffer') {
      this._binaryType = value;
    }
  }

  /**
   * Initialize connection through WASM ProxyEngine
   * @private
   */
  async _connect() {
    try {
      console.log('[SecureWebSocket] Connecting to:', this.url);

      // Get WASM ProxyEngine or Service Worker
      const instance = await getProxyEngine();

      if (instance.useServiceWorker) {
        // Use Service Worker
        await this._connectViaServiceWorker();
      } else {
        // Use direct WASM
        await this._connectViaDirect(instance.engine);
      }

    } catch (error) {
      console.error('[SecureWebSocket] Connection failed:', error);

      this._readyState = WebSocket.CLOSED;

      // Dispatch error event
      this._dispatchEvent('error', {
        message: error.toString(),
        error: error
      });

      // Dispatch close event
      this._dispatchEvent('close', {
        code: 1006,
        reason: error.toString(),
        wasClean: false
      });
    }
  }

  /**
   * Connect via Service Worker
   * @private
   */
  async _connectViaServiceWorker() {
    console.log('[SecureWebSocket] Connecting via Service Worker');

    const channel = new MessageChannel();
    const response = await new Promise((resolve, reject) => {
      channel.port1.onmessage = (event) => {
        if (event.data.success) {
          resolve(event.data.result);
        } else {
          reject(new Error(event.data.error));
        }
      };

      navigator.serviceWorker.controller.postMessage(
        {
          type: 'WEBSOCKET_OPEN',
          url: this.url,
          protocols: this.protocols
        },
        [channel.port2]
      );

      setTimeout(() => reject(new Error('Service Worker timeout')), 10000);
    });

    this._tunnelId = response.tunnelId;
    this._protocol = response.protocol || '';
    this._readyState = WebSocket.OPEN;
    this._useServiceWorker = true;

    console.log('[SecureWebSocket] Connected via SW! Tunnel ID:', this._tunnelId);

    // Dispatch open event
    this._dispatchEvent('open', {});

    // Listen for messages from Service Worker
    this._listenToServiceWorker();
  }

  /**
   * Connect via direct WASM
   * @private
   */
  async _connectViaDirect(engine) {
    console.log('[SecureWebSocket] Connecting via direct WASM');

    // Open WebSocket tunnel through E2EE proxy
    const result = await engine.open_websocket(this.url, this.protocols);

    this._tunnelId = result.tunnelId;
    this._protocol = result.protocol || '';
    this._readyState = WebSocket.OPEN;
    this._useServiceWorker = false;

    console.log('[SecureWebSocket] Connected! Tunnel ID:', this._tunnelId);

    // Dispatch open event
    this._dispatchEvent('open', {});

    // Start receiving messages in background
    this._receiveLoop(engine);
  }

  /**
   * Listen to Service Worker messages
   * @private
   */
  _listenToServiceWorker() {
    const handler = (event) => {
      if (event.data.type === 'WEBSOCKET_MESSAGE' &&
          event.data.tunnelId === this._tunnelId) {

        const msg = event.data.message;
        this._handleMessage(msg);
      }
    };

    navigator.serviceWorker.addEventListener('message', handler);
    this._swMessageHandler = handler;
  }

  /**
   * Handle incoming message
   * @private
   */
  _handleMessage(msg) {
    if (msg.type === 'text') {
      // Text message
      this._dispatchEvent('message', {
        data: msg.data,
        type: 'message',
        origin: this.url
      });

    } else if (msg.type === 'binary') {
      // Binary message
      let data;
      if (this._binaryType === 'arraybuffer') {
        data = new Uint8Array(msg.data).buffer;
      } else {
        data = new Blob([new Uint8Array(msg.data)]);
      }

      this._dispatchEvent('message', {
        data: data,
        type: 'message',
        origin: this.url
      });

    } else if (msg.type === 'close') {
      // Close message
      console.log('[SecureWebSocket] Received close:', msg.code, msg.reason);

      this._readyState = WebSocket.CLOSED;

      this._dispatchEvent('close', {
        code: msg.code || 1000,
        reason: msg.reason || '',
        wasClean: true
      });

      // Cleanup Service Worker listener
      if (this._swMessageHandler) {
        navigator.serviceWorker.removeEventListener('message', this._swMessageHandler);
      }
    }
  }

  /**
   * Background loop to receive messages from tunnel
   * @private
   * @param {Object} engine - WASM ProxyEngine instance
   */
  async _receiveLoop(engine) {
    try {
      while (this._readyState !== WebSocket.CLOSED && this._readyState !== WebSocket.CLOSING) {
        // Receive message from tunnel
        const msg = await engine.receive_websocket_message(this._tunnelId);

        if (msg.type === 'text') {
          // Text message
          this._dispatchEvent('message', {
            data: msg.data,
            type: 'message',
            origin: this.url
          });

        } else if (msg.type === 'binary') {
          // Binary message
          let data;
          if (this._binaryType === 'arraybuffer') {
            data = new Uint8Array(msg.data).buffer;
          } else {
            // Convert to Blob
            data = new Blob([new Uint8Array(msg.data)]);
          }

          this._dispatchEvent('message', {
            data: data,
            type: 'message',
            origin: this.url
          });

        } else if (msg.type === 'close') {
          // Close message
          console.log('[SecureWebSocket] Received close:', msg.code, msg.reason);

          this._readyState = WebSocket.CLOSED;

          this._dispatchEvent('close', {
            code: msg.code || 1000,
            reason: msg.reason || '',
            wasClean: true
          });

          break;
        }
      }

    } catch (error) {
      console.error('[SecureWebSocket] Receive loop error:', error);

      if (this._readyState !== WebSocket.CLOSED) {
        this._dispatchEvent('error', {
          message: error.toString(),
          error: error
        });

        this.close(1006, error.toString());
      }
    }
  }

  /**
   * Send data through the secure tunnel
   * @param {string|ArrayBuffer|Uint8Array|Blob} data - Data to send
   */
  send(data) {
    if (this._readyState !== WebSocket.OPEN) {
      throw new DOMException(
        'Failed to execute \'send\' on \'WebSocket\': Still in CONNECTING state.',
        'InvalidStateError'
      );
    }

    // Handle different data types
    if (typeof data === 'string') {
      // Text message
      this._sendMessage(data, false);

    } else if (data instanceof ArrayBuffer) {
      // Binary ArrayBuffer
      this._sendMessage(new Uint8Array(data), true);

    } else if (data instanceof Uint8Array) {
      // Binary Uint8Array
      this._sendMessage(data, true);

    } else if (data instanceof Blob) {
      // Blob - convert to ArrayBuffer
      this._bufferedAmount += data.size;

      data.arrayBuffer().then(buffer => {
        this._sendMessage(new Uint8Array(buffer), true);
        this._bufferedAmount = Math.max(0, this._bufferedAmount - data.size);
      });

    } else {
      throw new TypeError('Data must be string, ArrayBuffer, Uint8Array, or Blob');
    }
  }

  /**
   * Send message through WASM ProxyEngine
   * @private
   * @param {string|Uint8Array} data - Data to send
   * @param {boolean} isBinary - Whether data is binary
   */
  async _sendMessage(data, isBinary) {
    try {
      // Estimate buffer size
      const size = typeof data === 'string' ? data.length : data.length;
      this._bufferedAmount += size;

      if (this._useServiceWorker) {
        // Send via Service Worker
        const channel = new MessageChannel();
        await new Promise((resolve, reject) => {
          channel.port1.onmessage = (event) => {
            if (event.data.success) {
              resolve();
            } else {
              reject(new Error(event.data.error));
            }
          };

          navigator.serviceWorker.controller.postMessage(
            {
              type: 'WEBSOCKET_SEND',
              tunnelId: this._tunnelId,
              data: data,
              isBinary: isBinary
            },
            [channel.port2]
          );

          setTimeout(() => reject(new Error('Send timeout')), 5000);
        });
      } else {
        // Send via direct WASM
        const { engine } = await getProxyEngine();
        await engine.send_websocket_message(this._tunnelId, data, isBinary);
      }

      // Decrement buffered amount
      this._bufferedAmount = Math.max(0, this._bufferedAmount - size);

    } catch (error) {
      console.error('[SecureWebSocket] Send failed:', error);

      this._dispatchEvent('error', {
        message: error.toString(),
        error: error
      });
    }
  }

  /**
   * Close the WebSocket connection
   * @param {number} code - Close code (default 1000)
   * @param {string} reason - Close reason (default empty)
   */
  close(code = 1000, reason = '') {
    if (this._readyState === WebSocket.CLOSED || this._readyState === WebSocket.CLOSING) {
      return;
    }

    console.log('[SecureWebSocket] Closing:', code, reason);

    this._readyState = WebSocket.CLOSING;

    // Close tunnel
    (async () => {
      try {
        if (this._useServiceWorker) {
          // Close via Service Worker
          const channel = new MessageChannel();
          await new Promise((resolve, reject) => {
            channel.port1.onmessage = (event) => {
              if (event.data.success) {
                resolve();
              } else {
                reject(new Error(event.data.error));
              }
            };

            navigator.serviceWorker.controller.postMessage(
              {
                type: 'WEBSOCKET_CLOSE',
                tunnelId: this._tunnelId,
                code: code,
                reason: reason
              },
              [channel.port2]
            );

            setTimeout(() => resolve(), 2000); // Don't wait forever
          });
        } else {
          // Close via direct WASM
          const { engine } = await getProxyEngine();
          await engine.close_websocket(this._tunnelId, code, reason);
        }
      } catch (error) {
        console.error('[SecureWebSocket] Close failed:', error);

        // Force close
        this._readyState = WebSocket.CLOSED;
        this._dispatchEvent('close', {
          code: 1006,
          reason: error.toString(),
          wasClean: false
        });
      }
    })();
  }

  /**
   * Dispatch event to both EventTarget and legacy handler
   * @private
   * @param {string} type - Event type
   * @param {Object} detail - Event details
   */
  _dispatchEvent(type, detail) {
    // Create event
    const event = new Event(type);
    Object.assign(event, detail);

    // Dispatch to EventTarget listeners
    this.dispatchEvent(event);

    // Call legacy handler if exists
    const handler = this[`on${type}`];
    if (typeof handler === 'function') {
      try {
        handler.call(this, event);
      } catch (error) {
        console.error(`[SecureWebSocket] Error in on${type} handler:`, error);
      }
    }
  }
}

// Static constants (same as native WebSocket)
SecureWebSocket.CONNECTING = 0;
SecureWebSocket.OPEN = 1;
SecureWebSocket.CLOSING = 2;
SecureWebSocket.CLOSED = 3;

// ==============================================================================
// POLYFILL: Replace native WebSocket with SecureWebSocket
// ==============================================================================

(function() {
  // Save reference to native WebSocket
  const NativeWebSocket = window.WebSocket;

  // Configuration
  const config = {
    // Enable E2EE for all WebSockets by default
    enabled: window.PORTAL_E2EE_ENABLED !== false,

    // Patterns to intercept (regex strings)
    interceptPatterns: window.PORTAL_INTERCEPT_PATTERNS || [
      '.*' // Intercept all by default
    ],

    // Patterns to bypass (regex strings) - takes precedence
    bypassPatterns: window.PORTAL_BYPASS_PATTERNS || [
      '^wss?://localhost:4017/', // Don't intercept relay server itself
      '^wss?://localhost:8000/', // Don't intercept local dev server
      '^wss?://127\\.0\\.0\\.1',  // Don't intercept loopback
    ],

    // Debug mode
    debug: window.PORTAL_DEBUG || false
  };

  /**
   * Check if URL should be intercepted for E2EE
   * @param {string} url - WebSocket URL
   * @returns {boolean} True if should intercept
   */
  function shouldIntercept(url) {
    if (!config.enabled) {
      return false;
    }

    // Check bypass patterns first (higher priority)
    for (const pattern of config.bypassPatterns) {
      const regex = new RegExp(pattern);
      if (regex.test(url)) {
        if (config.debug) {
          console.log('[SecureWebSocket] Bypassing (matched bypass pattern):', url);
        }
        return false;
      }
    }

    // Check intercept patterns
    for (const pattern of config.interceptPatterns) {
      const regex = new RegExp(pattern);
      if (regex.test(url)) {
        if (config.debug) {
          console.log('[SecureWebSocket] Intercepting (matched intercept pattern):', url);
        }
        return true;
      }
    }

    if (config.debug) {
      console.log('[SecureWebSocket] Not intercepting (no match):', url);
    }
    return false;
  }

  /**
   * Polyfilled WebSocket constructor
   * @param {string} url - WebSocket URL
   * @param {string|string[]} protocols - Optional subprotocols
   * @returns {WebSocket|SecureWebSocket}
   */
  window.WebSocket = function(url, protocols) {
    if (shouldIntercept(url)) {
      // Use E2EE SecureWebSocket
      console.log('[SecureWebSocket] 🔒 Creating encrypted WebSocket:', url);
      return new SecureWebSocket(url, protocols);
    } else {
      // Use native WebSocket
      if (config.debug) {
        console.log('[SecureWebSocket] Creating native WebSocket:', url);
      }
      return new NativeWebSocket(url, protocols);
    }
  };

  // Copy static properties from native WebSocket
  window.WebSocket.CONNECTING = NativeWebSocket.CONNECTING;
  window.WebSocket.OPEN = NativeWebSocket.OPEN;
  window.WebSocket.CLOSING = NativeWebSocket.CLOSING;
  window.WebSocket.CLOSED = NativeWebSocket.CLOSED;

  // Expose SecureWebSocket class for direct access if needed
  window.SecureWebSocket = SecureWebSocket;
  window.NativeWebSocket = NativeWebSocket;

  console.log('[SecureWebSocket] ✅ Polyfill installed. E2EE enabled:', config.enabled);

})();
