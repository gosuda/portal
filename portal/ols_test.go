package portal

import (
	"testing"
)

func TestOLSManager(t *testing.T) {
	m := NewOLSManager()

	// Test with 4 nodes (2x2 grid)
	nodes := map[string]string{
		"node1": "addr1",
		"node2": "addr2",
		"node3": "addr3",
		"node4": "addr4",
	}
	m.UpdateNodes(nodes)

	if m.n != 2 {
		t.Errorf("expected n=2, got %d", m.n)
	}

	target, err := m.GetTargetNode("client1", "lease1")
	if err != nil {
		t.Fatal(err)
	}
	if target == nil {
		t.Fatal("target is nil")
	}

	// Test rotation
	m.UpdateLoad("node1", 100.0)
	m.UpdateLoad("node2", 100.0)
	m.UpdateLoad("node3", 0.0)
	m.UpdateLoad("node4", 0.0)

	// After load imbalance, it should eventually rotate if threshold met
	// Our variance-based rotation check: rowVar vs colVar
	// Row loads: (100+100)=200, (0+0)=0 -> mean 100, var (100^2 + 100^2)/2 = 10000
	// Col loads: (100+0)=100, (100+0)=100 -> mean 100, var (0^2 + 0^2)/2 = 0
	// 10000 > 0*2, so it should rotate!

	if m.rotation == 0 {
		t.Error("expected rotation, got 0")
	}
}
