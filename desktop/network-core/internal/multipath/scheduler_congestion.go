package multipath

import (
	"math"
	"sync"
	"time"

	"dualflow-core/internal/packet"
)

const (
	InitialCWNDBytes    uint32 = 12800  // 10 * 1280 (RFC 6928 initial window)
	MinimumCWNDBytes    uint32 = 2560   // 2 * 1280
	DefaultSsthresh     uint32 = 655350 // Initial slow start threshold
	MaxCWNDBytes        uint32 = 4194304 // 4 MB max path window
	StandardMSS         uint32 = 1280
)

// PathCongestionState tracks window, in-flight bytes, and SRTT for an individual physical path
type PathCongestionState struct {
	mu            sync.Mutex
	PathID        string
	CWND          uint32
	BytesInFlight uint32
	Ssthresh      uint32
	SRTT          time.Duration
	RTTVAR        time.Duration
	LastUpdated   time.Time
}

// NewPathCongestionState initializes state with safe TCP/MPTCP defaults
func NewPathCongestionState(pathID string) *PathCongestionState {
	return &PathCongestionState{
		PathID:      pathID,
		CWND:        InitialCWNDBytes,
		Ssthresh:    DefaultSsthresh,
		LastUpdated: time.Now(),
	}
}

// CongestionScheduler implements congestion-aware multipath scheduling
type CongestionScheduler struct {
	mu     sync.RWMutex
	states map[string]*PathCongestionState
}

// NewCongestionScheduler creates an initialized scheduler instance
func NewCongestionScheduler() *CongestionScheduler {
	return &CongestionScheduler{
		states: make(map[string]*PathCongestionState),
	}
}

func (s *CongestionScheduler) Name() SchedulerMode {
	return ModeCongestion
}

func (s *CongestionScheduler) getState(pathID string) *PathCongestionState {
	s.mu.Lock()
	defer s.mu.Unlock()

	st, exists := s.states[pathID]
	if !exists {
		st = NewPathCongestionState(pathID)
		s.states[pathID] = st
	}
	return st
}

// SelectPaths chooses the optimal path based on available CWND headroom and estimated completion time
func (s *CongestionScheduler) SelectPaths(pkt *packet.Packet, paths []*Path) ([]*Path, error) {
	eligible := FilterEligiblePaths(paths)
	if len(eligible) == 0 {
		return nil, ErrNoEligiblePaths
	}
	if len(eligible) == 1 {
		return eligible, nil
	}

	pktSize := StandardMSS
	if pkt != nil && len(pkt.Raw) > 0 {
		pktSize = uint32(len(pkt.Raw))
	}

	// 1. Evaluate paths with CWND capacity (BytesInFlight + pktSize <= CWND)
	var availablePaths []*Path
	for _, p := range eligible {
		st := s.getState(p.ID)
		st.mu.Lock()
		hasCapacity := (st.BytesInFlight + pktSize) <= st.CWND
		st.mu.Unlock()

		if hasCapacity {
			availablePaths = append(availablePaths, p)
		}
	}

	// 2. If one or more paths have available capacity, select the one with lowest SRTT
	candidatePool := availablePaths
	if len(candidatePool) == 0 {
		// All paths saturated: spillover to the path with lowest in-flight excess percentage
		candidatePool = eligible
	}

	bestPath := candidatePool[0]
	bestRTT := time.Duration(math.MaxInt64)

	for _, p := range candidatePool {
		st := s.getState(p.ID)
		st.mu.Lock()
		rtt := st.SRTT
		if rtt == 0 {
			rtt = p.GetRTT()
		}
		st.mu.Unlock()

		if rtt > 0 && rtt < bestRTT {
			bestRTT = rtt
			bestPath = p
		}
	}

	return []*Path{bestPath}, nil
}

// OnPacketSent records in-flight bytes on a path
func (s *CongestionScheduler) OnPacketSent(pathID string, size uint32) {
	st := s.getState(pathID)
	st.mu.Lock()
	defer st.mu.Unlock()

	st.BytesInFlight += size
}

// OnAckReceived adjusts CWND according to standard TCP congestion control (RFC 5681)
func (s *CongestionScheduler) OnAckReceived(pathID string, ackedBytes uint32, sampleRTT time.Duration) {
	st := s.getState(pathID)
	st.mu.Lock()
	defer st.mu.Unlock()

	if st.BytesInFlight >= ackedBytes {
		st.BytesInFlight -= ackedBytes
	} else {
		st.BytesInFlight = 0
	}

	// SRTT update via RFC 6298 EWMA
	if sampleRTT > 0 {
		if st.SRTT == 0 {
			st.SRTT = sampleRTT
			st.RTTVAR = sampleRTT / 2
		} else {
			diff := st.SRTT - sampleRTT
			if diff < 0 {
				diff = -diff
			}
			st.RTTVAR = (3*st.RTTVAR + diff) / 4
			st.SRTT = (7*st.SRTT + sampleRTT) / 8
		}
	}

	// CWND growth
	if st.CWND < st.Ssthresh {
		// Slow Start: increase by ackedBytes up to 1 MSS
		increment := ackedBytes
		if increment > StandardMSS {
			increment = StandardMSS
		}
		st.CWND += increment
	} else {
		// Congestion Avoidance: increase by (MSS * MSS) / CWND
		increment := (StandardMSS * StandardMSS) / st.CWND
		if increment == 0 {
			increment = 1
		}
		st.CWND += increment
	}

	if st.CWND > MaxCWNDBytes {
		st.CWND = MaxCWNDBytes
	}
	st.LastUpdated = time.Now()
}

// OnLossDetected triggers multiplicative decrease on packet loss
func (s *CongestionScheduler) OnLossDetected(pathID string) {
	st := s.getState(pathID)
	st.mu.Lock()
	defer st.mu.Unlock()

	st.Ssthresh = st.CWND / 2
	if st.Ssthresh < MinimumCWNDBytes {
		st.Ssthresh = MinimumCWNDBytes
	}

	st.CWND = MinimumCWNDBytes
	st.LastUpdated = time.Now()
}

func (s *CongestionScheduler) UpdateMetrics(pathID string, rtt time.Duration, loss float64) {
	st := s.getState(pathID)
	st.mu.Lock()
	defer st.mu.Unlock()

	if rtt > 0 {
		st.SRTT = rtt
	}
	if loss > 5.0 {
		// Significant loss signal
		st.Ssthresh = st.CWND / 2
		if st.Ssthresh < MinimumCWNDBytes {
			st.Ssthresh = MinimumCWNDBytes
		}
		st.CWND = MinimumCWNDBytes
	}
}

func (s *CongestionScheduler) SetWeights(weights map[string]uint32) error {
	// Congestion scheduler uses physical delay and bandwidth capacity rather than static weights
	return nil
}

func (s *CongestionScheduler) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.states = make(map[string]*PathCongestionState)
}

