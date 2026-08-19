// Package loadgen is the concurrent load harness that measures throughput,
// latency, and the correctness invariants (money conservation, no double-spend,
// exactly-once idempotency, zero-drift reconciliation) under real concurrency.
package loadgen

import (
	"sort"
	"time"
)

// latencies accumulates per-op durations and computes percentiles.
type latencies struct {
	d []time.Duration
}

func (l *latencies) add(d time.Duration) { l.d = append(l.d, d) }

func (l *latencies) merge(other []time.Duration) { l.d = append(l.d, other...) }

// percentile returns the p-th percentile (0..100) using nearest-rank.
func (l *latencies) percentile(p float64) time.Duration {
	if len(l.d) == 0 {
		return 0
	}
	sorted := make([]time.Duration, len(l.d))
	copy(sorted, l.d)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	rank := int(p/100*float64(len(sorted)-1) + 0.5)
	if rank < 0 {
		rank = 0
	}
	if rank >= len(sorted) {
		rank = len(sorted) - 1
	}
	return sorted[rank]
}

func (l *latencies) count() int { return len(l.d) }

func (l *latencies) mean() time.Duration {
	if len(l.d) == 0 {
		return 0
	}
	var sum time.Duration
	for _, d := range l.d {
		sum += d
	}
	return sum / time.Duration(len(l.d))
}

// ms renders a duration as milliseconds with 3 decimals for JSON/report.
func ms(d time.Duration) float64 {
	return float64(d.Microseconds()) / 1000.0
}
