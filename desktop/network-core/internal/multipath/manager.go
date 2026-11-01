package multipath

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"dualflow-core/internal/diagnostics"
	"dualflow-core/internal/packet"
	"dualflow-core/internal/transport"
)

// Engine coordinates multi-path packet multiplexing, interface-bound transport, and reordering
type Engine struct {
	mu           sync.RWMutex
	cfg          Config
	paths        map[string]*Path
	pathsList    []*Path
	scheduler    PacketScheduler
	seqGen       *SequenceGenerator
	dedup        *DedupFilter
	reorder      *ReorderBuffer
	prober       *PathProber
	stats        *AtomicStats
	reflector    *LocalReflector
	sessionID    uint32
	isRunning    bool
	isTestMode   bool
	inboundSink  func(rawPacket []byte)
	ctx          context.Context
	cancel       context.CancelFunc
	workerWg     sync.WaitGroup
	tickerChan   chan struct{}
}

// NewEngine instantiates a multi-path bonding engine
func NewEngine(cfg Config, sessionID uint32) *Engine {
	if sessionID == 0 {
		sessionID = 0x44465450 // 'DFTP'
	}

	seq := NewSequenceGenerator(1)
	dedup := NewDedupFilter(cfg.DedupWindowSize)
	reorder := NewReorderBuffer(cfg.ReorderCapacity, cfg.ReorderTimeout)
	prober := NewPathProber(cfg.ProbeInterval, cfg.ProbeTimeout)
	stats := NewAtomicStats()

	e := &Engine{
		cfg:        cfg,
		paths:      make(map[string]*Path),
		pathsList:  make([]*Path, 0),
		seqGen:     seq,
		dedup:      dedup,
		reorder:    reorder,
		prober:     prober,
		stats:      stats,
		sessionID:  sessionID,
		isTestMode: true,
	}

	// Initialize scheduler
	e.scheduler = e.createScheduler(cfg.SchedulerMode)

	// Hook prober metrics back into scheduler
	prober.SetMetricHook(func(pathID string, rtt time.Duration, loss float64) {
		e.mu.RLock()
		sched := e.scheduler
		e.mu.RUnlock()
		if sched != nil {
			sched.UpdateMetrics(pathID, rtt, loss)
		}
	})

	return e
}

// SetInboundSink registers the destination callback for delivered inbound packets
func (e *Engine) SetInboundSink(sink func(rawPacket []byte)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.inboundSink = sink
}

// SetTestMode sets whether the engine operates in safe in-memory test or live socket mode
func (e *Engine) SetTestMode(enabled bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.isTestMode = enabled
}

// IsAvailable implements packet.OutboundHandler
func (e *Engine) IsAvailable() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.isRunning
}

// AddOrUpdatePath registers or updates a network path in the engine
func (e *Engine) AddOrUpdatePath(p *Path) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	existing, ok := e.paths[p.ID]
	if ok {
		// Update fields
		existing.IfaceIndex = p.IfaceIndex
		existing.IfaceName = p.IfaceName
		existing.Description = p.Description
		existing.LocalIP = p.LocalIP
		existing.Gateway = p.Gateway
		existing.SetState(p.GetState())
		if e.isRunning && existing.socket != nil && !existing.socket.IsOpen() {
			_ = existing.socket.Open()
		}
		return nil
	}

	// New Path: Create associated bound UDP socket if not already provided
	if p.socket == nil {
		sock := NewPathSocket(p.ID, p.LocalIP, p.IfaceIndex, p.IfaceName)
		p.socket = sock
	}

	// Hook socket incoming data
	if p.socket != nil {
		p.socket.SetInboundSink(func(data []byte, from *net.UDPAddr) {
			e.handleInboundDatagram(data, p)
		})

		if e.isRunning && !p.socket.IsOpen() {
			_ = p.socket.Open()
		}
	}

	e.paths[p.ID] = p
	e.rebuildPathsListLocked()

	return nil
}

// Start boots all path sockets, probers, and background reorder timers
func (e *Engine) Start(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.isRunning {
		return nil
	}

	e.ctx, e.cancel = context.WithCancel(ctx)
	e.isRunning = true

	// If local reflector is enabled and needed for testing, start one
	if e.cfg.EnableLocalReflector && e.reflector == nil {
		ref, err := NewLocalReflector("127.0.0.1")
		if err == nil {
			e.reflector = ref
			e.prober.SetTargetEndpoint(ref.Addr())
		} else {
			diagnostics.Warn("Failed to start local reflector: %v", err)
		}
	}

	// Open all path sockets
	for _, p := range e.pathsList {
		if p.socket != nil && !p.socket.IsOpen() {
			if err := p.socket.Open(); err != nil {
				diagnostics.Warn("Failed to open PathSocket for %s (%s): %v", p.ID, p.LocalIP, err)
				p.SetState(StateError)
			} else {
				p.SetState(StateAvailable)
			}
		}
	}

	// Start prober
	_ = e.prober.Start(e.pathsList)

	// Launch reorder timer flush worker
	e.workerWg.Add(1)
	go e.reorderFlushWorker(e.ctx)

	return nil
}

// Stop shuts down all sockets, probers, and workers cleanly
func (e *Engine) Stop() error {
	e.mu.Lock()
	if !e.isRunning {
		e.mu.Unlock()
		return nil
	}

	e.isRunning = false
	if e.cancel != nil {
		e.cancel()
	}

	// Close sockets
	for _, p := range e.pathsList {
		if p.socket != nil {
			_ = p.socket.Close()
		}
		p.SetState(StateDisconnected)
	}

	// Close prober & reflector
	e.prober.Stop()
	if e.reflector != nil {
		e.reflector.Close()
		e.reflector = nil
	}

	e.mu.Unlock()
	e.workerWg.Wait()

	return nil
}

// SendPacket implements packet.OutboundHandler
// Encapsulates the IP packet into DFTP frame, schedules paths, and transmits
func (e *Engine) SendPacket(pkt *packet.Packet) error {
	e.mu.RLock()
	isRunning := e.isRunning
	sched := e.scheduler
	pathsList := e.pathsList
	sessionID := e.sessionID
	e.mu.RUnlock()

	if !isRunning {
		e.stats.IncFailedSends()
		return errors.New("multi-path bonding engine is not running")
	}

	if len(pathsList) == 0 {
		e.stats.IncFailedSends()
		return ErrNoEligiblePaths
	}

	// 1. Assign single monotonic logical sequence number
	seq := e.seqGen.Next()
	e.stats.IncLogicalPackets()

	// 2. Scheduler selects one or more paths
	targetPaths, err := sched.SelectPaths(pkt, pathsList)
	if err != nil || len(targetPaths) == 0 {
		e.stats.IncFailedSends()
		return fmt.Errorf("scheduler selection failed: %w", err)
	}

	// 3. Encapsulate and transmit to target paths
	sendErrors := 0
	for i, p := range targetPaths {
		// Set duplicate flag if this is a redundant copy beyond the first path
		flags := transport.FlagTestFrame
		if i > 0 {
			flags |= (1 << 4) // FlagDuplicate
		}

		frame, err := transport.EncodeFrame(transport.MsgTypeData, flags, sessionID, seq, pkt.Raw)
		if err != nil {
			sendErrors++
			p.RecordDrop()
			continue
		}

		// Transmit through bound socket
		if p.socket != nil && p.socket.IsOpen() {
			if err := p.socket.Send(frame, nil); err != nil {
				sendErrors++
				p.RecordDrop()
			} else {
				p.RecordTx(len(frame))
				e.stats.IncSuccessfulSends()
			}
		} else {
			sendErrors++
			p.RecordDrop()
		}
	}

	if sendErrors == len(targetPaths) {
		e.stats.IncFailedSends()
		return errors.New("failed to transmit packet across any scheduled path")
	}

	return nil
}

// handleInboundDatagram processes incoming packets from a path socket
func (e *Engine) handleInboundDatagram(data []byte, p *Path) {
	p.RecordRx(len(data))

	// 1. Check if this is a probe response
	if len(data) >= ProbePacketSize && data[0] == ProbeMagic[0] && data[1] == ProbeMagic[1] {
		if e.prober.HandleProbeReply(data, p) {
			return
		}
	}

	// 2. Decode DFTP Frame
	frame, err := transport.DecodeFrame(data)
	if err != nil {
		p.RecordDrop()
		return
	}

	// 3. Deduplication Check
	if e.dedup.CheckAndRecord(frame.Header.SequenceNumber) {
		e.stats.IncDuplicatesDropped()
		return // Duplicate suppressed!
	}

	// 4. Ingress Reorder Buffer
	readyPackets := e.reorder.Insert(frame.Header.SequenceNumber, frame.Payload, p.ID)

	// 5. Deliver consecutive ordered packets to sink
	if len(readyPackets) > 0 {
		e.mu.RLock()
		sink := e.inboundSink
		e.mu.RUnlock()

		if sink != nil {
			for _, pktData := range readyPackets {
				e.stats.IncReorderedDelivered()
				sink(pktData)
			}
		}
	}
}

// reorderFlushWorker periodically triggers reorder buffer timeout release
func (e *Engine) reorderFlushWorker(ctx context.Context) {
	defer e.workerWg.Done()

	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			ready := e.reorder.CheckTimeout()
			if len(ready) > 0 {
				e.mu.RLock()
				sink := e.inboundSink
				e.mu.RUnlock()

				if sink != nil {
					for _, pktData := range ready {
						e.stats.IncTimedOutPackets()
						sink(pktData)
					}
				}
			}
		}
	}
}

// SetSchedulerMode dynamically switches the distribution algorithm at runtime
func (e *Engine) SetSchedulerMode(mode SchedulerMode) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	sched := e.createScheduler(mode)
	e.scheduler = sched
	e.cfg.SchedulerMode = mode

	diagnostics.Info("Multi-Path Engine active scheduler switched to: %s", mode)
	return nil
}

// SetWeights updates path weights for weighted schedulers
func (e *Engine) SetWeights(weights map[string]uint32) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.cfg.Weights = weights
	if e.scheduler != nil {
		return e.scheduler.SetWeights(weights)
	}
	return nil
}

// GetPaths returns a snapshot slice of all registered paths
func (e *Engine) GetPaths() []*Path {
	e.mu.RLock()
	defer e.mu.RUnlock()
	res := make([]*Path, len(e.pathsList))
	copy(res, e.pathsList)
	return res
}

// GetStatus returns the current engine state snapshot
func (e *Engine) GetStatus() map[string]any {
	e.mu.RLock()
	defer e.mu.RUnlock()

	pathSnaps := make([]PathSnapshot, 0, len(e.pathsList))
	for _, p := range e.pathsList {
		pathSnaps = append(pathSnaps, p.Snapshot())
	}

	reordered, droppedLate, timedOut, qDepth := e.reorder.Stats()

	engineState := "STOPPED"
	if e.isRunning {
		engineState = "RUNNING"
		eligibleCount := 0
		for _, p := range e.pathsList {
			if p.IsEligible() {
				eligibleCount++
			}
		}
		if eligibleCount == 0 {
			engineState = "DEGRADED"
		}
	}

	return map[string]any{
		"state":                engineState,
		"scheduler_mode":       e.cfg.SchedulerMode,
		"paths":                pathSnaps,
		"paths_count":          len(e.pathsList),
		"stats":                e.stats.Snapshot(e.pathsList),
		"reorder_queue_depth":  qDepth,
		"reorder_reordered":    reordered,
		"reorder_dropped_late": droppedLate,
		"reorder_timed_out":    timedOut,
		"is_test_mode":         e.isTestMode,
	}
}

func (e *Engine) createScheduler(mode SchedulerMode) PacketScheduler {
	switch mode {
	case ModeWeightedRR:
		return NewWeightedRRScheduler(e.cfg.Weights)
	case ModeLowestLatency:
		return NewLowestLatencyScheduler(e.cfg.LatencyHysteresisMs)
	case ModeRedundant:
		return NewRedundantScheduler()
	case ModeRoundRobin:
		fallthrough
	default:
		return NewRoundRobinScheduler()
	}
}

func (e *Engine) rebuildPathsListLocked() {
	e.pathsList = make([]*Path, 0, len(e.paths))
	for _, p := range e.paths {
		e.pathsList = append(e.pathsList, p)
	}
}
