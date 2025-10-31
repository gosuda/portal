(function() {
    'use strict';
    
    // Save original WebSocket
    const NativeWebSocket = window.WebSocket;
    
    // WebSocket polyfill using HTTP
    class WebSocketPolyfill {
        constructor(url, protocols) {
            this.url = url;
            this.protocols = protocols;
            this.readyState = WebSocket.CONNECTING;
            this.bufferedAmount = 0;
            this.extensions = '';
            this.protocol = '';
            this.binaryType = 'blob';
            
            // Event handlers
            this.onopen = null;
            this.onmessage = null;
            this.onerror = null;
            this.onclose = null;
            
            // Internal state
            this._connId = null;
            this._sendQueue = [];
            this._isSending = false;
            this._streamAbortController = null;
            this._isClosed = false;
            
            // Initialize connection
            this._connect();
        }
        
        async _connect() {
            try {
                // Send connect request
                const response = await fetch('/sw-cgi/websocket/connect', {
                    method: 'POST',
                    headers: {
                        'Content-Type': 'application/json',
                    },
                    body: JSON.stringify({
                        url: this.url,
                        protocols: Array.isArray(this.protocols) ? this.protocols : (this.protocols ? [this.protocols] : [])
                    })
                });
                
                if (!response.ok) {
                    throw new Error(`Connection failed: ${response.status} ${response.statusText}`);
                }
                
                const result = await response.json();
                this._connId = result.connId;
                this.protocol = result.protocol || '';
                
                // Update state
                this.readyState = WebSocket.OPEN;
                
                // Fire onopen event
                if (this.onopen) {
                    this.onopen(new Event('open'));
                }
                this.dispatchEvent(new Event('open'));
                
                // Start receiving messages
                this._startStream();
                
            } catch (error) {
                this._handleError(error);
            }
        }
        
        async _startStream() {
            if (!this._connId || this._isClosed) return;
            
            try {
                this._streamAbortController = new AbortController();
                
                const response = await fetch(`/sw-cgi/websocket/stream/${this._connId}`, {
                    method: 'GET',
                    signal: this._streamAbortController.signal
                });
                
                if (!response.ok) {
                    throw new Error(`Stream failed: ${response.status}`);
                }
                
                const reader = response.body.getReader();
                const decoder = new TextDecoder();
                let buffer = '';
                
                while (!this._isClosed) {
                    const { done, value } = await reader.read();
                    
                    if (done) {
                        // Connection closed by server
                        this._handleClose(1000, 'Connection closed by server');
                        break;
                    }
                    
                    // Decode chunk
                    buffer += decoder.decode(value, { stream: true });
                    
                    // Process complete messages (assuming newline-delimited JSON)
                    let newlineIndex;
                    while ((newlineIndex = buffer.indexOf('\n')) !== -1) {
                        const line = buffer.slice(0, newlineIndex);
                        buffer = buffer.slice(newlineIndex + 1);
                        
                        if (line.trim()) {
                            try {
                                const message = JSON.parse(line);
                                this._handleMessage(message);
                            } catch (e) {
                                console.error('Failed to parse message:', e);
                            }
                        }
                    }
                }
                
            } catch (error) {
                if (error.name !== 'AbortError') {
                    this._handleError(error);
                }
            }
        }
        
        _handleMessage(message) {
            if (this.readyState !== WebSocket.OPEN) return;
            
            if (message.type === 'close') {
                this._handleClose(message.code || 1000, message.reason || '');
                return;
            }
            
            // Decode data from base64
            let data;
            try {
                const binaryString = atob(message.data);
                const bytes = new Uint8Array(binaryString.length);
                for (let i = 0; i < binaryString.length; i++) {
                    bytes[i] = binaryString.charCodeAt(i);
                }
                
                // Use messageType from server to determine if text or binary
                if (message.messageType === 'text') {
                    // Decode as text
                    const decoder = new TextDecoder('utf-8');
                    data = decoder.decode(bytes);
                } else {
                    // Binary message - respect binaryType setting
                    if (this.binaryType === 'blob') {
                        data = new Blob([bytes]);
                    } else {
                        data = bytes.buffer;
                    }
                }
            } catch (e) {
                console.error('Failed to decode message:', e);
                return;
            }
            
            // Create MessageEvent
            const event = new MessageEvent('message', {
                data: data,
                origin: new URL(this.url).origin
            });
            
            if (this.onmessage) {
                this.onmessage(event);
            }
            this.dispatchEvent(event);
        }
        
        _handleError(error) {
            console.error('WebSocket error:', error);
            
            const event = new Event('error');
            event.error = error;
            
            if (this.onerror) {
                this.onerror(event);
            }
            this.dispatchEvent(event);
            
            // Close connection after error
            this._handleClose(1006, error.message);
        }
        
        _handleClose(code, reason) {
            if (this._isClosed) return;
            
            this._isClosed = true;
            this.readyState = WebSocket.CLOSED;
            
            // Abort stream
            if (this._streamAbortController) {
                this._streamAbortController.abort();
            }
            
            // Create CloseEvent
            const event = new CloseEvent('close', {
                code: code,
                reason: reason,
                wasClean: code === 1000
            });
            
            if (this.onclose) {
                this.onclose(event);
            }
            this.dispatchEvent(event);
        }
        
        async send(data) {
            if (this.readyState !== WebSocket.OPEN) {
                throw new Error('WebSocket is not open');
            }
            
            // Add to queue
            this._sendQueue.push(data);
            
            // Process queue
            this._processSendQueue();
        }
        
        async _processSendQueue() {
            // Ensure only one send operation at a time
            if (this._isSending || this._sendQueue.length === 0) {
                return;
            }
            
            this._isSending = true;
            
            while (this._sendQueue.length > 0 && !this._isClosed) {
                const data = this._sendQueue.shift();
                
                try {
                    let payload;
                    
                    if (typeof data === 'string') {
                        payload = JSON.stringify({ type: 'text', data: data });
                    } else if (data instanceof ArrayBuffer) {
                        // Convert ArrayBuffer to base64
                        const bytes = new Uint8Array(data);
                        const base64 = btoa(String.fromCharCode(...bytes));
                        payload = JSON.stringify({ type: 'binary', data: base64 });
                    } else if (data instanceof Blob) {
                        // Convert Blob to base64
                        const arrayBuffer = await data.arrayBuffer();
                        const bytes = new Uint8Array(arrayBuffer);
                        const base64 = btoa(String.fromCharCode(...bytes));
                        payload = JSON.stringify({ type: 'binary', data: base64 });
                    } else {
                        throw new Error('Unsupported data type');
                    }
                    
                    const response = await fetch(`/sw-cgi/websocket/send/${this._connId}`, {
                        method: 'POST',
                        headers: {
                            'Content-Type': 'application/json',
                        },
                        body: payload
                    });
                    
                    if (!response.ok) {
                        throw new Error(`Send failed: ${response.status}`);
                    }
                    
                } catch (error) {
                    console.error('Failed to send message:', error);
                    this._handleError(error);
                    break;
                }
            }
            
            this._isSending = false;
        }
        
        close(code = 1000, reason = '') {
            if (this._isClosed || this.readyState === WebSocket.CLOSING) {
                return;
            }
            
            this.readyState = WebSocket.CLOSING;
            
            // Send close request
            if (this._connId) {
                fetch(`/sw-cgi/websocket/send/${this._connId}`, {
                    method: 'POST',
                    headers: {
                        'Content-Type': 'application/json',
                    },
                    body: JSON.stringify({ type: 'close', code: code, reason: reason })
                }).catch(err => {
                    console.error('Failed to send close frame:', err);
                });
            }
            
            // Handle close locally
            this._handleClose(code, reason);
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
            this._listeners[event.type].forEach(listener => {
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
            if (wsUrl.protocol === 'ws:') {
                wsOrigin = wsOrigin.replace('ws:', 'http:');
            } else if (wsUrl.protocol === 'wss:') {
                wsOrigin = wsOrigin.replace('wss:', 'https:');
            }
            
            return wsOrigin === currentOrigin;
        } catch (e) {
            return false;
        }
    }
    
    // Replace WebSocket with polyfill
    window.WebSocket = function(url, protocols) {
        // Use polyfill for same-origin, native for cross-origin
        if (isSameOrigin(url)) {
            console.log('[WebSocket Polyfill] Using HTTP-based polyfill for same-origin connection:', url);
            return new WebSocketPolyfill(url, protocols);
        } else {
            console.log('[WebSocket Polyfill] Using native WebSocket for cross-origin connection:', url);
            return new NativeWebSocket(url, protocols);
        }
    };
    
    // Copy static properties
    window.WebSocket.CONNECTING = NativeWebSocket.CONNECTING;
    window.WebSocket.OPEN = NativeWebSocket.OPEN;
    window.WebSocket.CLOSING = NativeWebSocket.CLOSING;
    window.WebSocket.CLOSED = NativeWebSocket.CLOSED;
    
    console.log('[WebSocket Polyfill] Initialized');
})();