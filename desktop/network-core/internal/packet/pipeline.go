package packet

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// OutboundHandler delivers validated IP packets to a transport layer
type OutboundHandler interface {
	SendPacket(pkt *Packet) error
	IsAvailable() bool
}

// InboundWintunSink delivers inbound tunnel packets back into Wintun virtual adapter
type InboundWintunSink interface {
	SendPacket(data []byte) error
}

// Pipeline coordinates packet validation, bounded queue buffering, and transport handoff.
type Pipeline struct {
	mu           sync.RWMutex
	queue        *BoundedQueue
	stats        *PipelineStats
	outbound     OutboundHandler
	wintunSink   InboundWintunSink
	ctx          context.Context
	cancel       context.CancelFunc
	workerWg     sync.WaitGroup
	isRunning    bool
	isTestMode   bool
	queueCap     int
}

// NewPipeline creates a new packet processing pipeline
func NewPipeline(queueCapacity int) *Pipeline {
	if queueCapacity <= 0 {
		queueCapacity = 1024
	}
	return &Pipeline{
		queueCap: queueCapacity,
		stats:    NewPipelineStats(),
	}
}

// SetOutboundHandler configures the egress transport sink
func (p *Pipeline) SetOutboundHandler(h OutboundHandler) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.outbound = h
}

// SetWintunSink configures the ingress Wintun delivery sink
func (p *Pipeline) SetWintunSink(sink InboundWintunSink) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.wintunSink = sink
}

// SetTestMode flags whether the pipeline operates in safe in-memory test mode
func (p *Pipeline) SetTestMode(enabled bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.isTestMode = enabled
}

// IsTestMode returns whether test mode is active
func (p *Pipeline) IsTestMode() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.isTestMode
}

// Start initiates worker routines with bounded queues and context cancellation
func (p *Pipeline) Start() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.isRunning {
		return nil
	}

	p.ctx, p.cancel = context.WithCancel(context.Background())
	p.queue = NewBoundedQueue(p.queueCap)
	p.isRunning = true

	// Launch worker pool
	p.workerWg.Add(1)
	go p.workerLoop(p.ctx)

	return nil
}

// Stop gracefully cancels running workers and drains queues
func (p *Pipeline) Stop() error {
	p.mu.Lock()
	if !p.isRunning {
		p.mu.Unlock()
		return nil
	}

	p.isRunning = false
	p.cancel()
	if p.queue != nil {
		p.queue.Close()
	}
	p.mu.Unlock()

	p.workerWg.Wait()
	return nil
}

// IngestRawPacket is the primary entrypoint for raw packets arriving from Wintun or synthetic injector.
func (p *Pipeline) IngestRawPacket(raw []byte) (*Packet, error) {
	p.mu.RLock()
	running := p.isRunning
	p.mu.RUnlock()

	if !running {
		p.stats.RecordDrop(DropSessionClosed)
		return nil, errors.New("packet pipeline is not running")
	}

	// 1. Strict validation
	pkt, err := ValidateIPPacket(raw, false)
	if err != nil {
		p.stats.RecordRejected()
		p.stats.RecordDrop(DropInvalidPacket)
		return nil, fmt.Errorf("packet validation rejected: %w", err)
	}

	p.stats.RecordValidated(len(raw))

	// 2. Enqueue into bounded queue
	if !p.queue.Push(pkt) {
		p.stats.RecordDrop(DropQueueFull)
		return nil, ErrQueueFull
	}

	p.stats.RecordQueued()
	return pkt, nil
}

// IngestTunnelPacket handles packets arriving from the transport tunnel to be written to Wintun
func (p *Pipeline) IngestTunnelPacket(raw []byte) error {
	p.mu.RLock()
	sink := p.wintunSink
	p.mu.RUnlock()

	pkt, err := ValidateIPPacket(raw, false)
	if err != nil {
		p.stats.RecordRejected()
		p.stats.RecordDrop(DropInvalidPacket)
		return fmt.Errorf("inbound tunnel packet invalid: %w", err)
	}

	p.stats.RecordValidated(pkt.TotalLength)

	if sink != nil {
		if err := sink.SendPacket(pkt.Raw); err != nil {
			p.stats.RecordDrop(DropWintunSendFailed)
			return fmt.Errorf("wintun sink send error: %w", err)
		}
	}

	return nil
}

// workerLoop drains the ingress queue and forwards to the outbound transport
func (p *Pipeline) workerLoop(ctx context.Context) {
	defer p.workerWg.Done()

	for {
		pkt, err := p.queue.Pop(ctx)
		if err != nil {
			return
		}

		p.mu.RLock()
		out := p.outbound
		p.mu.RUnlock()

		if out == nil || !out.IsAvailable() {
			p.stats.RecordDrop(DropTransportUnavailable)
			continue
		}

		if err := out.SendPacket(pkt); err != nil {
			p.stats.RecordDrop(DropTransportWriteFailed)
		}
	}
}

// GetStats returns the current atomic statistics snapshot
func (p *Pipeline) GetStats() StatsSnapshot {
	return p.stats.Snapshot()
}

// ResetStats resets atomic statistics
func (p *Pipeline) ResetStats() {
	p.stats.Reset()
}

// QueueDepth returns the current number of buffered items
func (p *Pipeline) QueueDepth() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.queue == nil {
		return 0
	}
	return p.queue.Len()
}

// Status returns a high-level status struct
func (p *Pipeline) Status() map[string]any {
	p.mu.RLock()
	defer p.mu.RUnlock()

	return map[string]any{
		"is_running":   p.isRunning,
		"is_test_mode": p.isTestMode,
		"queue_cap":    p.queueCap,
		"queue_depth":  p.QueueDepth(),
		"stats":        p.stats.Snapshot(),
		"timestamp":    time.Now().UnixMilli(),
	}
}

