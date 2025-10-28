/// Proxy Engine - Core network interception and E2EE tunneling
///
/// This module intercepts all browser network requests and routes them
/// through encrypted tunnels to the relay server

use crate::crypto::Credential;
use crate::protocol_codec::{
    HttpCodec, ProxyRequest, ProxyResponse, ProtocolType, TcpCodec, WebSocketCodec, WsData,
};
use crate::tunnel_manager::TunnelManager;
use parking_lot::Mutex;
use std::collections::HashMap;
use std::sync::Arc;
use wasm_bindgen::prelude::*;

/// Main proxy engine that handles all intercepted requests
#[wasm_bindgen]
pub struct ProxyEngine {
    inner: Arc<ProxyEngineInner>,
}

#[allow(dead_code)]
struct ProxyEngineInner {
    tunnel_manager: TunnelManager,
    pending_requests: Mutex<HashMap<String, PendingRequest>>,
    config: ProxyConfig,
}

#[allow(dead_code)]
struct PendingRequest {
    request_id: String,
    tunnel_id: String,
    protocol: ProtocolType,
}

/// Configuration for proxy engine
#[derive(Clone)]
pub struct ProxyConfig {
    pub server_url: String,
    pub enabled: bool,
    pub intercept_patterns: Vec<String>,
    pub bypass_patterns: Vec<String>,
}

impl Default for ProxyConfig {
    fn default() -> Self {
        Self {
            server_url: "ws://localhost:9001/ws".to_string(),
            enabled: true,
            intercept_patterns: vec!["*".to_string()],
            bypass_patterns: vec![],
        }
    }
}

#[wasm_bindgen]
impl ProxyEngine {
    /// Create a new proxy engine
    #[wasm_bindgen(constructor)]
    pub fn new(server_url: String) -> Self {
        let credential = Credential::new();
        let tunnel_manager = TunnelManager::new(credential, server_url.clone());

        let config = ProxyConfig {
            server_url,
            ..Default::default()
        };

        Self {
            inner: Arc::new(ProxyEngineInner {
                tunnel_manager,
                pending_requests: Mutex::new(HashMap::new()),
                config,
            }),
        }
    }

    /// Check if a URL should be intercepted
    #[wasm_bindgen(js_name = shouldIntercept)]
    pub fn should_intercept(&self, url: String) -> bool {
        // Don't intercept same-origin requests to WASM server
        if url.contains("localhost:8000") || url.contains("relaydns_wasm") {
            return false;
        }

        // Check bypass patterns
        for pattern in &self.inner.config.bypass_patterns {
            if url.contains(pattern) {
                return false;
            }
        }

        // Check intercept patterns
        if self.inner.config.intercept_patterns.contains(&"*".to_string()) {
            return true;
        }

        for pattern in &self.inner.config.intercept_patterns {
            if url.contains(pattern) {
                return true;
            }
        }

        false
    }

    /// Handle HTTP request
    #[wasm_bindgen(js_name = handleHttpRequest)]
    pub async fn handle_http_request(
        &self,
        method: String,
        url: String,
        headers: JsValue,
        body: Option<Vec<u8>>,
    ) -> Result<JsValue, JsValue> {
        // Validate method and URL
        HttpCodec::parse_method(&method)
            .map_err(|e| JsValue::from_str(&e))?;
        HttpCodec::validate_url(&url)
            .map_err(|e| JsValue::from_str(&e))?;

        // Parse headers
        let headers_map: HashMap<String, String> = serde_wasm_bindgen::from_value(headers)
            .unwrap_or_else(|_| HashMap::new());

        // Create tunnel for HTTP
        let tunnel = self.inner.tunnel_manager
            .create_tunnel(ProtocolType::Http)
            .await
            .map_err(|e| JsValue::from_str(&format!("failed to create tunnel: {}", e)))?;

        // Create proxy request
        let request = ProxyRequest::HttpRequest {
            method,
            url,
            headers: headers_map,
            body,
        };

        // Send request through tunnel
        tunnel
            .send_request(request)
            .await
            .map_err(|e| JsValue::from_str(&format!("failed to send request: {}", e)))?;

        // Wait for response
        let response = tunnel
            .receive_response()
            .await
            .map_err(|e| JsValue::from_str(&format!("failed to receive response: {}", e)))?;

        // Remove tunnel
        self.inner.tunnel_manager.remove_tunnel(&tunnel.id);

        // Convert response to JS
        match response {
            ProxyResponse::HttpResponse {
                status,
                status_text,
                headers,
                body,
            } => {
                let obj = js_sys::Object::new();
                js_sys::Reflect::set(&obj, &"status".into(), &JsValue::from_f64(status as f64))?;
                js_sys::Reflect::set(&obj, &"statusText".into(), &JsValue::from_str(&status_text))?;

                let headers_obj = js_sys::Object::new();
                for (key, value) in headers {
                    js_sys::Reflect::set(&headers_obj, &key.into(), &value.into())?;
                }
                js_sys::Reflect::set(&obj, &"headers".into(), &headers_obj)?;

                let body_array = js_sys::Uint8Array::from(&body[..]);
                js_sys::Reflect::set(&obj, &"body".into(), &body_array)?;

                Ok(obj.into())
            }
            ProxyResponse::Error { error, .. } => {
                Err(JsValue::from_str(&format!("proxy error: {}", error)))
            }
            _ => Err(JsValue::from_str("unexpected response type")),
        }
    }

    /// Open WebSocket connection through tunnel
    #[wasm_bindgen(js_name = openWebSocket)]
    pub async fn open_websocket(
        &self,
        url: String,
        protocols: Vec<String>,
    ) -> Result<JsValue, JsValue> {
        // Validate URL
        WebSocketCodec::validate_url(&url)
            .map_err(|e| JsValue::from_str(&e))?;

        // Create tunnel for WebSocket
        let tunnel = self.inner.tunnel_manager
            .create_tunnel(ProtocolType::WebSocket)
            .await
            .map_err(|e| JsValue::from_str(&format!("failed to create tunnel: {}", e)))?;

        let tunnel_id = tunnel.id.clone();

        // Create proxy request
        let request = ProxyRequest::WebSocketOpen {
            url: url.clone(),
            protocols,
        };

        // Send request
        tunnel
            .send_request(request)
            .await
            .map_err(|e| JsValue::from_str(&format!("failed to send request: {}", e)))?;

        // Wait for opened response
        let response = tunnel
            .receive_response()
            .await
            .map_err(|e| JsValue::from_str(&format!("failed to receive response: {}", e)))?;

        match response {
            ProxyResponse::WebSocketOpened { protocol, .. } => {
                // Store tunnel for future messages
                let obj = js_sys::Object::new();
                js_sys::Reflect::set(&obj, &"tunnelId".into(), &tunnel_id.into())?;
                js_sys::Reflect::set(
                    &obj,
                    &"protocol".into(),
                    &protocol.unwrap_or_default().into(),
                )?;
                Ok(obj.into())
            }
            ProxyResponse::Error { error, .. } => {
                self.inner.tunnel_manager.remove_tunnel(&tunnel_id);
                Err(JsValue::from_str(&format!("failed to open websocket: {}", error)))
            }
            _ => {
                self.inner.tunnel_manager.remove_tunnel(&tunnel_id);
                Err(JsValue::from_str("unexpected response type"))
            }
        }
    }

    /// Send WebSocket message
    #[wasm_bindgen(js_name = sendWebSocketMessage)]
    pub async fn send_websocket_message(
        &self,
        tunnel_id: String,
        data: JsValue,
        is_binary: bool,
    ) -> Result<(), JsValue> {
        // Get tunnel
        let tunnel = self.inner.tunnel_manager
            .get_tunnel(&tunnel_id)
            .ok_or_else(|| JsValue::from_str("tunnel not found"))?;

        // Convert data
        let ws_data = if is_binary {
            let array = js_sys::Uint8Array::from(data);
            WsData::Binary {
                content: array.to_vec(),
            }
        } else {
            let text = data.as_string().ok_or_else(|| JsValue::from_str("invalid text data"))?;
            WsData::Text { content: text }
        };

        // Create request
        let request = ProxyRequest::WebSocketMessage {
            tunnel_id: tunnel_id.clone(),
            data: ws_data,
        };

        // Send
        tunnel
            .send_request(request)
            .await
            .map_err(|e| JsValue::from_str(&format!("failed to send message: {}", e)))?;

        Ok(())
    }

    /// Receive WebSocket message
    #[wasm_bindgen(js_name = receiveWebSocketMessage)]
    pub async fn receive_websocket_message(&self, tunnel_id: String) -> Result<JsValue, JsValue> {
        // Get tunnel
        let tunnel = self.inner.tunnel_manager
            .get_tunnel(&tunnel_id)
            .ok_or_else(|| JsValue::from_str("tunnel not found"))?;

        // Receive response
        let response = tunnel
            .receive_response()
            .await
            .map_err(|e| JsValue::from_str(&format!("failed to receive: {}", e)))?;

        match response {
            ProxyResponse::WebSocketMessage { data, .. } => {
                let obj = js_sys::Object::new();
                match data {
                    WsData::Text { content } => {
                        js_sys::Reflect::set(&obj, &"type".into(), &"text".into())?;
                        js_sys::Reflect::set(&obj, &"data".into(), &content.into())?;
                    }
                    WsData::Binary { content } => {
                        js_sys::Reflect::set(&obj, &"type".into(), &"binary".into())?;
                        let array = js_sys::Uint8Array::from(&content[..]);
                        js_sys::Reflect::set(&obj, &"data".into(), &array)?;
                    }
                }
                Ok(obj.into())
            }
            ProxyResponse::WebSocketClosed { code, reason, .. } => {
                let obj = js_sys::Object::new();
                js_sys::Reflect::set(&obj, &"type".into(), &"close".into())?;
                js_sys::Reflect::set(&obj, &"code".into(), &JsValue::from_f64(code as f64))?;
                js_sys::Reflect::set(&obj, &"reason".into(), &reason.into())?;
                Ok(obj.into())
            }
            _ => Err(JsValue::from_str("unexpected response type")),
        }
    }

    /// Close WebSocket
    #[wasm_bindgen(js_name = closeWebSocket)]
    pub async fn close_websocket(
        &self,
        tunnel_id: String,
        code: u16,
        reason: String,
    ) -> Result<(), JsValue> {
        // Get tunnel
        let tunnel = self.inner.tunnel_manager
            .get_tunnel(&tunnel_id)
            .ok_or_else(|| JsValue::from_str("tunnel not found"))?;

        // Create close request
        let request = ProxyRequest::WebSocketClose {
            tunnel_id: tunnel_id.clone(),
            code,
            reason,
        };

        // Send
        tunnel
            .send_request(request)
            .await
            .map_err(|e| JsValue::from_str(&format!("failed to close: {}", e)))?;

        // Remove tunnel
        self.inner.tunnel_manager.remove_tunnel(&tunnel_id);

        Ok(())
    }

    /// Connect to TCP server
    #[wasm_bindgen(js_name = connectTcp)]
    pub async fn connect_tcp(&self, host: String, port: u16) -> Result<JsValue, JsValue> {
        // Validate address
        TcpCodec::validate_address(&host, port)
            .map_err(|e| JsValue::from_str(&e))?;

        // Create tunnel for TCP
        let tunnel = self.inner.tunnel_manager
            .create_tunnel(ProtocolType::Tcp)
            .await
            .map_err(|e| JsValue::from_str(&format!("failed to create tunnel: {}", e)))?;

        let tunnel_id = tunnel.id.clone();

        // Create connect request
        let request = ProxyRequest::TcpConnect { host, port };

        // Send
        tunnel
            .send_request(request)
            .await
            .map_err(|e| JsValue::from_str(&format!("failed to send: {}", e)))?;

        // Wait for connected response
        let response = tunnel
            .receive_response()
            .await
            .map_err(|e| JsValue::from_str(&format!("failed to receive: {}", e)))?;

        match response {
            ProxyResponse::TcpConnected { .. } => {
                let obj = js_sys::Object::new();
                js_sys::Reflect::set(&obj, &"tunnelId".into(), &tunnel_id.into())?;
                Ok(obj.into())
            }
            ProxyResponse::Error { error, .. } => {
                self.inner.tunnel_manager.remove_tunnel(&tunnel_id);
                Err(JsValue::from_str(&format!("failed to connect: {}", error)))
            }
            _ => {
                self.inner.tunnel_manager.remove_tunnel(&tunnel_id);
                Err(JsValue::from_str("unexpected response type"))
            }
        }
    }

    /// Get status information
    #[wasm_bindgen(js_name = getStatus)]
    pub fn get_status(&self) -> JsValue {
        let obj = js_sys::Object::new();
        let active = self.inner.tunnel_manager.active_tunnels();
        js_sys::Reflect::set(&obj, &"enabled".into(), &self.inner.config.enabled.into()).unwrap();
        js_sys::Reflect::set(&obj, &"activeTunnels".into(), &JsValue::from_f64(active.len() as f64)).unwrap();
        js_sys::Reflect::set(&obj, &"serverUrl".into(), &self.inner.config.server_url.clone().into()).unwrap();
        obj.into()
    }
}
