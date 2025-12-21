package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"io"
	"runtime"
	"strings"
	"sync"
	"syscall/js"
	"time"

	"gosuda.org/portal/portal/core/cryptoops"
	"gosuda.org/portal/portal/core/proto/rdverb"
	"gosuda.org/portal/sdk"
)

var (
	client *sdk.Client

	// JS handlers pinned to avoid GC
	goHTTPHandler js.Func

	// SDK connection manager for Service Worker messaging
	sdkConnections   = make(map[string]io.ReadWriteCloser)
	sdkConnectionsMu sync.RWMutex

	// DNS cache for lease name -> lease ID mapping
	dnsCache = struct {
		sync.RWMutex
		cache map[string]dnsCacheEntry
	}{
		cache: make(map[string]dnsCacheEntry, 100),
	}
	dnsCacheTTL = 5 * time.Minute

	// Buffer pool for reading from SDK connections
	bufferPool = sync.Pool{
		New: func() interface{} {
			return make([]byte, 32*1024)
		},
	}
)

// Test hooks (overridable in tests)
var (
	sdkLookupLease = func(name string) (*rdverb.Lease, error) {
		return client.LookupName(name)
	}
	sdkDialLease = func(cred *cryptoops.Credential, leaseID string, alpn string) (io.ReadWriteCloser, error) {
		return client.Dial(cred, leaseID, alpn)
	}
	sdkNewCredential = func() *cryptoops.Credential {
		return sdk.NewCredential()
	}
	sdkPostMessage = func(payload map[string]interface{}) {
		js.Global().Call("__sdk_post_message", js.ValueOf(payload))
	}
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
	dnsCache.RLock()
	entry, ok := dnsCache.cache[name]
	dnsCache.RUnlock()

	if ok {
		if time.Now().Before(entry.expiresAt) {
			return entry.leaseID, true
		}
		// Expired entry, delete it
		dnsCache.Lock()
		delete(dnsCache.cache, name)
		dnsCache.Unlock()
	}
	return "", false
}

// storeDNSCache stores a lease ID in the DNS cache
func storeDNSCache(name, leaseID string) {
	dnsCache.Lock()
	dnsCache.cache[name] = dnsCacheEntry{
		leaseID:   leaseID,
		expiresAt: time.Now().Add(dnsCacheTTL),
	}
	dnsCache.Unlock()
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

func generateConnID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func getLeaseID(hostname string) string {
	// Replaced idna.ToUnicode with simple uppercase
	// TinyGo doesn't support idna, so we assume inputs are already ascii or clean
	// Decode if needed? url.QueryUnescape removed per plan

	// Simplified normalization
	id := strings.Split(hostname, ".")[0]
	id = strings.TrimSpace(id)
	id = strings.ToUpper(id)
	return id
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
		// Convert leaseName to uppercase
		normalizedLeaseName := getLeaseID(leaseName)
		log.Debug().Str("original", leaseName).Str("normalized", normalizedLeaseName).Msg("[SDK Connect] Normalized lease name")

		// Check DNS cache first, then lookup if needed
		var leaseID string
		if cachedID, ok := lookupDNSCache(normalizedLeaseName); ok {
			log.Debug().Str("name", normalizedLeaseName).Str("id", cachedID).Msg("[SDK Connect] DNS cache hit")
			leaseID = cachedID
		} else {
			// Cache miss - perform lookup
			lease, err := sdkLookupLease(normalizedLeaseName)
			if err != nil {
				log.Error().Err(err).Str("leaseName", leaseName).Msg("[SDK Connect] Lease lookup failed")
				sdkPostMessage(map[string]interface{}{
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
		cred := sdkNewCredential()
		conn, err := sdkDialLease(cred, leaseID, "http/1.1")
		if err != nil {
			log.Error().Err(err).Str("leaseID", leaseID).Msg("[SDK Connect] Failed")

			// Send error to client
			sdkPostMessage(map[string]interface{}{
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
					sdkPostMessage(map[string]interface{}{
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
		connID, err := generateConnID()
		if err != nil {
			log.Error().Err(err).Msg("[SDK Connect] Failed to generate connection ID")
			conn.Close()
			sdkPostMessage(map[string]interface{}{
				"type":     "SDK_CONNECT_ERROR",
				"clientId": clientId,
				"error":    err.Error(),
			})
			return
		}

		// Store connection
		sdkConnectionsMu.Lock()
		sdkConnections[connID] = conn
		sdkConnectionsMu.Unlock()

		log.Info().Str("leaseName", leaseName).Str("connId", connID).Msg("[SDK Connect] Connected")

		// Send success to client
		sdkPostMessage(map[string]interface{}{
			"type":     "SDK_CONNECT_SUCCESS",
			"clientId": clientId,
			"connId":   connID,
		})

		// Start reading from connection
		go func() {
			buffer := bufferPool.Get().([]byte)
			defer bufferPool.Put(buffer)

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

					// Notify client of closure
					sdkPostMessage(map[string]interface{}{
						"type":   "SDK_DISCONNECT",
						"connId": connID,
					})
					return
				}

				if n > 0 {
					// Send data to client
					// We need to copy the data because the buffer is reused
					dataToSend := make([]byte, n)
					copy(dataToSend, buffer[:n])

					// Convert to Uint8Array for JS
					array := js.Global().Get("Uint8Array").New(n)
					js.CopyBytesToJS(array, dataToSend)

					sdkPostMessage(map[string]interface{}{
						"type":   "SDK_DATA",
						"connId": connID,
						"data":   array,
					})
				}
			}
		}()
	}()
}

func handleSDKSend(data js.Value) {
	connID := data.Get("connId").String()

	sdkConnectionsMu.RLock()
	conn, ok := sdkConnections[connID]
	sdkConnectionsMu.RUnlock()

	if !ok {
		log.Warn().Str("connId", connID).Msg("[SDK Send] Connection not found")
		return
	}

	jsData := data.Get("data")

	// Handle string or Uint8Array
	var sendData []byte
	if jsData.Type() == js.TypeString {
		// Base64 string
		decoded, err := base64.StdEncoding.DecodeString(jsData.String())
		if err != nil {
			log.Error().Err(err).Str("connId", connID).Msg("[SDK Send] Invalid base64")
			return
		}
		sendData = decoded
	} else {
		// Uint8Array
		length := jsData.Get("length").Int()
		sendData = make([]byte, length)
		js.CopyBytesToGo(sendData, jsData)
	}

	_, err := conn.Write(sendData)
	if err != nil {
		log.Error().Err(err).Str("connId", connID).Msg("[SDK Send] Write error")
		// Notify client of potential error?
		// For now just log
	}
}

func handleSDKClose(data js.Value) {
	connID := data.Get("connId").String()

	sdkConnectionsMu.Lock()
	conn, ok := sdkConnections[connID]
	if ok {
		delete(sdkConnections, connID)
	}
	sdkConnectionsMu.Unlock()

	if ok {
		log.Info().Str("connId", connID).Msg("[SDK Close] Closing connection")
		conn.Close()
	}
}

func main() {
	// Initialize logger (shimmed for prod)

	// Create channels to keep the program running
	c := make(chan struct{}, 0)

	log.Info().Msg("Starting Portal WebClient (TinyGo)")

	// Get bootstrap servers
	bootstraps := getBootstrapServers()

	// Initialize SDK client
	var err error
	client, err = sdk.NewClient(
		sdk.WithBootstrapServers(bootstraps),
	)
	if err != nil {
		log.Error().Err(err).Msg("Failed to initialize SDK client")
		return
	}

	// Register SDK message handler
	js.Global().Set("__sdk_message_handler", js.FuncOf(func(this js.Value, args []js.Value) interface{} {
		if len(args) < 1 {
			return nil
		}

		data := args[0]
		msgType := data.Get("type").String()

		switch msgType {
		case "SDK_CONNECT":
			handleSDKConnect(data)
		case "SDK_SEND":
			handleSDKSend(data)
		case "SDK_CLOSE":
			handleSDKClose(data)
		default:
			log.Warn().Str("type", msgType).Msg("Unknown SDK message type")
		}

		return nil
	}))

	// Register HTTP handler for Service Worker fetch passthrough
	goHTTPHandler = js.FuncOf(func(this js.Value, args []js.Value) interface{} {
		if len(args) < 1 {
			return nil
		}
		fetch := js.Global().Get("fetch")
		if fetch.IsUndefined() || fetch.IsNull() {
			return nil
		}
		return fetch.Invoke(args[0])
	})
	js.Global().Set("__go_jshttp", goHTTPHandler)

	// Check for TinyGo runtime
	if runtime.Compiler == "tinygo" {
		log.Info().Msg("Running with TinyGo runtime")
	} else {
		log.Info().Msg("Running with standard Go runtime")
	}

	// Keep running
	<-c
}
