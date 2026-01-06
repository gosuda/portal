package routing

import (
	"gosuda.org/portal/portal/corev2/common"
	"gosuda.org/portal/portal/corev2/metrics"
)

type PathSelector struct {
	decision *DecisionMaker
	metrics  map[common.PathID]*PathMetrics
}

type PathMetrics struct {
	Latency    *metrics.LatencyWindow
	Loss       *metrics.LossTracker
	Jitter     *metrics.JitterWindow
	PacketLoss float64
}

func NewPathSelector() *PathSelector {
	return &PathSelector{
		decision: NewDecisionMaker(),
		metrics:  make(map[common.PathID]*PathMetrics),
	}
}

func (ps *PathSelector) AddPath(pathID common.PathID) {
	ps.metrics[pathID] = &PathMetrics{
		Latency: &metrics.LatencyWindow{},
		Loss:    metrics.NewLossTracker(),
		Jitter:  &metrics.JitterWindow{},
	}
}

func (ps *PathSelector) RecordSample(pathID common.PathID, latencyNs uint64, jitterNs int64, lost bool) {
	metrics, exists := ps.metrics[pathID]
	if !exists {
		return
	}

	metrics.Latency.Add(latencyNs)
	metrics.Jitter.Add(jitterNs)

	if lost {
		metrics.Loss.Update(1.0)
	} else {
		metrics.Loss.Update(0.0)
	}

	avgLatency := metrics.Latency.Average()
	avgJitter := metrics.Jitter.Average()
	lossRate := metrics.Loss.Get()

	ps.decision.UpdateMetrics(pathID, uint64(avgLatency), int64(avgJitter), lossRate)
}

func (ps *PathSelector) Evaluate() (common.PathID, bool) {
	return ps.decision.ShouldSwitch()
}

func (ps *PathSelector) SwitchTo(pathID common.PathID) {
	ps.decision.Switch(pathID)
}

func (ps *PathSelector) GetCurrentPath() common.PathID {
	return ps.decision.GetCurrentPath()
}
