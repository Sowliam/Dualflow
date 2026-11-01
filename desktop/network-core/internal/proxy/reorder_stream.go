package proxy

import (
	"bytes"
	"sort"
	"sync"

	"dualflow-core/internal/transport"
)

// ReorderStreamBuffer provides per-flow zero-skip TCP stream reassembly with strict backpressure
type ReorderStreamBuffer struct {
	mu               sync.Mutex
	flowID           uint32
	contiguousOffset uint64
	chunks           map[uint64][]byte // key: start byte offset
	allocatedBytes   int64
	isBackpressured  bool
	finSeen          bool
	finOffset        uint64
	finDelivered     bool
	coord            *GlobalMemoryCoordinator
}

// NewReorderStreamBuffer creates a reassembly buffer for a flow
func NewReorderStreamBuffer(flowID uint32) *ReorderStreamBuffer {
	return &ReorderStreamBuffer{
		flowID: flowID,
		chunks: make(map[uint64][]byte),
		coord:  GetGlobalCoordinator(),
	}
}

// InsertChunk processes an inbound stream chunk, assembling contiguous bytes without skipping.
// Returns readyBytes to be written to the local/remote TCP socket.
// If the chunk carries FlagStreamFIN and all preceding bytes are contiguous, deliversFIN is true.
func (b *ReorderStreamBuffer) InsertChunk(offset uint64, flags uint16, data []byte) (readyBytes []byte, deliversFIN bool, isDuplicate bool, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	dataLen := len(data)

	// Track FIN in sequence space
	if (flags & transport.FlagStreamFIN) != 0 {
		b.finSeen = true
		b.finOffset = offset + uint64(dataLen)
	}

	// 1. Check if entirely duplicate or stale
	if offset+uint64(dataLen) <= b.contiguousOffset {
		// If this is a FIN retransmit for an already completed flow
		if b.finSeen && b.finDelivered && offset+uint64(dataLen) == b.finOffset {
			return nil, false, true, nil
		}
		if dataLen == 0 && b.finSeen && b.contiguousOffset == b.finOffset {
			// Zero-length FIN packet
			b.finDelivered = true
			b.contiguousOffset++
			return nil, true, false, nil
		}
		return nil, false, true, nil
	}

	// 2. Handle partial overlap with already contiguous data
	if offset < b.contiguousOffset {
		trim := b.contiguousOffset - offset
		if trim >= uint64(dataLen) {
			return nil, false, true, nil
		}
		data = data[trim:]
		offset = b.contiguousOffset
		dataLen = len(data)
	}

	// 2.5. Check if this out-of-order chunk was already buffered to prevent memory leakage on duplicate chunks
	if offset > b.contiguousOffset {
		if existing, exists := b.chunks[offset]; exists {
			if len(existing) >= dataLen {
				return nil, false, true, nil
			}
			// Smaller chunk existed; release its memory before admitting the larger chunk
			oldLen := int64(len(existing))
			b.allocatedBytes -= oldLen
			b.coord.Release(oldLen)
			delete(b.chunks, offset)
		}
	}

	// 3. Admission Control & Memory Quota Checks
	if int64(dataLen) > 0 {
		if b.allocatedBytes+int64(dataLen) > MaxMemoryPerFlow {
			return nil, false, false, ErrFlowMemoryExceeded
		}
		if !b.coord.TryAcquire(int64(dataLen)) {
			return nil, false, false, ErrGlobalMemoryExceeded
		}
	}

	// 4. Ingest data
	if offset == b.contiguousOffset {
		// Immediate contiguous data delivery
		var outBuf bytes.Buffer
		if dataLen > 0 {
			outBuf.Write(data)
			b.contiguousOffset += uint64(dataLen)
			// Released immediately since it's delivered directly to the socket
			b.coord.Release(int64(dataLen))
		}

		// Drain any buffered out-of-order chunks that now connect contiguously
		for {
			nextChunk, exists := b.chunks[b.contiguousOffset]
			if !exists {
				break
			}
			delete(b.chunks, b.contiguousOffset)
			chunkLen := int64(len(nextChunk))
			outBuf.Write(nextChunk)
			b.contiguousOffset += uint64(chunkLen)

			// Release memory from queue
			b.allocatedBytes -= chunkLen
			b.coord.Release(chunkLen)
		}

		// Check if FIN is reached
		if b.finSeen && !b.finDelivered && b.contiguousOffset == b.finOffset {
			b.finDelivered = true
			b.contiguousOffset++ // FIN consumes 1 sequence byte space so ACK confirms FIN
			deliversFIN = true
		}

		// Update backpressure state
		if b.allocatedBytes < LowWatermarkPerFlow {
			b.isBackpressured = false
		}

		return outBuf.Bytes(), deliversFIN, false, nil
	}

	// 5. Out of order: store in reassembly queue
	if dataLen > 0 {
		dataCopy := make([]byte, dataLen)
		copy(dataCopy, data)
		b.chunks[offset] = dataCopy
		b.allocatedBytes += int64(dataLen)

		// Check High Watermark for backpressure
		if b.allocatedBytes >= HighWatermarkPerFlow {
			b.isBackpressured = true
		}
	}

	return nil, false, false, nil
}

// GetCumulativeAck returns the current highest contiguous acknowledged byte offset
func (b *ReorderStreamBuffer) GetCumulativeAck() uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.contiguousOffset
}

// GetSackBlocks calculates up to 8 disjoint half-open SACK ranges [Start, End) for buffered out-of-order data
func (b *ReorderStreamBuffer) GetSackBlocks() []transport.SackBlock {
	b.mu.Lock()
	defer b.mu.Unlock()

	if len(b.chunks) == 0 {
		return nil
	}

	// Collect and sort offsets
	offsets := make([]uint64, 0, len(b.chunks))
	for off := range b.chunks {
		offsets = append(offsets, off)
	}
	sort.Slice(offsets, func(i, j int) bool {
		return offsets[i] < offsets[j]
	})

	var blocks []transport.SackBlock
	var currentStart, currentEnd uint64

	for _, off := range offsets {
		chunkLen := uint64(len(b.chunks[off]))
		end := off + chunkLen

		if currentEnd == 0 {
			currentStart = off
			currentEnd = end
		} else if off <= currentEnd {
			// Overlapping or contiguous chunk: extend
			if end > currentEnd {
				currentEnd = end
			}
		} else {
			// Disjoint gap: finish current block
			blocks = append(blocks, transport.SackBlock{
				StartOffset: currentStart,
				EndOffset:   currentEnd,
			})
			if len(blocks) >= transport.MaxSackBlocks {
				return blocks
			}
			currentStart = off
			currentEnd = end
		}
	}

	if currentEnd > 0 && len(blocks) < transport.MaxSackBlocks {
		blocks = append(blocks, transport.SackBlock{
			StartOffset: currentStart,
			EndOffset:   currentEnd,
		})
	}

	return blocks
}

// IsBackpressured returns true if reassembly queue exceeds 80% (1.6 MB)
func (b *ReorderStreamBuffer) IsBackpressured() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.isBackpressured
}

// AllocatedBytes returns memory currently consumed by out-of-order chunks
func (b *ReorderStreamBuffer) AllocatedBytes() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.allocatedBytes
}

// Close frees all buffered chunks and restores global memory
func (b *ReorderStreamBuffer) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.allocatedBytes > 0 {
		b.coord.Release(b.allocatedBytes)
		b.allocatedBytes = 0
	}
	b.chunks = make(map[uint64][]byte)
}

