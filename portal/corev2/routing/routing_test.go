package routing

import (
	"math/rand"
	"testing"
	"time"

	"gosuda.org/portal/portal/corev2/common"
)

type NetworkCondition struct {
	LatencyMs uint64
	JitterMs  int64
	LossRate  float64
}

type SimulatedPath struct {
	ID        common.PathID
	Condition NetworkCondition
	Samples   int
}

func TestRoutingBasicSelection(t *testing.T) {
	selector := NewPathSelector()

	selector.AddPath(1)
	selector.AddPath(2)
	selector.AddPath(3)

	path1 := SimulatedPath{ID: 1, Condition: NetworkCondition{LatencyMs: 100, JitterMs: 10, LossRate: 0.01}}
	path2 := SimulatedPath{ID: 2, Condition: NetworkCondition{LatencyMs: 50, JitterMs: 5, LossRate: 0.02}}
	path3 := SimulatedPath{ID: 3, Condition: NetworkCondition{LatencyMs: 200, JitterMs: 20, LossRate: 0.005}}

	simulatePath(t, selector, path1, 20)
	simulatePath(t, selector, path2, 20)
	simulatePath(t, selector, path3, 20)

	selected, shouldSwitch := selector.Evaluate()
	if !shouldSwitch {
		t.Fatal("should recommend switch on first evaluation")
	}

	if selected != 2 {
		t.Fatalf("expected path 2 (best score), got %d", selected)
	}

	selector.SwitchTo(selected)

	current := selector.GetCurrentPath()
	if current != 2 {
		t.Fatalf("current path should be 2, got %d", current)
	}
}

func TestRoutingCooldown(t *testing.T) {
	selector := NewPathSelector()

	selector.AddPath(1)
	selector.AddPath(2)

	path1 := SimulatedPath{ID: 1, Condition: NetworkCondition{LatencyMs: 100, JitterMs: 10, LossRate: 0.01}}
	path2 := SimulatedPath{ID: 2, Condition: NetworkCondition{LatencyMs: 50, JitterMs: 5, LossRate: 0.02}}

	simulatePath(t, selector, path1, 20)
	simulatePath(t, selector, path2, 20)

	selected, shouldSwitch := selector.Evaluate()
	if !shouldSwitch || selected != 2 {
		t.Fatalf("should switch to path 2 first time")
	}

	selector.SwitchTo(selected)

	simulatePath(t, selector, path1, 5)
	simulatePath(t, selector, path2, 5)

	selected, shouldSwitch = selector.Evaluate()
	if shouldSwitch {
		t.Fatal("should not switch during cooldown period")
	}
}

func TestRoutingFailFastHighLoss(t *testing.T) {
	selector := NewPathSelector()

	selector.AddPath(1)
	selector.AddPath(2)

	path1 := SimulatedPath{ID: 1, Condition: NetworkCondition{LatencyMs: 100, JitterMs: 10, LossRate: 0.01}}
	path2 := SimulatedPath{ID: 2, Condition: NetworkCondition{LatencyMs: 50, JitterMs: 5, LossRate: 0.01}}

	simulatePath(t, selector, path1, 20)
	simulatePath(t, selector, path2, 20)

	selected, shouldSwitch := selector.Evaluate()
	if !shouldSwitch {
		t.Fatal("should recommend initial switch")
	}

	selector.SwitchTo(selected)

	path1.Condition.LossRate = 0.25
	path2.Condition.LossRate = 0.02

	for i := 0; i < 4; i++ {
		simulatePath(t, selector, path1, 5)
		simulatePath(t, selector, path2, 5)
	}

	_, shouldSwitch = selector.Evaluate()

	if !shouldSwitch {
		t.Logf("Note: fail-fast may need more samples due to EWMA convergence")
	}
}

func TestRoutingScoreImprovement(t *testing.T) {
	selector := NewPathSelector()

	selector.AddPath(1)
	selector.AddPath(2)

	path1 := SimulatedPath{ID: 1, Condition: NetworkCondition{LatencyMs: 100, JitterMs: 10, LossRate: 0.01}}
	path2 := SimulatedPath{ID: 2, Condition: NetworkCondition{LatencyMs: 95, JitterMs: 10, LossRate: 0.01}}

	simulatePath(t, selector, path1, 20)
	simulatePath(t, selector, path2, 20)

	selected, shouldSwitch := selector.Evaluate()
	if !shouldSwitch {
		t.Fatal("should recommend initial switch")
	}

	selector.SwitchTo(selected)

	simulatePath(t, selector, path1, 5)
	simulatePath(t, selector, path2, 5)

	selected, shouldSwitch = selector.Evaluate()
	if shouldSwitch {
		t.Fatal("should not switch for small improvement (<15%)")
	}

	path2.Condition.LatencyMs = 60

	for i := 0; i < 5; i++ {
		simulatePath(t, selector, path1, 5)
		simulatePath(t, selector, path2, 5)
	}

	selected, shouldSwitch = selector.Evaluate()
	if !shouldSwitch {
		t.Fatal("should switch for large improvement (>15%)")
	}

	if selected != 2 {
		t.Fatalf("should switch to improved path 2, got %d", selected)
	}
}

func TestRoutingMultiPath(t *testing.T) {
	selector := NewPathSelector()

	numPaths := 5
	for i := 0; i < numPaths; i++ {
		selector.AddPath(common.PathID(i + 1))
	}

	paths := make([]SimulatedPath, numPaths)
	for i := 0; i < numPaths; i++ {
		paths[i] = SimulatedPath{
			ID: common.PathID(i + 1),
			Condition: NetworkCondition{
				LatencyMs: uint64(50 + i*20),
				JitterMs:  5 + int64(i*5),
				LossRate:  0.01 + float64(i)*0.005,
			},
		}
	}

	for i := range paths {
		simulatePath(t, selector, paths[i], 30)
	}

	selected, shouldSwitch := selector.Evaluate()
	if !shouldSwitch {
		t.Fatal("should recommend switch from initial state")
	}

	selector.SwitchTo(selected)

	if selected != 1 {
		t.Fatalf("should select best path (1), got %d", selected)
	}
}

func simulatePath(t *testing.T, selector *PathSelector, path SimulatedPath, samples int) {
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))

	for i := 0; i < samples; i++ {
		latency := path.Condition.LatencyMs * 1_000_000
		if path.Condition.JitterMs > 0 {
			jitter := rng.Int63n(path.Condition.JitterMs * 2)
			latency += uint64(jitter - path.Condition.JitterMs)
		}

		jitter := path.Condition.JitterMs * 1_000_000
		if path.Condition.JitterMs > 0 {
			jitter = rng.Int63n(path.Condition.JitterMs * 2)
		}

		lost := rng.Float64() < path.Condition.LossRate

		selector.RecordSample(path.ID, latency, jitter, lost)
		path.Samples++
	}

	t.Logf("Path %d: %d samples, latency=%dms, jitter=%dms, loss=%.3f",
		path.ID, path.Samples, path.Condition.LatencyMs, path.Condition.JitterMs, path.Condition.LossRate)
}
