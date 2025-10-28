/// Tunnel manager for handling E2EE connections
///
/// Manages the lifecycle of encrypted tunnels between browser and relay server

use crate::crypto::{Credential, SecureConnection};
use crate::protocol_codec::{ProtocolType, ProxyPacket, ProxyRequest, ProxyResponse};
use crate::ws_stream::WebSocketStream;
use parking_lot::Mutex;
use std::collections::HashMap;
use std::sync::Arc;

/// Represents a single encrypted tunnel
#[allow(dead_code)]
pub struct Tunnel {
    pub id: String,
    pub protocol: ProtocolType,
    pub connection: Arc<Mutex<SecureConnection<WebSocketStream>>>,
    pub credential: Credential,
}

impl Tunnel {
    /// Create a new tunnel
    pub fn new(
        id: String,
        protocol: ProtocolType,
        connection: SecureConnection<WebSocketStream>,
        credential: Credential,
    ) -> Self {
        Self {
            id,
            protocol,
            connection: Arc::new(Mutex::new(connection)),
            credential,
        }
    }

    /// Send a request through the tunnel
    pub async fn send_request(&self, request: ProxyRequest) -> Result<(), String> {
        let packet = ProxyPacket::new_request(self.id.clone(), request);
        let data = packet.encode()?;

        // Directly write to connection
        {
            let mut conn = self.connection.lock();
            conn.write(&data)
                .await
                .map_err(|e| format!("failed to write to tunnel: {}", e))?;
        }

        Ok(())
    }

    /// Receive a response from the tunnel
    pub async fn receive_response(&self) -> Result<ProxyResponse, String> {
        // Read data from connection
        let data = {
            let mut conn = self.connection.lock();
            let mut buf = vec![0u8; 65536]; // 64KB buffer

            let n = conn
                .read(&mut buf)
                .await
                .map_err(|e| format!("failed to read from tunnel: {}", e))?;

            if n == 0 {
                return Err("tunnel closed".to_string());
            }

            buf.truncate(n);
            buf
        };

        let packet = ProxyPacket::decode(&data)?;

        match packet.payload {
            crate::protocol_codec::ProxyPayload::Response(resp) => Ok(resp),
            _ => Err("unexpected packet type".to_string()),
        }
    }
}

/// Manages multiple tunnels
pub struct TunnelManager {
    tunnels: Arc<Mutex<HashMap<String, Arc<Tunnel>>>>,
    credential: Credential,
    server_url: String,
}

impl TunnelManager {
    /// Create a new tunnel manager
    pub fn new(credential: Credential, server_url: String) -> Self {
        Self {
            tunnels: Arc::new(Mutex::new(HashMap::new())),
            credential,
            server_url,
        }
    }

    /// Create a new tunnel for a specific protocol
    pub async fn create_tunnel(&self, protocol: ProtocolType) -> Result<Arc<Tunnel>, String> {
        // Connect to relay server
        let ws = WebSocketStream::connect(&self.server_url)
            .await
            .map_err(|e| format!("WebSocket connection failed: {:?}", e))?;

        // Perform E2EE handshake
        let alpn = match protocol {
            ProtocolType::Http => "http",
            ProtocolType::WebSocket => "websocket",
            ProtocolType::Tcp => "tcp",
        };

        let secure_conn = SecureConnection::client_handshake(ws, &self.credential, alpn)
            .await
            .map_err(|e| format!("E2EE handshake failed: {}", e))?;

        // Generate tunnel ID
        let tunnel_id = self.generate_tunnel_id();

        // Create tunnel
        let tunnel = Arc::new(Tunnel::new(
            tunnel_id.clone(),
            protocol,
            secure_conn,
            self.credential.clone(),
        ));

        // Store tunnel
        {
            let mut tunnels = self.tunnels.lock();
            tunnels.insert(tunnel_id, tunnel.clone());
        }

        Ok(tunnel)
    }

    /// Get an existing tunnel
    pub fn get_tunnel(&self, tunnel_id: &str) -> Option<Arc<Tunnel>> {
        let tunnels = self.tunnels.lock();
        tunnels.get(tunnel_id).cloned()
    }

    /// Remove a tunnel
    pub fn remove_tunnel(&self, tunnel_id: &str) {
        let mut tunnels = self.tunnels.lock();
        tunnels.remove(tunnel_id);
    }

    /// Get all active tunnel IDs
    pub fn active_tunnels(&self) -> Vec<String> {
        let tunnels = self.tunnels.lock();
        tunnels.keys().cloned().collect()
    }

    /// Close all tunnels
    #[allow(dead_code)]
    pub fn close_all(&self) {
        let mut tunnels = self.tunnels.lock();
        tunnels.clear();
    }

    /// Generate a unique tunnel ID
    fn generate_tunnel_id(&self) -> String {
        use rand_core::RngCore;
        let mut rng = rand_core::OsRng;
        let mut bytes = [0u8; 16];
        rng.fill_bytes(&mut bytes);
        hex::encode(bytes)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_tunnel_id_generation() {
        let credential = Credential::new();
        let manager = TunnelManager::new(credential, "ws://localhost:9001/ws".to_string());

        let id1 = manager.generate_tunnel_id();
        let id2 = manager.generate_tunnel_id();

        assert_ne!(id1, id2);
        assert_eq!(id1.len(), 32); // 16 bytes hex = 32 chars
    }
}
