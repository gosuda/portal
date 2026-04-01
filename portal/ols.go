package portal

import (
	"fmt"
	"math"
	"sync"
)

// OLSNode represents a node in the grid.
type OLSNode struct {
	ID      string
	Address string
	Load    float64
}

// OLSManager manages the grid topology using recursive composition.
type OLSManager struct {
	mu sync.RWMutex

	nodes map[string]*OLSNode
	n     int
	grid  [][]*OLSNode

	// Orthogonal Latin Squares
	l1 [][]int
	l2 [][]int

	rotation int
}

func NewOLSManager() *OLSManager {
	return &OLSManager{
		nodes: make(map[string]*OLSNode),
	}
}

// UpdateNodes updates the set of nodes and reconfigures the grid.
func (m *OLSManager) UpdateNodes(nodes map[string]string) {
	m.mu.Lock()
	defer m.mu.Unlock()

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

// reconfigure builds a new grid using recursive composition of squares.
func (m *OLSManager) reconfigure(n int) {
	m.n = n
	m.grid = make([][]*OLSNode, n)
	for i := range m.grid {
		m.grid[i] = make([]*OLSNode, n)
	}

	// Assign nodes to grid
	count := 0
	for _, node := range m.nodes {
		if count >= n*n {
			break
		}
		m.grid[count/n][count%n] = node
		count++
	}

	// Generate MOLS using recursive composition
	m.l1, m.l2 = generateMOLS(n)
}

// generateMOLS constructs a pair of orthogonal latin squares of order n.
// It uses recursive composition to build larger squares from smaller components.
func generateMOLS(n int) ([][]int, [][]int) {
	if n < 2 {
		return [][]int{{0}}, [][]int{{0}}
	}

	// Base case: Prime or small order
	if isPrime(n) {
		return generateBaseMOLS(n, 1), generateBaseMOLS(n, n-1)
	}

	// Recursive step: Find factors m, k such that n = m * k
	m, k := findFactors(n)
	if m == 1 {
		// Fallback if no good factors found (should not happen for n >= 2)
		return generateBaseMOLS(n, 1), generateBaseMOLS(n, n-1)
	}

	// Recurse to get MOLS for components
	a1, a2 := generateMOLS(m)
	b1, b2 := generateMOLS(k)

	// Compose the components into a larger square (Kronecker product style)
	return composeMOLS(a1, b1), composeMOLS(a2, b2)
}

func generateBaseMOLS(n, step int) [][]int {
	ls := make([][]int, n)
	for i := 0; i < n; i++ {
		ls[i] = make([]int, n)
		for j := 0; j < n; j++ {
			ls[i][j] = (step*i + j) % n
		}
	}
	return ls
}

// composeMOLS combines two squares of order m and k into a square of order m*k.
func composeMOLS(a, b [][]int) [][]int {
	m := len(a)
	k := len(b)
	n := m * k
	res := make([][]int, n)
	for i := 0; i < n; i++ {
		res[i] = make([]int, n)
		for j := 0; j < n; j++ {
			// (i, j) in n x n grid maps to
			// (i/k, j/k) in m x m grid and (i%k, j%k) in k x k grid
			valA := a[i/k][j/k]
			valB := b[i%k][j%k]
			res[i][j] = valA*k + valB
		}
	}
	return res
}

func isPrime(n int) bool {
	if n < 2 {
		return false
	}
	for i := 2; i*i <= n; i++ {
		if n%i == 0 {
			return false
		}
	}
	return true
}

func findFactors(n int) (int, int) {
	for i := 2; i*i <= n; i++ {
		if n%i == 0 {
			return i, n / i
		}
	}
	return 1, n
}

func (m *OLSManager) GetTargetNode(clientID, leaseID string) (*OLSNode, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.n < 2 {
		return nil, fmt.Errorf("grid not initialized")
	}

	i := hashString(clientID) % m.n
	j := hashString(leaseID) % m.n

	row := m.l1[i][j]
	col := m.l2[i][j]

	row, col = m.applyRotation(row, col)

	if m.grid[row][col] == nil {
		return nil, fmt.Errorf("node not found at %d,%d", row, col)
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

	rowLoad := make([]float64, m.n)
	colLoad := make([]float64, m.n)
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

	rowVar := variance(rowLoad)
	colVar := variance(colLoad)

	if rowVar > colVar*2 {
		m.rotation = (m.rotation + 90) % 360
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
