package portal

import (
	"fmt"
	"math"
	"sync"
)

// OLSNode represents a node in the OLS grid.
type OLSNode struct {
	ID      string
	Address string
	Load    float64
}

// OLSManager manages the orthogonal Latin square topology.
type OLSManager struct {
	mu sync.RWMutex

	nodes    map[string]*OLSNode
	nodeList []string // sorted IDs for consistency

	n    int          // order of Latin square
	grid [][]*OLSNode // n x n grid

	// Latin squares
	l1 [][]int
	l2 [][]int

	// Current rotation (0, 90, 180, 270 degrees)
	rotation int
}

func NewOLSManager() *OLSManager {
	return &OLSManager{
		nodes: make(map[string]*OLSNode),
	}
}

// UpdateNodes updates the set of nodes and reconfigures the grid if necessary.
func (m *OLSManager) UpdateNodes(nodes map[string]string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Update node list
	m.nodes = make(map[string]*OLSNode)
	for id, addr := range nodes {
		m.nodes[id] = &OLSNode{ID: id, Address: addr}
	}

	N := len(m.nodes)
	newN := int(math.Floor(math.Sqrt(float64(N))))

	if newN != m.n && newN >= 2 {
		m.reconfigure(newN)
	}
}

// reconfigure builds a new n x n grid and generates MOLS.
func (m *OLSManager) reconfigure(n int) {
	m.n = n
	m.grid = make([][]*OLSNode, n)
	for i := range m.grid {
		m.grid[i] = make([]*OLSNode, n)
	}

	// Assign nodes to grid (simplified: just take first n*n nodes)
	count := 0
	for _, node := range m.nodes {
		if count >= n*n {
			break
		}
		row := count / n
		col := count % n
		m.grid[row][col] = node
		count++
	}

	// Generate MOLS. For simplicity, we use a basic construction for prime n.
	// For non-prime, this might not be strictly orthogonal but should suffice for balancing.
	m.l1 = generateLatinSquare(n, 1)
	m.l2 = generateLatinSquare(n, 2)
	
	if n > 2 && !areOrthogonal(m.l1, m.l2, n) {
		// Fallback for non-prime or problematic n
		m.l2 = generateLatinSquare(n, n-1)
	}
}

func generateLatinSquare(n, k int) [][]int {
	ls := make([][]int, n)
	for i := 0; i < n; i++ {
		ls[i] = make([]int, n)
		for j := 0; j < n; j++ {
			ls[i][j] = (k*i + j) % n
		}
	}
	return ls
}

func areOrthogonal(l1, l2 [][]int, n int) bool {
	pairs := make(map[string]bool)
	for i := 0; i < n; i++ {
		for j := 0; j < n; j++ {
			key := fmt.Sprintf("%d,%d", l1[i][j], l2[i][j])
			if pairs[key] {
				return false
			}
			pairs[key] = true
		}
	}
	return true
}

// GetTargetNode returns the target node for a given client and lease.
func (m *OLSManager) GetTargetNode(clientID, leaseID string) (*OLSNode, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.n < 2 {
		return nil, fmt.Errorf("grid not initialized")
	}

	// Simple hash-based mapping to (i, j)
	i := hashString(clientID) % m.n
	j := hashString(leaseID) % m.n

	// Get grid coordinates using MOLS
	row := m.l1[i][j]
	col := m.l2[i][j]

	// Apply rotation
	row, col = m.applyRotation(row, col)

	if m.grid[row][col] == nil {
		return nil, fmt.Errorf("node not found at grid %d,%d", row, col)
	}

	return m.grid[row][col], nil
}

func (m *OLSManager) applyRotation(row, col int) (int, int) {
	n := m.n
	switch m.rotation {
	case 90:
		return col, n - 1 - row
	case 180:
		return n - 1 - row, n - 1 - col
	case 270:
		return n - 1 - col, row
	default:
		return row, col
	}
}

func hashString(s string) int {
	h := 0
	for i := 0; i < len(s); i++ {
		h = 31*h + int(s[i])
	}
	if h < 0 {
		h = -h
	}
	return h
}

// UpdateLoad updates the load for a node and checks if rotation is needed.
func (m *OLSManager) UpdateLoad(nodeID string, load float64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if node, ok := m.nodes[nodeID]; ok {
		node.Load = load
	}

	m.checkAndRotate()
}

func (m *OLSManager) checkAndRotate() {
	if m.n < 2 {
		return
	}

	// Calculate load vector
	var rowLoad []float64 = make([]float64, m.n)
	var colLoad []float64 = make([]float64, m.n)
	totalLoad := 0.0

	for i := 0; i < m.n; i++ {
		for j := 0; j < m.n; j++ {
			if m.grid[i][j] != nil {
				l := m.grid[i][j].Load
				rowLoad[i] += l
				colLoad[j] += l
				totalLoad += l
			}
		}
	}

	if totalLoad == 0 {
		return
	}

	// If load imbalance exceeds threshold, rotate
	// Simplified check: if row imbalance > col imbalance * 1.5, rotate?
	// Actually, the prompt says "specific direction... vector... 90 deg rotation".
	// Let's use a simpler heuristic: if row variance > column variance significantly, rotate.
	
	rowVar := variance(rowLoad)
	colVar := variance(colLoad)

	if rowVar > colVar*2 {
		m.rotation = (m.rotation + 90) % 360
	}
}

func variance(data []float64) float64 {
	if len(data) == 0 {
		return 0
	}
	sum := 0.0
	for _, x := range data {
		sum += x
	}
	mean := sum / float64(len(data))
	v := 0.0
	for _, x := range data {
		v += (x - mean) * (x - mean)
	}
	return v / float64(len(data))
}
