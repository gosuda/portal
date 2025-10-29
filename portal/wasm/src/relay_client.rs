use crate::crypto::Credential;
use crate::proto::{self, PacketType, ResponseCode};
use crate::utils;
use crate::ws_stream::WebSocketStream;
use parking_lot::Mutex;
use serde::{Deserialize, Serialize};
use std::collections::HashMap;
use std::sync::Arc;
use wasm_bindgen::prelude::*;

#[wasm_bindgen]
#[derive(Clone)]
pub struct RelayClient {
    inner: Arc<Mutex<RelayClientInner>>,
}

struct RelayClientInner {
    server_url: String,
    credential: Credential,
    leases: HashMap<String, LeaseInfo>,
}

#[derive(Clone, Serialize, Deserialize)]
pub struct LeaseInfo {
    pub name: String,
    pub alpns: Vec<String>,
    pub expires: i64,
}

#[derive(Serialize, Deserialize)]
pub struct RelayInfo {
    pub identity: IdentityInfo,
    pub address: Vec<String>,
    pub leases: Vec<String>,
}

#[derive(Serialize, Deserialize)]
pub struct IdentityInfo {
    pub id: String,
    pub public_key: String,
}

#[wasm_bindgen]
impl RelayClient {
    /// Connect to Portal server
    pub async fn connect(server_url: String) -> Result<RelayClient, JsValue> {
        // Test connection
        let _test_ws = WebSocketStream::connect(&server_url)
            .await
            .map_err(|e| JsValue::from_str(&format!("WebSocket connection failed: {:?}", e)))?;

        // Generate credential
        let credential = Credential::new();

        crate::console_log!("RelayClient created with ID: {}", credential.id());

        Ok(Self {
            inner: Arc::new(Mutex::new(RelayClientInner {
                server_url,
                credential,
                leases: HashMap::new(),
            })),
        })
    }

    /// Get relay server information
    #[wasm_bindgen(js_name = getRelayInfo)]
    pub async fn get_relay_info(&self) -> Result<JsValue, JsValue> {
        let server_url = {
            let inner = self.inner.lock();
            inner.server_url.clone()
        };

        // Connect WebSocket for this request
        let mut ws = WebSocketStream::connect(&server_url)
            .await
            .map_err(utils::to_js_error)?;

        // Create request
        let request = proto::RelayInfoRequest {};
        let payload = proto::encode_message(&request);

        let packet = proto::Packet {
            r#type: PacketType::RelayInfoRequest as i32,
            payload,
        };

        // Send request
        proto::write_packet_async(&mut ws, &packet)
            .await
            .map_err(utils::to_js_error)?;

        // Read response
        let response_packet = proto::read_packet_async(&mut ws)
            .await
            .map_err(utils::to_js_error)?;

        if response_packet.r#type != PacketType::RelayInfoResponse as i32 {
            return Err(JsValue::from_str("invalid response type"));
        }

        let response: proto::RelayInfoResponse =
            proto::decode_message(&response_packet.payload).map_err(utils::to_js_error)?;

        let relay_info = response
            .relay_info
            .ok_or_else(|| JsValue::from_str("missing relay info"))?;

        // Convert to JSON-friendly format
        let info = RelayInfo {
            identity: IdentityInfo {
                id: relay_info
                    .identity
                    .as_ref()
                    .map(|i| i.id.clone())
                    .unwrap_or_default(),
                public_key: relay_info
                    .identity
                    .as_ref()
                    .map(|i| hex::encode(&i.public_key))
                    .unwrap_or_default(),
            },
            address: relay_info.address,
            leases: relay_info.leases,
        };

        serde_wasm_bindgen::to_value(&info).map_err(utils::to_js_error)
    }

    /// Register a lease
    #[wasm_bindgen(js_name = registerLease)]
    pub async fn register_lease(&self, name: String, alpns: Vec<String>) -> Result<(), JsValue> {
        let (server_url, credential, lease) = {
            let inner = self.inner.lock();

            let expires = utils::unix_timestamp() + 60; // 60 seconds from now
            let lease = proto::Lease {
                identity: Some(inner.credential.identity()),
                expires,
                name: name.clone(),
                alpn: alpns.clone(),
            };

            (inner.server_url.clone(), inner.credential.clone(), lease)
        };

        // Connect WebSocket for this request
        let mut ws = WebSocketStream::connect(&server_url)
            .await
            .map_err(utils::to_js_error)?;

        // Create signed request
        let nonce = utils::random_bytes(12);
        let timestamp = utils::unix_timestamp();

        let request = proto::LeaseUpdateRequest {
            lease: Some(lease.clone()),
            nonce,
            timestamp,
        };

        let payload = proto::encode_message(&request);
        let signature = credential.sign(&payload);

        let signed = proto::SignedPayload {
            data: payload,
            signature,
        };

        let signed_data = proto::encode_message(&signed);

        let packet = proto::Packet {
            r#type: PacketType::LeaseUpdateRequest as i32,
            payload: signed_data,
        };

        // Send request
        proto::write_packet_async(&mut ws, &packet)
            .await
            .map_err(utils::to_js_error)?;

        // Read response
        let response_packet = proto::read_packet_async(&mut ws)
            .await
            .map_err(utils::to_js_error)?;

        if response_packet.r#type != PacketType::LeaseUpdateResponse as i32 {
            return Err(JsValue::from_str("invalid response type"));
        }

        let response: proto::LeaseUpdateResponse =
            proto::decode_message(&response_packet.payload).map_err(utils::to_js_error)?;

        if response.code != ResponseCode::Accepted as i32 {
            return Err(JsValue::from_str(&format!(
                "lease registration rejected: code {}",
                response.code
            )));
        }

        // Store lease info
        {
            let mut inner = self.inner.lock();
            let credential_id = inner.credential.id().to_string();
            inner.leases.insert(
                credential_id,
                LeaseInfo {
                    name,
                    alpns,
                    expires: lease.expires,
                },
            );
        }

        crate::console_log!("Lease registered successfully");

        Ok(())
    }

    /// Get client credential ID
    #[wasm_bindgen(js_name = getCredentialId)]
    pub fn get_credential_id(&self) -> String {
        let inner = self.inner.lock();
        inner.credential.id().to_string()
    }

    /// Request connection to another peer
    #[wasm_bindgen(js_name = requestConnection)]
    pub async fn request_connection(
        &self,
        lease_id: String,
        _alpn: String,
    ) -> Result<JsValue, JsValue> {
        let (server_url, credential) = {
            let inner = self.inner.lock();
            (inner.server_url.clone(), inner.credential.clone())
        };

        // Connect WebSocket for this request
        let mut ws = WebSocketStream::connect(&server_url)
            .await
            .map_err(utils::to_js_error)?;

        // Create connection request
        let request = proto::ConnectionRequest {
            lease_id,
            client_identity: Some(credential.identity()),
        };

        let payload = proto::encode_message(&request);

        let packet = proto::Packet {
            r#type: PacketType::ConnectionRequest as i32,
            payload,
        };

        // Send request
        proto::write_packet_async(&mut ws, &packet)
            .await
            .map_err(utils::to_js_error)?;

        // Read response
        let response_packet = proto::read_packet_async(&mut ws)
            .await
            .map_err(utils::to_js_error)?;

        if response_packet.r#type != PacketType::ConnectionResponse as i32 {
            return Err(JsValue::from_str("invalid response type"));
        }

        let response: proto::ConnectionResponse =
            proto::decode_message(&response_packet.payload).map_err(utils::to_js_error)?;

        if response.code != ResponseCode::Accepted as i32 {
            return Err(JsValue::from_str(&format!(
                "connection rejected: code {}",
                response.code
            )));
        }

        Ok(JsValue::from_str("connection established"))
    }
}
