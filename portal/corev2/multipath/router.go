package multipath

import (
	"errors"
	"sync"
	"time"

	"github.com/xtaci/kcp-go/v5"
	"gosuda.org/portal/portal/corev2/common"
	"gosuda.org/portal/portal/corev2/routing"
)

type MultipathRouter struct {
	mu              sync.RWMutex
	paths           map[common.PathID]*KCPPath
	currentPathID   common.PathID
	router          *routing.PathSelector
	lastSwitch      time.Time
	cooldown        time.Duration
	switchThreshold float64
	failFastLoss    float64
}

type KCPPath struct {
	PathID     common.PathID
	KCP        *kcp.UDPSession
	RemoteAddr string
	IsActive   bool
	LastActive time.Time
}

func NewMultipathRouter() *MultipathRouter {
	return &MultipathRouter{
		paths:           make(map[common.PathID]*KCPPath),
		router:          routing.NewPathSelector(),
		cooldown:        5 * time.Second,
		switchThreshold: 0.15,
		failFastLoss:    0.20,
	}
}

func (mr *MultipathRouter) AddPath(pathID common.PathID, remoteAddr string) (*kcp.UDPSession, error) {
	mr.mu.Lock()
	defer mr.mu.Unlock()

	block, err := kcp.NewNoneBlockCrypt(nil)
	if err != nil {
		return nil, err
	}

	session, err := kcp.DialWithOptions(remoteAddr, block, 10, 3)
	if err != nil {
		return nil, err
	}

	configureKCP(session)

	path := &KCPPath{
		PathID:     pathID,
		KCP:        session,
		RemoteAddr: remoteAddr,
		IsActive:   true,
		LastActive: time.Now(),
	}

	mr.paths[pathID] = path
	mr.router.AddPath(pathID)

	return session, nil
}

func configureKCP(session *kcp.UDPSession) {
	session.SetNoDelay(1, 10, 2, 1)
	session.SetMtu(1400)
	session.SetWindowSize(128, 128)
	session.SetACKNoDelay(true)
}

func (mr *MultipathRouter) RemovePath(pathID common.PathID) {
	mr.mu.Lock()
	defer mr.mu.Unlock()

	if path, exists := mr.paths[pathID]; exists {
		path.IsActive = false
		path.KCP.Close()
	}
	delete(mr.paths, pathID)
}

func (mr *MultipathRouter) SelectPath() common.PathID {
	selected, shouldSwitch := mr.router.Evaluate()

	if shouldSwitch && selected != 0 {
		mr.SwitchTo(selected)
	}

	return selected
}

func (mr *MultipathRouter) SwitchTo(pathID common.PathID) {
	mr.mu.Lock()
	defer mr.mu.Unlock()

	if pathID == mr.currentPathID {
		return
	}

	now := time.Now()
	if !mr.lastSwitch.IsZero() && now.Sub(mr.lastSwitch) < mr.cooldown {
		return
	}

	mr.currentPathID = pathID
	mr.router.SwitchTo(pathID)
	mr.lastSwitch = now
}

func (mr *MultipathRouter) Send(data []byte) (int, error) {
	mr.mu.RLock()
	defer mr.mu.RUnlock()

	path := mr.getCurrentPath()
	if path == nil {
		return 0, errors.New("no active path")
	}

	path.LastActive = time.Now()

	return path.KCP.Write(data)
}

func (mr *MultipathRouter) ReceiveFrom(pathID common.PathID, data []byte, latencyNs uint64) {
	mr.mu.RLock()
	defer mr.mu.RUnlock()

	path, exists := mr.paths[pathID]
	if !exists || !path.IsActive {
		return
	}

	path.LastActive = time.Now()

	jitterNs := int64(0)
	mr.router.RecordSample(pathID, latencyNs, jitterNs, false)
}

func (mr *MultipathRouter) GetCurrentPathID() common.PathID {
	mr.mu.RLock()
	defer mr.mu.RUnlock()
	return mr.currentPathID
}

func (mr *MultipathRouter) GetPath(pathID common.PathID) *KCPPath {
	mr.mu.RLock()
	defer mr.mu.RUnlock()
	return mr.paths[pathID]
}

func (mr *MultipathRouter) GetAllPaths() map[common.PathID]*KCPPath {
	mr.mu.RLock()
	defer mr.mu.RUnlock()

	result := make(map[common.PathID]*KCPPath)
	for k, v := range mr.paths {
		result[k] = v
	}
	return result
}

func (mr *MultipathRouter) Update() {
	mr.mu.RLock()
	defer mr.mu.RUnlock()

	now := time.Now()

	for _, path := range mr.paths {
		if !path.IsActive {
			continue
		}

		if now.Sub(path.LastActive) > 30*time.Second {
			path.IsActive = false
			path.KCP.Close()
			continue
		}
	}

	selected, shouldSwitch := mr.router.Evaluate()
	if shouldSwitch && selected != 0 {
		mr.SwitchTo(selected)
	}
}

func (mr *MultipathRouter) ForceSwitch(pathID common.PathID) bool {
	mr.mu.Lock()
	defer mr.mu.Unlock()

	if _, exists := mr.paths[pathID]; !exists {
		return false
	}

	mr.currentPathID = pathID
	mr.router.SwitchTo(pathID)
	mr.lastSwitch = time.Now()

	return true
}

func (mr *MultipathRouter) Close() {
	mr.mu.Lock()
	defer mr.mu.Unlock()

	for _, path := range mr.paths {
		if path.IsActive {
			path.KCP.Close()
			path.IsActive = false
		}
	}
}

func (mr *MultipathRouter) getCurrentPath() *KCPPath {
	if mr.currentPathID == 0 {
		for _, path := range mr.paths {
			if path.IsActive {
				mr.currentPathID = path.PathID
				return path
			}
		}
		return nil
	}
	return mr.paths[mr.currentPathID]
}
