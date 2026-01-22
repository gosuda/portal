//go:build prod

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
	"runtime"
	"strings"
	"sync"
	"syscall/js"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/net/idna"
	"gosuda.org/portal/cmd/webclient/httpjs"
	"gosuda.org/portal/portal/core/cryptoops"
	"gosuda.org/portal/sdk"
	"gosuda.org/portal/utils"
)

// Production build: no logging overhead

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
	bootstrapsValue := js.Global().Get("__BOOTSTRAP_SERVERS__")

	if bootstrapsValue.IsUndefined() || bootstrapsValue.IsNull() {
		return []string{"ws://localhost:4017/relay"}
	}

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

	if bootstrapsValue.Type() == js.TypeObject && bootstrapsValue.Length() > 0 {
		servers := make([]string, bootstrapsValue.Length())
		for i := 0; i < bootstrapsValue.Length(); i++ {
			servers[i] = bootstrapsValue.Index(i).String()
		}
		return servers
	}

	return []string{"ws://localhost:4017/relay"}
}

func lookupDNSCache(name string) (string, bool) {
	if entry, ok := dnsCache.Load(name); ok {
		cached := entry.(*dnsCacheEntry)
		if time.Now().Before(cached.expiresAt) {
			return cached.leaseID, true
		}
		dnsCache.Delete(name)
	}
	return "", false
}

func storeDNSCache(name, leaseID string) {
	dnsCache.Store(name, &dnsCacheEntry{
		leaseID:   leaseID,
		expiresAt: time.Now().Add(dnsCacheTTL),
	})
}

func isValidUpgradeRequest(req []byte) bool {
	if len(req) < 20 {
		return false
	}
	s := string(req)
	if !strings.HasPrefix(s, "GET ") {
		return false
	}
	if !strings.HasSuffix(s, "\r\n\r\n") {
		return false
	}
	if !strings.Contains(strings.ToLower(s), "upgrade:") {
		return false
	}
	return true
}

var rdDialer = func(ctx context.Context, network, address string) (net.Conn, error) {
	address = strings.TrimSuffix(address, ":80")
	address = strings.TrimSuffix(address, ":443")

	decodedAddr, err := url.QueryUnescape(address)
	if err != nil {
		decodedAddr = address
	}
	address = decodedAddr

	unicodeAddr, err := idna.ToUnicode(address)
	if err != nil {
		unicodeAddr = address
	}
	address = unicodeAddr

	if cachedID, ok := lookupDNSCache(address); ok {
		address = cachedID
	} else {
		lease, err := client.LookupName(address)
		if err == nil && lease != nil {
			leaseID := lease.Identity.Id
			storeDNSCache(unicodeAddr, leaseID)
			address = leaseID
		}
	}

	conn, err := client.Dial(dialerCredential, address, "http/1.1")
	if err != nil {
		return nil, err
	}

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

type WebSocketManager struct {
	connections sync.Map
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
	Type   string `json:"type"`
	Data   string `json:"data,omitempty"`
	Code   int    `json:"code,omitempty"`
	Reason string `json:"reason,omitempty"`
}

type StreamMessage struct {
	Type        string `json:"type"`
	Data        string `json:"data,omitempty"`
	MessageType string `json:"messageType,omitempty"`
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

	dialer := websocket.Dialer{
		NetDialContext: rdDialer,
		Subprotocols:   protocols,
	}

	conn, resp, err := dialer.Dial(u.String(), nil)
	if err != nil {
		return nil, "", err
	}

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
			c.queueMu.Lock()
			c.isClosed = true
			c.queueMu.Unlock()
			return
		}

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
	decoded, err := url.QueryUnescape(hostname)
	if err != nil {
		decoded = hostname
	}

	decoded = strings.ToLower(decoded)

	host, err := idna.ToUnicode(decoded)
	if err != nil {
		host = decoded
	}

	id := strings.Split(host, ".")[0]
	id = strings.TrimSpace(id)
	id = strings.ToUpper(id)
	return id
}

func InjectHTML(body []byte) []byte {
	// In production, HTML injection is handled by service worker
	return body
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/sw-cgi/websocket/") {
		p.handleWebSocketPolyfill(w, r)
		return
	}

	r = r.Clone(context.Background())

	decodedHost := getLeaseID(r.URL.Hostname())
	r.URL.Host = decodedHost
	r.URL.Scheme = "http"

	resp, err := httpClient.Do(r)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to proxy request to %s", r.URL.String()), http.StatusBadGateway)
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
			return
		}
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

	wsConn, protocol, err := p.wsManager.CreateConnection(req.URL, req.Protocols)
	if err != nil {
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

	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()

	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	var messages []StreamMessage

	for {
		select {
		case <-timeout.C:
			messages = wsConn.GetMessages()
			goto respond

		case <-ticker.C:
			messages = wsConn.GetMessages()
			if len(messages) > 0 {
				goto respond
			}

		case <-r.Context().Done():
			return
		}
	}

respond:
	if wsConn.IsClosed() && len(messages) > 0 {
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
		http.Error(w, fmt.Sprintf("Failed to send: %v", err), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func (p *Proxy) handleDisconnect(w http.ResponseWriter, r *http.Request, connID string) {
	wsConn, ok := p.wsManager.GetConnection(connID)
	if !ok {
		w.WriteHeader(http.StatusOK)
		return
	}

	wsConn.Close()
	p.wsManager.RemoveConnection(connID)

	w.WriteHeader(http.StatusOK)
}

func handleSDKConnect(data js.Value) {
	defer func() {
		if r := recover(); r != nil {
		}
	}()

	if data.Get("leaseName").Type() == js.TypeUndefined || data.Get("clientId").Type() == js.TypeUndefined {
		return
	}

	leaseName := data.Get("leaseName").String()
	clientId := data.Get("clientId").String()

	var upgradeRequest []byte
	upgradeReqJS := data.Get("upgradeRequest")
	if upgradeReqJS.Type() != js.TypeUndefined && upgradeReqJS.Type() != js.TypeNull {
		if upgradeReqJS.InstanceOf(js.Global().Get("Uint8Array")) {
			length := upgradeReqJS.Get("length").Int()
			upgradeRequest = make([]byte, length)
			js.CopyBytesToGo(upgradeRequest, upgradeReqJS)
		}
	}

	go func() {
		defer func() {
			if r := recover(); r != nil {
			}
		}()

		lease, err := client.LookupName(leaseName)
		if err != nil {
			js.Global().Call("__sdk_post_message", map[string]interface{}{
				"type":     "SDK_CONNECT_ERROR",
				"clientId": clientId,
				"error":    "lease not found",
			})
			return
		}

		connID := lease.Identity.Id

		cred := sdk.NewCredential()
		conn, err := client.Dial(cred, connID, "rdsec/1.0")
		if err != nil {
			js.Global().Call("__sdk_post_message", map[string]interface{}{
				"type":     "SDK_CONNECT_ERROR",
				"clientId": clientId,
				"error":    err.Error(),
			})
			return
		}

		sdkConnectionsMu.Lock()
		sdkConnections[connID] = conn
		sdkConnectionsMu.Unlock()

		js.Global().Call("__sdk_post_message", map[string]interface{}{
			"type":     "SDK_CONNECT_SUCCESS",
			"clientId": clientId,
			"connId":   connID,
		})

		if len(upgradeRequest) > 0 {
			if !isValidUpgradeRequest(upgradeRequest) {
				js.Global().Call("__sdk_post_message", map[string]interface{}{
					"type":     "SDK_CONNECT_ERROR",
					"clientId": clientId,
					"error":    "invalid upgrade request",
				})
				return
			}

			_, err = conn.Write(upgradeRequest)
			if err != nil {
				js.Global().Call("__sdk_post_message", map[string]interface{}{
					"type":     "SDK_SEND_ERROR",
					"clientId": clientId,
					"connId":   connID,
					"error":    err.Error(),
				})
				return
			}
		}

		buffer := make([]byte, 32*1024)
		for {
			n, err := conn.Read(buffer)
			if err != nil {
				sdkConnectionsMu.Lock()
				delete(sdkConnections, connID)
				sdkConnectionsMu.Unlock()

				js.Global().Call("__sdk_post_message", map[string]interface{}{
					"type":     "SDK_DATA_CLOSE",
					"clientId": clientId,
					"connId":   connID,
					"code":     1000,
				})
				return
			}

			if n > 0 {
				data := make([]byte, n)
				copy(data, buffer[:n])

				uint8Array := js.Global().Get("Uint8Array").New(n)
				js.CopyBytesToJS(uint8Array, data)

				js.Global().Call("__sdk_post_message", map[string]interface{}{
					"type":     "SDK_DATA",
					"clientId": clientId,
					"connId":   connID,
					"data":     uint8Array,
				})
			}
		}
	}()
}

func handleSDKSend(data js.Value) {
	defer func() {
		if r := recover(); r != nil {
		}
	}()

	if data.Get("connId").Type() == js.TypeUndefined || data.Get("clientId").Type() == js.TypeUndefined || data.Get("data").Type() == js.TypeUndefined {
		return
	}

	connID := data.Get("connId").String()
	clientId := data.Get("clientId").String()
	payload := data.Get("data")

	sdkConnectionsMu.RLock()
	conn, ok := sdkConnections[connID]
	sdkConnectionsMu.RUnlock()

	if !ok {
		js.Global().Call("__sdk_post_message", map[string]interface{}{
			"type":     "SDK_SEND_ERROR",
			"clientId": clientId,
			"connId":   connID,
			"error":    "connection not found",
		})
		return
	}

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
		return
	}

	go func() {
		_, err := conn.Write(bytes)
		if err != nil {
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
		}
	}()

	if data.Get("connId").Type() == js.TypeUndefined || data.Get("clientId").Type() == js.TypeUndefined {
		return
	}

	connID := data.Get("connId").String()
	clientId := data.Get("clientId").String()

	sdkConnectionsMu.Lock()
	conn, ok := sdkConnections[connID]
	if ok {
		delete(sdkConnections, connID)
	}
	sdkConnectionsMu.Unlock()

	if !ok {
		return
	}

	conn.Close()

	js.Global().Call("__sdk_post_message", map[string]interface{}{
		"type":     "SDK_DATA_CLOSE",
		"clientId": clientId,
		"connId":   connID,
		"code":     1000,
	})
}

func main() {
	bootstrapServerList := getBootstrapServers()

	var err error
	client, err = sdk.NewClient(
		sdk.WithBootstrapServers(bootstrapServerList),
		sdk.WithDialer(WebSocketDialerJS()),
	)
	if err != nil {
		panic(err)
	}
	defer client.Close()

	dialerCredential = sdk.NewCredential()

	wsManager := NewWebSocketManager()
	proxy := &Proxy{
		wsManager: wsManager,
	}

	js.Global().Set("__go_jshttp", js.FuncOf(func(this js.Value, args []js.Value) interface{} {
		if len(args) < 1 {
			return js.Global().Get("Promise").Call("reject",
				js.Global().Get("Error").New("required parameter JSRequest missing"))
		}

		jsReq := args[0]
		return httpjs.ServeHTTPAsyncWithStreaming(proxy, jsReq)
	}))

	js.Global().Set("__sdk_message_handler", js.FuncOf(func(this js.Value, args []js.Value) interface{} {
		if len(args) < 2 {
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
		}

		return nil
	}))

	if runtime.Compiler == "tinygo" {
		return
	}

	ch := make(chan bool)
	<-ch
}
