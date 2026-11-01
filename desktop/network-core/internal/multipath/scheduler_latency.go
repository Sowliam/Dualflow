package multipath

import (
	"sync"
	"time"

	"dualflow-core/internal/packet"
)

// LowestLatencyScheduler routes traffic through the path with lowest verified RTT
type LowestLatencyScheduler struct {
	mu             sync.Mutex
	hysteresis     time.Duration
	currentPathID  string
	metrics        map[string]time.Duration
	lastUpdate     map[string]time.Time
	fallbackRR     *RoundRobinScheduler
}

// NewLowestLatencyScheduler initializes an LLF scheduler with hysteresis protection
func NewLowestLatencyScheduler(hysteresisMs float64) *LowestLatencyScheduler {
	if hysteresisMs <= 0 {
		hysteresisMs = 10.0
	}
	return &LowestLatencyScheduler{
		hysteresis: time.Duration(hysteresisMs * float64(time.Millisecond)),
		metrics:    make(map[string]time.Duration),
		lastUpdate: make(map[string]time.Time),
		fallbackRR: NewRoundRobinScheduler(),
	}
}

func (s *LowestLatencyScheduler) Name() SchedulerMode {
	return ModeLowestLatency
}

func (s *LowestLatencyScheduler) SelectPaths(pkt *packet.Packet, paths []*Path) ([]*Path, error) {
	eligible := FilterEligiblePaths(paths)
	if len(eligible) == 0 {
		return nil, ErrNoEligiblePaths
	}
	if len(eligible) == 1 {
		return []*Path{eligible[0]}, nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	var bestPath *Path
	var bestRTT time.Duration = time.Hour

	// Find path with lowest valid, non-stale RTT
	for _, p := range eligible {
		rtt, hasMetric := s.metrics[p.ID]
		updateTime := s.lastUpdate[p.ID]

		// Also check live path snapshot if prober updated it directly
		if !hasMetric && p.RTT > 0 {
			rtt = p.RTT
			hasMetric = true
			updateTime = p.LastProbeTime
		}

		// Reject measurements older than 5 seconds (stale)
		if !hasMetric || now.Sub(updateTime) > 5*time.Second || rtt <= 0 {
			continue
		}

		if bestPath == nil || rtt < bestRTT {
			bestRTT = rtt
			bestPath = p
		}
	}

	// If no valid latency metrics available across all paths, fallback to round-robin
	if bestPath == nil {
		return s.fallbackRR.SelectPaths(pkt, eligible)
	}

	// Apply Hysteresis to prevent high-frequency route oscillation (route flapping)
	if s.currentPathID != "" && s.currentPathID != bestPath.ID {
		var currentPath *Path
		for _, p := range eligible {
			if p.ID == s.currentPathID {
				currentPath = p
				break
			}
		}

		if currentPath != nil {
			currRTT := s.metrics[currentPath.ID]
			if currRTT > 0 {
				// Only switch if the new path improves latency by at least the hysteresis margin
				if (currRTT - bestRTT) < s.hysteresis {
					// Latency improvement is within margin: retain current path
					return []*Path{currentPath}, nil
				}
			}
		}
	}

	s.currentPathID = bestPath.ID
	return []*Path{bestPath}, nil
}

func (s *LowestLatencyScheduler) UpdateMetrics(pathID string, rtt time.Duration, loss float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.metrics[pathID] = rtt
	s.lastUpdate[pathID] = time.Now()
}

func (s *LowestLatencyScheduler) SetWeights(weights map[string]uint32) error {
	return nil
}

func (s *LowestLatencyScheduler) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.metrics = make(map[string]time.Duration)
	s.lastUpdate = make(map[string]time.Time)
	s.currentPathID = ""
	s.fallbackRR.Reset()
}

