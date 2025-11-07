(function () {
  "use strict";

  // Capture reference to current script for later removal
  const currentScript = document.currentScript;

  // Save original WebSocket
  const NativeWebSocket = window.WebSocket;

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

  // WebSocket polyfill using Service Worker E2EE
  class WebSocketPolyfill {
    constructor(url, protocols) {
      this.url = url;
      this.protocols = protocols;
      this.readyState = WebSocket.CONNECTING;
      this.bufferedAmount = 0;
      this.extensions = "";
      this.protocol = "";
      this.binaryType = "blob";

      // Event handlers
      this.onopen = null;
      this.onmessage = null;
      this.onerror = null;
      this.onclose = null;

      // Internal state
      this._clientId = generateClientId();
      this._connId = null;
      this._isClosed = false;
      this._wsKey = generateWebSocketKey();

      // Setup Service Worker message listener
      this._setupMessageListener();

      // Initialize connection
      this._connect();
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
      console.log("[WebSocket Polyfill] Connecting via Service Worker SDK to:", this.url);
      try {
        // Extract hostname from URL
        const urlObj = new URL(this.url);
        const hostname = urlObj.hostname;
        const leaseName = hostname.split('.')[0].toUpperCase();

        console.log("[WebSocket Polyfill] Lease name:", leaseName);

        // Wait for Service Worker to be ready
        await navigator.serviceWorker.ready;

        // Send connect message to Service Worker
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

      console.log("[WebSocket Polyfill] E2EE connection established, sending WebSocket upgrade");

      // Send WebSocket HTTP Upgrade request
      this._sendWebSocketUpgrade();
    }

    _sendWebSocketUpgrade() {
      // Parse URL to get path
      const urlObj = new URL(this.url);
      const path = urlObj.pathname || "/";
      const host = urlObj.host;

      // Build HTTP Upgrade request
      let upgradeRequest = `GET ${path} HTTP/1.1\r\n`;
      upgradeRequest += `Host: ${host}\r\n`;
      upgradeRequest += `Upgrade: websocket\r\n`;
      upgradeRequest += `Connection: Upgrade\r\n`;
      upgradeRequest += `Sec-WebSocket-Key: ${this._wsKey}\r\n`;
      upgradeRequest += `Sec-WebSocket-Version: 13\r\n`;

      if (this.protocols) {
        const protocolStr = Array.isArray(this.protocols)
          ? this.protocols.join(', ')
          : this.protocols;
        upgradeRequest += `Sec-WebSocket-Protocol: ${protocolStr}\r\n`;
      }

      upgradeRequest += `\r\n`;

      console.log("[WebSocket Polyfill] Sending upgrade request:", upgradeRequest);

      // Convert to bytes and send
      const encoder = new TextEncoder();
      const bytes = encoder.encode(upgradeRequest);

      navigator.serviceWorker.controller.postMessage({
        type: "SDK_SEND",
        clientId: this._clientId,
        connId: this._connId,
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
        console.log("[WebSocket Polyfill] Received upgrade response:", headers);

        // Check if upgrade was successful
        if (!headers.includes('HTTP/1.1 101') && !headers.includes('HTTP/1.0 101')) {
          this._handleError(new Error("WebSocket upgrade failed: " + headers.split('\r\n')[0]));
          return;
        }

        // Extract protocol if present
        const protocolMatch = headers.match(/Sec-WebSocket-Protocol:\s*(\S+)/i);
        if (protocolMatch) {
          this.protocol = protocolMatch[1];
        }

        // Upgrade successful!
        this._waitingForUpgrade = false;
        this.readyState = WebSocket.OPEN;

        console.log("[WebSocket Polyfill] WebSocket connection established");

        // Fire onopen event
        if (this.onopen) {
          this.onopen(new Event("open"));
        }
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

      if (this.readyState !== WebSocket.OPEN) return;

      // Simple frame parsing - check if this is a complete frame
      if (data.length < 2) return;

      const byte1 = data[0];
      const byte2 = data[1];

      const fin = (byte1 & 0x80) !== 0;
      const opcode = byte1 & 0x0F;
      const masked = (byte2 & 0x80) !== 0;
      let payloadLen = byte2 & 0x7F;

      let offset = 2;

      // Handle extended payload length
      if (payloadLen === 126) {
        if (data.length < 4) return; // Need more data
        payloadLen = (data[2] << 8) | data[3];
        offset = 4;
      } else if (payloadLen === 127) {
        if (data.length < 10) return; // Need more data
        // For simplicity, assuming payload < 2^32
        payloadLen = (data[6] << 24) | (data[7] << 16) | (data[8] << 8) | data[9];
        offset = 10;
      }

      // Server messages should not be masked
      if (masked) {
        offset += 4; // Skip mask key
      }

      if (data.length < offset + payloadLen) {
        // Incomplete frame, buffer it
        // TODO: Implement frame buffering
        return;
      }

      const payload = data.slice(offset, offset + payloadLen);

      // Handle different opcodes
      if (opcode === 0x01) {
        // Text frame
        const text = new TextDecoder().decode(payload);
        const event = new MessageEvent("message", {
          data: text,
          origin: new URL(this.url).origin,
        });
        if (this.onmessage) {
          this.onmessage(event);
        }
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
          origin: new URL(this.url).origin,
        });
        if (this.onmessage) {
          this.onmessage(event);
        }
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

    _sendPong(payload) {
      // Send pong frame
      const frame = this._createWebSocketFrame(0x0A, payload);
      navigator.serviceWorker.controller.postMessage({
        type: "SDK_SEND",
        clientId: this._clientId,
        connId: this._connId,
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

      console.log(
        "[WebSocket Polyfill] Connection closed, code:",
        code,
        "reason:",
        reason
      );

      this._isClosed = true;
      this.readyState = WebSocket.CLOSED;

      const event = new CloseEvent("close", {
        code: code,
        reason: reason,
        wasClean: code === 1000,
      });

      if (this.onclose) {
        this.onclose(event);
      }
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

      if (this.onerror) {
        this.onerror(event);
      }
      this.dispatchEvent(event);

      // Close connection after error
      if (!this._isClosed) {
        this._handleDataClose({ code: 1006, reason: error.message });
      }
    }

    send(data) {
      if (this.readyState !== WebSocket.OPEN) {
        throw new Error("WebSocket is not open");
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
            navigator.serviceWorker.controller.postMessage({
              type: "SDK_SEND",
              clientId: this._clientId,
              connId: this._connId,
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
        navigator.serviceWorker.controller.postMessage({
          type: "SDK_SEND",
          clientId: this._clientId,
          connId: this._connId,
          data: frame,
        });
      } catch (error) {
        console.error("[WebSocket Polyfill] Failed to send message:", error);
        this._handleError(error);
      }
    }

    close(code = 1000, reason = "") {
      if (this._isClosed || this.readyState === WebSocket.CLOSING) {
        return;
      }

      console.log(
        "[WebSocket Polyfill] Client initiated close, code:",
        code,
        "reason:",
        reason
      );

      this.readyState = WebSocket.CLOSING;

      if (this._connId && this.readyState === WebSocket.OPEN) {
        // Send WebSocket close frame
        const reasonBytes = new TextEncoder().encode(reason);
        const payload = new Uint8Array(2 + reasonBytes.length);
        payload[0] = (code >> 8) & 0xFF;
        payload[1] = code & 0xFF;
        payload.set(reasonBytes, 2);

        const frame = this._createWebSocketFrame(0x08, payload);

        navigator.serviceWorker.controller.postMessage({
          type: "SDK_SEND",
          clientId: this._clientId,
          connId: this._connId,
          data: frame,
        });
      }

      // Close SDK connection
      if (this._connId) {
        navigator.serviceWorker.controller.postMessage({
          type: "SDK_CLOSE",
          clientId: this._clientId,
          connId: this._connId,
        });
      }

      // Handle close locally
      this._handleDataClose({ code, reason });
    }

    // EventTarget implementation
    addEventListener(type, listener) {
      if (!this._listeners) {
        this._listeners = {};
      }
      if (!this._listeners[type]) {
        this._listeners[type] = [];
      }
      this._listeners[type].push(listener);
    }

    removeEventListener(type, listener) {
      if (!this._listeners || !this._listeners[type]) {
        return;
      }
      const index = this._listeners[type].indexOf(listener);
      if (index !== -1) {
        this._listeners[type].splice(index, 1);
      }
    }

    dispatchEvent(event) {
      if (!this._listeners || !this._listeners[event.type]) {
        return true;
      }
      this._listeners[event.type].forEach((listener) => {
        listener.call(this, event);
      });
      return true;
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

  // Replace WebSocket with polyfill
  window.WebSocket = function (url, protocols) {
    // Use polyfill for same-origin, native for cross-origin
    if (isSameOrigin(url)) {
      console.log(
        "[WebSocket Polyfill] Using E2EE polyfill for same-origin connection:",
        url
      );
      return new WebSocketPolyfill(url, protocols);
    } else {
      console.log(
        "[WebSocket Polyfill] Using native WebSocket for cross-origin connection:",
        url
      );
      return new NativeWebSocket(url, protocols);
    }
  };

  // Copy static properties
  window.WebSocket.CONNECTING = NativeWebSocket.CONNECTING;
  window.WebSocket.OPEN = NativeWebSocket.OPEN;
  window.WebSocket.CLOSING = NativeWebSocket.CLOSING;
  window.WebSocket.CLOSED = NativeWebSocket.CLOSED;

  console.log("[WebSocket Polyfill] Initialized with E2EE and WebSocket protocol support");

  // Remove the polyfill script tag after initialization
  if (currentScript && currentScript.parentNode) {
    currentScript.parentNode.removeChild(currentScript);
    console.log("[WebSocket Polyfill] Script tag removed");
  }
})();
