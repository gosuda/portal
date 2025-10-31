package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"
	"syscall/js"
	"time"

	"github.com/gosuda/portal/cmd/webclient/httpjs"
	"github.com/gosuda/portal/cmd/webclient/wsjs"
	"github.com/gosuda/portal/sdk"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"golang.org/x/net/idna"
)

var (
	bootstrapServers = []string{"ws://localhost:4017/relay", "wss://portal.gosuda.org/relay"}
	rdClient         *sdk.RDClient
)

var client = &http.Client{
	Timeout: time.Second * 30,
	Transport: &http.Transport{
		MaxIdleConns:        1000,
		MaxIdleConnsPerHost: 100,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			address = strings.TrimSuffix(address, ":80")
			address = strings.TrimSuffix(address, ":443")
			cred := sdk.NewCredential()
			conn, err := rdClient.Dial(cred, address, "http/1.1")
			if err != nil {
				return nil, err
			}
			return conn, nil
		},
	},
}

type Proxy struct {
	wsManager *WebSocketManager
}

// WebSocket connection manager
type WebSocketManager struct {
	connections sync.Map // map[string]*WSConnection
}

type WSConnection struct {
	id          string
	conn        *wsjs.Conn
	messageChan chan []byte
	closeChan   chan struct{}
	closeOnce   sync.Once
	mu          sync.Mutex
}

type ConnectRequest struct {
	URL       string   `json:"url"`
	Protocols []string `json:"protocols"`
}

type ConnectResponse struct {
	ConnID   string `json:"connId"`
	Protocol string `json:"protocol"`
}

type SendRequest struct {
	Type   string `json:"type"` // "text", "binary", "close"
	Data   string `json:"data,omitempty"`
	Code   int    `json:"code,omitempty"`
	Reason string `json:"reason,omitempty"`
}

type StreamMessage struct {
	Type   string `json:"type"` // "message", "close"
	Data   string `json:"data,omitempty"`
	Code   int    `json:"code,omitempty"`
	Reason string `json:"reason,omitempty"`
}

func NewWebSocketManager() *WebSocketManager {
	return &WebSocketManager{}
}

func generateConnID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func (m *WebSocketManager) CreateConnection(url string) (*WSConnection, error) {
	conn, err := wsjs.Dial(url)
	if err != nil {
		return nil, err
	}

	wsConn := &WSConnection{
		id:          generateConnID(),
		conn:        conn,
		messageChan: make(chan []byte, 100),
		closeChan:   make(chan struct{}),
	}

	m.connections.Store(wsConn.id, wsConn)

	// Start message receiver
	go wsConn.receiveMessages()

	return wsConn, nil
}

func (m *WebSocketManager) GetConnection(id string) (*WSConnection, bool) {
	conn, ok := m.connections.Load(id)
	if !ok {
		return nil, false
	}
	return conn.(*WSConnection), true
}

func (m *WebSocketManager) RemoveConnection(id string) {
	m.connections.Delete(id)
}

func (c *WSConnection) receiveMessages() {
	defer c.Close()

	for {
		msg, err := c.conn.NextMessage()
		if err != nil {
			log.Error().Err(err).Str("connId", c.id).Msg("Error receiving message")
			return
		}

		select {
		case c.messageChan <- msg:
		case <-c.closeChan:
			return
		}
	}
}

func (c *WSConnection) Send(data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	select {
	case <-c.closeChan:
		return fmt.Errorf("connection closed")
	default:
		return c.conn.Send(data)
	}
}

func (c *WSConnection) Close() {
	c.closeOnce.Do(func() {
		close(c.closeChan)
		c.conn.Close()
	})
}

// IsHTMLContentType checks if the Content-Type header indicates HTML content
// It properly handles media type parsing with parameters like charset
func IsHTMLContentType(contentType string) bool {
	if contentType == "" {
		return false
	}

	// Parse the media type and parameters
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		// If parsing fails, do a simple case-insensitive check for "text/html"
		return strings.HasPrefix(strings.ToLower(contentType), "text/html")
	}

	// Check if the media type is HTML
	return mediaType == "text/html"
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Handle WebSocket polyfill endpoints
	if strings.HasPrefix(r.URL.Path, "/sw-cgi/websocket/") {
		p.handleWebSocketPolyfill(w, r)
		return
	}

	log.Info().Msgf("Proxying request to %s", r.URL.String())

	host, err := idna.ToUnicode(r.URL.Hostname())
	if err != nil {
		host = r.URL.Hostname()
	}
	id := strings.Split(host, ".")[0]
	id = strings.TrimSpace(id)
	id = strings.ToUpper(id)

	r = r.Clone(context.Background())
	r.URL.Host = id
	r.URL.Scheme = "http"

	resp, err := client.Do(r)
	if err != nil {
		log.Error().Err(err).Msgf("Failed to proxy request to %s", r.URL.String())
		http.Error(w, fmt.Sprintf("Failed to proxy request to %s, err: %v", r.URL.String(), err), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	for key, value := range resp.Header {
		w.Header()[key] = value
	}

	if IsHTMLContentType(resp.Header.Get("Content-Type")) {
		w.WriteHeader(resp.StatusCode)
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			log.Error().Err(err).Msg("Failed to read response body")
			return
		}
		body = InjectHTML(body)
		w.Write(body)
		return
	}

	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

func (p *Proxy) handleWebSocketPolyfill(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path

	if path == "/sw-cgi/websocket/connect" && r.Method == http.MethodPost {
		p.handleConnect(w, r)
		return
	}

	if strings.HasPrefix(path, "/sw-cgi/websocket/stream/") && r.Method == http.MethodGet {
		connID := strings.TrimPrefix(path, "/sw-cgi/websocket/stream/")
		p.handleStream(w, r, connID)
		return
	}

	if strings.HasPrefix(path, "/sw-cgi/websocket/send/") && r.Method == http.MethodPost {
		connID := strings.TrimPrefix(path, "/sw-cgi/websocket/send/")
		p.handleSend(w, r, connID)
		return
	}

	http.Error(w, "Not found", http.StatusNotFound)
}

func (p *Proxy) handleConnect(w http.ResponseWriter, r *http.Request) {
	var req ConnectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	log.Info().Str("url", req.URL).Msg("Creating WebSocket connection")

	wsConn, err := p.wsManager.CreateConnection(req.URL)
	if err != nil {
		log.Error().Err(err).Msg("Failed to create WebSocket connection")
		http.Error(w, fmt.Sprintf("Failed to connect: %v", err), http.StatusBadGateway)
		return
	}

	resp := ConnectResponse{
		ConnID:   wsConn.id,
		Protocol: "", // TODO: handle protocol negotiation
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (p *Proxy) handleStream(w http.ResponseWriter, r *http.Request, connID string) {
	wsConn, ok := p.wsManager.GetConnection(connID)
	if !ok {
		http.Error(w, "Connection not found", http.StatusNotFound)
		return
	}

	log.Info().Str("connId", connID).Msg("Starting message stream")

	// Set headers for streaming
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Content-Type-Options", "nosniff")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	// Send messages as newline-delimited JSON
	encoder := json.NewEncoder(w)
	for {
		select {
		case msg := <-wsConn.messageChan:
			streamMsg := StreamMessage{
				Type: "message",
				Data: base64.StdEncoding.EncodeToString(msg),
			}
			if err := encoder.Encode(streamMsg); err != nil {
				log.Error().Err(err).Msg("Failed to encode message")
				return
			}
			flusher.Flush()

		case <-wsConn.closeChan:
			streamMsg := StreamMessage{
				Type:   "close",
				Code:   1000,
				Reason: "Connection closed",
			}
			encoder.Encode(streamMsg)
			flusher.Flush()
			return

		case <-r.Context().Done():
			log.Info().Str("connId", connID).Msg("Stream context cancelled")
			return
		}
	}
}

func (p *Proxy) handleSend(w http.ResponseWriter, r *http.Request, connID string) {
	wsConn, ok := p.wsManager.GetConnection(connID)
	if !ok {
		http.Error(w, "Connection not found", http.StatusNotFound)
		return
	}

	var req SendRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	if req.Type == "close" {
		log.Info().Str("connId", connID).Msg("Closing WebSocket connection")
		wsConn.Close()
		p.wsManager.RemoveConnection(connID)
		w.WriteHeader(http.StatusOK)
		return
	}

	var data []byte
	var err error

	if req.Type == "binary" {
		data, err = base64.StdEncoding.DecodeString(req.Data)
		if err != nil {
			http.Error(w, "Invalid base64 data", http.StatusBadRequest)
			return
		}
	} else if req.Type == "text" {
		data = []byte(req.Data)
	} else {
		http.Error(w, "Invalid message type", http.StatusBadRequest)
		return
	}

	if err := wsConn.Send(data); err != nil {
		log.Error().Err(err).Msg("Failed to send message")
		http.Error(w, fmt.Sprintf("Failed to send: %v", err), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func main() {
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339})
	var err error

	rdClient, err = sdk.NewClient(
		sdk.WithBootstrapServers(bootstrapServers),
		sdk.WithDialer(WebSocketDialerJS()),
	)
	if err != nil {
		panic(err)
	}
	defer rdClient.Close()

	// Initialize WebSocket manager
	wsManager := NewWebSocketManager()
	proxy := &Proxy{
		wsManager: wsManager,
	}

	// Expose HTTP handler to JavaScript as __go_jshttp
	js.Global().Set("__go_jshttp", js.FuncOf(func(this js.Value, args []js.Value) interface{} {
		if len(args) < 1 {
			return js.Global().Get("Promise").Call("reject",
				js.Global().Get("Error").New("required parameter JSRequest missing"))
		}

		jsReq := args[0]
		return httpjs.ServeHTTPAsyncWithStreaming(proxy, jsReq)
	}))
	log.Info().Msg("Portal proxy handler registered as __go_jshttp")

	go serverWorker(rdClient)

	if runtime.Compiler == "tinygo" {
		return
	}
	// Wait
	ch := make(chan bool)
	<-ch
}

func serverWorker(client *sdk.RDClient) {
	time.Sleep(time.Second)

	cred := sdk.NewCredential()
	ln, err := client.Listen(cred, "WASM-Client-WebServer-"+cred.ID()[:8], []string{"http/1.1"})
	if err != nil {
		log.Error().Err(err).Msg("Failed to start listener")
		return
	}
	defer ln.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`<!DOCTYPE html>
<html>
<head>
	<title>WASM Client Server</title>
</head>
<body>
	<h1>Hello, World! This server is running in a WASM client</h1>
	<p>Server ID: ` + cred.ID() + `</p>
</body>
</html>`))
	})

	if err := http.Serve(ln, mux); err != nil {
		log.Error().Err(err).Msg("Failed to start server")
	}
}
