package policy

import (
	"math"
	"testing"
)

func TestOLSManager(t *testing.T) {
	m := NewOLSManager()

	// Test with 4 nodes (2x2 grid)
	nodes := []string{
		"node1",
		"node2",
		"node3",
		"node4",
	}
	m.UpdateNodes(nodes)

	if m.n != 2 {
		t.Errorf("expected n=2, got %d", m.n)
	}

	targetID, err := m.GetTargetNodeID("client1", "lease1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if targetID == "" {
		t.Fatal("targetID is empty")
	}

	// Test rotation
	m.UpdateLoad("node1", NodeLoad{ActiveConns: 100}, 0, 0)
	m.UpdateLoad("node2", NodeLoad{ActiveConns: 100}, 0, 0)
	m.UpdateLoad("node3", NodeLoad{ActiveConns: 0}, 0, 0)
	m.UpdateLoad("node4", NodeLoad{ActiveConns: 0}, 0, 0)

	// After load imbalance, it should eventually rotate if threshold met
	if m.rotation == 0 {
		t.Error("expected rotation, got 0")
	}
}

func TestOLSManagerConservativeRotationWhenVarianceIsSimilar(t *testing.T) {
	m := NewOLSManager()
	m.UpdateNodes([]string{
		"node1", "node2", "node3",
		"node4", "node5", "node6",
		"node7", "node8", "node9",
	})
	if m.n != 3 {
		t.Fatalf("expected n=3, got %d", m.n)
	}

	loads := [][]float64{
		{2.0, 2.0, 1.0},
		{1.5, 1.5, 1.0},
		{1.4, 0.5, 1.1},
	}
	for i := 0; i < m.n; i++ {
		for j := 0; j < m.n; j++ {
			m.grid[i][j].LoadScore = loads[i][j]
		}
	}

	m.rotation = 0
	m.checkAndRotate()

	if m.rotation <= 0 {
		t.Fatalf("expected positive conservative rotation, got %v", m.rotation)
	}
	if m.rotation >= 90 {
		t.Fatalf("expected conservative angle < 90, got %v", m.rotation)
	}
}

func TestOLSManagerKeepsNinetyDegreeRotationForSevereBurst(t *testing.T) {
	m := NewOLSManager()
	m.UpdateNodes([]string{
		"node1", "node2", "node3",
		"node4", "node5", "node6",
		"node7", "node8", "node9",
	})
	if m.n != 3 {
		t.Fatalf("expected n=3, got %d", m.n)
	}

	loads := [][]float64{
		{2.0, 2.0, 2.0},
		{2.0, 2.0, 2.0},
		{0.0, 0.0, 0.0},
	}
	for i := 0; i < m.n; i++ {
		for j := 0; j < m.n; j++ {
			m.grid[i][j].LoadScore = loads[i][j]
		}
	}

	m.rotation = 0
	m.checkAndRotate()

	if math.Abs(m.rotation-90.0) > 0.0001 {
		t.Fatalf("expected 90 degree rotation for severe burst, got %v", m.rotation)
	}
}
