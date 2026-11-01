package multipath

import (
	"sync"
	"sync/atomic"
	"time"
)

// PathState describes the operational condition of an interface path
type PathState string

const (
	StateDiscovered   PathState = "DISCOVERED"
	StateAvailable    PathState = "AVAILABLE"
	StateDegraded     PathState = "DEGRADED"
	StateUnavailable  PathState = "UNAVAILABLE"
	StateDisconnected PathState = "DISCONNECTED"
	StateError        PathState = "ERROR"
)

// Path represents an independent physical or virtual network interface path
type Path struct {
	mu sync.RWMutex

	ID           string    `json:"id"`            // Stable ID (e.g. "modem", "android")
	Name         string    `json:"name"`          // Human label
	IfaceIndex   int       `json:"iface_index"`   // Windows interface index
	IfaceName    string    `json:"iface_name"`    // e.g. "Ethernet", "Ethernet 3"
	Description  string    `json:"description"`   // e.g. "Realtek PCIe GbE", "Remote NDIS"
	LocalIP      string    `json:"local_ip"`      // Assigned IPv4
	Gateway      string    `json:"gateway"`       // Next hop router
	State        PathState `json:"state"`         // DISCOVERED, AVAILABLE, etc.
	LinkSpeedBps uint64    `json:"link_speed_bps"`// Adapter link speed in bps
	Weight       uint32    `json:"weight"`        // Scheduler weight

	// Health and Quality Telemetry
	RTT             time.Duration `json:"rtt"`
	RTTMin          time.Duration `json:"rtt_min"`
	RTTMax          time.Duration `json:"rtt_max"`
	RTTJitterEWMA   time.Duration `json:"rtt_jitter_ewma"` // EWMA RTT Variation
	PacketLoss      float64       `json:"packet_loss"`     // 0.0 to 100.0%
	ProbeCount      uint64        `json:"probe_count"`
	ProbeSuccess    uint64        `json:"probe_success"`
	ProbeFailures   uint64        `json:"probe_failures"`
	LastProbeTime   time.Time     `json:"last_probe_time"`
	LastStateChange time.Time     `json:"last_state_change"`

	// Atomic Traffic Counters
	txPackets uint64
	rxPackets uint64
	txBytes   uint64
	rxBytes   uint64
	drops     uint64

	// Associated bound UDP socket
	socket *PathSocket
}

// NewPath instantiates a path entity
func NewPath(id, name string, ifaceIndex int, ifaceName, desc, localIP, gateway string) *Path {
	now := time.Now()
	return &Path{
		ID:              id,
		Name:            name,
		IfaceIndex:      ifaceIndex,
		IfaceName:       ifaceName,
		Description:     desc,
		LocalIP:         localIP,
		Gateway:         gateway,
		State:           StateDiscovered,
		Weight:          1,
		LastStateChange: now,
	}
}

// SetState transitions the path state and updates timestamp
func (p *Path) SetState(newState PathState) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.State != newState {
		p.State = newState
		p.LastStateChange = time.Now()
	}
}

// GetState returns current path state
func (p *Path) GetState() PathState {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.State
}

// GetRTT returns current path RTT
func (p *Path) GetRTT() time.Duration {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.RTT
}

// GetSocket returns the associated bound PathSocket
func (p *Path) GetSocket() *PathSocket {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.socket
}

// SetSocket assigns the socket to the path
func (p *Path) SetSocket(sock *PathSocket) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.socket = sock
}

// IsEligible returns true if the path is capable of transmitting packets
func (p *Path) IsEligible() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return (p.State == StateAvailable || p.State == StateDegraded) && p.socket != nil && p.socket.IsOpen()
}

// RecordTx atomically increments transmission counters
func (p *Path) RecordTx(bytes int) {
	atomic.AddUint64(&p.txPackets, 1)
	atomic.AddUint64(&p.txBytes, uint64(bytes))
}

// RecordRx atomically increments reception counters
func (p *Path) RecordRx(bytes int) {
	atomic.AddUint64(&p.rxPackets, 1)
	atomic.AddUint64(&p.rxBytes, uint64(bytes))
}

// RecordDrop atomically increments drop counter
func (p *Path) RecordDrop() {
	atomic.AddUint64(&p.drops, 1)
}

// UpdateProbeMetrics updates live RTT and EWMA jitter
func (p *Path) UpdateProbeMetrics(rttSample time.Duration, success bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.ProbeCount++
	p.LastProbeTime = time.Now()

	if !success {
		p.ProbeFailures++
		// If 3 consecutive failures, degrade or mark unavailable
		if p.ProbeFailures >= 3 && p.State == StateAvailable {
			p.State = StateDegraded
			p.LastStateChange = time.Now()
		}
		return
	}

	p.ProbeSuccess++
	p.ProbeFailures = 0 // reset consecutive failures
	if p.State == StateDegraded {
		p.State = StateAvailable
		p.LastStateChange = time.Now()
	}

	// Update RTT Min/Max
	if p.RTTMin == 0 || rttSample < p.RTTMin {
		p.RTTMin = rttSample
	}
	if rttSample > p.RTTMax {
		p.RTTMax = rttSample
	}

	// Calculate EWMA RTT Jitter:
	// Jitter_new = Jitter_old + (|RTT_sample - RTT_avg| - Jitter_old) / 16
	if p.RTT == 0 {
		p.RTT = rttSample
		p.RTTJitterEWMA = 0
	} else {
		delta := rttSample - p.RTT
		if delta < 0 {
			delta = -delta
		}
		// EWMA formula
		p.RTTJitterEWMA = p.RTTJitterEWMA + (delta-p.RTTJitterEWMA)/16
		// Smooth average RTT (alpha = 0.125 = 1/8)
		p.RTT = p.RTT + (rttSample-p.RTT)/8
	}

	// Calculate Loss Estimate
	if p.ProbeCount > 0 {
		p.PacketLoss = (float64(p.ProbeFailures) / float64(p.ProbeCount)) * 100.0
	}
}

// Snapshot returns a thread-safe telemetry snapshot
type PathSnapshot struct {
	ID              string        `json:"id"`
	Name            string        `json:"name"`
	IfaceIndex      int           `json:"iface_index"`
	IfaceName       string        `json:"iface_name"`
	Description     string        `json:"description"`
	LocalIP         string        `json:"local_ip"`
	Gateway         string        `json:"gateway"`
	State           PathState     `json:"state"`
	Weight          uint32        `json:"weight"`
	RTTMs           float64       `json:"rtt_ms"`
	RTTMinMs        float64       `json:"rtt_min_ms"`
	RTTMaxMs        float64       `json:"rtt_max_ms"`
	RTTJitterMs     float64       `json:"rtt_jitter_ms"`
	PacketLoss      float64       `json:"packet_loss"`
	ProbeCount      uint64        `json:"probe_count"`
	ProbeSuccess    uint64        `json:"probe_success"`
	ProbeFailures   uint64        `json:"probe_failures"`
	TxPackets       uint64        `json:"tx_packets"`
	RxPackets       uint64        `json:"rx_packets"`
	TxBytes         uint64        `json:"tx_bytes"`
	RxBytes         uint64        `json:"rx_bytes"`
	Drops           uint64        `json:"drops"`
	LastProbeMs     int64         `json:"last_probe_ms"`
	IsEligible      bool          `json:"is_eligible"`
}

// Snapshot creates a detached telemetry copy
func (p *Path) Snapshot() PathSnapshot {
	p.mu.RLock()
	defer p.mu.RUnlock()

	var lastProbe int64
	if !p.LastProbeTime.IsZero() {
		lastProbe = p.LastProbeTime.UnixMilli()
	}

	eligible := (p.State == StateAvailable || p.State == StateDegraded) && p.socket != nil && p.socket.IsOpen()

	return PathSnapshot{
		ID:          p.ID,
		Name:        p.Name,
		IfaceIndex:  p.IfaceIndex,
		IfaceName:   p.IfaceName,
		Description: p.Description,
		LocalIP:     p.LocalIP,
		Gateway:     p.Gateway,
		State:       p.State,
		Weight:      p.Weight,
		RTTMs:       float64(p.RTT.Microseconds()) / 1000.0,
		RTTMinMs:    float64(p.RTTMin.Microseconds()) / 1000.0,
		RTTMaxMs:    float64(p.RTTMax.Microseconds()) / 1000.0,
		RTTJitterMs: float64(p.RTTJitterEWMA.Microseconds()) / 1000.0,
		PacketLoss:  p.PacketLoss,
		ProbeCount:  p.ProbeCount,
		ProbeSuccess: p.ProbeSuccess,
		ProbeFailures: p.ProbeFailures,
		TxPackets:   atomic.LoadUint64(&p.txPackets),
		RxPackets:   atomic.LoadUint64(&p.rxPackets),
		TxBytes:     atomic.LoadUint64(&p.txBytes),
		RxBytes:     atomic.LoadUint64(&p.rxBytes),
		Drops:       atomic.LoadUint64(&p.drops),
		LastProbeMs: lastProbe,
		IsEligible:  eligible,
	}
}
