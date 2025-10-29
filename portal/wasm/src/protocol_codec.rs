/// Protocol codec for converting between browser requests and relay protocol
///
/// This module handles the encoding/decoding of HTTP, WebSocket, and TCP data
/// for transmission through the E2EE tunnel
use serde::{Deserialize, Serialize};
use std::collections::HashMap;

/// Protocol types supported by the proxy
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
pub enum ProtocolType {
    Http,
    WebSocket,
    Tcp,
}

/// Request from browser to be proxied
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(tag = "type")]
pub enum ProxyRequest {
    /// HTTP request
    #[serde(rename = "http")]
    HttpRequest {
        method: String,
        url: String,
        headers: HashMap<String, String>,
        body: Option<Vec<u8>>,
    },

    /// Open WebSocket connection
    #[serde(rename = "ws_open")]
    WebSocketOpen { url: String, protocols: Vec<String> },

    /// Send WebSocket message
    #[serde(rename = "ws_message")]
    WebSocketMessage { tunnel_id: String, data: WsData },

    /// Close WebSocket
    #[serde(rename = "ws_close")]
    WebSocketClose {
        tunnel_id: String,
        code: u16,
        reason: String,
    },

    /// TCP connect
    #[serde(rename = "tcp_connect")]
    TcpConnect { host: String, port: u16 },

    /// TCP data
    #[serde(rename = "tcp_data")]
    TcpData { tunnel_id: String, data: Vec<u8> },

    /// TCP close
    #[serde(rename = "tcp_close")]
    TcpClose { tunnel_id: String },
}

/// WebSocket data types
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(tag = "data_type")]
pub enum WsData {
    #[serde(rename = "text")]
    Text { content: String },

    #[serde(rename = "binary")]
    Binary { content: Vec<u8> },
}

/// Response from relay back to browser
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(tag = "type")]
pub enum ProxyResponse {
    /// HTTP response
    #[serde(rename = "http")]
    HttpResponse {
        status: u16,
        status_text: String,
        headers: HashMap<String, String>,
        body: Vec<u8>,
    },

    /// WebSocket opened
    #[serde(rename = "ws_opened")]
    WebSocketOpened {
        tunnel_id: String,
        protocol: Option<String>,
    },

    /// WebSocket message received
    #[serde(rename = "ws_message")]
    WebSocketMessage { tunnel_id: String, data: WsData },

    /// WebSocket closed
    #[serde(rename = "ws_closed")]
    WebSocketClosed {
        tunnel_id: String,
        code: u16,
        reason: String,
    },

    /// TCP connected
    #[serde(rename = "tcp_connected")]
    TcpConnected { tunnel_id: String },

    /// TCP data received
    #[serde(rename = "tcp_data")]
    TcpData { tunnel_id: String, data: Vec<u8> },

    /// TCP closed
    #[serde(rename = "tcp_closed")]
    TcpClosed { tunnel_id: String },

    /// Error response
    #[serde(rename = "error")]
    Error { request_id: String, error: String },
}

/// Wire format for transmission through E2EE tunnel
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ProxyPacket {
    /// Unique request/response ID
    pub id: String,

    /// Protocol version
    pub version: u8,

    /// Payload
    pub payload: ProxyPayload,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(untagged)]
pub enum ProxyPayload {
    Request(ProxyRequest),
    Response(ProxyResponse),
}

impl ProxyPacket {
    /// Create a new request packet
    pub fn new_request(id: String, request: ProxyRequest) -> Self {
        Self {
            id,
            version: 1,
            payload: ProxyPayload::Request(request),
        }
    }

    /// Create a new response packet
    #[allow(dead_code)]
    pub fn new_response(id: String, response: ProxyResponse) -> Self {
        Self {
            id,
            version: 1,
            payload: ProxyPayload::Response(response),
        }
    }

    /// Encode packet to bytes
    pub fn encode(&self) -> Result<Vec<u8>, String> {
        serde_json::to_vec(self).map_err(|e| format!("failed to encode packet: {}", e))
    }

    /// Decode packet from bytes
    pub fn decode(data: &[u8]) -> Result<Self, String> {
        serde_json::from_slice(data).map_err(|e| format!("failed to decode packet: {}", e))
    }
}

/// HTTP request codec
pub struct HttpCodec;

impl HttpCodec {
    /// Parse HTTP method from string
    pub fn parse_method(method: &str) -> Result<String, String> {
        match method.to_uppercase().as_str() {
            "GET" | "POST" | "PUT" | "DELETE" | "PATCH" | "HEAD" | "OPTIONS" => {
                Ok(method.to_uppercase())
            }
            _ => Err(format!("unsupported HTTP method: {}", method)),
        }
    }

    /// Validate HTTP URL
    pub fn validate_url(url: &str) -> Result<(), String> {
        if url.starts_with("http://") || url.starts_with("https://") {
            Ok(())
        } else {
            Err(format!("invalid HTTP URL: {}", url))
        }
    }

    /// Parse headers from key-value pairs
    #[allow(dead_code)]
    pub fn parse_headers(headers: Vec<(String, String)>) -> HashMap<String, String> {
        headers.into_iter().collect()
    }
}

/// WebSocket codec
pub struct WebSocketCodec;

impl WebSocketCodec {
    /// Validate WebSocket URL
    pub fn validate_url(url: &str) -> Result<(), String> {
        if url.starts_with("ws://") || url.starts_with("wss://") {
            Ok(())
        } else {
            Err(format!("invalid WebSocket URL: {}", url))
        }
    }

    /// Generate tunnel ID
    #[allow(dead_code)]
    pub fn generate_tunnel_id() -> String {
        use rand_core::RngCore;
        let mut rng = rand_core::OsRng;
        let mut bytes = [0u8; 16];
        rng.fill_bytes(&mut bytes);
        hex::encode(bytes)
    }
}

/// TCP codec
pub struct TcpCodec;

impl TcpCodec {
    /// Validate TCP address
    pub fn validate_address(host: &str, port: u16) -> Result<(), String> {
        if host.is_empty() {
            return Err("empty host".to_string());
        }
        if port == 0 {
            return Err("invalid port".to_string());
        }
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_http_request_encoding() {
        let req = ProxyRequest::HttpRequest {
            method: "GET".to_string(),
            url: "https://example.com".to_string(),
            headers: HashMap::new(),
            body: None,
        };

        let packet = ProxyPacket::new_request("test-id".to_string(), req);
        let encoded = packet.encode().unwrap();
        let decoded = ProxyPacket::decode(&encoded).unwrap();

        assert_eq!(packet.id, decoded.id);
    }

    #[test]
    fn test_websocket_message() {
        let req = ProxyRequest::WebSocketMessage {
            tunnel_id: "tunnel-123".to_string(),
            data: WsData::Text {
                content: "Hello".to_string(),
            },
        };

        let packet = ProxyPacket::new_request("msg-id".to_string(), req);
        let encoded = packet.encode().unwrap();
        let decoded = ProxyPacket::decode(&encoded).unwrap();

        assert_eq!(packet.id, decoded.id);
    }
}
