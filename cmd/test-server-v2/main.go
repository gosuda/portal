package main

import (
	"context"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/xtaci/kcp-go/v5"

	"gosuda.org/portal/portal"
	"gosuda.org/portal/portal/corev2/common"
	"gosuda.org/portal/portal/corev2/identity"
	"gosuda.org/portal/portal/corev2/serdes"
)

var (
	flagPort      int
	flagLeaseName string
	flagDebug     bool
)

func main() {
	flag.IntVar(&flagPort, "port", 4117, "UDP port for V2 relay")
	flag.StringVar(&flagLeaseName, "name", "test-relay", "Relay name")
	flag.BoolVar(&flagDebug, "debug", false, "Enable debug logging")
	flag.Parse()

	if err := runV2TestServer(); err != nil {
		log.Fatalf("V2 test server failed: %v", err)
	}
}

func runV2TestServer() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Printf("[V2 Test Server] Starting V2 test relay server on port %d", flagPort)
	log.Printf("[V2 Test Server] Relay name: %s", flagLeaseName)

	// Create V2 credential
	cred, err := identity.NewCredential()
	if err != nil {
		return fmt.Errorf("failed to create credential: %w", err)
	}

	// Create V2 relay server
	relayServer, err := portal.NewRelayServerV2(cred, nil)
	if err != nil {
		return fmt.Errorf("failed to create relay server: %w", err)
	}

	// Set debug logging
	if flagDebug {
		log.SetFlags(log.LstdFlags | log.Lshortfile)
	}

	// Start relay server
	relayServer.Start()
	defer relayServer.Stop()

	// Display relay info
	relayID := relayServer.GetRelayID()
	log.Printf("[V2 Test Server] Relay ID: %s", relayIDToString(relayID))
	log.Printf("[V2 Test Server] Relay Cert ID: %s", relayServer.GetCertificate().ID)

	// Set up callbacks
	leaseCount := 0
	relayServer.SetOnNewLease(func(lease *portal.LeaseV2) {
		leaseCount++
		log.Printf("[V2 Test Server] NEW LEASE: id=%s name=%s alpn=%v expires=%s",
			sessionIDToString(lease.SessionID),
			lease.Name,
			lease.ALPN,
			lease.Expires.Format(time.RFC3339))
	})

	sessionCount := 0
	relayServer.SetOnConnection(func(session *portal.RelaySessionV2) {
		sessionCount++
		log.Printf("[V2 Test Server] NEW SESSION: id=%s addr=%s",
			sessionIDToString(session.SessionID),
			session.RemoteAddr.String())
	})

	// Start UDP listener for KCP connections
	addr, err := net.ResolveUDPAddr("udp", fmt.Sprintf(":%d", flagPort))
	if err != nil {
		return fmt.Errorf("failed to resolve UDP address: %w", err)
	}

	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return fmt.Errorf("failed to listen on UDP: %w", err)
	}
	defer conn.Close()

	log.Printf("[V2 Test Server] Listening on %s", addr.String())

	// Periodic stats reporter
	statsTicker := time.NewTicker(10 * time.Second)
	defer statsTicker.Stop()

	go func() {
		for {
			select {
			case <-statsTicker.C:
				printServerStats(relayServer, leaseCount, sessionCount)
			case <-ctx.Done():
				return
			}
		}
	}()

	// Accept KCP connections
	buf := make([]byte, 1500)

	for {
		select {
		case <-ctx.Done():
			log.Println("[V2 Test Server] Shutting down...")
			return nil
		default:
			n, remoteAddr, err := conn.ReadFrom(buf)
			if err != nil {
				log.Printf("[V2 Test Server] Error reading from UDP: %v", err)
				continue
			}

			// Create KCP session for this connection
			if err := handleKCPConnection(ctx, relayServer, buf[:n], remoteAddr, conn); err != nil {
				if flagDebug {
					log.Printf("[V2 Test Server] Error handling KCP connection: %v", err)
				}
			}
		}
	}
}

func handleKCPConnection(ctx context.Context, relayServer *portal.RelayServerV2, data []byte, remoteAddr net.Addr, conn *net.UDPConn) error {
	// Extract conversation ID from KCP packet
	if len(data) < 4 {
		return fmt.Errorf("packet too short: %d bytes", len(data))
	}

	conv := makeKCPConvID(data[:4])

	if flagDebug {
		log.Printf("[V2 Test Server] New KCP connection: conv=%d remote=%s", conv, remoteAddr.String())
	}

	// Create KCP connection
	block, err := kcp.NewNoneBlockCrypt(nil)
	if err != nil {
		return fmt.Errorf("failed to create KCP block crypt: %w", err)
	}

	// Create new KCP connection
	kcpSession, err := kcp.NewConn(remoteAddr.String(), block, 10, 3, conn)
	if err != nil {
		return fmt.Errorf("failed to create KCP session: %w", err)
	}

	// Configure KCP
	configureKCP(kcpSession)

	// Handle V2 packets from this KCP session
	go func() {
		buf := make([]byte, 1500)

		for {
			select {
			case <-ctx.Done():
				if flagDebug {
					log.Println("[V2 Test Server] Closing KCP connection handler")
				}
				kcpSession.Close()
				return
			default:
				n, err := kcpSession.Read(buf)
				if err != nil {
					if flagDebug {
						log.Printf("[V2 Test Server] KCP read error: %v", err)
					}
					return
				}

				if flagDebug {
					log.Printf("[V2 Test Server] KCP read %d bytes from session", n)
				}

				// Parse V2 packet
				if _, err := serdes.DeserializePacket(buf[:n]); err != nil {
					log.Printf("[V2 Test Server] Failed to deserialize V2 packet: %v", err)
					continue
				}

				// Handle V2 packet
				if err := relayServer.HandleV2Packet(ctx, kcpSession, remoteAddr); err != nil {
					log.Printf("[V2 Test Server] Error handling V2 packet: %v", err)
				}
			}
		}
	}()

	return nil
}

func configureKCP(kcpSession *kcp.UDPSession) {
	// Configure KCP for V2 protocol
	kcpSession.SetNoDelay(1, 10, 2, 1)
	kcpSession.SetMtu(1400)
	kcpSession.SetWindowSize(128, 128)
	kcpSession.SetACKNoDelay(true)
}

func makeKCPConvID(data []byte) uint32 {
	if len(data) < 4 {
		return 0
	}
	return uint32(data[0])<<24 | uint32(data[1])<<16 | uint32(data[2])<<8 | uint32(data[3])
}

func printServerStats(relayServer *portal.RelayServerV2, leaseCount, sessionCount int) {
	stats := relayServer.GetStats()

	fmt.Printf("\n=== V2 Test Server Statistics ===\n")
	fmt.Printf("Active Sessions: %d\n", stats["active_sessions"])
	fmt.Printf("Total Leases:    %d\n", leaseCount)
	fmt.Printf("Total Sessions:  %d\n", sessionCount)
	if bs, ok := stats["total_bytes_sent"].(uint64); ok {
		fmt.Printf("Bytes Sent:       %d\n", bs)
	}
	if br, ok := stats["total_bytes_recv"].(uint64); ok {
		fmt.Printf("Bytes Received:   %d\n", br)
	}
	if ps, ok := stats["total_packets_sent"].(uint64); ok {
		fmt.Printf("Packets Sent:     %d\n", ps)
	}
	if pr, ok := stats["total_packets_recv"].(uint64); ok {
		fmt.Printf("Packets Received: %d\n", pr)
	}
	fmt.Printf("=============================\n")
}

func sessionIDToString(sid common.SessionID) string {
	return hex.EncodeToString(sid[:])
}

func relayIDToString(rid common.RelayID) string {
	return hex.EncodeToString(rid[:])
}
