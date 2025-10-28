/// Data adapters for browser integration
/// Handles HTTP transfers (files, API) and WebSocket data interpretation
use serde::{Deserialize, Serialize};
use wasm_bindgen::prelude::*;
use web_sys::{Blob, FormData, Headers, Request, RequestInit, Response};

/// HTTP Adapter for file and API transfers
#[wasm_bindgen]
pub struct HttpAdapter {
    base_url: String,
}

#[wasm_bindgen]
impl HttpAdapter {
    #[wasm_bindgen(constructor)]
    pub fn new(base_url: String) -> Self {
        Self { base_url }
    }

    /// Send GET request
    #[wasm_bindgen(js_name = get)]
    pub async fn get(&self, path: String) -> Result<JsValue, JsValue> {
        let url = format!("{}{}", self.base_url, path);

        let window = web_sys::window().ok_or_else(|| JsValue::from_str("no window"))?;
        let response = window.fetch_with_str(&url).into_future().await?;

        let response: Response = response.dyn_into()?;

        if !response.ok() {
            return Err(JsValue::from_str(&format!(
                "HTTP error: {}",
                response.status()
            )));
        }

        response.json()?.into_future().await
    }

    /// Send POST request with JSON body
    #[wasm_bindgen(js_name = postJson)]
    pub async fn post_json(&self, path: String, body: JsValue) -> Result<JsValue, JsValue> {
        let url = format!("{}{}", self.base_url, path);

        let body_str = js_sys::JSON::stringify(&body)?;

        let opts = RequestInit::new();
        opts.set_method("POST");
        let body_value: JsValue = body_str.into();
        opts.set_body(&body_value);

        let headers = Headers::new()?;
        headers.set("Content-Type", "application/json")?;

        let request = Request::new_with_str_and_init(&url, &opts)?;
        request.headers().set("Content-Type", "application/json")?;

        let window = web_sys::window().ok_or_else(|| JsValue::from_str("no window"))?;
        let response = window.fetch_with_request(&request).into_future().await?;

        let response: Response = response.dyn_into()?;

        if !response.ok() {
            return Err(JsValue::from_str(&format!(
                "HTTP error: {}",
                response.status()
            )));
        }

        response.json()?.into_future().await
    }

    /// Upload file
    #[wasm_bindgen(js_name = uploadFile)]
    pub async fn upload_file(
        &self,
        path: String,
        file_name: String,
        file_data: Vec<u8>,
    ) -> Result<JsValue, JsValue> {
        let url = format!("{}{}", self.base_url, path);

        // Create Blob from file data
        let array = js_sys::Uint8Array::from(&file_data[..]);
        let blob = Blob::new_with_u8_array_sequence(&js_sys::Array::of1(&array))?;

        // Create FormData
        let form_data = FormData::new()?;
        form_data.append_with_blob(&file_name, &blob)?;

        // Create request
        let opts = RequestInit::new();
        opts.set_method("POST");
        let form_value: JsValue = form_data.into();
        opts.set_body(&form_value);

        let request = Request::new_with_str_and_init(&url, &opts)?;

        let window = web_sys::window().ok_or_else(|| JsValue::from_str("no window"))?;
        let response = window.fetch_with_request(&request).into_future().await?;

        let response: Response = response.dyn_into()?;

        if !response.ok() {
            return Err(JsValue::from_str(&format!(
                "HTTP error: {}",
                response.status()
            )));
        }

        response.json()?.into_future().await
    }

    /// Download file
    #[wasm_bindgen(js_name = downloadFile)]
    pub async fn download_file(&self, path: String) -> Result<Vec<u8>, JsValue> {
        let url = format!("{}{}", self.base_url, path);

        let window = web_sys::window().ok_or_else(|| JsValue::from_str("no window"))?;
        let response = window.fetch_with_str(&url).into_future().await?;

        let response: Response = response.dyn_into()?;

        if !response.ok() {
            return Err(JsValue::from_str(&format!(
                "HTTP error: {}",
                response.status()
            )));
        }

        let array_buffer = response.array_buffer()?.into_future().await?;
        let array = js_sys::Uint8Array::new(&array_buffer);
        Ok(array.to_vec())
    }
}

/// WebSocket Data Adapter for browser
#[wasm_bindgen]
pub struct WebSocketAdapter {
    url: String,
    ws: Option<web_sys::WebSocket>,
    message_callback: Option<js_sys::Function>,
    error_callback: Option<js_sys::Function>,
}

#[wasm_bindgen]
impl WebSocketAdapter {
    #[wasm_bindgen(constructor)]
    pub fn new(url: String) -> Self {
        Self {
            url,
            ws: None,
            message_callback: None,
            error_callback: None,
        }
    }

    /// Connect to WebSocket
    #[wasm_bindgen]
    pub async fn connect(&mut self) -> Result<(), JsValue> {
        let ws = web_sys::WebSocket::new(&self.url)?;
        ws.set_binary_type(web_sys::BinaryType::Arraybuffer);

        // Wait for connection
        let (tx, rx) = futures::channel::oneshot::channel();
        let tx = std::sync::Arc::new(parking_lot::Mutex::new(Some(tx)));

        {
            let tx = tx.clone();
            let onopen = wasm_bindgen::closure::Closure::wrap(Box::new(move |_| {
                if let Some(tx) = tx.lock().take() {
                    let _ = tx.send(());
                }
            })
                as Box<dyn FnMut(JsValue)>);

            ws.set_onopen(Some(onopen.as_ref().unchecked_ref()));
            onopen.forget();
        }

        rx.await
            .map_err(|_| JsValue::from_str("connection failed"))?;

        self.ws = Some(ws);
        Ok(())
    }

    /// Set message callback
    #[wasm_bindgen(js_name = onMessage)]
    pub fn on_message(&mut self, callback: js_sys::Function) {
        self.message_callback = Some(callback.clone());

        if let Some(ws) = &self.ws {
            let cb = callback;
            let onmessage =
                wasm_bindgen::closure::Closure::wrap(Box::new(move |e: web_sys::MessageEvent| {
                    // Parse message data
                    if let Ok(array_buffer) = e.data().dyn_into::<js_sys::ArrayBuffer>() {
                        let array = js_sys::Uint8Array::new(&array_buffer);
                        let data = array.to_vec();

                        // Convert to JS object
                        let obj = js_sys::Object::new();
                        js_sys::Reflect::set(&obj, &"type".into(), &"binary".into()).unwrap();
                        js_sys::Reflect::set(
                            &obj,
                            &"data".into(),
                            &js_sys::Uint8Array::from(&data[..]),
                        )
                        .unwrap();

                        // Call callback
                        let _ = cb.call1(&JsValue::NULL, &obj);
                    } else if let Ok(text) = e.data().dyn_into::<js_sys::JsString>() {
                        let obj = js_sys::Object::new();
                        js_sys::Reflect::set(&obj, &"type".into(), &"text".into()).unwrap();
                        js_sys::Reflect::set(&obj, &"data".into(), &text).unwrap();

                        let _ = cb.call1(&JsValue::NULL, &obj);
                    }
                })
                    as Box<dyn FnMut(web_sys::MessageEvent)>);

            ws.set_onmessage(Some(onmessage.as_ref().unchecked_ref()));
            onmessage.forget();
        }
    }

    /// Set error callback
    #[wasm_bindgen(js_name = onError)]
    pub fn on_error(&mut self, callback: js_sys::Function) {
        self.error_callback = Some(callback.clone());

        if let Some(ws) = &self.ws {
            let cb = callback;
            let onerror =
                wasm_bindgen::closure::Closure::wrap(Box::new(move |e: web_sys::ErrorEvent| {
                    let obj = js_sys::Object::new();
                    let msg: JsValue = e.message().into();
                    js_sys::Reflect::set(&obj, &"error".into(), &msg).unwrap();
                    let _ = cb.call1(&JsValue::NULL, &obj);
                })
                    as Box<dyn FnMut(web_sys::ErrorEvent)>);

            ws.set_onerror(Some(onerror.as_ref().unchecked_ref()));
            onerror.forget();
        }
    }

    /// Send text message
    #[wasm_bindgen(js_name = sendText)]
    pub fn send_text(&self, message: String) -> Result<(), JsValue> {
        if let Some(ws) = &self.ws {
            ws.send_with_str(&message)?;
            Ok(())
        } else {
            Err(JsValue::from_str("not connected"))
        }
    }

    /// Send binary message
    #[wasm_bindgen(js_name = sendBinary)]
    pub fn send_binary(&self, data: Vec<u8>) -> Result<(), JsValue> {
        if let Some(ws) = &self.ws {
            ws.send_with_u8_array(&data)?;
            Ok(())
        } else {
            Err(JsValue::from_str("not connected"))
        }
    }

    /// Close connection
    #[wasm_bindgen]
    pub fn close(&self) -> Result<(), JsValue> {
        if let Some(ws) = &self.ws {
            ws.close()?;
        }
        Ok(())
    }
}

/// Message types for structured communication
#[derive(Serialize, Deserialize, Debug, Clone)]
#[serde(tag = "type")]
pub enum Message {
    #[serde(rename = "text")]
    Text { data: String },

    #[serde(rename = "binary")]
    Binary { data: Vec<u8> },

    #[serde(rename = "file")]
    File {
        name: String,
        size: usize,
        mime_type: String,
        data: Vec<u8>,
    },

    #[serde(rename = "api")]
    Api {
        endpoint: String,
        method: String,
        headers: std::collections::HashMap<String, String>,
        body: Option<Vec<u8>>,
    },
}

/// Data interpreter for converting relay protocol to browser-friendly format
#[wasm_bindgen]
pub struct DataInterpreter;

#[wasm_bindgen]
impl DataInterpreter {
    /// Parse relay protocol packet to browser message
    #[wasm_bindgen(js_name = parsePacket)]
    pub fn parse_packet(data: Vec<u8>) -> Result<JsValue, JsValue> {
        // Check packet header to determine type
        if data.len() < 4 {
            return Err(JsValue::from_str("packet too short"));
        }

        let packet_type = data[0];

        match packet_type {
            0x01 => {
                // Text message
                let text = String::from_utf8(data[4..].to_vec())
                    .map_err(|e| JsValue::from_str(&format!("utf8 error: {}", e)))?;

                let msg = Message::Text { data: text };
                serde_wasm_bindgen::to_value(&msg)
                    .map_err(|e| JsValue::from_str(&format!("serialize error: {}", e)))
            }
            0x02 => {
                // Binary data
                let msg = Message::Binary {
                    data: data[4..].to_vec(),
                };
                serde_wasm_bindgen::to_value(&msg)
                    .map_err(|e| JsValue::from_str(&format!("serialize error: {}", e)))
            }
            _ => Err(JsValue::from_str(&format!(
                "unknown packet type: {}",
                packet_type
            ))),
        }
    }

    /// Create relay protocol packet from browser message
    #[wasm_bindgen(js_name = createPacket)]
    pub fn create_packet(msg: JsValue) -> Result<Vec<u8>, JsValue> {
        let msg: Message = serde_wasm_bindgen::from_value(msg)
            .map_err(|e| JsValue::from_str(&format!("deserialize error: {}", e)))?;

        let mut packet = Vec::new();

        match msg {
            Message::Text { data } => {
                packet.push(0x01); // Type: text
                packet.extend_from_slice(&[0, 0, 0]); // Reserved
                packet.extend_from_slice(data.as_bytes());
            }
            Message::Binary { data } => {
                packet.push(0x02); // Type: binary
                packet.extend_from_slice(&[0, 0, 0]); // Reserved
                packet.extend_from_slice(&data);
            }
            Message::File { data, .. } => {
                packet.push(0x03); // Type: file
                packet.extend_from_slice(&[0, 0, 0]); // Reserved
                packet.extend_from_slice(&data);
            }
            Message::Api { body, .. } => {
                packet.push(0x04); // Type: API
                packet.extend_from_slice(&[0, 0, 0]); // Reserved
                if let Some(body) = body {
                    packet.extend_from_slice(&body);
                }
            }
        }

        Ok(packet)
    }
}

use wasm_bindgen_futures::JsFuture;

trait IntoFuture {
    type Output;
    fn into_future(self) -> JsFuture;
}

impl IntoFuture for js_sys::Promise {
    type Output = JsValue;

    fn into_future(self) -> JsFuture {
        JsFuture::from(self)
    }
}
