package multipath

import (
	"sync"
	"time"

	"dualflow-core/internal/packet"
)

// WeightedRRScheduler distributes packets proportionally across paths using smooth weighted round-robin
type WeightedRRScheduler struct {
	mu            sync.Mutex
	weights       map[string]uint32
	currentWeight map[string]int
}

// NewWeightedRRScheduler initializes a smooth weighted round-robin scheduler
func NewWeightedRRScheduler(initialWeights map[string]uint32) *WeightedRRScheduler {
	w := make(map[string]uint32)
	for k, v := range initialWeights {
		if v > 0 {
			w[k] = v
		} else {
			w[k] = 1
		}
	}
	return &WeightedRRScheduler{
		weights:       w,
		currentWeight: make(map[string]int),
	}
}

func (s *WeightedRRScheduler) Name() SchedulerMode {
	return ModeWeightedRR
}

// SelectPaths selects a path using the NGINX smooth weighted round-robin algorithm
func (s *WeightedRRScheduler) SelectPaths(pkt *packet.Packet, paths []*Path) ([]*Path, error) {
	eligible := FilterEligiblePaths(paths)
	n := len(eligible)
	if n == 0 {
		return nil, ErrNoEligiblePaths
	}
	if n == 1 {
		return []*Path{eligible[0]}, nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	totalWeight := 0
	var bestPath *Path
	bestWeight := -1000000

	for _, p := range eligible {
		w := int(s.weights[p.ID])
		if w <= 0 {
			w = 1
		}
		totalWeight += w

		s.currentWeight[p.ID] += w
		if bestPath == nil || s.currentWeight[p.ID] > bestWeight {
			bestWeight = s.currentWeight[p.ID]
			bestPath = p
		}
	}

	if bestPath != nil {
		s.currentWeight[bestPath.ID] -= totalWeight
		return []*Path{bestPath}, nil
	}

	return []*Path{eligible[0]}, nil
}

func (s *WeightedRRScheduler) UpdateMetrics(pathID string, rtt time.Duration, loss float64) {}

func (s *WeightedRRScheduler) SetWeights(weights map[string]uint32) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for k, v := range weights {
		if v == 0 {
			return ErrInvalidWeight
		}
		s.weights[k] = v
	}
	s.currentWeight = make(map[string]int)
	return nil
}

func (s *WeightedRRScheduler) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.currentWeight = make(map[string]int)
}
