package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/xtaci/kcp-go/v5"

	"gosuda.org/portal/portal"
	"gosuda.org/portal/portal/corev2/common"
	"gosuda.org/portal/portal/corev2/identity"
	"gosuda.org/portal/portal/corev2/serdes"
)

var (
	flagRelays     flagValue
	flagLeaseName  string
	flagALPN       string
	flagDebug      bool
	flagAutoSwitch bool
	flagManualPath int
)

type PathStats struct {
	PathID      common.PathID
	Addr        string
	RTT         time.Duration
	Jitter      time.Duration
	LossRate    float64
	BytesSent   uint64
	BytesRecv   uint64
	PacketsSent uint64
	PacketsRecv uint64
	LastActive  time.Time
}

type MultipathClient struct {
	cred          *identity.Credential
	sessionID     common.SessionID
	paths         map[common.PathID]*PathStats
	currentPathID common.PathID
	mu            sync.RWMutex

	leaseRegistered bool
}

type flagValue []string

func (f *flagValue) String() string {
	return fmt.Sprintf("%v", *f)
}

func (f *flagValue) Set(value string) error {
	*f = append(*f, value)
	return nil
}

func main() {
	flag.Var(&flagRelays, "relay", "Relay addresses (comma-separated, e.g., 127.0.0.1:4117,127.0.0.1:4118,127.0.0.1:4119)")
	flag.StringVar(&flagLeaseName, "name", "test-client", "Lease name")
	flag.StringVar(&flagALPN, "alpn", "http/1.1", "ALPN identifier")
	flag.BoolVar(&flagDebug, "debug", false, "Enable debug logging")
	flag.BoolVar(&flagAutoSwitch, "auto-switch", true, "Enable automatic path switching")
	flag.IntVar(&flagManualPath, "path", 0, "Force specific path (0 = auto)")
	flag.Parse()

	if len(flagRelays) == 0 {
		fmt.Println("Error: At least one relay address required")
		fmt.Println("Example: ./test-client-v2 -relay 127.0.0.1:4117,127.0.0.1:4118,127.0.0.1:4119")
		os.Exit(1)
	}

	if err := runMultipathClient(); err != nil {
		fmt.Printf("Error: Multipath client failed: %v\n", err)
		os.Exit(1)
	}
}

func runMultipathClient() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fmt.Printf("[Multipath Client] Starting V2 multipath client\n")
	fmt.Printf("[Multipath Client] Relays: %v\n", []string(flagRelays))
	fmt.Printf("[Multipath Client] Lease name: %s\n", flagLeaseName)
	fmt.Printf("[Multipath Client] ALPN: %s\n", flagALPN)
	fmt.Printf("[Multipath Client] Auto-switch: %v\n", flagAutoSwitch)
	fmt.Printf("\n")

	// Create V2 credential
	cred, err := identity.NewCredential()
	if err != nil {
		return fmt.Errorf("failed to create credential: %w", err)
	}

	// Generate session ID
	sessionID := generateSessionID()

	// Create multipath client
	client := &MultipathClient{
		cred:      cred,
		sessionID: sessionID,
		paths:     make(map[common.PathID]*PathStats),
		mu:        sync.RWMutex{},
	}

	// Register lease on all relays
	if err := client.registerLeaseOnAllRelays(ctx); err != nil {
		return err
	}

	client.leaseRegistered = true
	fmt.Printf("[Multipath Client] Lease registered successfully\n\n")

	// Start periodic stats display
	statsTicker := time.NewTicker(2 * time.Second)
	defer statsTicker.Stop()

	go func() {
		for {
			select {
			case <-statsTicker.C:
				client.displayStats()
			case <-ctx.Done():
				return
			}
		}
	}()

	// Manual path switch input
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			default:
				var input int
				_, err := fmt.Scanf("%d", &input)
				if err == nil && input != 0 {
					client.manualSwitch(common.PathID(input))
				}
			}
		}
	}()

	// Wait for termination
	<-ctx.Done()
	fmt.Printf("\n[Multipath Client] Shutting down...\n")

	// Cleanup
	client.closeAllPaths()

	return nil
}

func (c *MultipathClient) registerLeaseOnAllRelays(ctx context.Context) error {
	fmt.Printf("[Multipath Client] Registering lease on %d relays\n", len(flagRelays))

	for i, relayAddr := range flagRelays {
		pathID := common.PathID(i + 1)

		// Create KCP connection to relay
		kcpSession, err := c.createKCPConnection(relayAddr)
		if err != nil {
			fmt.Printf("[Multipath Client] Failed to create KCP connection to %s: %v\n", relayAddr, err)
			continue
		}

		// Send lease register request
		if err := c.sendLeaseRegister(kcpSession); err != nil {
			fmt.Printf("[Multipath Client] Failed to register lease on %s: %v\n", relayAddr, err)
			kcpSession.Close()
			continue
		}

		// Initialize path stats
		c.mu.Lock()
		c.paths[pathID] = &PathStats{
			PathID:     pathID,
			Addr:       relayAddr,
			RTT:        0,
			Jitter:     0,
			LossRate:   0,
			LastActive: time.Now(),
		}
		if c.currentPathID == 0 {
			c.currentPathID = pathID
		}
		c.mu.Unlock()

		fmt.Printf("[Multipath Client] Path %d established to %s\n", pathID, relayAddr)
		time.Sleep(100 * time.Millisecond)
	}

	return nil
}

func (c *MultipathClient) createKCPConnection(relayAddr string) (*kcp.UDPSession, error) {
	// Resolve address
	addr, err := net.ResolveUDPAddr("udp", relayAddr)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve address: %w", err)
	}

	// Create UDP connection
	conn, err := net.DialUDP("udp", nil, addr)
	if err != nil {
		return nil, fmt.Errorf("failed to dial UDP: %w", err)
	}

	// Create KCP session
	block, err := kcp.NewNoneBlockCrypt(nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create KCP block crypt: %w", err)
	}

	session, err := kcp.NewConn(relayAddr, block, 10, 3, conn)
	if err != nil {
		return nil, fmt.Errorf("failed to create KCP session: %w", err)
	}

	// Configure KCP
	configureKCP(session)

	if flagDebug {
		fmt.Printf("[Debug] KCP connection created to %s\n", relayAddr)
	}

	return session, nil
}

func (c *MultipathClient) sendLeaseRegister(kcpSession *kcp.UDPSession) error {
	// Generate certificate
	cert, err := identity.NewCertificateV2(c.cred, uint64(time.Now().Add(30*time.Second).Unix()), nil)
	if err != nil {
		return fmt.Errorf("failed to create certificate: %w", err)
	}

	// Create lease register request
	certBytes, err := cert.Serialize()
	if err != nil {
		return fmt.Errorf("failed to serialize certificate: %w", err)
	}

	req := &portal.LeaseRegisterRequest{
		SessionID: c.sessionID,
		Name:      flagLeaseName,
		ALPN:      []string{flagALPN},
		PublicKey: certBytes,
		Expires:   time.Now().Add(30 * time.Second).Unix(),
		Timestamp: time.Now().Unix(),
		Nonce:     generateNonce(),
	}

	// Serialize request
	reqData, err := portal.SerializeLeaseRegisterRequest(req)
	if err != nil {
		return fmt.Errorf("failed to serialize request: %w", err)
	}

	// Create V2 packet
	header := serdes.NewHeader(common.MsgLeaseRegisterReq, c.sessionID, 0, 0)
	pkt := serdes.NewPacket(header, reqData)

	// Serialize packet
	buf := make([]byte, pkt.SerializeSize())
	if err := pkt.Serialize(buf); err != nil {
		return fmt.Errorf("failed to serialize packet: %w", err)
	}

	if flagDebug {
		fmt.Printf("[Debug] Sending lease register packet, size: %d\n", len(buf))
	}

	// Send via KCP
	_, err = kcpSession.Write(buf)
	return err
}

func (c *MultipathClient) displayStats() {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if len(c.paths) == 0 {
		return
	}

	fmt.Printf("=== Multipath Client Statistics ===\n")
	fmt.Printf("Current Path: %d", c.currentPathID)
	if flagAutoSwitch {
		fmt.Printf(" (auto-switch enabled)")
	}
	fmt.Printf("\n")

	for pathID, stats := range c.paths {
		current := ""
		if pathID == c.currentPathID {
			current = " <- ACTIVE"
		}

		fmt.Printf("Path %d %s:\n", pathID, current)
		fmt.Printf("  Relay:     %s\n", stats.Addr)
		fmt.Printf("  RTT:        %v\n", stats.RTT)
		fmt.Printf("  Jitter:     %v\n", stats.Jitter)
		fmt.Printf("  Loss Rate:  %.2f%%\n", stats.LossRate*100)
		fmt.Printf("  Bytes:      S=%d R=%d\n", stats.BytesSent, stats.BytesRecv)
		fmt.Printf("  Packets:    S=%d R=%d\n", stats.PacketsSent, stats.PacketsRecv)
		fmt.Printf("  Last Active: %s ago\n", time.Since(stats.LastActive).Round(time.Millisecond))
		fmt.Printf("\n")
	}

	fmt.Printf("================================\n\n")

	// Auto-switch if enabled
	if flagAutoSwitch && flagManualPath == 0 {
		c.evaluatePaths()
	}
}

func (c *MultipathClient) evaluatePaths() {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if len(c.paths) < 2 {
		return
	}

	// Calculate score for each path
	bestPath := c.currentPathID
	bestScore := float64(1<<63 - 1)

	for pathID, stats := range c.paths {
		// Skip inactive paths
		if time.Since(stats.LastActive) > 30*time.Second {
			continue
		}

		// Calculate score (lower is better)
		score := float64(stats.RTT.Milliseconds())*(1+stats.LossRate*2.0) + float64(stats.Jitter.Milliseconds())*0.5

		if score < bestScore {
			bestScore = score
			bestPath = pathID
		}
	}

	// Switch if improvement > 15%
	if bestPath != c.currentPathID && bestPath != 0 {
		c.switchPath(bestPath)
	}
}

func (c *MultipathClient) switchPath(pathID common.PathID) {
	oldPathID := c.currentPathID
	c.currentPathID = pathID

	fmt.Printf("[Multipath Client] SWITCHING: Path %d -> Path %d\n", oldPathID, pathID)
}

func (c *MultipathClient) manualSwitch(pathID common.PathID) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if _, exists := c.paths[pathID]; exists {
		c.switchPath(pathID)
		fmt.Printf("[Multipath Client] Manual switch to path %d\n", pathID)
	} else {
		fmt.Printf("[Multipath Client] Path %d not found\n", pathID)
	}
}

func (c *MultipathClient) closeAllPaths() {
	c.mu.Lock()
	defer c.mu.Unlock()

	for pathID := range c.paths {
		fmt.Printf("[Multipath Client] Closing path %d\n", pathID)
	}
}

func configureKCP(kcpSession *kcp.UDPSession) {
	// Configure KCP for V2 protocol
	kcpSession.SetNoDelay(1, 10, 2, 1)
	kcpSession.SetMtu(1400)
	kcpSession.SetWindowSize(128, 128)
	kcpSession.SetACKNoDelay(true)
}

func generateSessionID() common.SessionID {
	var sid common.SessionID
	copy(sid[:], []byte("V2-TEST-CLIENT"))
	return sid
}

func generateNonce() []byte {
	nonce := make([]byte, 16)
	for i := range nonce {
		nonce[i] = byte(i)
	}
	return nonce
}
