use futures::io::{AsyncRead, AsyncWrite};
use parking_lot::Mutex;
use std::collections::VecDeque;
use std::io;
use std::pin::Pin;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Arc;
use std::task::{Context, Poll, Waker};
use wasm_bindgen::prelude::*;
use wasm_bindgen::JsCast;
use web_sys::{CloseEvent, ErrorEvent, MessageEvent, WebSocket};

/// WebSocket stream adapter that implements AsyncRead + AsyncWrite
pub struct WebSocketStream {
    ws: WebSocket,
    read_buffer: Arc<Mutex<VecDeque<u8>>>,
    read_waker: Arc<Mutex<Option<Waker>>>,
    write_waker: Arc<Mutex<Option<Waker>>>,
    closed: Arc<AtomicBool>,
    error: Arc<Mutex<Option<String>>>,
}

impl WebSocketStream {
    /// Create a new WebSocketStream and wait for connection
    pub async fn connect(url: &str) -> Result<Self, JsValue> {
        let ws = WebSocket::new(url)?;
        ws.set_binary_type(web_sys::BinaryType::Arraybuffer);

        let read_buffer = Arc::new(Mutex::new(VecDeque::new()));
        let read_waker: Arc<Mutex<Option<Waker>>> = Arc::new(Mutex::new(None));
        let write_waker: Arc<Mutex<Option<Waker>>> = Arc::new(Mutex::new(None));
        let closed = Arc::new(AtomicBool::new(false));
        let error: Arc<Mutex<Option<String>>> = Arc::new(Mutex::new(None));

        // Setup onmessage handler
        {
            let read_buffer = read_buffer.clone();
            let read_waker = read_waker.clone();

            let onmessage = Closure::wrap(Box::new(move |e: MessageEvent| {
                if let Ok(array_buffer) = e.data().dyn_into::<js_sys::ArrayBuffer>() {
                    let array = js_sys::Uint8Array::new(&array_buffer);
                    let data = array.to_vec();

                    // Add data to read buffer
                    {
                        let mut buffer = read_buffer.lock();
                        buffer.extend(data.iter());
                    }

                    // Wake up pending read
                    if let Some(waker) = read_waker.lock().take() {
                        waker.wake();
                    }
                }
            }) as Box<dyn FnMut(MessageEvent)>);

            ws.set_onmessage(Some(onmessage.as_ref().unchecked_ref()));
            onmessage.forget();
        }

        // Setup onerror handler
        {
            let error = error.clone();
            let closed = closed.clone();
            let read_waker = read_waker.clone();

            let onerror = Closure::wrap(Box::new(move |_e: ErrorEvent| {
                *error.lock() = Some("WebSocket error".to_string());
                closed.store(true, Ordering::SeqCst);

                if let Some(waker) = read_waker.lock().take() {
                    waker.wake();
                }
            }) as Box<dyn FnMut(ErrorEvent)>);

            ws.set_onerror(Some(onerror.as_ref().unchecked_ref()));
            onerror.forget();
        }

        // Setup onclose handler
        {
            let closed = closed.clone();
            let read_waker = read_waker.clone();

            let onclose = Closure::wrap(Box::new(move |_e: CloseEvent| {
                closed.store(true, Ordering::SeqCst);

                if let Some(waker) = read_waker.lock().take() {
                    waker.wake();
                }
            }) as Box<dyn FnMut(CloseEvent)>);

            ws.set_onclose(Some(onclose.as_ref().unchecked_ref()));
            onclose.forget();
        }

        // Wait for connection to open
        let (tx, rx) = futures::channel::oneshot::channel();
        let tx = Arc::new(Mutex::new(Some(tx)));

        {
            let tx = tx.clone();
            let onopen = Closure::wrap(Box::new(move |_| {
                if let Some(tx) = tx.lock().take() {
                    let _ = tx.send(());
                }
            }) as Box<dyn FnMut(JsValue)>);

            ws.set_onopen(Some(onopen.as_ref().unchecked_ref()));
            onopen.forget();
        }

        // Wait for open event
        rx.await
            .map_err(|_| JsValue::from_str("WebSocket connection failed"))?;

        Ok(Self {
            ws,
            read_buffer,
            read_waker,
            write_waker,
            closed,
            error,
        })
    }

    /// Check if there's an error
    fn check_error(&self) -> io::Result<()> {
        if let Some(err) = self.error.lock().as_ref() {
            return Err(io::Error::new(io::ErrorKind::Other, err.clone()));
        }
        Ok(())
    }
}

impl AsyncRead for WebSocketStream {
    fn poll_read(
        self: Pin<&mut Self>,
        cx: &mut Context<'_>,
        buf: &mut [u8],
    ) -> Poll<io::Result<usize>> {
        // Check for errors
        self.check_error()?;

        // Check if closed
        if self.closed.load(Ordering::SeqCst) {
            let read_buffer = self.read_buffer.lock();
            if read_buffer.is_empty() {
                return Poll::Ready(Ok(0)); // EOF
            }
        }

        let mut read_buffer = self.read_buffer.lock();

        if read_buffer.is_empty() {
            // No data available, register waker
            *self.read_waker.lock() = Some(cx.waker().clone());
            return Poll::Pending;
        }

        // Copy data from buffer
        let to_copy = buf.len().min(read_buffer.len());
        for i in 0..to_copy {
            buf[i] = read_buffer.pop_front().unwrap();
        }

        Poll::Ready(Ok(to_copy))
    }
}

impl AsyncWrite for WebSocketStream {
    fn poll_write(
        self: Pin<&mut Self>,
        cx: &mut Context<'_>,
        buf: &[u8],
    ) -> Poll<io::Result<usize>> {
        // Check for errors
        self.check_error()?;

        // Check if closed
        if self.closed.load(Ordering::SeqCst) {
            return Poll::Ready(Err(io::Error::new(
                io::ErrorKind::BrokenPipe,
                "WebSocket closed",
            )));
        }

        // Check buffered amount (backpressure)
        const MAX_BUFFER_SIZE: u32 = 64 * 1024; // 64KB
        if self.ws.buffered_amount() > MAX_BUFFER_SIZE {
            // Too much buffered data, apply backpressure
            *self.write_waker.lock() = Some(cx.waker().clone());
            return Poll::Pending;
        }

        // Send data
        match self.ws.send_with_u8_array(buf) {
            Ok(_) => Poll::Ready(Ok(buf.len())),
            Err(e) => Poll::Ready(Err(io::Error::new(
                io::ErrorKind::Other,
                format!("WebSocket send failed: {:?}", e),
            ))),
        }
    }

    fn poll_flush(self: Pin<&mut Self>, _cx: &mut Context<'_>) -> Poll<io::Result<()>> {
        // WebSocket flushes automatically
        Poll::Ready(Ok(()))
    }

    fn poll_close(self: Pin<&mut Self>, _cx: &mut Context<'_>) -> Poll<io::Result<()>> {
        if !self.closed.load(Ordering::SeqCst) {
            let _ = self.ws.close();
            self.closed.store(true, Ordering::SeqCst);
        }
        Poll::Ready(Ok(()))
    }
}
