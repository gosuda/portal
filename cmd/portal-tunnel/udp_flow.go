package main

import (
	"context"
	"net"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/gosuda/portal/v2/sdk"
	"github.com/gosuda/portal/v2/types"
)

type udpFlowKey struct {
	flowID   uint32
	address  string
	relayURL string
}

type udpFlowEntry struct {
	conn     *net.UDPConn
	lastSeen time.Time
	frame    types.DatagramFrame
}

type udpFlowManager struct {
	target   *net.UDPAddr
	exposure *sdk.Exposure
	mu       sync.Mutex
	flows    map[udpFlowKey]*udpFlowEntry
}

func newUDPFlowManager(target *net.UDPAddr, exposure *sdk.Exposure) *udpFlowManager {
	return &udpFlowManager{
		target:   target,
		exposure: exposure,
		flows:    make(map[udpFlowKey]*udpFlowEntry),
	}
}

func (m *udpFlowManager) runCleanup(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.mu.Lock()
			now := time.Now()
			for key, f := range m.flows {
				if now.Sub(f.lastSeen) > 30*time.Second {
					_ = f.conn.Close()
					delete(m.flows, key)
				}
			}
			m.mu.Unlock()
		}
	}
}

func (m *udpFlowManager) getOrCreate(ctx context.Context, frame types.DatagramFrame) (*net.UDPConn, error) {
	key := udpFlowKey{
		flowID:   frame.FlowID,
		address:  frame.Address,
		relayURL: frame.RelayURL,
	}

	m.mu.Lock()
	if f, ok := m.flows[key]; ok {
		f.lastSeen = time.Now()
		m.mu.Unlock()
		return f.conn, nil
	}
	m.mu.Unlock()

	localConn, err := net.DialUDP("udp", nil, m.target)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	if f, ok := m.flows[key]; ok {
		m.mu.Unlock()
		_ = localConn.Close()
		f.lastSeen = time.Now()
		return f.conn, nil
	}
	m.flows[key] = &udpFlowEntry{
		conn:     localConn,
		lastSeen: time.Now(),
		frame: types.DatagramFrame{
			FlowID:   frame.FlowID,
			Address:  frame.Address,
			RelayURL: frame.RelayURL,
			UDPAddr:  frame.UDPAddr,
		},
	}
	m.mu.Unlock()

	go m.readLoop(ctx, key, localConn)
	return localConn, nil
}

func (m *udpFlowManager) removeFlow(key udpFlowKey) {
	m.mu.Lock()
	if f, ok := m.flows[key]; ok {
		_ = f.conn.Close()
		delete(m.flows, key)
	}
	m.mu.Unlock()
}

func (m *udpFlowManager) readLoop(ctx context.Context, key udpFlowKey, conn *net.UDPConn) {
	buf := make([]byte, 65535)
	for {
		n, err := conn.Read(buf)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Debug().
				Err(err).
				Uint32("flow_id", key.flowID).
				Str("address", key.address).
				Str("relay_url", key.relayURL).
				Msg("local read ended")
			m.removeFlow(key)
			return
		}

		m.mu.Lock()
		entry := m.flows[key]
		if entry == nil {
			m.mu.Unlock()
			return
		}
		entry.lastSeen = time.Now()
		replyFrame := entry.frame
		replyFrame.Payload = append([]byte(nil), buf[:n]...)
		m.mu.Unlock()

		if sendErr := m.exposure.SendDatagram(replyFrame); sendErr != nil {
			log.Debug().
				Err(sendErr).
				Uint32("flow_id", key.flowID).
				Str("address", key.address).
				Str("relay_url", key.relayURL).
				Msg("send datagram to relay failed")
			m.removeFlow(key)
			return
		}
	}
}
