package multipath

import (
	"time"
)

// SchedulerMode defines the packet distribution algorithm
type SchedulerMode string

const (
	ModeRoundRobin     SchedulerMode = "round_robin"
	ModeWeightedRR     SchedulerMode = "weighted_rr"
	ModeLowestLatency  SchedulerMode = "lowest_latency"
	ModeRedundant      SchedulerMode = "redundant"
	ModeCongestion     SchedulerMode = "congestion"
)

// Config defines the runtime configuration for the Multi-Path Bonding Engine
type Config struct {
	SchedulerMode       SchedulerMode           `json:"scheduler_mode"`
	ProbeInterval       time.Duration           `json:"probe_interval"`       // Default: 1000ms
	ProbeTimeout        time.Duration           `json:"probe_timeout"`        // Default: 1500ms
	ReorderTimeout      time.Duration           `json:"reorder_timeout"`      // Default: 40ms
	ReorderCapacity     int                     `json:"reorder_capacity"`     // Default: 256 packets
	DedupWindowSize     int                     `json:"dedup_window_size"`    // Default: 1024 packets
	LatencyHysteresisMs float64                 `json:"latency_hysteresis_ms"`// Default: 10.0 ms
	Weights             map[string]uint32       `json:"weights"`              // PathID -> Weight
	EnableLocalReflector bool                   `json:"enable_local_reflector"`
}

// DefaultConfig returns safe production defaults
func DefaultConfig() Config {
	return Config{
		SchedulerMode:       ModeRoundRobin,
		ProbeInterval:       1000 * time.Millisecond,
		ProbeTimeout:        1500 * time.Millisecond,
		ReorderTimeout:      40 * time.Millisecond,
		ReorderCapacity:     256,
		DedupWindowSize:     1024,
		LatencyHysteresisMs: 10.0,
		Weights: map[string]uint32{
			"modem":   1,
			"android": 1,
		},
		EnableLocalReflector: true,
	}
}

