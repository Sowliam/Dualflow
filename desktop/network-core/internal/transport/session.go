package transport

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"dualflow-core/internal/packet"
)

// Transport defines the common contract for all tunnel transports
type Transport interface {
	packet.OutboundHandler

	Start(ctx context.Context) error
	Stop() error
	GetState() TransportState
	GetStats() TransportStats
	IsTest() bool
	SetInboundSink(sink func(rawPacket []byte))
}

// InMemoryTransport provides a deterministic in-memory transport harness for testing.
// It is explicitly flagged as a test transport (IsTest() == true).
type InMemoryTransport struct {
	mu           sync.RWMutex
	state        TransportState
	isTest       bool
	sessionID    uint32
	seqCounter   uint64
	inboundSink  func(rawPacket []byte)
	ctx          context.Context
	cancel       context.CancelFunc
	decoder      *StreamDecoder

	// Atomic Telemetry Counters
	txPackets      uint64
	rxPackets      uint64
	txBytes        uint64
	rxBytes        uint64
	droppedPackets uint64
	lastError      string

	// Test settings
	loopbackEnabled bool
	simulatedDelay  time.Duration
}

// NewInMemoryTransport creates a safe in-memory test transport
func NewInMemoryTransport(sessionID uint32, loopback bool) *InMemoryTransport {
	return &InMemoryTransport{
		state:           StateDisconnected,
		isTest:          true, // STRICTLY TEST ONLY
		sessionID:       sessionID,
		decoder:         NewStreamDecoder(),
		loopbackEnabled: loopback,
	}
}

// SetInboundSink sets the receiver for inbound packets coming from the transport tunnel
func (t *InMemoryTransport) SetInboundSink(sink func(rawPacket []byte)) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.inboundSink = sink
}

// IsTest returns true, indicating this transport does not interact with external networks
func (t *InMemoryTransport) IsTest() bool {
	return true
}

// IsAvailable returns true if the in-memory transport is started
func (t *InMemoryTransport) IsAvailable() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.state == StateConnected
}

// GetState returns the current connection state
func (t *InMemoryTransport) GetState() TransportState {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.state
}

// Start activates the in-memory test transport
func (t *InMemoryTransport) Start(ctx context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.state == StateConnected {
		return nil
	}

	t.ctx, t.cancel = context.WithCancel(ctx)
	t.state = StateConnected
	t.lastError = ""
	return nil
}

// Stop deactivates the in-memory transport
func (t *InMemoryTransport) Stop() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.state == StateDisconnected {
		return nil
	}

	if t.cancel != nil {
		t.cancel()
	}
	t.state = StateDisconnected
	return nil
}

// SendPacket encodes an IP packet into a DFTP frame and transmits it across the transport
func (t *InMemoryTransport) SendPacket(pkt *packet.Packet) error {
	t.mu.RLock()
	state := t.state
	sink := t.inboundSink
	loopback := t.loopbackEnabled
	delay := t.simulatedDelay
	t.mu.RUnlock()

	if state != StateConnected {
		atomic.AddUint64(&t.droppedPackets, 1)
		return errors.New("transport is not connected")
	}

	seq := atomic.AddUint64(&t.seqCounter, 1)

	// Encode frame with checksum
	frameBytes, err := EncodeFrame(MsgTypeData, FlagTestFrame, t.sessionID, seq, pkt.Raw)
	if err != nil {
		atomic.AddUint64(&t.droppedPackets, 1)
		t.mu.Lock()
		t.lastError = err.Error()
		t.mu.Unlock()
		return err
	}

	atomic.AddUint64(&t.txPackets, 1)
	atomic.AddUint64(&t.txBytes, uint64(len(frameBytes)))

	// In test mode with loopback, simulate receiver decode and delivery
	if loopback && sink != nil {
		go func(rawFrame []byte) {
			if delay > 0 {
				time.Sleep(delay)
			}

			// Verify stream decode
			frames, err := t.decoder.Feed(rawFrame)
			if err != nil || len(frames) == 0 {
				atomic.AddUint64(&t.droppedPackets, 1)
				return
			}

			for _, f := range frames {
				atomic.AddUint64(&t.rxPackets, 1)
				atomic.AddUint64(&t.rxBytes, uint64(len(f.Payload)))
				sink(f.Payload)
			}
		}(frameBytes)
	}

	return nil
}

// GetStats returns atomic telemetry counters
func (t *InMemoryTransport) GetStats() TransportStats {
	t.mu.RLock()
	state := t.state
	lastErr := t.lastError
	t.mu.RUnlock()

	return TransportStats{
		State:          state,
		IsTest:         true,
		TxPackets:      atomic.LoadUint64(&t.txPackets),
		RxPackets:      atomic.LoadUint64(&t.rxPackets),
		TxBytes:        atomic.LoadUint64(&t.txBytes),
		RxBytes:        atomic.LoadUint64(&t.rxBytes),
		DroppedPackets: atomic.LoadUint64(&t.droppedPackets),
		LastError:      lastErr,
	}
}

// ResetStats resets transport counters
func (t *InMemoryTransport) ResetStats() {
	atomic.StoreUint64(&t.txPackets, 0)
	atomic.StoreUint64(&t.rxPackets, 0)
	atomic.StoreUint64(&t.txBytes, 0)
	atomic.StoreUint64(&t.rxBytes, 0)
	atomic.StoreUint64(&t.droppedPackets, 0)

	t.mu.Lock()
	t.lastError = ""
	t.mu.Unlock()
}

