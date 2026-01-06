package metrics

type LossTracker struct {
	alpha       float64
	ewma        float64
	sampleCount uint64
}

func NewLossTracker() *LossTracker {
	return &LossTracker{
		alpha: 0.5,
		ewma: 0,
	}
}

func (lt *LossTracker) Update(loss float64) {
	lt.sampleCount++
	if lt.sampleCount == 1 {
		lt.ewma = loss
	} else {
		lt.ewma = lt.alpha*loss + (1-lt.alpha)*lt.ewma
	}
}

func (lt *LossTracker) Get() float64 {
	return lt.ewma
}

func (lt *LossTracker) Reset() {
	lt.ewma = 0
	lt.sampleCount = 0
}
