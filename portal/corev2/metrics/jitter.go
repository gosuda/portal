package metrics

const (
	jitterWindowLen = 16
)

type JitterWindow [jitterWindowLen]int64

func (w *JitterWindow) Add(jitterNs int64) {
	copy(w[1:], w[:])
	w[0] = jitterNs
}

func (w *JitterWindow) Average() float64 {
	var sum int64
	var count int64
	for _, v := range w {
		if v != 0 {
			sum += v
			count++
		}
	}
	if count == 0 {
		return 0
	}
	return float64(sum) / float64(count)
}

func (w *JitterWindow) Clear() {
	for i := range w {
		w[i] = 0
	}
}
