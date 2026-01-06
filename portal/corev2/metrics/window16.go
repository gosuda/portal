package metrics

const (
	LatencyWindowLen = 16
)

type LatencyWindow [LatencyWindowLen]uint64

func (w *LatencyWindow) Add(latencyNs uint64) {
	copy(w[1:], w[:])
	w[0] = latencyNs
}

func (w *LatencyWindow) Average() float64 {
	var sum uint64
	var count uint64
	for _, v := range w {
		if v > 0 {
			sum += v
			count++
		}
	}
	if count == 0 {
		return 0
	}
	return float64(sum) / float64(count)
}

func (w *LatencyWindow) Clear() {
	for i := range w {
		w[i] = 0
	}
}
