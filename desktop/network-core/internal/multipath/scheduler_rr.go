package multipath

import (
	"sync"
	"time"

	"dualflow-core/internal/packet"
)

// RoundRobinScheduler distributes packets across all eligible paths in deterministic sequence
type RoundRobinScheduler struct {
	mu    sync.Mutex
	index uint64
}

// NewRoundRobinScheduler instantiates a round-robin scheduler
func NewRoundRobinScheduler() *RoundRobinScheduler {
	return &RoundRobinScheduler{}
}

func (s *RoundRobinScheduler) Name() SchedulerMode {
	return ModeRoundRobin
}

func (s *RoundRobinScheduler) SelectPaths(pkt *packet.Packet, paths []*Path) ([]*Path, error) {
	eligible := FilterEligiblePaths(paths)
	if len(eligible) == 0 {
		return nil, ErrNoEligiblePaths
	}

	s.mu.Lock()
	idx := s.index % uint64(len(eligible))
	s.index++
	s.mu.Unlock()

	return []*Path{eligible[idx]}, nil
}

func (s *RoundRobinScheduler) UpdateMetrics(pathID string, rtt time.Duration, loss float64) {
	// Round-Robin is unweighted by latency
}

func (s *RoundRobinScheduler) SetWeights(weights map[string]uint32) error {
	return nil
}

func (s *RoundRobinScheduler) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.index = 0
}

