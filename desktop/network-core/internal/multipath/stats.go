package multipath

import (
	"sync/atomic"
)

// EngineStats holds atomic counters for the multi-path bonding pipeline
type EngineStats struct {
	TotalLogicalPackets uint64            `json:"total_logical_packets"`
	PathTxPackets       map[string]uint64 `json:"path_tx_packets"`
	PathRxPackets       map[string]uint64 `json:"path_rx_packets"`
	SuccessfulSends     uint64            `json:"successful_sends"`
	FailedSends         uint64            `json:"failed_sends"`
	DuplicatesDropped   uint64            `json:"duplicates_dropped"`
	ReorderedDelivered  uint64            `json:"reordered_delivered"`
	LatePacketsDropped  uint64            `json:"late_packets_dropped"`
	TimedOutPackets     uint64            `json:"timed_out_packets"`
	TotalDrops          uint64            `json:"total_drops"`
}

// AtomicStats provides lock-free tracking of engine operations
type AtomicStats struct {
	totalLogicalPackets uint64
	successfulSends     uint64
	failedSends         uint64
	duplicatesDropped   uint64
	reorderedDelivered  uint64
	latePacketsDropped  uint64
	timedOutPackets     uint64
	totalDrops          uint64
}

func NewAtomicStats() *AtomicStats {
	return &AtomicStats{}
}

func (s *AtomicStats) IncLogicalPackets() uint64 {
	return atomic.AddUint64(&s.totalLogicalPackets, 1)
}

func (s *AtomicStats) IncSuccessfulSends() {
	atomic.AddUint64(&s.successfulSends, 1)
}

func (s *AtomicStats) IncFailedSends() {
	atomic.AddUint64(&s.failedSends, 1)
	atomic.AddUint64(&s.totalDrops, 1)
}

func (s *AtomicStats) IncDuplicatesDropped() {
	atomic.AddUint64(&s.duplicatesDropped, 1)
}

func (s *AtomicStats) IncReorderedDelivered() {
	atomic.AddUint64(&s.reorderedDelivered, 1)
}

func (s *AtomicStats) IncLatePacketsDropped() {
	atomic.AddUint64(&s.latePacketsDropped, 1)
	atomic.AddUint64(&s.totalDrops, 1)
}

func (s *AtomicStats) IncTimedOutPackets() {
	atomic.AddUint64(&s.timedOutPackets, 1)
}

func (s *AtomicStats) IncTotalDrops() {
	atomic.AddUint64(&s.totalDrops, 1)
}

func (s *AtomicStats) Snapshot(paths []*Path) EngineStats {
	pathTx := make(map[string]uint64)
	pathRx := make(map[string]uint64)

	for _, p := range paths {
		if p != nil {
			pathTx[p.ID] = atomic.LoadUint64(&p.txPackets)
			pathRx[p.ID] = atomic.LoadUint64(&p.rxPackets)
		}
	}

	return EngineStats{
		TotalLogicalPackets: atomic.LoadUint64(&s.totalLogicalPackets),
		PathTxPackets:       pathTx,
		PathRxPackets:       pathRx,
		SuccessfulSends:     atomic.LoadUint64(&s.successfulSends),
		FailedSends:         atomic.LoadUint64(&s.failedSends),
		DuplicatesDropped:   atomic.LoadUint64(&s.duplicatesDropped),
		ReorderedDelivered:  atomic.LoadUint64(&s.reorderedDelivered),
		LatePacketsDropped:  atomic.LoadUint64(&s.latePacketsDropped),
		TimedOutPackets:     atomic.LoadUint64(&s.timedOutPackets),
		TotalDrops:          atomic.LoadUint64(&s.totalDrops),
	}
}

func (s *AtomicStats) Reset() {
	atomic.StoreUint64(&s.totalLogicalPackets, 0)
	atomic.StoreUint64(&s.successfulSends, 0)
	atomic.StoreUint64(&s.failedSends, 0)
	atomic.StoreUint64(&s.duplicatesDropped, 0)
	atomic.StoreUint64(&s.reorderedDelivered, 0)
	atomic.StoreUint64(&s.latePacketsDropped, 0)
	atomic.StoreUint64(&s.timedOutPackets, 0)
	atomic.StoreUint64(&s.totalDrops, 0)
}

