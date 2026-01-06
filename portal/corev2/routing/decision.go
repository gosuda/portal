package routing

import (
	"sync"
	"time"

	"gosuda.org/portal/portal/corev2/common"
)

type Route struct {
	PathID        common.PathID
	LatencyNs     uint64
	JitterNs      int64
	LossRate      float64
	Score         float64
	LastEval      time.Time
	HighLossCount int
}

type DecisionMaker struct {
	mu            sync.RWMutex
	paths         map[common.PathID]*Route
	currentPathID common.PathID
	lastSwitch    time.Time
	highLossPath  common.PathID
}

func NewDecisionMaker() *DecisionMaker {
	return &DecisionMaker{
		paths:        make(map[common.PathID]*Route),
		lastSwitch:   time.Time{},
		highLossPath: 0,
	}
}

func (dm *DecisionMaker) UpdateMetrics(pathID common.PathID, latencyNs uint64, jitterNs int64, loss float64) {
	dm.mu.Lock()
	defer dm.mu.Unlock()

	path, exists := dm.paths[pathID]
	if !exists {
		path = &Route{
			PathID:        pathID,
			HighLossCount: 0,
		}
		dm.paths[pathID] = path
	}

	path.LatencyNs = latencyNs
	path.JitterNs = jitterNs
	path.LossRate = loss
	path.LastEval = time.Now()

	dm.calculateScore(path)

	if loss > common.LossThreshold {
		path.HighLossCount++
	} else {
		path.HighLossCount = 0
	}
}

func (dm *DecisionMaker) calculateScore(route *Route) {
	latencyMs := float64(route.LatencyNs) / 1_000_000
	jitterMs := float64(route.JitterNs) / 1_000_000

	lossFactor := 1 + route.LossRate*2.0
	route.Score = latencyMs*lossFactor + jitterMs*0.5
}

func (dm *DecisionMaker) ShouldSwitch() (common.PathID, bool) {
	dm.mu.RLock()
	defer dm.mu.RUnlock()

	for _, path := range dm.paths {
		if path.HighLossCount >= 2 {
			return dm.selectBestPath()
		}
	}

	now := time.Now()
	if !dm.lastSwitch.IsZero() && now.Sub(dm.lastSwitch) < common.SwitchCooldown {
		return 0, false
	}

	return dm.selectBestPath()
}

func (dm *DecisionMaker) selectBestPath() (common.PathID, bool) {
	var bestPathID common.PathID
	var bestScore float64 = -1
	var foundBest bool

	currentPath, currentExists := dm.paths[dm.currentPathID]
	currentScore := -1.0
	if currentExists {
		currentScore = currentPath.Score
	}

	for pathID, path := range dm.paths {
		if path.Score < 0 {
			continue
		}

		if !foundBest || path.Score < bestScore {
			bestPathID = pathID
			bestScore = path.Score
			foundBest = true
		}
	}

	if !foundBest {
		return 0, false
	}

	if dm.currentPathID == 0 {
		return bestPathID, true
	}

	currentPath, currentExists = dm.paths[dm.currentPathID]
	if !currentExists {
		return bestPathID, true
	}

	improvement := (currentScore - bestScore) / currentScore
	improvementPct := improvement * 100

	if improvementPct > float64(common.RoutingDeltaPct) {
		return bestPathID, true
	}

	if currentPath.HighLossCount >= 2 {
		return bestPathID, true
	}

	return 0, false
}

func (dm *DecisionMaker) Switch(newPathID common.PathID) {
	dm.mu.Lock()
	defer dm.mu.Unlock()

	dm.currentPathID = newPathID
	dm.lastSwitch = time.Now()

	if path, exists := dm.paths[newPathID]; exists {
		path.HighLossCount = 0
	}
}

func (dm *DecisionMaker) GetCurrentPath() common.PathID {
	dm.mu.RLock()
	defer dm.mu.RUnlock()
	return dm.currentPathID
}

func (dm *DecisionMaker) GetPath(pathID common.PathID) *Route {
	dm.mu.RLock()
	defer dm.mu.RUnlock()
	return dm.paths[pathID]
}
