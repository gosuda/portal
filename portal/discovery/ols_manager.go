package discovery

import (
	"math"
	"slices"

	"github.com/gosuda/portal/v2/types"
)

// OLSManager computes relay polling order from a non-linear load vector.
//
// Why this exists:
// - We need deterministic redistribution without simple vector rotation.
// - We keep selection in one owner so bootstrap and syncable relay polling share
//   the same balancing contract.
// - We apply inverse-load pre-distortion before OLS-style permutation so heavily
//   loaded relays are naturally delayed while preserving one-pass fairness.
type OLSManager struct{}

func NewOLSManager() *OLSManager {
	return &OLSManager{}
}

// OrderDescriptors returns a deterministic OLS-based order for this round.
//
// Procedure:
// 1) Build a non-linear load score f(x)=x^2 for each relay URL.
// 2) Apply inverse pre-distortion by mapping y=f(x) back to approximately x.
//    In practice we keep zero-safe behavior with sqrt(y+1), then rank by this
//    compensated value (monotonic with respect to load).
// 3) Convert compensated weights into a stable inverse permutation (least-loaded
//    first, ties by URL).
// 4) Feed the inverse permutation into a latin-square style affine map
//    slot=(a*i+b) mod n where a is coprime with n (no rotation).
func (m *OLSManager) OrderDescriptors(relays []types.RelayDescriptor, loadByURL map[string]float64, round uint64) []types.RelayDescriptor {
	if len(relays) <= 1 {
		return relays
	}
	ordered := make([]types.RelayDescriptor, len(relays))
	copy(ordered, relays)
	slices.SortStableFunc(ordered, func(a, b types.RelayDescriptor) int {
		if a.APIHTTPSAddr < b.APIHTTPSAddr {
			return -1
		}
		if a.APIHTTPSAddr > b.APIHTTPSAddr {
			return 1
		}
		return 0
	})

	type weighted struct {
		desc         types.RelayDescriptor
		compensated  float64
	}
	weights := make([]weighted, 0, len(ordered))
	for _, relay := range ordered {
		load := clampNonNegative(loadByURL[relay.APIHTTPSAddr])
		// x^2 increases penalty faster for high-load relays than low-load relays.
		distorted := load * load // f(x)=x^2
		// sqrt(y+1) is used as a stable inverse-like compensation that stays finite
		// at zero load and preserves ordering for scheduling.
		compensated := math.Sqrt(distorted + 1.0)
		weights = append(weights, weighted{
			desc:        relay,
			compensated: compensated,
		})
	}
	slices.SortStableFunc(weights, func(a, b weighted) int {
		if a.compensated < b.compensated {
			return -1
		}
		if a.compensated > b.compensated {
			return 1
		}
		if a.desc.APIHTTPSAddr < b.desc.APIHTTPSAddr {
			return -1
		}
		if a.desc.APIHTTPSAddr > b.desc.APIHTTPSAddr {
			return 1
		}
		return 0
	})

	n := len(weights)
	a := pickCoprimeStep(n, int(round))
	b := int(round % uint64(n))
	out := make([]types.RelayDescriptor, 0, n)
	for i := range n {
		slot := (a*i + b) % n
		out = append(out, weights[slot].desc)
	}
	return out
}

func clampNonNegative(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return 0
	}
	return v
}

func gcd(a, b int) int {
	for b != 0 {
		a, b = b, a%b
	}
	if a < 0 {
		return -a
	}
	return a
}

func pickCoprimeStep(n int, round int) int {
	if n <= 1 {
		// Guard keeps the modulo below safe; (n-1) is never zero afterwards.
		return 1
	}
	candidate := (round % (n - 1)) + 1
	for candidate < n {
		if gcd(candidate, n) == 1 {
			return candidate
		}
		candidate++
	}
	return 1
}
