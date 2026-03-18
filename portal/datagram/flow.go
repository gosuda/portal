package datagram

import (
	"sync"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/rs/zerolog/log"

	"github.com/gosuda/portal/v2/types"
)

const (
	defaultFlowIdleTimeout     = 30 * time.Second
	defaultFlowCleanupInterval = 30 * time.Second
)

type flowReplyFunc func([]byte) error

type flowState struct {
	key      string
	lastSeen time.Time
	reply    flowReplyFunc
}

// FlowMux manages a single QUIC connection from a tunnel for one lease.
// All UDP traffic for the lease is multiplexed over DATAGRAM frames on this
// connection, identified by flow IDs.
type FlowMux struct {
	leaseID string

	session   *Session
	flowTable map[uint32]*flowState // flowID -> client addr + liveness + reply path
	addrIndex map[string]uint32     // ingress key -> flowID
	nextFlow  uint32
	mu        sync.Mutex
}

func NewFlowMux(leaseID string) *FlowMux {
	mux := &FlowMux{
		leaseID: leaseID,
		session: NewSession(256, true, func(err error) {
			log.Warn().
				Err(err).
				Str("component", "quic-flow-mux").
				Str("lease_id", leaseID).
				Msg("quic receive loop ended")
		}),
		flowTable: make(map[uint32]*flowState),
		addrIndex: make(map[string]uint32),
		nextFlow:  1,
	}
	go mux.runDispatchLoop()
	go mux.runCleanupLoop()
	return mux
}

// Register stores the QUIC connection from the tunnel for this lease.
// Replaces any existing connection.
func (b *FlowMux) Register(conn *quic.Conn) error {
	if _, err := b.session.Bind(conn); err != nil {
		return err
	}

	log.Info().
		Str("component", "quic-flow-mux").
		Str("lease_id", b.leaseID).
		Str("remote_addr", conn.RemoteAddr().String()).
		Msg("quic tunnel connection registered")
	return nil
}

// HasConnection reports whether a tunnel QUIC connection is active.
func (b *FlowMux) HasConnection() bool {
	return b.session.HasConnection()
}

// SendDatagram encodes a flow-framed datagram and sends it to the tunnel.
func (b *FlowMux) SendDatagram(flowID uint32, payload []byte) error {
	return b.session.Send(flowID, payload)
}

// TouchFlow assigns a flow ID for an ingress key and updates its liveness and reply path.
// If the key already has a flow, the existing ID is returned.
func (b *FlowMux) TouchFlow(key string, reply flowReplyFunc) uint32 {
	now := time.Now()

	b.mu.Lock()
	defer b.mu.Unlock()

	if id, ok := b.addrIndex[key]; ok {
		if flow, exists := b.flowTable[id]; exists && flow != nil {
			flow.lastSeen = now
			if reply != nil {
				flow.reply = reply
			}
			return id
		}
		delete(b.addrIndex, key)
	}

	id := b.nextFlow
	b.nextFlow++
	b.flowTable[id] = &flowState{
		key:      key,
		lastSeen: now,
		reply:    reply,
	}
	b.addrIndex[key] = id
	return id
}

func (b *FlowMux) runDispatchLoop() {
	incoming := b.session.Incoming()
	for {
		select {
		case <-b.session.Done():
			return
		case frame := <-incoming:
			b.dispatch(frame)
		}
	}
}

func (b *FlowMux) dispatch(frame types.DatagramFrame) {
	b.mu.Lock()
	flow, ok := b.flowTable[frame.FlowID]
	if !ok || flow == nil || flow.reply == nil {
		b.mu.Unlock()
		return
	}

	flow.lastSeen = time.Now()
	reply := flow.reply
	b.mu.Unlock()

	if err := reply(frame.Payload); err != nil {
		log.Warn().
			Err(err).
			Str("component", "quic-flow-mux").
			Str("lease_id", b.leaseID).
			Uint32("flow_id", frame.FlowID).
			Msg("flow writeback failed")
		b.forgetFlow(frame.FlowID)
	}
}

func (b *FlowMux) runCleanupLoop() {
	ticker := time.NewTicker(defaultFlowCleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-b.session.Done():
			return
		case now := <-ticker.C:
			b.expireIdleFlows(now)
		}
	}
}

func (b *FlowMux) expireIdleFlows(now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()

	for flowID, flow := range b.flowTable {
		if flow == nil || now.Sub(flow.lastSeen) > defaultFlowIdleTimeout {
			if flow != nil {
				delete(b.addrIndex, flow.key)
			}
			delete(b.flowTable, flowID)
		}
	}
}

func (b *FlowMux) forgetFlow(flowID uint32) {
	b.mu.Lock()
	defer b.mu.Unlock()

	flow, ok := b.flowTable[flowID]
	if !ok {
		return
	}
	if flow != nil {
		delete(b.addrIndex, flow.key)
	}
	delete(b.flowTable, flowID)
}

// Stop tears down the QUIC connection and signals done.
func (b *FlowMux) Stop() {
	b.session.Stop("lease stopped")
}
