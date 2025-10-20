package relaydns

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
)

type Advertise struct {
	Peer  string    `json:"peer"`
	Name  string    `json:"name,omitempty"`
	DNS   string    `json:"dns,omitempty"`
	Addrs []string  `json:"addrs"`
	Ready bool      `json:"ready"`
	Load  float64   `json:"load"`
	TS    time.Time `json:"ts"`
}

type HostEntry struct {
	Info     Advertise
	AddrInfo *peer.AddrInfo
	LastSeen time.Time
}

type Picker struct {
	mu     sync.RWMutex
	rr     uint64
	list   []HostEntry
	pinTo  string
	pinTil time.Time
}

func (p *Picker) update(list []HostEntry) {
	p.mu.Lock()
	p.list = list
	p.mu.Unlock()
}
func (p *Picker) choose() (HostEntry, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if len(p.list) == 0 {
		return HostEntry{}, false
	}
	if p.pinTo != "" && time.Now().Before(p.pinTil) {
		for _, e := range p.list {
			if e.Info.Peer == p.pinTo {
				return e, true
			}
		}
	}
	i := atomic.AddUint64(&p.rr, 1)
	return p.list[i%uint64(len(p.list))], true
}
func (p *Picker) pin(peerID string, dur time.Duration) {
	p.mu.Lock()
	p.pinTo = peerID
	p.pinTil = time.Now().Add(dur)
	p.mu.Unlock()
}
func (p *Picker) unpin() {
	p.mu.Lock()
	p.pinTo = ""
	p.pinTil = time.Time{}
	p.mu.Unlock()
}

func protocolID(s string) protocol.ID {
	return protocol.ID(s)
}
