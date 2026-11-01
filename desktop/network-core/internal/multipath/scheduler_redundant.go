package multipath

import (
	"time"

	"dualflow-core/internal/packet"
)

// RedundantScheduler duplicates each logical packet across ALL eligible paths concurrently.
// It minimizes packet drop probability for competitive gaming and high-reliability streams.
type RedundantScheduler struct{}

// NewRedundantScheduler initializes a redundant multi-path scheduler
func NewRedundantScheduler() *RedundantScheduler {
	return &RedundantScheduler{}
}

func (s *RedundantScheduler) Name() SchedulerMode {
	return ModeRedundant
}

func (s *RedundantScheduler) SelectPaths(pkt *packet.Packet, paths []*Path) ([]*Path, error) {
	eligible := FilterEligiblePaths(paths)
	if len(eligible) == 0 {
		return nil, ErrNoEligiblePaths
	}
	// Return all eligible paths for simultaneous redundant broadcast
	return eligible, nil
}

func (s *RedundantScheduler) UpdateMetrics(pathID string, rtt time.Duration, loss float64) {
	// Redundant mode broadcasts to all active paths
}

func (s *RedundantScheduler) SetWeights(weights map[string]uint32) error {
	return nil
}

func (s *RedundantScheduler) Reset() {}

