/**
 * SecureWebSocket for Service Worker
 *
 * SecureWebSocket implementation for use in Service Worker
 * Communicates with main thread via MessageChannel
 */

// Service Worker global ProxyEngine (initialized in sw-proxy.js)
// proxyEngine and wasmReady are provided by sw-proxy.js

/**
 * WebSocket tunnel manager in Service Worker
 */
class ServiceWorkerWebSocketTunnel {
  constructor() {
    this.tunnels = new Map(); // tunnelId -> tunnel info
    this.messageQueues = new Map(); // tunnelId -> message queue
    this.clients = new Map(); // tunnelId -> clientId
  }

  /**
   * Create WebSocket tunnel
   */
  async createTunnel(url, protocols, clientId) {
    if (!wasmReady || !proxyEngine) {
      throw new Error('WASM ProxyEngine not ready');
    }

    console.log('[SW-WebSocket] Creating tunnel:', url);

    try {
      // Open WebSocket tunnel via WASM ProxyEngine
      const result = await proxyEngine.open_websocket(url, protocols || []);
      const tunnelId = result.tunnelId;
      const protocol = result.protocol || '';

      console.log('[SW-WebSocket] Tunnel created:', tunnelId);

      // Store tunnel information
      this.tunnels.set(tunnelId, {
        tunnelId,
        url,
        protocol,
        state: 'open',
        created: Date.now()
      });

      this.messageQueues.set(tunnelId, []);
      this.clients.set(tunnelId, clientId);

      // Start receiving messages in background
      this._startReceiving(tunnelId);

      return {
        tunnelId,
        protocol
      };

    } catch (error) {
      console.error('[SW-WebSocket] Failed to create tunnel:', error);
      throw error;
    }
  }

  /**
   * Background message receiving loop
   */
  async _startReceiving(tunnelId) {
    console.log('[SW-WebSocket] Starting receive loop:', tunnelId);

    try {
      while (this.tunnels.has(tunnelId)) {
        const tunnel = this.tunnels.get(tunnelId);
        if (!tunnel || tunnel.state !== 'open') {
          break;
        }

        // Receive message from WASM
        const msg = await proxyEngine.receive_websocket_message(tunnelId);

        console.log('[SW-WebSocket] Received message:', msg.type);

        // Add to message queue
        const queue = this.messageQueues.get(tunnelId);
        if (queue) {
          queue.push(msg);
        }

        // Notify client
        this._notifyClient(tunnelId, msg);

        // Handle close message
        if (msg.type === 'close') {
          console.log('[SW-WebSocket] Tunnel closed:', tunnelId);
          this._closeTunnel(tunnelId);
          break;
        }
      }

    } catch (error) {
      console.error('[SW-WebSocket] Receive loop error:', error);
      this._closeTunnel(tunnelId, 1006, error.toString());
    }
  }

  /**
   * Notify client of message
   */
  async _notifyClient(tunnelId, message) {
    const clientId = this.clients.get(tunnelId);
    if (!clientId) return;

    try {
      const client = await self.clients.get(clientId);
      if (client) {
        client.postMessage({
          type: 'WEBSOCKET_MESSAGE',
          tunnelId,
          message
        });
      }
    } catch (error) {
      console.error('[SW-WebSocket] Failed to notify client:', error);
    }
  }

  /**
   * Send message
   */
  async sendMessage(tunnelId, data, isBinary) {
    if (!this.tunnels.has(tunnelId)) {
      throw new Error('Tunnel not found: ' + tunnelId);
    }

    console.log('[SW-WebSocket] Sending message:', tunnelId, isBinary ? 'binary' : 'text');

    try {
      await proxyEngine.send_websocket_message(tunnelId, data, isBinary);
    } catch (error) {
      console.error('[SW-WebSocket] Send failed:', error);
      throw error;
    }
  }

  /**
   * Close tunnel
   */
  async closeTunnel(tunnelId, code = 1000, reason = '') {
    if (!this.tunnels.has(tunnelId)) {
      return;
    }

    console.log('[SW-WebSocket] Closing tunnel:', tunnelId, code, reason);

    try {
      await proxyEngine.close_websocket(tunnelId, code, reason);
    } catch (error) {
      console.error('[SW-WebSocket] Close failed:', error);
    }

    this._closeTunnel(tunnelId);
  }

  /**
   * Internal tunnel cleanup
   */
  _closeTunnel(tunnelId, code = 1000, reason = '') {
    const tunnel = this.tunnels.get(tunnelId);
    if (tunnel) {
      tunnel.state = 'closed';
    }

    // Cleanup
    this.tunnels.delete(tunnelId);
    this.messageQueues.delete(tunnelId);
    this.clients.delete(tunnelId);

    console.log('[SW-WebSocket] Tunnel cleaned up:', tunnelId);
  }

  /**
   * Get tunnel state
   */
  getTunnelState(tunnelId) {
    const tunnel = this.tunnels.get(tunnelId);
    return tunnel ? tunnel.state : 'closed';
  }

  /**
   * Get all tunnel information
   */
  getAllTunnels() {
    return Array.from(this.tunnels.values());
  }
}

// Global tunnel manager instance
let tunnelManager = null;

/**
 * Initialize tunnel manager
 */
function initTunnelManager() {
  if (!tunnelManager) {
    tunnelManager = new ServiceWorkerWebSocketTunnel();
    console.log('[SW-WebSocket] Tunnel manager initialized');
  }
  return tunnelManager;
}

/**
 * WebSocket message handler to add to Service Worker
 */
async function handleWebSocketMessage(event) {
  const { type, tunnelId, url, protocols, data, isBinary, code, reason } = event.data || {};
  const manager = initTunnelManager();

  switch (type) {
    case 'WEBSOCKET_OPEN':
      try {
        const clientId = event.source?.id || event.clientId;
        const result = await manager.createTunnel(url, protocols, clientId);

        event.ports[0]?.postMessage({
          success: true,
          result
        });
      } catch (error) {
        event.ports[0]?.postMessage({
          success: false,
          error: error.toString()
        });
      }
      break;

    case 'WEBSOCKET_SEND':
      try {
        await manager.sendMessage(tunnelId, data, isBinary);
        event.ports[0]?.postMessage({ success: true });
      } catch (error) {
        event.ports[0]?.postMessage({
          success: false,
          error: error.toString()
        });
      }
      break;

    case 'WEBSOCKET_CLOSE':
      try {
        await manager.closeTunnel(tunnelId, code, reason);
        event.ports[0]?.postMessage({ success: true });
      } catch (error) {
        event.ports[0]?.postMessage({
          success: false,
          error: error.toString()
        });
      }
      break;

    case 'WEBSOCKET_STATE':
      const state = manager.getTunnelState(tunnelId);
      event.ports[0]?.postMessage({
        success: true,
        state
      });
      break;

    case 'WEBSOCKET_LIST':
      const tunnels = manager.getAllTunnels();
      event.ports[0]?.postMessage({
        success: true,
        tunnels
      });
      break;

    default:
      return false; // Not handled
  }

  return true; // Handled
}

console.log('[SW-WebSocket] Service Worker WebSocket module loaded');
