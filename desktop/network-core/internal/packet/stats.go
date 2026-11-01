package packet

import (
	"sync"
	"sync/atomic"
	"time"
)

// PipelineStats tracks real packet lifecycle counters with drop breakdown
type PipelineStats struct {
	mu sync.RWMutex

	// Atomic primary counters
	ValidatedPackets uint64
	RejectedPackets  uint64
	QueuedPackets    uint64
	ProcessedBytes   uint64

	// Atomic drop breakdown counters
	DropsInvalidPacket        uint64
	DropsQueueFull            uint64
	DropsTransportUnavailable uint64
	DropsTransportWriteFailed uint64
	DropsWintunSendFailed     uint64
	DropsSessionClosed        uint64

	// Contextual drop tracking
	LastDropReason string
	LastDropTime   int64
}

// StatsSnapshot is the JSON-serializable snapshot of pipeline statistics
type StatsSnapshot struct {
	ValidatedPackets          uint64 `json:"validated_packets"`
	RejectedPackets           uint64 `json:"rejected_packets"`
	QueuedPackets             uint64 `json:"queued_packets"`
	ProcessedBytes            uint64 `json:"processed_bytes"`
	TotalDrops                uint64 `json:"total_drops"`
	DropsInvalidPacket        uint64 `json:"drops_invalid_packet"`
	DropsQueueFull            uint64 `json:"drops_queue_full"`
	DropsTransportUnavailable uint64 `json:"drops_transport_unavailable"`
	DropsTransportWriteFailed uint64 `json:"drops_transport_write_failed"`
	DropsWintunSendFailed     uint64 `json:"drops_wintun_send_failed"`
	DropsSessionClosed        uint64 `json:"drops_session_closed"`
	LastDropReason            string `json:"last_drop_reason,omitempty"`
	LastDropTime              int64  `json:"last_drop_time,omitempty"`
}

// NewPipelineStats initializes a new statistics tracker
func NewPipelineStats() *PipelineStats {
	return &PipelineStats{}
}

// RecordValidated increments validated packet and byte counters
func (s *PipelineStats) RecordValidated(bytes int) {
	atomic.AddUint64(&s.ValidatedPackets, 1)
	atomic.AddUint64(&s.ProcessedBytes, uint64(bytes))
}

// RecordRejected increments rejected packet counter
func (s *PipelineStats) RecordRejected() {
	atomic.AddUint64(&s.RejectedPackets, 1)
}

// RecordQueued increments queued packet counter
func (s *PipelineStats) RecordQueued() {
	atomic.AddUint64(&s.QueuedPackets, 1)
}

// RecordDrop tracks a dropped packet by its exact reason
func (s *PipelineStats) RecordDrop(reason DropReason) {
	switch reason {
	case DropInvalidPacket:
		atomic.AddUint64(&s.DropsInvalidPacket, 1)
	case DropQueueFull:
		atomic.AddUint64(&s.DropsQueueFull, 1)
	case DropTransportUnavailable:
		atomic.AddUint64(&s.DropsTransportUnavailable, 1)
	case DropTransportWriteFailed:
		atomic.AddUint64(&s.DropsTransportWriteFailed, 1)
	case DropWintunSendFailed:
		atomic.AddUint64(&s.DropsWintunSendFailed, 1)
	case DropSessionClosed:
		atomic.AddUint64(&s.DropsSessionClosed, 1)
	}

	s.mu.Lock()
	s.LastDropReason = string(reason)
	s.LastDropTime = time.Now().UnixMilli()
	s.mu.Unlock()
}

// Snapshot returns a copy of current metrics for IPC / telemetry
func (s *PipelineStats) Snapshot() StatsSnapshot {
	s.mu.RLock()
	lastReason := s.LastDropReason
	lastTime := s.LastDropTime
	s.mu.RUnlock()

	dInv := atomic.LoadUint64(&s.DropsInvalidPacket)
	dQFull := atomic.LoadUint64(&s.DropsQueueFull)
	dTU := atomic.LoadUint64(&s.DropsTransportUnavailable)
	dTW := atomic.LoadUint64(&s.DropsTransportWriteFailed)
	dWS := atomic.LoadUint64(&s.DropsWintunSendFailed)
	dSC := atomic.LoadUint64(&s.DropsSessionClosed)
	totalDrops := dInv + dQFull + dTU + dTW + dWS + dSC

	return StatsSnapshot{
		ValidatedPackets:          atomic.LoadUint64(&s.ValidatedPackets),
		RejectedPackets:           atomic.LoadUint64(&s.RejectedPackets),
		QueuedPackets:             atomic.LoadUint64(&s.QueuedPackets),
		ProcessedBytes:            atomic.LoadUint64(&s.ProcessedBytes),
		TotalDrops:                totalDrops,
		DropsInvalidPacket:        dInv,
		DropsQueueFull:            dQFull,
		DropsTransportUnavailable: dTU,
		DropsTransportWriteFailed: dTW,
		DropsWintunSendFailed:     dWS,
		DropsSessionClosed:        dSC,
		LastDropReason:            lastReason,
		LastDropTime:              lastTime,
	}
}

// Reset clears all counters to zero
func (s *PipelineStats) Reset() {
	atomic.StoreUint64(&s.ValidatedPackets, 0)
	atomic.StoreUint64(&s.RejectedPackets, 0)
	atomic.StoreUint64(&s.QueuedPackets, 0)
	atomic.StoreUint64(&s.ProcessedBytes, 0)
	atomic.StoreUint64(&s.DropsInvalidPacket, 0)
	atomic.StoreUint64(&s.DropsQueueFull, 0)
	atomic.StoreUint64(&s.DropsTransportUnavailable, 0)
	atomic.StoreUint64(&s.DropsTransportWriteFailed, 0)
	atomic.StoreUint64(&s.DropsWintunSendFailed, 0)
	atomic.StoreUint64(&s.DropsSessionClosed, 0)

	s.mu.Lock()
	s.LastDropReason = ""
	s.LastDropTime = 0
	s.mu.Unlock()
}

