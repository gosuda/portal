package policy

import (
	"fmt"
	"math"
	"sort"
	"sync"
	"time"
)

const (
	w1 = 0.4 // ActiveConns
	w2 = 0.3 // BytesIn+BytesOut
	w3 = 0.2 // ConnRate
	w4 = 0.1 // AvgLatencyMs

	alpha = 0.2 // EWMA alpha

	rotationTriggerRatio   = 1.05
	rotationAggressiveRatio = 1.5
	rotationMinStepDeg     = 15.0
	rotationMaxStepDeg     = 90.0
)

// NodeLoad represents a node's load vector.
type NodeLoad struct {
	ActiveConns  int64
	BytesIn      int64
	BytesOut     int64
	ConnRate     float64
	AvgLatencyMs float64
}

// OLSNode represents a node in the grid.
type OLSNode struct {
	ID        string
	LoadScore float64
	Load      NodeLoad

	// Health tracking
	Healthy      bool
	LastFailure  time.Time
	FailureCount int

	LastUpdated int64
}

// NodeTable represents a versioned set of nodes.
type NodeTable struct {
	Version int64
	NodeIDs []string // sorted
}

// RouteContext tracks routing metadata to prevent loops.
type RouteContext struct {
	OriginNodeID string
	Visited      []string
	HopCount     int
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

	rotation float64

	table NodeTable
}

func NewOLSManager() *OLSManager {
	return &OLSManager{
		nodes: make(map[string]*OLSNode),
		table: NodeTable{Version: 0},
	}
}

// UpdateNodes updates the set of nodes and reconfigures the grid.
func (m *OLSManager) UpdateNodes(ids []string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Ensure deterministic ordering
	sort.Strings(ids)

	// Check if NodeTable version needs to be updated
	changed := false
	if len(ids) != len(m.table.NodeIDs) {
		changed = true
	} else {
		for i := range ids {
			if ids[i] != m.table.NodeIDs[i] {
				changed = true
				break
			}
		}
	}

	if changed {
		m.table.Version = time.Now().UnixNano()
		m.table.NodeIDs = make([]string, len(ids))
		copy(m.table.NodeIDs, ids)
	}

	oldNodes := m.nodes
	m.nodes = make(map[string]*OLSNode)
	for _, id := range ids {
		if node, ok := oldNodes[id]; ok {
			m.nodes[id] = node
		} else {
			m.nodes[id] = &OLSNode{ID: id, Healthy: true}
		}
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

	// Assign nodes to grid (deterministically using m.table.NodeIDs)
	count := 0
	for _, id := range m.table.NodeIDs {
		if count >= n*n {
			break
		}
		// Only add healthy nodes to the grid if possible
		if node, ok := m.nodes[id]; ok && node.Healthy {
			m.grid[count/n][count%n] = node
			count++
		}
	}

	// If we don't have enough healthy nodes, use unhealthy ones to fill the grid
	if count < n*n {
		for _, id := range m.table.NodeIDs {
			if count >= n*n {
				break
			}
			if node, ok := m.nodes[id]; ok && !node.Healthy {
				m.grid[count/n][count%n] = node
				count++
			}
		}
	}

	// Generate MOLS using recursive composition
	m.l1, m.l2 = generateMOLS(n)
}

// generateMOLS constructs a pair of orthogonal latin squares of order n.
func generateMOLS(n int) ([][]int, [][]int) {
	if n < 2 {
		return [][]int{{0}}, [][]int{{0}}
	}

	if isPrime(n) {
		return generateBaseMOLS(n, 1), generateBaseMOLS(n, n-1)
	}

	m, k := findFactors(n)
	if m == 1 {
		return generateBaseMOLS(n, 1), generateBaseMOLS(n, n-1)
	}

	a1, a2 := generateMOLS(m)
	b1, b2 := generateMOLS(k)

	return composeMOLS(a1, b1), composeMOLS(a2, b2)
}

// generateBaseMOLS fills an n×n grid where each cell (i,j) gets value
// (step*i + j) % n. Intermediate products are kept bounded by reducing
// modulo n at each multiplication step.
func generateBaseMOLS(n, step int) [][]int {
	ls := make([][]int, n)
	for i := 0; i < n; i++ {
		ls[i] = make([]int, n)
		// Compute step*i mod n once per row, then add j mod n per column.
		base := (step % n) * (i % n) % n
		for j := 0; j < n; j++ {
			ls[i][j] = (base + j) % n
		}
	}
	return ls
}

func composeMOLS(a, b [][]int) [][]int {
	m := len(a)
	k := len(b)
	n := m * k
	res := make([][]int, n)
	for i := 0; i < n; i++ {
		res[i] = make([]int, n)
		for j := 0; j < n; j++ {
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

func (m *OLSManager) GetTargetNodeID(clientID, leaseID string, ctx *RouteContext) (string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.n < 2 {
		return "", fmt.Errorf("grid not initialized")
	}

	if ctx != nil {
		if ctx.HopCount > 2 {
			return "", fmt.Errorf("max hops exceeded")
		}
	}

	i := hashStringMod(clientID, m.n)
	j := hashStringMod(leaseID, m.n)

	row := m.l1[i][j]
	col := m.l2[i][j]

	row, col = m.applyRotation(row, col)

	target := m.grid[row][col]
	if target == nil {
		return "", fmt.Errorf("node not found at %d,%d", row, col)
	}

	// Failure Amplification Mitigation: skip unhealthy
	if !target.Healthy || (time.Since(target.LastFailure) < 10*time.Second && target.FailureCount > 3) {
		// Fallback to next deterministic candidate
		nextIdx := (row*m.n + col + 1) % (m.n * m.n)
		target = m.grid[nextIdx/m.n][nextIdx%m.n]
	}

	if target == nil {
		return "", fmt.Errorf("no healthy node found")
	}

	// Loop Prevention
	if ctx != nil {
		for _, visited := range ctx.Visited {
			if visited == target.ID {
				return "", fmt.Errorf("loop detected")
			}
		}
	}

	return target.ID, nil
}

func (m *OLSManager) applyRotation(row, col int) (int, int) {
	if m.n <= 1 || m.rotation == 0 {
		return row, col
	}

	center := float64(m.n-1) / 2.0
	x := float64(col) - center
	y := float64(row) - center
	rad := m.rotation * math.Pi / 180.0
	cosV := math.Cos(rad)
	sinV := math.Sin(rad)

	rotX := x*cosV - y*sinV
	rotY := x*sinV + y*cosV

	newCol := int(math.Round(rotX + center))
	newRow := int(math.Round(rotY + center))

	if newRow < 0 {
		newRow = 0
	} else if newRow >= m.n {
		newRow = m.n - 1
	}
	if newCol < 0 {
		newCol = 0
	} else if newCol >= m.n {
		newCol = m.n - 1
	}
	return newRow, newCol
}

func (m *OLSManager) UpdateLoad(nodeID string, newLoad NodeLoad, score float64, timestamp int64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	node, ok := m.nodes[nodeID]
	if !ok {
		return
	}

	// Reject stale load data
	if timestamp > 0 && node.LastUpdated > timestamp {
		return
	}

	if score > 0 {
		// Load Score provided (distributed propagation)
		node.LoadScore = score
	} else {
		// Compute Load Score locally
		node.Load = newLoad
		node.LoadScore = m.computeLoadScore(newLoad, node.LoadScore)
	}
	node.LastUpdated = timestamp
	if timestamp == 0 {
		node.LastUpdated = time.Now().Unix()
	}

	m.checkAndRotate()
}

func (m *OLSManager) computeLoadScore(load NodeLoad, currentScore float64) float64 {
	// Simple normalization for this example
	norm := func(v float64, maxVal float64) float64 {
		if maxVal <= 0 {
			return 0
		}
		return math.Min(1.0, v/maxVal)
	}

	// Bounds for normalization
	score := w1*norm(float64(load.ActiveConns), 1000) +
		w2*norm(float64(load.BytesIn+load.BytesOut), 100*1024*1024) +
		w3*norm(load.ConnRate, 100) +
		w4*norm(load.AvgLatencyMs, 500)

	// EWMA to resist spikes
	if currentScore == 0 {
		return score
	}
	return alpha*score + (1-alpha)*currentScore
}

func (m *OLSManager) MarkFailure(nodeID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if node, ok := m.nodes[nodeID]; ok {
		node.FailureCount++
		node.LastFailure = time.Now()
		if node.FailureCount > 5 {
			node.Healthy = false
		}
	}
}

func (m *OLSManager) MarkSuccess(nodeID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if node, ok := m.nodes[nodeID]; ok {
		node.FailureCount = 0
		node.Healthy = true
	}
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
				l := m.grid[i][j].LoadScore
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

	rowDominant := rowVar > colVar*rotationTriggerRatio
	colDominant := colVar > rowVar*rotationTriggerRatio
	if !rowDominant && !colDominant {
		return
	}

	dominantVar := rowVar
	otherVar := colVar
	sign := 1.0
	if colDominant {
		dominantVar = colVar
		otherVar = rowVar
		sign = -1.0
	}

	ratio := dominantVar / math.Max(otherVar, 1e-9)
	step := rotationMaxStepDeg
	if ratio < rotationAggressiveRatio {
		scale := (ratio - rotationTriggerRatio) / (rotationAggressiveRatio - rotationTriggerRatio)
		if scale < 0 {
			scale = 0
		} else if scale > 1 {
			scale = 1
		}
		step = rotationMinStepDeg + (rotationMaxStepDeg-rotationMinStepDeg)*scale
	}

	m.rotation = math.Mod(m.rotation+sign*step+360.0, 360.0)
}

// hashStringMod computes a polynomial hash of s modulo mod using Horner's method.
// Reducing modulo mod at each step keeps intermediate values bounded as n grows.
func hashStringMod(s string, mod int) int {
	if mod <= 0 {
		return 0
	}
	h := 0
	for i := 0; i < len(s); i++ {
		h = (31*h + int(s[i])) % mod
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
