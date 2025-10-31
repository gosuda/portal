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
	"net/url"
	"os"
	"runtime"
	"strings"
	"sync"
	"syscall/js"
	"time"

	"github.com/gorilla/websocket"
	"github.com/gosuda/portal/cmd/webclient/httpjs"
	"github.com/gosuda/portal/sdk"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"golang.org/x/net/idna"
)

var (
	bootstrapServers = []string{"ws://localhost:4017/relay", "wss://portal.gosuda.org/relay"}
	rdClient         *sdk.RDClient
)

var rdDialer = func(ctx context.Context, network, address string) (net.Conn, error) {
	address = strings.TrimSuffix(address, ":80")
	address = strings.TrimSuffix(address, ":443")
	cred := sdk.NewCredential()
	conn, err := rdClient.Dial(cred, address, "http/1.1")
	if err != nil {
		return nil, err
	}
	return conn, nil
}

var client = &http.Client{
	Timeout: time.Second * 30,
	Transport: &http.Transport{
		MaxIdleConns:        1000,
		MaxIdleConnsPerHost: 100,
		DialContext:         rdDialer,
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
	id           string
	conn         *websocket.Conn
	messageChan  chan wsMessage
	closeChan    chan struct{}
	closeOnce    sync.Once
	mu           sync.Mutex
	messageQueue []StreamMessage
	queueMu      sync.Mutex
	isClosed     bool
}

type wsMessage struct {
	data   []byte
	isText bool
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
	Type        string `json:"type"` // "message", "close"
	Data        string `json:"data,omitempty"`
	MessageType string `json:"messageType,omitempty"` // "text", "binary"
	Code        int    `json:"code,omitempty"`
	Reason      string `json:"reason,omitempty"`
}

func NewWebSocketManager() *WebSocketManager {
	return &WebSocketManager{}
}

func generateConnID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func (m *WebSocketManager) CreateConnection(uri string, protocols []string) (*WSConnection, string, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return nil, "", err
	}
	id := getLeaseID(u.Hostname())

	u.Scheme = "ws"
	u.Host = id

	// Parse URL to extract host for rdDialer
	dialer := websocket.Dialer{
		NetDialContext: rdDialer,
		Subprotocols:   protocols,
	}

	conn, resp, err := dialer.Dial(u.String(), nil)
	if err != nil {
		return nil, "", err
	}

	// Get negotiated protocol
	negotiatedProtocol := ""
	if resp != nil && resp.Header != nil {
		negotiatedProtocol = resp.Header.Get("Sec-WebSocket-Protocol")
	}

	wsConn := &WSConnection{
		id:           generateConnID(),
		conn:         conn,
		messageChan:  make(chan wsMessage, 100),
		closeChan:    make(chan struct{}),
		messageQueue: make([]StreamMessage, 0),
	}

	m.connections.Store(wsConn.id, wsConn)

	// Start message receiver and queue manager
	go wsConn.receiveMessages()
	go wsConn.manageQueue()

	return wsConn, negotiatedProtocol, nil
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
		messageType, msg, err := c.conn.ReadMessage()
		if err != nil {
			log.Error().Err(err).Str("connId", c.id).Msg("Error receiving message")
			c.queueMu.Lock()
			c.isClosed = true
			c.queueMu.Unlock()
			return
		}

		// Only handle binary and text messages
		if messageType != websocket.BinaryMessage && messageType != websocket.TextMessage {
			continue
		}

		wsMsg := wsMessage{
			data:   msg,
			isText: messageType == websocket.TextMessage,
		}

		select {
		case c.messageChan <- wsMsg:
		case <-c.closeChan:
			return
		}
	}
}

func (c *WSConnection) manageQueue() {
	for {
		select {
		case msg := <-c.messageChan:
			c.queueMu.Lock()

			// Use message type from WebSocket frame
			messageType := "binary"
			if msg.isText {
				messageType = "text"
			}

			streamMsg := StreamMessage{
				Type:        "message",
				Data:        base64.StdEncoding.EncodeToString(msg.data),
				MessageType: messageType,
			}
			c.messageQueue = append(c.messageQueue, streamMsg)
			c.queueMu.Unlock()

		case <-c.closeChan:
			c.queueMu.Lock()
			c.isClosed = true
			c.messageQueue = append(c.messageQueue, StreamMessage{
				Type:   "close",
				Code:   1000,
				Reason: "Connection closed",
			})
			c.queueMu.Unlock()
			return
		}
	}
}

func (c *WSConnection) GetMessages() []StreamMessage {
	c.queueMu.Lock()
	defer c.queueMu.Unlock()

	messages := make([]StreamMessage, len(c.messageQueue))
	copy(messages, c.messageQueue)
	c.messageQueue = c.messageQueue[:0]

	return messages
}

func (c *WSConnection) IsClosed() bool {
	c.queueMu.Lock()
	defer c.queueMu.Unlock()
	return c.isClosed
}

func (c *WSConnection) Send(data []byte, isText bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	select {
	case <-c.closeChan:
		return fmt.Errorf("connection closed")
	default:
		messageType := websocket.BinaryMessage
		if isText {
			messageType = websocket.TextMessage
		}
		return c.conn.WriteMessage(messageType, data)
	}
}

func (c *WSConnection) Close() {
	c.closeOnce.Do(func() {
		close(c.closeChan)
		c.conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
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

func getLeaseID(hostname string) string {
	host, err := idna.ToUnicode(hostname)
	if err != nil {
		host = hostname
	}
	id := strings.Split(host, ".")[0]
	id = strings.TrimSpace(id)
	id = strings.ToUpper(id)
	return id
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Handle WebSocket polyfill endpoints
	if strings.HasPrefix(r.URL.Path, "/sw-cgi/websocket/") {
		p.handleWebSocketPolyfill(w, r)
		return
	}

	log.Info().Msgf("Proxying request to %s", r.URL.String())

	r = r.Clone(context.Background())
	r.URL.Host = getLeaseID(r.URL.Hostname())
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

	if strings.HasPrefix(path, "/sw-cgi/websocket/poll/") && r.Method == http.MethodGet {
		connID := strings.TrimPrefix(path, "/sw-cgi/websocket/poll/")
		p.handlePoll(w, r, connID)
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

	log.Info().Str("url", req.URL).Strs("protocols", req.Protocols).Msg("Creating WebSocket connection")

	wsConn, protocol, err := p.wsManager.CreateConnection(req.URL, req.Protocols)
	if err != nil {
		log.Error().Err(err).Msg("Failed to create WebSocket connection")
		http.Error(w, fmt.Sprintf("Failed to connect: %v", err), http.StatusBadGateway)
		return
	}

	resp := ConnectResponse{
		ConnID:   wsConn.id,
		Protocol: protocol,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (p *Proxy) handlePoll(w http.ResponseWriter, r *http.Request, connID string) {
	wsConn, ok := p.wsManager.GetConnection(connID)
	if !ok {
		http.Error(w, "Connection not found", http.StatusNotFound)
		return
	}

	// Long polling: wait up to 5 seconds for messages
	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()

	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	var messages []StreamMessage

	for {
		select {
		case <-timeout.C:
			// Timeout - return empty or existing messages
			messages = wsConn.GetMessages()
			goto respond

		case <-ticker.C:
			// Check for messages periodically
			messages = wsConn.GetMessages()
			if len(messages) > 0 {
				goto respond
			}

		case <-r.Context().Done():
			// Client disconnected
			return
		}
	}

respond:
	// Check if connection is closed and cleanup if needed
	if wsConn.IsClosed() && len(messages) > 0 {
		// Check if close message is in the queue
		for _, msg := range messages {
			if msg.Type == "close" {
				defer func() {
					p.wsManager.RemoveConnection(connID)
					wsConn.Close()
				}()
				break
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"messages": messages,
	})
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
	var isText bool

	switch req.Type {
	case "binary":
		data, err = base64.StdEncoding.DecodeString(req.Data)
		if err != nil {
			http.Error(w, "Invalid base64 data", http.StatusBadRequest)
			return
		}
		isText = false
	case "text":
		data = []byte(req.Data)
		isText = true
	default:
		http.Error(w, "Invalid message type", http.StatusBadRequest)
		return
	}

	if err := wsConn.Send(data, isText); err != nil {
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

	if runtime.Compiler == "tinygo" {
		return
	}
	// Wait
	ch := make(chan bool)
	<-ch
}
