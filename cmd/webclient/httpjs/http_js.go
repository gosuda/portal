package httpjs

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/textproto"
	"strings"
	"syscall/js"

	"gosuda.org/portal/cmd/webclient/streamjs"
)

var (
	ErrRequestFailed = errors.New("request failed")
	ErrAborted       = errors.New("request aborted")
)

var (
	_fetch       = js.Global().Get("fetch")
	_Headers     = js.Global().Get("Headers")
	_Response    = js.Global().Get("Response")
	_ArrayBuffer = js.Global().Get("ArrayBuffer")
	_Uint8Array  = js.Global().Get("Uint8Array")
	_Promise     = js.Global().Get("Promise")
	_Object      = js.Global().Get("Object")
	_Array       = js.Global().Get("Array")
	_Error       = js.Global().Get("Error")
)

// Request represents an HTTP request that will be sent via fetch API
type Request struct {
	Method  string
	URL     string
	Headers map[string]string
	Body    []byte
}

// Response represents an HTTP response with streaming body support
type Response struct {
	StatusCode int
	Headers    map[string]string
	Body       *streamjs.ReadableStream

	jsResponse js.Value
	bodyReader io.ReadCloser // Store the underlying reader for ReadAll
}

// NewRequest creates a new HTTP request
func NewRequest(method, url string) *Request {
	return &Request{
		Method:  method,
		URL:     url,
		Headers: make(map[string]string),
	}
}

// SetHeader sets a request header
func (r *Request) SetHeader(key, value string) {
	r.Headers[key] = value
}

// SetBody sets the request body from a byte slice
func (r *Request) SetBody(body []byte) {
	r.Body = body
}

// Do executes the HTTP request and returns a Response
func (r *Request) Do() (*Response, error) {
	// Create fetch options
	opts := _Object.New()
	opts.Set("method", r.Method)

	// Set headers
	if len(r.Headers) > 0 {
		jsHeaders := _Headers.New()
		for key, value := range r.Headers {
			jsHeaders.Call("append", key, value)
		}
		opts.Set("headers", jsHeaders)
	}

	// Set body if present (convert to ArrayBuffer)
	if len(r.Body) > 0 {
		buffer := _ArrayBuffer.New(len(r.Body))
		array := _Uint8Array.New(buffer)
		js.CopyBytesToJS(array, r.Body)
		opts.Set("body", buffer)
	}

	// Create channels for async result
	resultCh := make(chan *Response, 1)
	errCh := make(chan error, 1)

	// Execute fetch
	fetchPromise := _fetch.Invoke(r.URL, opts)

	// Handle response
	var thenFunc, catchFunc js.Func

	thenFunc = js.FuncOf(func(this js.Value, args []js.Value) interface{} {
		defer thenFunc.Release()

		jsResp := args[0]

		// Parse response
		resp := &Response{
			StatusCode: jsResp.Get("status").Int(),
			Headers:    make(map[string]string),
			jsResponse: jsResp,
		}

		// Extract headers
		jsHeaders := jsResp.Get("headers")
		entriesIter := jsHeaders.Call("entries")

		for {
			next := entriesIter.Call("next")
			if next.Get("done").Bool() {
				break
			}
			entry := next.Get("value")
			key := entry.Index(0).String()
			value := entry.Index(1).String()
			resp.Headers[key] = value
		}

		// Get body as ReadableStream
		jsBody := jsResp.Get("body")
		if !jsBody.IsNull() && !jsBody.IsUndefined() {
			// Create a Go reader that reads from JS ReadableStream
			reader := &jsStreamReader{
				jsReader: jsBody.Call("getReader"),
			}
			resp.bodyReader = reader
			resp.Body = streamjs.NewReadableStream(reader)
		}

		resultCh <- resp
		return nil
	})

	catchFunc = js.FuncOf(func(this js.Value, args []js.Value) interface{} {
		defer catchFunc.Release()

		if len(args) > 0 {
			errMsg := args[0].Get("message").String()
			errCh <- errors.New(errMsg)
		} else {
			errCh <- ErrRequestFailed
		}
		return nil
	})

	fetchPromise.Call("then", thenFunc).Call("catch", catchFunc)

	// Wait for result
	select {
	case resp := <-resultCh:
		return resp, nil
	case err := <-errCh:
		return nil, err
	}
}

// jsStreamReader implements io.ReadCloser by reading from a JS ReadableStream
type jsStreamReader struct {
	jsReader js.Value
	closed   bool
}

func (r *jsStreamReader) Read(p []byte) (n int, err error) {
	if r.closed {
		return 0, io.EOF
	}

	// Create channels for async read
	resultCh := make(chan readResult, 1)

	// Call read() on the reader
	readPromise := r.jsReader.Call("read")

	var thenFunc js.Func
	thenFunc = js.FuncOf(func(this js.Value, args []js.Value) interface{} {
		defer thenFunc.Release()

		result := args[0]
		done := result.Get("done").Bool()

		if done {
			resultCh <- readResult{n: 0, err: io.EOF}
			return nil
		}

		// Get the chunk (Uint8Array)
		chunk := result.Get("value")
		if chunk.IsNull() || chunk.IsUndefined() {
			resultCh <- readResult{n: 0, err: nil}
			return nil
		}

		// Copy data from JS to Go
		length := chunk.Get("byteLength").Int()
		if length == 0 {
			resultCh <- readResult{n: 0, err: nil}
			return nil
		}

		// Copy as much as we can fit in p
		copyLen := length
		if copyLen > len(p) {
			copyLen = len(p)
		}

		// Create a temporary Uint8Array view if we need to copy partial data
		if copyLen < length {
			chunk = _Uint8Array.New(chunk.Get("buffer"), chunk.Get("byteOffset"), copyLen)
		}

		js.CopyBytesToGo(p[:copyLen], chunk)
		resultCh <- readResult{n: copyLen, err: nil}
		return nil
	})

	readPromise.Call("then", thenFunc)

	// Wait for result
	res := <-resultCh
	return res.n, res.err
}

func (r *jsStreamReader) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true

	// Cancel the reader
	if !r.jsReader.IsNull() && !r.jsReader.IsUndefined() {
		r.jsReader.Call("cancel")
	}
	return nil
}

type readResult struct {
	n   int
	err error
}

// ReadAll reads the entire response body into a byte slice
func (resp *Response) ReadAll() ([]byte, error) {
	if resp.bodyReader == nil {
		return []byte{}, nil
	}

	var buf bytes.Buffer
	buffer := make([]byte, 4096)

	for {
		n, err := resp.bodyReader.Read(buffer)
		if n > 0 {
			buf.Write(buffer[:n])
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
	}

	return buf.Bytes(), nil
}

// Close closes the response body stream
func (resp *Response) Close() error {
	if resp.Body != nil {
		resp.Body.Close()
	}
	return nil
}

// Get performs a GET request
func Get(url string) (*Response, error) {
	req := NewRequest("GET", url)
	return req.Do()
}

// Post performs a POST request with the given body
func Post(url string, contentType string, body []byte) (*Response, error) {
	req := NewRequest("POST", url)
	if contentType != "" {
		req.SetHeader("Content-Type", contentType)
	}
	req.SetBody(body)
	return req.Do()
}

// Put performs a PUT request with the given body
func Put(url string, contentType string, body []byte) (*Response, error) {
	req := NewRequest("PUT", url)
	if contentType != "" {
		req.SetHeader("Content-Type", contentType)
	}
	req.SetBody(body)
	return req.Do()
}

// Delete performs a DELETE request
func Delete(url string) (*Response, error) {
	req := NewRequest("DELETE", url)
	return req.Do()
}

// JSRequestToHTTPRequest converts a JavaScript Request object to net/http.Request
func JSRequestToHTTPRequest(jsReq js.Value) (*http.Request, error) {
	// Get method and URL
	method := jsReq.Get("method").String()
	url := jsReq.Get("url").String()

	// Read body as ArrayBuffer
	var bodyReader io.Reader
	jsBody := jsReq.Get("body")

	if !jsBody.IsNull() && !jsBody.IsUndefined() {
		// Create promise to read body
		bodyPromise := jsReq.Call("arrayBuffer")

		bodyChan := make(chan []byte, 1)
		errChan := make(chan error, 1)

		var successFunc, failFunc js.Func
		successFunc = js.FuncOf(func(this js.Value, args []js.Value) interface{} {
			defer successFunc.Release()

			jsBodyArray := _Uint8Array.New(args[0])
			bodyBuffer := make([]byte, jsBodyArray.Get("byteLength").Int())
			js.CopyBytesToGo(bodyBuffer, jsBodyArray)
			bodyChan <- bodyBuffer
			return nil
		})

		failFunc = js.FuncOf(func(this js.Value, args []js.Value) interface{} {
			defer failFunc.Release()

			if len(args) > 0 {
				errChan <- errors.New(args[0].String())
			} else {
				errChan <- errors.New("failed to read request body")
			}
			return nil
		})

		bodyPromise.Call("then", successFunc).Call("catch", failFunc)

		select {
		case body := <-bodyChan:
			bodyReader = bytes.NewReader(body)
		case err := <-errChan:
			return nil, err
		}
	} else {
		bodyReader = bytes.NewReader([]byte{})
	}

	// Create HTTP request
	httpReq, err := http.NewRequest(method, url, bodyReader)
	if err != nil {
		return nil, err
	}

	// Convert headers
	jsHeaders := _Array.Call("from", jsReq.Get("headers").Call("entries"))
	headersLen := jsHeaders.Length()

	var headerBuilder strings.Builder
	for i := 0; i < headersLen; i++ {
		entry := jsHeaders.Index(i)
		if entry.Length() < 2 {
			continue
		}

		key := entry.Index(0).String()
		value := entry.Index(1).String()

		headerBuilder.WriteString(key)
		headerBuilder.WriteString(": ")
		headerBuilder.WriteString(value)
		headerBuilder.WriteString("\r\n")
	}
	headerBuilder.WriteString("\r\n")

	// Parse headers using textproto
	tpr := textproto.NewReader(bufio.NewReader(strings.NewReader(headerBuilder.String())))
	mimeHeader, err := tpr.ReadMIMEHeader()
	if err != nil {
		return nil, err
	}
	httpReq.Header = http.Header(mimeHeader)

	return httpReq, nil
}

// HTTPResponseToJSResponse converts an http.Response to a JavaScript Response object with streaming support
func HTTPResponseToJSResponse(httpResp *http.Response) js.Value {
	// Create JS headers object
	jsHeaders := _Object.New()
	for key, values := range httpResp.Header {
		if len(values) > 0 {
			jsHeaders.Set(key, values[0])
		}
	}

	// Create streaming body using ReadableStream
	var jsBody js.Value
	if httpResp.Body != nil {
		stream := streamjs.NewReadableStream(httpResp.Body)
		jsBody = stream.Value
	} else {
		jsBody = js.Null()
	}

	// Create response options
	jsOptions := _Object.New()
	jsOptions.Set("status", httpResp.StatusCode)
	jsOptions.Set("statusText", httpResp.Status)
	jsOptions.Set("headers", jsHeaders)

	// Create and return JS Response
	jsResp := _Response.New(jsBody, jsOptions)
	return jsResp
}

// ServeHTTPAsyncWithStreaming handles an HTTP request asynchronously and returns a streaming JS Response
func ServeHTTPAsyncWithStreaming(handler http.Handler, jsReq js.Value) js.Value {
	return _Promise.New(js.FuncOf(func(this js.Value, args []js.Value) interface{} {
		resolve := args[0]
		reject := args[1]

		go func() {
			// Convert JS Request to http.Request
			httpReq, err := JSRequestToHTTPRequest(jsReq)
			if err != nil {
				reject.Invoke(_Error.New(err.Error()))
				return
			}

			// Create a pipe for streaming response
			pr, pw := io.Pipe()

			// Create custom ResponseWriter that writes to pipe
			respWriter := &streamingResponseWriter{
				pipeWriter:      pw,
				header:          make(http.Header),
				statusCode:      200,
				wroteHeaderChan: make(chan struct{}, 1),
			}

			// Serve HTTP in goroutine
			go func() {
				defer pw.Close()
				defer func() {
					if r := recover(); r != nil {
						// Handle panic
						respWriter.statusCode = http.StatusInternalServerError
						pw.CloseWithError(errors.New("internal server error"))
					}
				}()

				handler.ServeHTTP(respWriter, httpReq)

				if !respWriter.wroteHeader {
					respWriter.WriteHeader(http.StatusBadGateway)
					http.Error(respWriter, "Bad Gateway\n\nUpstream server error", http.StatusBadGateway)
				}
			}()

			<-respWriter.wroteHeaderChan

			// Create http.Response with streaming body
			httpResp := &http.Response{
				StatusCode: respWriter.statusCode,
				Status:     http.StatusText(respWriter.statusCode),
				Header:     respWriter.header,
				Body:       pr,
			}

			// Convert to JS Response with streaming
			jsResp := HTTPResponseToJSResponse(httpResp)
			resolve.Invoke(jsResp)
		}()

		return nil
	}))
}

// streamingResponseWriter implements http.ResponseWriter for streaming responses
type streamingResponseWriter struct {
	pipeWriter      *io.PipeWriter
	header          http.Header
	statusCode      int
	wroteHeader     bool
	wroteHeaderChan chan struct{}
}

func (w *streamingResponseWriter) Header() http.Header {
	return w.header
}

func (w *streamingResponseWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.pipeWriter.Write(b)
}

func (w *streamingResponseWriter) WriteHeader(statusCode int) {
	if !w.wroteHeader {
		w.statusCode = statusCode
		w.wroteHeader = true
		close(w.wroteHeaderChan)
	}
}
