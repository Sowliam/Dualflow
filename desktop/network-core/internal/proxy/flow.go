package proxy

import (
	"errors"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"dualflow-core/internal/transport"
)

type FlowState string

const (
	FlowStateActive           FlowState = "ACTIVE"
	FlowStateHalfClosedLocal  FlowState = "HALF_CLOSED_LOCAL"  // Local app sent FIN
	FlowStateHalfClosedRemote FlowState = "HALF_CLOSED_REMOTE" // Remote server sent FIN
	FlowStateClosing          FlowState = "CLOSING"            // Both sides sent FIN
	FlowStateTimeWait         FlowState = "TIME_WAIT"          // 20s quarantine before reuse
	FlowStateClosed           FlowState = "CLOSED"
)

var (
	ErrFlowClosed          = errors.New("flow is closed or in time-wait")
	ErrMaxRetriesExceeded  = errors.New("flow retry exhaustion: exceeded maximum 5 retries")
	ErrFlowWriteHalfClosed = errors.New("cannot write to locally half-closed flow")
)

const (
	MaxRetriesPerChunk   = 5
	MaxRetryDuration     = 15 * time.Second
	InitialRTO           = 1000 * time.Millisecond
	MinRTO               = 200 * time.Millisecond
	MaxRTO               = 3000 * time.Millisecond
	MaxBackoffRTO        = 10 * time.Second
	FlowTimeWaitDuration = 20 * time.Second
	FastRetransmitThreshold = 3
)

// RetransmitChunk represents an in-flight unacknowledged stream chunk
type RetransmitChunk struct {
	Offset       uint64
	Flags        uint16
	Data         []byte
	FirstSentAt  time.Time
	LastSentAt   time.Time
	RetryCount   int
	AckedBySack  bool
}

// RTOCalculator manages RFC 6298 round-trip timing and Karn's algorithm
type RTOCalculator struct {
	srtt    time.Duration
	rttvar  time.Duration
	rto     time.Duration
	hasSample bool
}

// NewRTOCalculator creates an initialized RTO tracker
func NewRTOCalculator() *RTOCalculator {
	return &RTOCalculator{
		rto: InitialRTO,
	}
}

// RecordSample updates SRTT and RTTVAR per RFC 6298.
// Karn's Algorithm Invariant: MUST NOT be called for retransmitted chunks!
func (c *RTOCalculator) RecordSample(rtt time.Duration) {
	if rtt <= 0 {
		return
	}
	if !c.hasSample {
		c.srtt = rtt
		c.rttvar = rtt / 2
		c.hasSample = true
	} else {
		// RFC 6298: alpha = 1/8, beta = 1/4
		diff := time.Duration(math.Abs(float64(c.srtt - rtt)))
		c.rttvar = (3*c.rttvar + diff) / 4
		c.srtt = (7*c.srtt + rtt) / 8
	}

	calculated := c.srtt + (4 * c.rttvar)
	if calculated < MinRTO {
		c.rto = MinRTO
	} else if calculated > MaxRTO {
		c.rto = MaxRTO
	} else {
		c.rto = calculated
	}
}

// Backoff applies binary exponential backoff on timeout
func (c *RTOCalculator) Backoff() time.Duration {
	c.rto *= 2
	if c.rto > MaxBackoffRTO {
		c.rto = MaxBackoffRTO
	}
	return c.rto
}

// CurrentRTO returns current timeout duration
func (c *RTOCalculator) CurrentRTO() time.Duration {
	if c.rto < MinRTO {
		return MinRTO
	}
	return c.rto
}

// Flow models an active end-to-end TCP connection proxied across DualFlow
type Flow struct {
	mu                  sync.Mutex
	ID                  uint32
	State               FlowState
	NextTxOffset        uint64
	LastCumAck          uint64
	DuplicateAckCount   int
	RetransmitQueue     []*RetransmitChunk
	RTO                 *RTOCalculator
	ReorderBuffer       *ReorderStreamBuffer
	FinSent             bool
	FinOffset           uint64
	FinAcked            bool
	RemoteFinDelivered  bool
	TimeWaitExpiry      time.Time
	CreatedAt           time.Time
	LastActive          time.Time
	coord               *GlobalMemoryCoordinator
}

// NewFlow creates an initialized flow
func NewFlow(id uint32) *Flow {
	now := time.Now()
	coord := GetGlobalCoordinator()
	coord.TryRegisterFlow()

	return &Flow{
		ID:            id,
		State:         FlowStateActive,
		RTO:           NewRTOCalculator(),
		ReorderBuffer: NewReorderStreamBuffer(id),
		CreatedAt:     now,
		LastActive:    now,
		coord:         coord,
	}
}

// Touch updates flow liveness
func (f *Flow) Touch() {
	f.mu.Lock()
	f.LastActive = time.Now()
	f.mu.Unlock()
}

// EnqueueOutbound packages application data into an outbound chunk and records it in retransmission buffer.
func (f *Flow) EnqueueOutbound(data []byte, flags uint16) (*RetransmitChunk, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.State == FlowStateClosed || f.State == FlowStateTimeWait {
		return nil, ErrFlowClosed
	}
	if f.FinSent && (flags&transport.FlagStreamFIN) == 0 {
		return nil, ErrFlowWriteHalfClosed
	}

	offset := f.NextTxOffset
	dataLen := len(data)

	// If FIN flag is present, record its offset
	if (flags & transport.FlagStreamFIN) != 0 {
		f.FinSent = true
		f.FinOffset = offset + uint64(dataLen)
		if f.State == FlowStateActive {
			f.State = FlowStateHalfClosedLocal
		} else if f.State == FlowStateHalfClosedRemote {
			f.State = FlowStateClosing
		}
		// FIN consumes 1 byte in sequence space so its ACK is distinguishable
		f.NextTxOffset = f.FinOffset + 1
	} else {
		f.NextTxOffset += uint64(dataLen)
	}

	chunk := &RetransmitChunk{
		Offset:      offset,
		Flags:       flags,
		Data:        append([]byte(nil), data...),
		FirstSentAt: time.Now(),
		LastSentAt:  time.Now(),
		RetryCount:  0,
	}

	f.RetransmitQueue = append(f.RetransmitQueue, chunk)
	f.LastActive = time.Now()
	return chunk, nil
}

// ProcessAck processes incoming cumulative ACK and SACK blocks.
// Returns fastRetransmitChunk if 3 duplicate ACKs were detected.
func (f *Flow) ProcessAck(cumAck uint64, sackBlocks []transport.SackBlock) (fastRetransmitChunk *RetransmitChunk, entersTimeWait bool) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.LastActive = time.Now()

	// 1. Stale ACK check
	if cumAck < f.LastCumAck {
		return nil, false
	}

	// 2. Duplicate ACK check
	if cumAck == f.LastCumAck && len(f.RetransmitQueue) > 0 {
		f.DuplicateAckCount++
		if f.DuplicateAckCount == FastRetransmitThreshold {
			// Find the first unacknowledged chunk for Fast Retransmit
			for _, chunk := range f.RetransmitQueue {
				if !chunk.AckedBySack {
					chunk.RetryCount++
					chunk.LastSentAt = time.Now()
					fastRetransmitChunk = chunk
					break
				}
			}
		}
	} else if cumAck > f.LastCumAck {
		// 3. New ACK advances cumulative offset
		f.LastCumAck = cumAck
		f.DuplicateAckCount = 0

		// Check if FIN is acknowledged
		if f.FinSent && cumAck > f.FinOffset {
			f.FinAcked = true
		}

		// Release fully acknowledged chunks from queue
		var surviving []*RetransmitChunk
		for _, chunk := range f.RetransmitQueue {
			chunkEnd := chunk.Offset + uint64(len(chunk.Data))
			if (chunk.Flags & transport.FlagStreamFIN) != 0 {
				chunkEnd = f.FinOffset + 1
			}

			if chunkEnd <= cumAck {
				// Sample RTT for non-retransmitted chunk (Karn's algorithm)
				if chunk.RetryCount == 0 {
					f.RTO.RecordSample(time.Since(chunk.FirstSentAt))
				}
			} else {
				surviving = append(surviving, chunk)
			}
		}
		f.RetransmitQueue = surviving
	}

	// 4. Update SACK ranges on surviving chunks
	for _, chunk := range f.RetransmitQueue {
		chunkStart := chunk.Offset
		chunkEnd := chunk.Offset + uint64(len(chunk.Data))

		for _, block := range sackBlocks {
			if chunkStart >= block.StartOffset && chunkEnd <= block.EndOffset {
				chunk.AckedBySack = true
				break
			}
		}
	}

	// 5. Evaluate state transitions to TIME_WAIT
	if f.FinSent && f.FinAcked && f.RemoteFinDelivered && f.State != FlowStateTimeWait && f.State != FlowStateClosed {
		f.State = FlowStateTimeWait
		f.TimeWaitExpiry = time.Now().Add(FlowTimeWaitDuration)
		entersTimeWait = true
	}

	return fastRetransmitChunk, entersTimeWait
}

// MarkRemoteFinDelivered records that the remote end has sent FIN and it was delivered to local TCP socket
func (f *Flow) MarkRemoteFinDelivered() bool {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.RemoteFinDelivered = true
	if f.State == FlowStateActive {
		f.State = FlowStateHalfClosedRemote
	} else if f.State == FlowStateHalfClosedLocal {
		f.State = FlowStateClosing
	}

	// If local FIN was already ACKed, enter TIME_WAIT
	if f.FinSent && f.FinAcked && f.State != FlowStateTimeWait && f.State != FlowStateClosed {
		f.State = FlowStateTimeWait
		f.TimeWaitExpiry = time.Now().Add(FlowTimeWaitDuration)
		return true
	}
	return false
}

// GetTimedOutChunks returns any chunks whose RTO has expired and increments their retry counts
func (f *Flow) GetTimedOutChunks() ([]*RetransmitChunk, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.State == FlowStateClosed || f.State == FlowStateTimeWait {
		return nil, nil
	}

	now := time.Now()
	currentRTO := f.RTO.CurrentRTO()
	var timedOut []*RetransmitChunk

	for _, chunk := range f.RetransmitQueue {
		if chunk.AckedBySack {
			continue // Suppress timer retransmission for chunks received out-of-order
		}

		if now.Sub(chunk.LastSentAt) >= currentRTO {
			chunk.RetryCount++
			if chunk.RetryCount > MaxRetriesPerChunk || now.Sub(chunk.FirstSentAt) > MaxRetryDuration {
				f.State = FlowStateClosed
				return nil, ErrMaxRetriesExceeded
			}
			chunk.LastSentAt = now
			timedOut = append(timedOut, chunk)
		}
	}

	if len(timedOut) > 0 {
		f.RTO.Backoff()
	}

	return timedOut, nil
}

// Close deallocates flow buffers and unregisters from global coordinator
func (f *Flow) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.State == FlowStateClosed {
		return
	}
	f.State = FlowStateClosed
	f.RetransmitQueue = nil
	f.ReorderBuffer.Close()
	f.coord.UnregisterFlow()
}

// FlowTable manages concurrent flows and safe FlowID allocation
type FlowTable struct {
	mu         sync.RWMutex
	flows      map[uint32]*Flow
	nextFlowID atomic.Uint32
}

// NewFlowTable instantiates a flow table
func NewFlowTable() *FlowTable {
	ft := &FlowTable{
		flows: make(map[uint32]*Flow),
	}
	ft.nextFlowID.Store(1)
	return ft
}

// AllocateFlow creates and registers a fresh flow
func (ft *FlowTable) AllocateFlow() (*Flow, error) {
	ft.mu.Lock()
	defer ft.mu.Unlock()

	if len(ft.flows) >= MaxFlowsPerSession {
		return nil, ErrFlowLimitExceeded
	}

	for attempts := 0; attempts < 10000; attempts++ {
		id := ft.nextFlowID.Add(1)
		if id == 0 {
			id = ft.nextFlowID.Add(1)
		}
		if _, exists := ft.flows[id]; !exists {
			flow := NewFlow(id)
			ft.flows[id] = flow
			return flow, nil
		}
	}

	return nil, ErrFlowLimitExceeded
}

// RegisterWithID registers a flow with a specific ID (used by Responder / Relay)
func (ft *FlowTable) RegisterWithID(id uint32) (*Flow, error) {
	ft.mu.Lock()
	defer ft.mu.Unlock()

	if len(ft.flows) >= MaxFlowsPerSession {
		return nil, ErrFlowLimitExceeded
	}

	if existing, exists := ft.flows[id]; exists {
		return existing, nil
	}

	flow := NewFlow(id)
	ft.flows[id] = flow
	return flow, nil
}

// Get returns an active flow
func (ft *FlowTable) Get(id uint32) (*Flow, bool) {
	ft.mu.RLock()
	defer ft.mu.RUnlock()

	f, ok := ft.flows[id]
	return f, ok
}

// Remove cleans up a flow
func (ft *FlowTable) Remove(id uint32) {
	ft.mu.Lock()
	f, ok := ft.flows[id]
	if ok {
		delete(ft.flows, id)
	}
	ft.mu.Unlock()

	if ok {
		f.Close()
	}
}

// CleanupStale checks for expired TIME_WAIT flows and closed connections
func (ft *FlowTable) CleanupStale() int {
	ft.mu.Lock()
	now := time.Now()
	var toRemove []uint32

	for id, f := range ft.flows {
		f.mu.Lock()
		if f.State == FlowStateTimeWait && now.After(f.TimeWaitExpiry) {
			toRemove = append(toRemove, id)
		} else if f.State == FlowStateClosed {
			toRemove = append(toRemove, id)
		}
		f.mu.Unlock()
	}

	for _, id := range toRemove {
		if f, ok := ft.flows[id]; ok {
			delete(ft.flows, id)
			f.Close()
		}
	}
	ft.mu.Unlock()

	return len(toRemove)
}

