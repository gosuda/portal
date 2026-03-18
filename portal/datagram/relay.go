package datagram

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

const DefaultMaxPacketSize = 1350

// Relay binds a UDP port for a lease and relays datagrams bidirectionally
// between raw UDP clients and the tunnel's QUIC connection via the flow mux.
type Relay struct {
	leaseID string
	port    int
	flowMux *FlowMux
	conn    *net.UDPConn

	cancel    context.CancelFunc
	closeOnce sync.Once
}

func NewRelay(leaseID string, port int, flowMux *FlowMux) *Relay {
	return &Relay{
		leaseID: leaseID,
		port:    port,
		flowMux: flowMux,
	}
}

func (r *Relay) Start(ctx context.Context) error {
	addr := &net.UDPAddr{Port: r.port}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return fmt.Errorf("listen udp :%d: %w", r.port, err)
	}
	r.conn = conn

	relayCtx, cancel := context.WithCancel(ctx)
	r.cancel = cancel

	go r.readLoop(relayCtx)

	log.Info().
		Str("component", "udp-relay").
		Str("lease_id", r.leaseID).
		Int("port", r.port).
		Msg("udp relay started")

	return nil
}

func (r *Relay) Stop() {
	r.closeOnce.Do(func() {
		if r.cancel != nil {
			r.cancel()
		}
		if r.conn != nil {
			_ = r.conn.Close()
		}
		log.Info().
			Str("component", "udp-relay").
			Str("lease_id", r.leaseID).
			Int("port", r.port).
			Msg("udp relay stopped")
	})
}

func (r *Relay) readLoop(ctx context.Context) {
	buf := make([]byte, DefaultMaxPacketSize)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		_ = r.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		n, clientAddr, err := r.conn.ReadFromUDP(buf)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				continue
			}
			log.Warn().
				Str("component", "udp-relay").
				Str("lease_id", r.leaseID).
				Err(err).
				Msg("readLoop exiting: unexpected read error")
			return
		}

		flowID := r.flowMux.TouchFlow("udp:"+clientAddr.String(), func(payload []byte) error {
			_, err := r.conn.WriteToUDP(payload, clientAddr)
			return err
		})
		payload := make([]byte, n)
		copy(payload, buf[:n])

		if err := r.flowMux.SendDatagram(flowID, payload); err != nil {
			log.Warn().
				Str("component", "udp-relay").
				Str("lease_id", r.leaseID).
				Err(err).
				Uint32("flow_id", flowID).
				Int("bytes", n).
				Msg("send datagram to tunnel failed, dropping packet")
			continue
		}
	}
}
