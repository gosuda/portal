package policy

import (
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

	targetID, err := m.GetTargetNodeID("client1", "lease1")
	if err != nil {
		t.Fatal(err)
	}
	if targetID == "" {
		t.Fatal("targetID is empty")
	}

	// Test rotation
	m.UpdateLoad("node1", 100.0)
	m.UpdateLoad("node2", 100.0)
	m.UpdateLoad("node3", 0.0)
	m.UpdateLoad("node4", 0.0)

	// After load imbalance, it should eventually rotate if threshold met
	if m.rotation == 0 {
		t.Error("expected rotation, got 0")
	}
}
