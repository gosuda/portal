(function () {
  "use strict";

  // Capture reference to current script for later removal
  const currentScript = document.currentScript;

  // Save original WebSocket
  const NativeWebSocket = window.WebSocket;

  // Conditional logging helper - only log when localhost is in URL
  const isLocalhost = window.location.hostname === 'localhost' ||
                      window.location.hostname === '127.0.0.1' ||
                      window.location.hostname.endsWith('.localhost');

  function debugLog(...args) {
    if (isLocalhost) {
      console.log(...args);
    }
  }

  // Generate unique client ID
  function generateClientId() {
    return `client-${Date.now()}-${Math.random().toString(36).substr(2, 9)}`;
  }

  // Generate WebSocket key for handshake
  function generateWebSocketKey() {
    const bytes = new Uint8Array(16);
    crypto.getRandomValues(bytes);
    return btoa(String.fromCharCode(...bytes));
  }

  // Helper to validate WebSocket constructor arguments (mimics native behavior)
  function validateWebSocketArgs(url, protocols) {
    if (!url) {
      throw new DOMException("Failed to construct 'WebSocket': 1 argument required, but only 0 present.");
    }

    let parsedUrl;
    try {
      parsedUrl = new URL(url, window.location.href);
    } catch (e) {
      throw new DOMException(`Failed to construct 'WebSocket': The URL '${url}' is invalid.`);
    }

    if (parsedUrl.protocol !== 'ws:' && parsedUrl.protocol !== 'wss:') {
      throw new DOMException(`Failed to construct 'WebSocket': The URL's scheme must be either 'ws' or 'wss'. '${parsedUrl.protocol.slice(0, -1)}' is not allowed.`);
    }

    let normalizedProtocols = protocols;
    if (protocols !== undefined && protocols !== null) {
      if (typeof protocols === 'string') {
        normalizedProtocols = [protocols];
      } else if (Array.isArray(protocols)) {
        normalizedProtocols = protocols;
      } else {
        throw new DOMException("Failed to construct 'WebSocket': The subprotocol '" + protocols + "' is invalid.");
      }

      const seen = new Set();
      for (const protocol of normalizedProtocols) {
        if (typeof protocol !== 'string') {
          throw new DOMException("Failed to construct 'WebSocket': The subprotocol '" + protocol + "' is invalid.");
        }
        if (protocol === '') {
          throw new DOMException("Failed to construct 'WebSocket': The subprotocol '' is invalid.");
        }
        if (seen.has(protocol)) {
          throw new DOMException(`Failed to construct 'WebSocket': The subprotocol '${protocol}' is duplicated.`);
        }
        seen.add(protocol);
      }
    }

    return { parsedUrl, normalizedProtocols };
  }

  // WebSocket polyfill using Service Worker E2EE - extends EventTarget for native event handling
  class WebSocketPolyfill extends EventTarget {
    // WebSocket ready state constants (matching native WebSocket)
    static CONNECTING = 0;
    static OPEN = 1;
    static CLOSING = 2;
    static CLOSED = 3;

    constructor(url, protocols) {
      super(); // Initialize EventTarget

      // Validate arguments using native-like validation
      const { parsedUrl, normalizedProtocols } = validateWebSocketArgs(url, protocols);

      // Store original URL and protocols
      this._url = parsedUrl.href;
      this._protocols = normalizedProtocols;
      this._parsedUrl = parsedUrl;

      // Define read-only properties to match native WebSocket
      Object.defineProperty(this, 'url', {
        get: () => this._url,
        enumerable: true,
        configurable: true
      });

      Object.defineProperty(this, 'readyState', {
        get: () => this._readyState,
        enumerable: true,
        configurable: true
      });

      Object.defineProperty(this, 'bufferedAmount', {
        get: () => this._bufferedAmount,
        enumerable: true,
        configurable: true
      });

      Object.defineProperty(this, 'extensions', {
        get: () => this._extensions,
        enumerable: true,
        configurable: true
      });

      Object.defineProperty(this, 'protocol', {
        get: () => this._protocol,
        enumerable: true,
        configurable: true
      });

      // Internal state
      this._readyState = WebSocketPolyfill.CONNECTING;
      this._bufferedAmount = 0;
      this._extensions = "";
      this._protocol = "";
      this.binaryType = "blob";

      // Event handlers (use native-like pattern)
      this.onopen = null;
      this.onmessage = null;
      this.onerror = null;
      this.onclose = null;

      // Internal connection state
      this._clientId = generateClientId();
      this._connId = null;
      this._isClosed = false;
      this._wsKey = generateWebSocketKey();
      this._frameBuffer = new Uint8Array(0);

      // Setup and connect
      this._setupMessageListener();
      this._connect();
    }

    // Helper to send messages to Service Worker
    _postToServiceWorker(message) {
      navigator.serviceWorker.controller.postMessage({
        clientId: this._clientId,
        connId: this._connId,
        ...message
      });
    }

    _setupMessageListener() {
      navigator.serviceWorker.addEventListener("message", (event) => {
        const data = event.data;

        // Only handle messages for this client
        if (data.clientId !== this._clientId) {
          return;
        }

        switch (data.type) {
          case "SDK_CONNECT_SUCCESS":
            this._handleConnectSuccess(data);
            break;
          case "SDK_CONNECT_ERROR":
            this._handleConnectError(data);
            break;
          case "SDK_DATA":
            this._handleData(data);
            break;
          case "SDK_DATA_CLOSE":
            this._handleDataClose(data);
            break;
          case "SDK_SEND_ERROR":
            this._handleSendError(data);
            break;
        }
      });
    }

    async _connect() {
      debugLog("[WebSocket Polyfill] Connecting via Service Worker SDK to:", this._url);
      try {
        // Extract and normalize hostname (already parsed in constructor)
        let hostname = this._parsedUrl.hostname;

        // Normalize punycode to lowercase (punycode is case-insensitive per RFC 3492)
        // This ensures XN--CW4B85OB9G becomes xn--cw4b85ob9g before processing
        hostname = hostname.toLowerCase();

        // Extract first label (keep lowercase for punycode conversion in Go backend)
        // Go backend will convert punycode->unicode and then uppercase
        const leaseName = hostname.split('.')[0];

        debugLog("[WebSocket Polyfill] Lease name:", leaseName);

        // Wait for Service Worker to be ready
        await navigator.serviceWorker.ready;

        // Send connect message to Service Worker (note: connId not set yet, so we manually include clientId)
        navigator.serviceWorker.controller.postMessage({
          type: "SDK_CONNECT",
          clientId: this._clientId,
          leaseName: leaseName,
        });

      } catch (error) {
        console.error("[WebSocket Polyfill] Failed to connect:", error);
        this._handleError(new Error(error));
      }
    }

    _handleConnectSuccess(data) {
      this._connId = data.connId;

      debugLog("[WebSocket Polyfill] E2EE connection established, sending WebSocket upgrade");

      // Send WebSocket HTTP Upgrade request
      this._sendWebSocketUpgrade();
    }

    _sendWebSocketUpgrade() {
      // Parse URL to get path with query parameters
      const path = (this._parsedUrl.pathname || "/") + (this._parsedUrl.search || "");
      const host = this._parsedUrl.host;

      // Build HTTP Upgrade request
      let upgradeRequest = `GET ${path} HTTP/1.1\r\n`;
      upgradeRequest += `Host: ${host}\r\n`;
      upgradeRequest += `Upgrade: websocket\r\n`;
      upgradeRequest += `Connection: Upgrade\r\n`;
      upgradeRequest += `Sec-WebSocket-Key: ${this._wsKey}\r\n`;
      upgradeRequest += `Sec-WebSocket-Version: 13\r\n`;

      if (this._protocols) {
        const protocolStr = Array.isArray(this._protocols)
          ? this._protocols.join(', ')
          : this._protocols;
        upgradeRequest += `Sec-WebSocket-Protocol: ${protocolStr}\r\n`;
      }

      upgradeRequest += `\r\n`;

      debugLog("[WebSocket Polyfill] Sending upgrade request:", upgradeRequest);

      // Convert to bytes and send
      const encoder = new TextEncoder();
      const bytes = encoder.encode(upgradeRequest);

      this._postToServiceWorker({
        type: "SDK_SEND",
        data: bytes,
      });

      // Wait for upgrade response in _handleData
      this._waitingForUpgrade = true;
      this._upgradeBuffer = new Uint8Array(0);
    }

    _handleConnectError(data) {
      console.error("[WebSocket Polyfill] Connection error:", data.error);
      this._handleError(new Error(data.error));
    }

    _handleData(data) {
      const uint8Array = data.data;

      // If waiting for upgrade response, buffer and parse HTTP response
      if (this._waitingForUpgrade) {
        // Append to buffer
        const newBuffer = new Uint8Array(this._upgradeBuffer.length + uint8Array.length);
        newBuffer.set(this._upgradeBuffer);
        newBuffer.set(uint8Array, this._upgradeBuffer.length);
        this._upgradeBuffer = newBuffer;

        // Try to parse HTTP response
        const decoder = new TextDecoder();
        const text = decoder.decode(this._upgradeBuffer);

        // Look for end of HTTP headers (\r\n\r\n)
        const headerEndIndex = text.indexOf('\r\n\r\n');
        if (headerEndIndex === -1) {
          // Not complete yet, keep buffering
          return;
        }

        // Parse HTTP response
        const headers = text.substring(0, headerEndIndex);
        debugLog("[WebSocket Polyfill] Received upgrade response:", headers);

        // Check if upgrade was successful
        if (!headers.includes('HTTP/1.1 101') && !headers.includes('HTTP/1.0 101')) {
          this._handleError(new Error("WebSocket upgrade failed: " + headers.split('\r\n')[0]));
          return;
        }

        // Extract protocol if present
        const protocolMatch = headers.match(/Sec-WebSocket-Protocol:\s*(\S+)/i);
        if (protocolMatch) {
          this._protocol = protocolMatch[1];
        }

        // Upgrade successful!
        this._waitingForUpgrade = false;
        this._readyState = WebSocketPolyfill.OPEN;

        debugLog("[WebSocket Polyfill] WebSocket connection established");

        // Fire onopen event (dispatchEvent will handle both onopen and listeners)
        this.dispatchEvent(new Event("open"));

        // If there's any data after the headers, process it as WebSocket frames
        const remainingBytes = this._upgradeBuffer.slice(headerEndIndex + 4);
        if (remainingBytes.length > 0) {
          this._processWebSocketFrames(remainingBytes);
        }
        this._upgradeBuffer = null;

        return;
      }

      // Normal WebSocket data - process frames
      this._processWebSocketFrames(uint8Array);
    }

    _processWebSocketFrames(data) {
      // For now, assume data is the payload (we'll implement frame parsing if needed)
      // WebSocket frames from server are not masked

      if (this._readyState !== WebSocketPolyfill.OPEN) return;

      // Append incoming data to frame buffer
      const newBuffer = new Uint8Array(this._frameBuffer.length + data.length);
      newBuffer.set(this._frameBuffer);
      newBuffer.set(data, this._frameBuffer.length);
      this._frameBuffer = newBuffer;

      // Process all complete frames in buffer
      while (this._frameBuffer.length >= 2) {
        const byte1 = this._frameBuffer[0];
        const byte2 = this._frameBuffer[1];

        const fin = (byte1 & 0x80) !== 0;
        const opcode = byte1 & 0x0F;
        const masked = (byte2 & 0x80) !== 0;
        let payloadLen = byte2 & 0x7F;

        let offset = 2;

        // Handle extended payload length
        if (payloadLen === 126) {
          if (this._frameBuffer.length < 4) return; // Need more data
          payloadLen = (this._frameBuffer[2] << 8) | this._frameBuffer[3];
          offset = 4;
        } else if (payloadLen === 127) {
          if (this._frameBuffer.length < 10) return; // Need more data
          // For simplicity, assuming payload < 2^32
          payloadLen = (this._frameBuffer[6] << 24) | (this._frameBuffer[7] << 16) | (this._frameBuffer[8] << 8) | this._frameBuffer[9];
          offset = 10;
        }

        // Server messages should not be masked
        if (masked) {
          offset += 4; // Skip mask key
        }

        if (this._frameBuffer.length < offset + payloadLen) {
          // Incomplete frame, wait for more data
          return;
        }

        const payload = this._frameBuffer.slice(offset, offset + payloadLen);

        // Remove processed frame from buffer
        this._frameBuffer = this._frameBuffer.slice(offset + payloadLen);

        // Handle different opcodes
        if (opcode === 0x01) {
          // Text frame
          const text = new TextDecoder().decode(payload);
          const event = new MessageEvent("message", {
            data: text,
            origin: this._parsedUrl.origin,
          });
          this.dispatchEvent(event);
        } else if (opcode === 0x02) {
          // Binary frame
          let eventData;
          if (this.binaryType === "blob") {
            eventData = new Blob([payload]);
          } else {
            eventData = payload.buffer;
          }
          const event = new MessageEvent("message", {
            data: eventData,
            origin: this._parsedUrl.origin,
          });
          this.dispatchEvent(event);
        } else if (opcode === 0x08) {
          // Close frame
          let code = 1000;
          let reason = "";
          if (payload.length >= 2) {
            code = (payload[0] << 8) | payload[1];
            if (payload.length > 2) {
              reason = new TextDecoder().decode(payload.slice(2));
            }
          }
          this._handleDataClose({ code, reason });
        } else if (opcode === 0x09) {
          // Ping - send pong
          this._sendPong(payload);
        } else if (opcode === 0x0A) {
          // Pong - ignore
        }
      }
    }

    _sendPong(payload) {
      // Send pong frame
      const frame = this._createWebSocketFrame(0x0A, payload);
      this._postToServiceWorker({
        type: "SDK_SEND",
        data: frame,
      });
    }

    _createWebSocketFrame(opcode, payload) {
      // Create WebSocket frame (client to server, must be masked)
      const payloadLen = payload.length;
      let frameHeader;
      let offset;

      if (payloadLen < 126) {
        frameHeader = new Uint8Array(2 + 4 + payloadLen);
        frameHeader[0] = 0x80 | opcode; // FIN + opcode
        frameHeader[1] = 0x80 | payloadLen; // MASK + length
        offset = 2;
      } else if (payloadLen < 65536) {
        frameHeader = new Uint8Array(4 + 4 + payloadLen);
        frameHeader[0] = 0x80 | opcode;
        frameHeader[1] = 0x80 | 126;
        frameHeader[2] = (payloadLen >> 8) & 0xFF;
        frameHeader[3] = payloadLen & 0xFF;
        offset = 4;
      } else {
        frameHeader = new Uint8Array(10 + 4 + payloadLen);
        frameHeader[0] = 0x80 | opcode;
        frameHeader[1] = 0x80 | 127;
        // Simplified: assuming payload < 2^32
        frameHeader[2] = 0;
        frameHeader[3] = 0;
        frameHeader[4] = 0;
        frameHeader[5] = 0;
        frameHeader[6] = (payloadLen >> 24) & 0xFF;
        frameHeader[7] = (payloadLen >> 16) & 0xFF;
        frameHeader[8] = (payloadLen >> 8) & 0xFF;
        frameHeader[9] = payloadLen & 0xFF;
        offset = 10;
      }

      // Generate masking key
      const maskKey = new Uint8Array(4);
      crypto.getRandomValues(maskKey);
      frameHeader.set(maskKey, offset);

      // Mask payload
      const maskedPayload = new Uint8Array(payloadLen);
      for (let i = 0; i < payloadLen; i++) {
        maskedPayload[i] = payload[i] ^ maskKey[i % 4];
      }

      frameHeader.set(maskedPayload, offset + 4);
      return frameHeader;
    }

    _handleDataClose(data) {
      if (this._isClosed) return;

      const code = data.code || 1000;
      const reason = data.reason || "";

      debugLog(
        "[WebSocket Polyfill] Connection closed, code:",
        code,
        "reason:",
        reason
      );

      this._isClosed = true;
      this._readyState = WebSocketPolyfill.CLOSED;

      const event = new CloseEvent("close", {
        code: code,
        reason: reason,
        wasClean: code === 1000,
      });

      // dispatchEvent will handle both onclose and event listeners
      this.dispatchEvent(event);
    }

    _handleSendError(data) {
      console.error("[WebSocket Polyfill] Send error:", data.error);
      this._handleError(new Error(data.error));
    }

    _handleError(error) {
      console.error("[WebSocket Polyfill] Error occurred:", error);

      const event = new Event("error");
      event.error = error;

      // dispatchEvent will handle both onerror and event listeners
      this.dispatchEvent(event);

      // Close connection after error
      if (!this._isClosed) {
        this._handleDataClose({ code: 1006, reason: error.message });
      }
    }

    send(data) {
      if (this._readyState !== WebSocketPolyfill.OPEN) {
        throw new DOMException("Failed to execute 'send' on 'WebSocket': Still in CONNECTING state.");
      }

      if (!this._connId) {
        throw new Error("Connection not established");
      }

      try {
        // Convert data to Uint8Array
        let bytes;
        let opcode;

        if (typeof data === "string") {
          const encoder = new TextEncoder();
          bytes = encoder.encode(data);
          opcode = 0x01; // Text frame
        } else if (data instanceof ArrayBuffer) {
          bytes = new Uint8Array(data);
          opcode = 0x02; // Binary frame
        } else if (data instanceof Uint8Array) {
          bytes = data;
          opcode = 0x02; // Binary frame
        } else if (data instanceof Blob) {
          // Handle Blob asynchronously
          data.arrayBuffer().then(arrayBuffer => {
            const bytes = new Uint8Array(arrayBuffer);
            const frame = this._createWebSocketFrame(0x02, bytes);
            this._postToServiceWorker({
              type: "SDK_SEND",
              data: frame,
            });
          });
          return;
        } else {
          throw new Error("Unsupported data type");
        }

        // Create WebSocket frame
        const frame = this._createWebSocketFrame(opcode, bytes);

        // Send to Service Worker
        this._postToServiceWorker({
          type: "SDK_SEND",
          data: frame,
        });
      } catch (error) {
        console.error("[WebSocket Polyfill] Failed to send message:", error);
        this._handleError(error);
      }
    }

    close(code = 1000, reason = "") {
      if (this._isClosed || this._readyState === WebSocketPolyfill.CLOSING) {
        return;
      }

      debugLog(
        "[WebSocket Polyfill] Client initiated close, code:",
        code,
        "reason:",
        reason
      );

      this._readyState = WebSocketPolyfill.CLOSING;

      if (this._connId && this._readyState === WebSocketPolyfill.OPEN) {
        // Send WebSocket close frame
        const reasonBytes = new TextEncoder().encode(reason);
        const payload = new Uint8Array(2 + reasonBytes.length);
        payload[0] = (code >> 8) & 0xFF;
        payload[1] = code & 0xFF;
        payload.set(reasonBytes, 2);

        const frame = this._createWebSocketFrame(0x08, payload);

        this._postToServiceWorker({
          type: "SDK_SEND",
          data: frame,
        });
      }

      // Close SDK connection
      if (this._connId) {
        this._postToServiceWorker({
          type: "SDK_CLOSE",
        });
      }

      // Handle close locally
      this._handleDataClose({ code, reason });
    }

    // Override dispatchEvent to handle onXXX handlers (EventTarget provides addEventListener/removeEventListener)
    dispatchEvent(event) {
      // Call onXXX handler first (matches native WebSocket behavior)
      const handlerName = 'on' + event.type;
      if (typeof this[handlerName] === 'function') {
        try {
          this[handlerName].call(this, event);
        } catch (e) {
          console.error('Error in event handler:', e);
        }
      }

      // Use native EventTarget.dispatchEvent for event listeners
      return super.dispatchEvent(event);
    }
  }

  // Check if URL is same-origin
  function isSameOrigin(url) {
    try {
      const wsUrl = new URL(url, window.location.href);
      const currentOrigin = window.location.origin;

      // Convert ws:// to http:// and wss:// to https:// for comparison
      let wsOrigin = wsUrl.origin;
      if (wsUrl.protocol === "ws:") {
        wsOrigin = wsOrigin.replace("ws:", "http:");
      } else if (wsUrl.protocol === "wss:") {
        wsOrigin = wsOrigin.replace("wss:", "https:");
      }

      return wsOrigin === currentOrigin;
    } catch (e) {
      return false;
    }
  }

  // Replace WebSocket with a simple factory function (zero overhead after construction)
  window.WebSocket = function WebSocket(url, protocols) {
    // Use polyfill for same-origin, native for cross-origin
    if (isSameOrigin(url)) {
      debugLog(
        "[WebSocket Polyfill] Using E2EE polyfill for same-origin connection:",
        url
      );
      return new WebSocketPolyfill(url, protocols);
    } else {
      debugLog(
        "[WebSocket Polyfill] Using native WebSocket for cross-origin connection:",
        url
      );
      return new NativeWebSocket(url, protocols);
    }
  };

  // Copy static constants from WebSocketPolyfill
  // Essential for: WebSocket.CONNECTING, WebSocket.OPEN, WebSocket.CLOSING, WebSocket.CLOSED
  window.WebSocket.CONNECTING = WebSocketPolyfill.CONNECTING; // 0
  window.WebSocket.OPEN = WebSocketPolyfill.OPEN;             // 1
  window.WebSocket.CLOSING = WebSocketPolyfill.CLOSING;       // 2
  window.WebSocket.CLOSED = WebSocketPolyfill.CLOSED;         // 3

  debugLog("[WebSocket Polyfill] Initialized with E2EE and WebSocket protocol support");

  // Remove the polyfill script tag after initialization
  if (currentScript && currentScript.parentNode) {
    currentScript.parentNode.removeChild(currentScript);
    debugLog("[WebSocket Polyfill] Script tag removed");
  }
})();
