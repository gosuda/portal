use wasm_bindgen::JsValue;

/// Convert Rust error to JsValue
pub fn to_js_error<E: std::fmt::Debug>(err: E) -> JsValue {
    JsValue::from_str(&format!("{:?}", err))
}

/// Current Unix timestamp in seconds
pub fn unix_timestamp() -> i64 {
    (js_sys::Date::now() / 1000.0) as i64
}

/// Generate random bytes using browser's crypto API
pub fn random_bytes(len: usize) -> Vec<u8> {
    let mut buf = vec![0u8; len];
    getrandom::getrandom(&mut buf).expect("failed to generate random bytes");
    buf
}
