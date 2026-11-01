package multipath

import (
	"errors"
	"time"

	"dualflow-core/internal/packet"
)

var (
	ErrNoEligiblePaths = errors.New("no eligible network paths available for transmission")
	ErrInvalidWeight   = errors.New("invalid path weight: must be greater than zero")
)

// PacketScheduler defines the interface for all Multi-Path packet distribution algorithms
type PacketScheduler interface {
	// Name returns the identifier of the scheduler algorithm
	Name() SchedulerMode

	// SelectPaths chooses one or more eligible paths to transmit the given packet
	SelectPaths(pkt *packet.Packet, paths []*Path) ([]*Path, error)

	// UpdateMetrics informs the scheduler of updated path latency and loss statistics
	UpdateMetrics(pathID string, rtt time.Duration, loss float64)

	// SetWeights updates relative path weights for weighted schedulers
	SetWeights(weights map[string]uint32) error

	// Reset reinitializes internal counters and scheduling state
	Reset()
}

// FilterEligiblePaths returns only paths that are AVAILABLE/DEGRADED and have active open sockets
func FilterEligiblePaths(paths []*Path) []*Path {
	eligible := make([]*Path, 0, len(paths))
	for _, p := range paths {
		if p != nil && p.IsEligible() {
			eligible = append(eligible, p)
		}
	}
	return eligible
}

