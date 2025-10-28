use wasm_bindgen::prelude::*;

mod adapters;
mod crypto;
mod proto;
mod protocol_codec;
mod proxy_engine;
mod relay_client;
mod simple_mux;
mod tunnel_manager;
mod utils;
mod ws_stream;

pub use adapters::{DataInterpreter, HttpAdapter, WebSocketAdapter};
pub use proxy_engine::ProxyEngine;
pub use relay_client::RelayClient;
pub use ws_stream::WebSocketStream;

// Global allocator for smaller binary size
#[global_allocator]
static ALLOC: wee_alloc::WeeAlloc = wee_alloc::WeeAlloc::INIT;

/// Initialize WASM module
#[wasm_bindgen(start)]
pub fn init() {
    // Set panic hook for better error messages in console
    console_error_panic_hook::set_once();
}

#[wasm_bindgen]
extern "C" {
    #[wasm_bindgen(js_namespace = console, js_name = log)]
    pub(crate) fn console_log_impl(s: &str);
}

#[allow(unused_macros)]
macro_rules! console_log {
    ($($t:tt)*) => (crate::console_log_impl(&format_args!($($t)*).to_string()))
}

pub(crate) use console_log;
