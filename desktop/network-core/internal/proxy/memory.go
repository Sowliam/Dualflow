package proxy

import (
	"errors"
	"sync/atomic"
)

var (
	ErrGlobalMemoryExceeded = errors.New("global proxy memory limit exceeded (64MB)")
	ErrFlowMemoryExceeded   = errors.New("per-flow memory limit exceeded (2MB)")
	ErrFlowLimitExceeded    = errors.New("maximum concurrent flows limit reached (1024)")
)

const (
	// MaxMemoryPerFlow is the hard buffer limit per flow (2 MB)
	MaxMemoryPerFlow int64 = 2 * 1024 * 1024

	// HighWatermarkPerFlow is 80% (1.6 MB) where backpressure pause is triggered
	HighWatermarkPerFlow int64 = 1600 * 1024

	// LowWatermarkPerFlow is 50% (1.0 MB) where backpressure is released
	LowWatermarkPerFlow int64 = 1000 * 1024

	// GlobalMemoryCap is the hard admission ceiling across all flows (64 MB)
	GlobalMemoryCap int64 = 64 * 1024 * 1024

	// MaxFlowsPerSession is the maximum concurrent flows per session (1024)
	MaxFlowsPerSession = 1024
)

// GlobalMemoryCoordinator tracks and enforces memory limits across all active flows
type GlobalMemoryCoordinator struct {
	allocatedBytes atomic.Int64
	activeFlows    atomic.Int32
}

var globalCoordinator = &GlobalMemoryCoordinator{}

// GetGlobalCoordinator returns the singleton coordinator
func GetGlobalCoordinator() *GlobalMemoryCoordinator {
	return globalCoordinator
}

// Reset clears global memory counters (primarily for testing)
func (g *GlobalMemoryCoordinator) Reset() {
	g.allocatedBytes.Store(0)
	g.activeFlows.Store(0)
}

// TryAcquire attempts to reserve memory within the global 64 MB budget
func (g *GlobalMemoryCoordinator) TryAcquire(bytes int64) bool {
	for {
		current := g.allocatedBytes.Load()
		if current+bytes > GlobalMemoryCap {
			return false
		}
		if g.allocatedBytes.CompareAndSwap(current, current+bytes) {
			return true
		}
	}
}

// Release frees reserved global memory
func (g *GlobalMemoryCoordinator) Release(bytes int64) {
	if bytes <= 0 {
		return
	}
	for {
		current := g.allocatedBytes.Load()
		newVal := current - bytes
		if newVal < 0 {
			newVal = 0
		}
		if g.allocatedBytes.CompareAndSwap(current, newVal) {
			return
		}
	}
}

// Allocated returns total bytes currently reserved globally
func (g *GlobalMemoryCoordinator) Allocated() int64 {
	return g.allocatedBytes.Load()
}

// TryRegisterFlow checks if another flow can be accommodated under the 1024 flow cap
func (g *GlobalMemoryCoordinator) TryRegisterFlow() bool {
	for {
		current := g.activeFlows.Load()
		if current >= MaxFlowsPerSession {
			return false
		}
		if g.activeFlows.CompareAndSwap(current, current+1) {
			return true
		}
	}
}

// UnregisterFlow releases a flow registration slot
func (g *GlobalMemoryCoordinator) UnregisterFlow() {
	for {
		current := g.activeFlows.Load()
		if current <= 0 {
			return
		}
		if g.activeFlows.CompareAndSwap(current, current-1) {
			return
		}
	}
}

// ActiveFlows returns current registered flow count
func (g *GlobalMemoryCoordinator) ActiveFlows() int32 {
	return g.activeFlows.Load()
}

