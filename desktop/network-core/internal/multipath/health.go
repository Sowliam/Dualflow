package multipath

import (
	"time"
)

// HealthEvaluator monitors path health transitions based on probe metrics
type HealthEvaluator struct {
	consecutiveFailureThreshold int
	staleThreshold               time.Duration
}

// NewHealthEvaluator creates a health evaluator with configurable thresholds
func NewHealthEvaluator(failureThreshold int, staleThreshold time.Duration) *HealthEvaluator {
	if failureThreshold <= 0 {
		failureThreshold = 3
	}
	if staleThreshold <= 0 {
		staleThreshold = 5 * time.Second
	}
	return &HealthEvaluator{
		consecutiveFailureThreshold: failureThreshold,
		staleThreshold:               staleThreshold,
	}
}

// Evaluate updates path operational state according to latest probe telemetry
func (h *HealthEvaluator) Evaluate(p *Path) PathState {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := time.Now()

	// Check if socket is open
	if p.socket == nil || !p.socket.IsOpen() {
		p.State = StateUnavailable
		return p.State
	}

	// Check for stale measurements
	if !p.LastProbeTime.IsZero() && now.Sub(p.LastProbeTime) > h.staleThreshold {
		if p.State == StateAvailable {
			p.State = StateDegraded
			p.LastStateChange = now
		}
		return p.State
	}

	// Check consecutive failures
	if p.ProbeFailures >= uint64(h.consecutiveFailureThreshold) {
		if p.State == StateAvailable {
			p.State = StateDegraded
			p.LastStateChange = now
		} else if p.ProbeFailures >= uint64(h.consecutiveFailureThreshold*2) {
			p.State = StateUnavailable
			p.LastStateChange = now
		}
		return p.State
	}

	// Healthy state restoration
	if p.State == StateDegraded && p.ProbeSuccess > 0 {
		p.State = StateAvailable
		p.LastStateChange = now
	}

	return p.State
}

