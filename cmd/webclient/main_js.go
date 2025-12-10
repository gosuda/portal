package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
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
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"golang.org/x/net/idna"
	"gosuda.org/portal/cmd/webclient/httpjs"
	"gosuda.org/portal/portal/core/cryptoops"
	"gosuda.org/portal/sdk"
	"gosuda.org/portal/utils"
)

var (
	client *sdk.Client

	// SDK connection manager for Service Worker messaging
	sdkConnections   = make(map[string]io.ReadWriteCloser)
	sdkConnectionsMu sync.RWMutex

	// Reusable credential for HTTP connections (enables Keep-Alive)
	dialerCredential *cryptoops.Credential

	// DNS cache for lease name -> lease ID mapping
	dnsCache    sync.Map // map[string]*dnsCacheEntry
	dnsCacheTTL = 5 * time.Minute
)

type dnsCacheEntry struct {
	leaseID   string
	expiresAt time.Time
}

// getBootstrapServers retrieves bootstrap servers from global JavaScript variable
func getBootstrapServers() []string {
	// Try to get bootstrap servers from window.__BOOTSTRAP_SERVERS__
	bootstrapsValue := js.Global().Get("__BOOTSTRAP_SERVERS__")

	if bootstrapsValue.IsUndefined() || bootstrapsValue.IsNull() {
		log.Warn().Msg("__BOOTSTRAP_SERVERS__ not found in global scope, using default")
		return []string{"ws://localhost:4017/relay"}
	}

	// Handle string (comma-separated)
	if bootstrapsValue.Type() == js.TypeString {
		bootstrapsStr := bootstrapsValue.String()
		if bootstrapsStr == "" {
			return []string{"ws://localhost:4017/relay"}
		}
		servers := strings.Split(bootstrapsStr, ",")
		for i := range servers {
			servers[i] = strings.TrimSpace(servers[i])
		}
		return servers
	}

	// Handle array
	if bootstrapsValue.Type() == js.TypeObject && bootstrapsValue.Length() > 0 {
		servers := make([]string, bootstrapsValue.Length())
		for i := 0; i < bootstrapsValue.Length(); i++ {
			servers[i] = bootstrapsValue.Index(i).String()
		}
		return servers
	}

	log.Warn().Msg("Invalid __BOOTSTRAP_SERVERS__ format, using default")
	return []string{"ws://localhost:4017/relay"}
}

// lookupDNSCache checks the DNS cache for a cached lease ID
func lookupDNSCache(name string) (string, bool) {
	if entry, ok := dnsCache.Load(name); ok {
		cached := entry.(*dnsCacheEntry)
		if time.Now().Before(cached.expiresAt) {
			return cached.leaseID, true
		}
		// Expired entry, delete it
		dnsCache.Delete(name)
	}
	return "", false
}

// storeDNSCache stores a lease ID in the DNS cache
func storeDNSCache(name, leaseID string) {
	dnsCache.Store(name, &dnsCacheEntry{
		leaseID:   leaseID,
		expiresAt: time.Now().Add(dnsCacheTTL),
	})
}

// isValidUpgradeRequest validates that the upgrade request is a well-formed HTTP WebSocket upgrade
func isValidUpgradeRequest(req []byte) bool {
	if len(req) < 20 { // Minimum: "GET / HTTP/1.1\r\n\r\n"
		return false
	}
	s := string(req)
	// Must start with GET and end with double CRLF
	if !strings.HasPrefix(s, "GET ") {
		return false
	}
	if !strings.HasSuffix(s, "\r\n\r\n") {
		return false
	}
	// Must contain Upgrade header (case-insensitive check)
	if !strings.Contains(strings.ToLower(s), "upgrade:") {
		return false
	}
	return true
}

var rdDialer = func(ctx context.Context, network, address string) (net.Conn, error) {
	originalAddr := address
	address = strings.TrimSuffix(address, ":80")
	address = strings.TrimSuffix(address, ":443")

	decodedAddr, err := url.QueryUnescape(address)
	if err != nil {
		log.Debug().Err(err).Str("address", address).Msg("[Dialer] Failed to unescape address")
		decodedAddr = address
	}
	address = decodedAddr

	unicodeAddr, err := idna.ToUnicode(address)
	if err != nil {
		log.Debug().Err(err).Str("address", address).Msg("[Dialer] Failed to convert punycode")
		unicodeAddr = address
	} else if unicodeAddr != address {
		log.Debug().Str("punycode", address).Str("unicode", unicodeAddr).Msg("[Dialer] Converted punycode to unicode")
	}
	address = unicodeAddr

	// Check DNS cache first
	if cachedID, ok := lookupDNSCache(address); ok {
		log.Debug().Str("name", address).Str("id", cachedID).Msg("[Dialer] DNS cache hit")
		address = cachedID
	} else {
		// Cache miss - perform lookup
		lease, err := client.LookupName(address)
		if err == nil && lease != nil {
			leaseID := lease.Identity.Id
			log.Debug().Str("name", address).Str("id", leaseID).Msg("[Dialer] Found lease, caching")
			storeDNSCache(unicodeAddr, leaseID)
			address = leaseID
		} else {
			log.Debug().Err(err).Str("name", address).Msg("[Dialer] Lease lookup failed")
		}
	}

	if originalAddr != address {
		log.Info().Str("name", unicodeAddr).Str("resolved", address).Msg("[Dialer] Address resolved")
	}

	// Use reusable credential to enable HTTP Keep-Alive
	conn, err := client.Dial(dialerCredential, address, "http/1.1")
	if err != nil {
		log.Error().Err(err).Str("address", address).Msg("[Dialer] Dial failed")
		return nil, err
	}
	log.Info().Str("address", address).Msg("[Dialer] Connection Established")

	return conn, nil
}

var httpClient = &http.Client{
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

func getLeaseID(hostname string) string {
	// First, decode URL-encoded characters (e.g., %ED%8E%98%EC%9D%B8%ED%8A%B8 -> 페인트)
	decoded, err := url.QueryUnescape(hostname)
	if err != nil {
		decoded = hostname
	}

	// Normalize punycode to lowercase before conversion (punycode is case-insensitive)
	decoded = strings.ToLower(decoded)

	// Then, convert punycode to unicode (e.g., xn--v9jub -> 日本語)
	host, err := idna.ToUnicode(decoded)
	if err != nil {
		host = decoded
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

	// Decode hostname properly for IDN domains
	decodedHost := getLeaseID(r.URL.Hostname())
	r.URL.Host = decodedHost
	r.URL.Scheme = "http"

	resp, err := httpClient.Do(r)
	if err != nil {
		log.Error().Err(err).Msgf("Failed to proxy request to %s", r.URL.String())
		http.Error(w, fmt.Sprintf("Failed to proxy request to %s, err: %v", r.URL.String(), err), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	for key, value := range resp.Header {
		w.Header()[key] = value
	}

	if utils.IsHTMLContentType(resp.Header.Get("Content-Type")) {
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

	if strings.HasPrefix(path, "/sw-cgi/websocket/disconnect/") && r.Method == http.MethodPost {
		connID := strings.TrimPrefix(path, "/sw-cgi/websocket/disconnect/")
		p.handleDisconnect(w, r, connID)
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

func (p *Proxy) handleDisconnect(w http.ResponseWriter, r *http.Request, connID string) {
	log.Info().Str("connId", connID).Msg("Handling disconnect request")

	wsConn, ok := p.wsManager.GetConnection(connID)
	if !ok {
		// Connection already removed or doesn't exist - this is OK
		log.Debug().Str("connId", connID).Msg("Connection not found (already disconnected)")
		w.WriteHeader(http.StatusOK)
		return
	}

	// Close the WebSocket connection
	wsConn.Close()

	// Remove from manager
	p.wsManager.RemoveConnection(connID)

	log.Info().Str("connId", connID).Msg("WebSocket connection disconnected successfully")
	w.WriteHeader(http.StatusOK)
}

// SDK Connection handlers for Service Worker messaging

func handleSDKConnect(data js.Value) {
	defer func() {
		if r := recover(); r != nil {
			log.Error().Interface("panic", r).Msg("[SDK Connect] Recovered from panic")
		}
	}()

	// Safely extract fields with validation
	if data.Get("leaseName").Type() == js.TypeUndefined || data.Get("clientId").Type() == js.TypeUndefined {
		log.Warn().Msg("[SDK Connect] Missing required fields")
		return
	}

	leaseName := data.Get("leaseName").String()
	clientId := data.Get("clientId").String()

	// Extract pipelined upgrade request if present (reduces RTT from 2 to 1)
	var upgradeRequest []byte
	upgradeReqJS := data.Get("upgradeRequest")
	if upgradeReqJS.Type() != js.TypeUndefined && upgradeReqJS.Type() != js.TypeNull {
		if upgradeReqJS.InstanceOf(js.Global().Get("Uint8Array")) {
			length := upgradeReqJS.Get("length").Int()
			upgradeRequest = make([]byte, length)
			js.CopyBytesToGo(upgradeRequest, upgradeReqJS)
			log.Debug().Int("size", length).Msg("[SDK Connect] Pipelined upgrade request received")
		}
	}

	log.Info().Str("leaseName", leaseName).Str("clientId", clientId).Bool("pipelined", len(upgradeRequest) > 0).Msg("[SDK Connect] Connecting")

	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Error().Interface("panic", r).Str("clientId", clientId).Msg("[SDK Connect] Goroutine recovered from panic")
			}
		}()
		// Convert leaseName (may be punycode) to uppercase unicode
		normalizedLeaseName := getLeaseID(leaseName)
		log.Debug().Str("original", leaseName).Str("normalized", normalizedLeaseName).Msg("[SDK Connect] Normalized lease name")

		// Check DNS cache first, then lookup if needed
		var leaseID string
		if cachedID, ok := lookupDNSCache(normalizedLeaseName); ok {
			log.Debug().Str("name", normalizedLeaseName).Str("id", cachedID).Msg("[SDK Connect] DNS cache hit")
			leaseID = cachedID
		} else {
			// Cache miss - perform lookup
			lease, err := client.LookupName(normalizedLeaseName)
			if err != nil {
				log.Error().Err(err).Str("leaseName", leaseName).Msg("[SDK Connect] Lease lookup failed")
				js.Global().Call("__sdk_post_message", map[string]interface{}{
					"type":     "SDK_CONNECT_ERROR",
					"clientId": clientId,
					"error":    err.Error(),
				})
				return
			}
			leaseID = lease.GetIdentity().GetId()
			storeDNSCache(normalizedLeaseName, leaseID)
			log.Info().Str("leaseName", leaseName).Str("leaseID", leaseID).Msg("[SDK Connect] Lease found, cached")
		}

		// Create E2EE connection using SDK with lease ID
		cred := sdk.NewCredential()
		conn, err := client.Dial(cred, leaseID, "http/1.1")
		if err != nil {
			log.Error().Err(err).Str("leaseID", leaseID).Msg("[SDK Connect] Failed")

			// Send error to client
			js.Global().Call("__sdk_post_message", map[string]interface{}{
				"type":     "SDK_CONNECT_ERROR",
				"clientId": clientId,
				"error":    err.Error(),
			})
			return
		}

		// If pipelined upgrade request is present, validate and send it immediately (saves 1 RTT)
		if len(upgradeRequest) > 0 {
			if !isValidUpgradeRequest(upgradeRequest) {
				log.Warn().Str("leaseID", leaseID).Msg("[SDK Connect] Invalid pipelined upgrade request, skipping")
			} else {
				_, err := conn.Write(upgradeRequest)
				if err != nil {
					log.Error().Err(err).Str("leaseID", leaseID).Msg("[SDK Connect] Failed to send pipelined upgrade request")
					conn.Close()
					js.Global().Call("__sdk_post_message", map[string]interface{}{
						"type":     "SDK_CONNECT_ERROR",
						"clientId": clientId,
						"error":    err.Error(),
					})
					return
				}
				log.Debug().Str("leaseID", leaseID).Msg("[SDK Connect] Pipelined upgrade request sent")
			}
		}

		// Generate connection ID
		connID := generateConnID()

		// Store connection
		sdkConnectionsMu.Lock()
		sdkConnections[connID] = conn
		sdkConnectionsMu.Unlock()

		log.Info().Str("leaseName", leaseName).Str("connId", connID).Msg("[SDK Connect] Connected")

		// Send success to client
		js.Global().Call("__sdk_post_message", map[string]interface{}{
			"type":     "SDK_CONNECT_SUCCESS",
			"clientId": clientId,
			"connId":   connID,
		})

		// Start reading from connection
		go func() {
			buffer := make([]byte, 32*1024)
			for {
				n, err := conn.Read(buffer)
				if err != nil {
					if err != io.EOF {
						log.Error().Err(err).Str("connId", connID).Msg("[SDK Connect] Read error")
					}

					// Remove connection
					sdkConnectionsMu.Lock()
					delete(sdkConnections, connID)
					sdkConnectionsMu.Unlock()

					// Send close to client
					code := 1000
					if err != io.EOF {
						code = 1006
					}
					js.Global().Call("__sdk_post_message", map[string]interface{}{
						"type":     "SDK_DATA_CLOSE",
						"clientId": clientId,
						"connId":   connID,
						"code":     code,
					})
					return
				}

				// Copy data to JavaScript Uint8Array
				data := make([]byte, n)
				copy(data, buffer[:n])

				uint8Array := js.Global().Get("Uint8Array").New(n)
				js.CopyBytesToJS(uint8Array, data)

				// Send data to client
				js.Global().Call("__sdk_post_message", map[string]interface{}{
					"type":     "SDK_DATA",
					"clientId": clientId,
					"connId":   connID,
					"data":     uint8Array,
				})
			}
		}()
	}()
}

func handleSDKSend(data js.Value) {
	defer func() {
		if r := recover(); r != nil {
			log.Error().Interface("panic", r).Msg("[SDK Send] Recovered from panic")
		}
	}()

	// Safely extract fields with validation
	if data.Get("connId").Type() == js.TypeUndefined || data.Get("clientId").Type() == js.TypeUndefined || data.Get("data").Type() == js.TypeUndefined {
		log.Warn().Msg("[SDK Send] Missing required fields")
		return
	}

	connID := data.Get("connId").String()
	clientId := data.Get("clientId").String()
	payload := data.Get("data")

	// Get connection
	sdkConnectionsMu.RLock()
	conn, ok := sdkConnections[connID]
	sdkConnectionsMu.RUnlock()

	if !ok {
		log.Warn().Str("connId", connID).Msg("[SDK Send] Connection not found")
		js.Global().Call("__sdk_post_message", map[string]interface{}{
			"type":     "SDK_SEND_ERROR",
			"clientId": clientId,
			"connId":   connID,
			"error":    "connection not found",
		})
		return
	}

	// Convert payload to bytes
	var bytes []byte
	if payload.InstanceOf(js.Global().Get("Uint8Array")) {
		length := payload.Get("length").Int()
		bytes = make([]byte, length)
		js.CopyBytesToGo(bytes, payload)
	} else if payload.InstanceOf(js.Global().Get("ArrayBuffer")) {
		uint8Array := js.Global().Get("Uint8Array").New(payload)
		length := uint8Array.Get("length").Int()
		bytes = make([]byte, length)
		js.CopyBytesToGo(bytes, uint8Array)
	} else {
		log.Warn().Str("connId", connID).Msg("[SDK Send] Unsupported data type")
		return
	}

	go func() {
		_, err := conn.Write(bytes)
		if err != nil {
			log.Error().Err(err).Str("connId", connID).Msg("[SDK Send] Write failed")
			js.Global().Call("__sdk_post_message", map[string]interface{}{
				"type":     "SDK_SEND_ERROR",
				"clientId": clientId,
				"connId":   connID,
				"error":    err.Error(),
			})
		}
	}()
}

func handleSDKClose(data js.Value) {
	defer func() {
		if r := recover(); r != nil {
			log.Error().Interface("panic", r).Msg("[SDK Close] Recovered from panic")
		}
	}()

	// Safely extract fields with validation
	if data.Get("connId").Type() == js.TypeUndefined || data.Get("clientId").Type() == js.TypeUndefined {
		log.Warn().Msg("[SDK Close] Missing required fields")
		return
	}

	connID := data.Get("connId").String()
	clientId := data.Get("clientId").String()

	// Get and remove connection
	sdkConnectionsMu.Lock()
	conn, ok := sdkConnections[connID]
	if ok {
		delete(sdkConnections, connID)
	}
	sdkConnectionsMu.Unlock()

	if !ok {
		log.Warn().Str("connId", connID).Msg("[SDK Close] Connection not found")
		return
	}

	log.Info().Str("connId", connID).Msg("[SDK Close] Closing connection")
	conn.Close()

	// Send close confirmation to client
	js.Global().Call("__sdk_post_message", map[string]interface{}{
		"type":     "SDK_DATA_CLOSE",
		"clientId": clientId,
		"connId":   connID,
		"code":     1000,
	})
}

func main() {
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339})
	var err error

	// Get bootstrap servers from global JavaScript variable
	bootstrapServerList := getBootstrapServers()

	log.Info().Strs("servers", bootstrapServerList).Msg("Initializing RDClient with bootstrap servers from global variable")

	client, err = sdk.NewClient(
		sdk.WithBootstrapServers(bootstrapServerList),
		sdk.WithDialer(WebSocketDialerJS()),
	)
	if err != nil {
		panic(err)
	}
	defer client.Close()

	// Initialize reusable credential for HTTP connections
	dialerCredential = sdk.NewCredential()

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

	// Expose SDK connection handler for Service Worker messaging
	js.Global().Set("__sdk_message_handler", js.FuncOf(func(this js.Value, args []js.Value) interface{} {
		if len(args) < 2 {
			log.Warn().Msg("[SDK Message] Invalid arguments")
			return nil
		}

		messageType := args[0].String()
		data := args[1]

		switch messageType {
		case "SDK_CONNECT":
			handleSDKConnect(data)
		case "SDK_SEND":
			handleSDKSend(data)
		case "SDK_CLOSE":
			handleSDKClose(data)
		default:
			log.Warn().Str("type", messageType).Msg("[SDK Message] Unknown message type")
		}

		return nil
	}))
	log.Info().Msg("SDK message handler registered as __sdk_message_handler")

	// Expose benchmark function
	js.Global().Set("benchmarkCrypto", js.FuncOf(func(this js.Value, args []js.Value) interface{} {
		iterations := 100
		if len(args) > 0 && args[0].Type() == js.TypeNumber {
			iterations = args[0].Int()
		}

		startTime := time.Now()
		for i := 0; i < iterations; i++ {
			_, err := cryptoops.NewCredential()
			if err != nil {
				log.Error().Err(err).Msg("Failed to create credential during benchmark")
				return nil
			}
		}
		duration := time.Since(startTime)

		return duration.Milliseconds()
	}))
	log.Info().Msg("WASM benchmark function registered as benchmarkCrypto")

	// Signal to JS that the WASM module is ready
	js.Global().Call("wasmIsReady")

	if runtime.Compiler == "tinygo" {
		return
	}
	// Wait
	ch := make(chan bool)
	<-ch
}
