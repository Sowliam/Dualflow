package multipath

import (
	"container/heap"
	"sync"
	"time"
)

// ReorderPacket encapsulates a packet queued inside the reordering priority queue
type ReorderPacket struct {
	Sequence  uint64
	Data      []byte
	PathID    string
	Arrival   time.Time
	index     int
}

// packetMinHeap implements container/heap for min-sequence ordering
type packetMinHeap []*ReorderPacket

func (h packetMinHeap) Len() int           { return len(h) }
func (h packetMinHeap) Less(i, j int) bool { return SequenceDistance(h[i].Sequence, h[j].Sequence) < 0 }
func (h packetMinHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i]; h[i].index = i; h[j].index = j }

func (h *packetMinHeap) Push(x any) {
	n := len(*h)
	item := x.(*ReorderPacket)
	item.index = n
	*h = append(*h, item)
}

func (h *packetMinHeap) Pop() any {
	old := *h
	n := len(old)
	item := old[n-1]
	old[n-1] = nil
	item.index = -1
	*h = old[0 : n-1]
	return item
}

// ReorderBuffer coordinates packet reassembly across disparate latency paths
type ReorderBuffer struct {
	mu             sync.Mutex
	capacity       int
	timeout        time.Duration
	expectedSeq    uint64
	initialized    bool
	queue          packetMinHeap
	reorderedCount uint64
	droppedLate    uint64
	timedOutCount  uint64
}

// NewReorderBuffer creates a reorder buffer with bounded capacity and timeout
func NewReorderBuffer(capacity int, timeout time.Duration) *ReorderBuffer {
	if capacity <= 0 {
		capacity = 256
	}
	if timeout <= 0 {
		timeout = 40 * time.Millisecond
	}
	b := &ReorderBuffer{
		capacity: capacity,
		timeout:  timeout,
		queue:    make(packetMinHeap, 0, capacity),
	}
	heap.Init(&b.queue)
	return b
}

// Insert adds an incoming packet and returns any consecutive in-order packets ready for immediate delivery
func (b *ReorderBuffer) Insert(seq uint64, data []byte, pathID string) [][]byte {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := time.Now()

	// Initial baseline sequence setup
	if !b.initialized {
		b.expectedSeq = seq
		b.initialized = true
	}

	diff := SequenceDistance(seq, b.expectedSeq)

	// Case 1: Exactly the expected sequence
	if diff == 0 {
		ready := [][]byte{data}
		b.expectedSeq++

		// Check if subsequent consecutive buffered packets can now be drained
		for b.queue.Len() > 0 {
			top := b.queue[0]
			if SequenceDistance(top.Sequence, b.expectedSeq) == 0 {
				heap.Pop(&b.queue)
				ready = append(ready, top.Data)
				b.expectedSeq++
			} else {
				break
			}
		}
		return ready
	}

	// Case 2: Packet arrived too late (already passed expectedSeq)
	if diff < 0 {
		b.droppedLate++
		return nil
	}

	// Case 3: Out-of-order packet arriving early (seq > expectedSeq)
	b.reorderedCount++

	// Check capacity: if full, evict the oldest or force forward advancement
	if b.queue.Len() >= b.capacity {
		// Drain and advance
		return b.drainTimedOutLocked(now, true)
	}

	// Insert into heap
	item := &ReorderPacket{
		Sequence: seq,
		Data:     data,
		PathID:   pathID,
		Arrival:  now,
	}
	heap.Push(&b.queue, item)

	// Check if any existing items timed out
	return b.drainTimedOutLocked(now, false)
}

// CheckTimeout inspects the buffer for expired packets and advances expectedSeq if needed
func (b *ReorderBuffer) CheckTimeout() [][]byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.drainTimedOutLocked(time.Now(), false)
}

func (b *ReorderBuffer) drainTimedOutLocked(now time.Time, forceAdvance bool) [][]byte {
	var ready [][]byte

	for b.queue.Len() > 0 {
		top := b.queue[0]
		// If oldest packet timed out or force advance is requested
		if forceAdvance || now.Sub(top.Arrival) >= b.timeout {
			b.timedOutCount++
			heap.Pop(&b.queue)
			ready = append(ready, top.Data)
			b.expectedSeq = top.Sequence + 1

			// Drain consecutive following packets
			for b.queue.Len() > 0 {
				next := b.queue[0]
				if SequenceDistance(next.Sequence, b.expectedSeq) == 0 {
					heap.Pop(&b.queue)
					ready = append(ready, next.Data)
					b.expectedSeq++
				} else {
					break
				}
			}
			forceAdvance = false
		} else {
			break
		}
	}

	return ready
}

// Stats returns reorder buffer telemetry
func (b *ReorderBuffer) Stats() (reordered, droppedLate, timedOut uint64, queueDepth int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.reorderedCount, b.droppedLate, b.timedOutCount, b.queue.Len()
}

// Reset clears the reorder buffer
func (b *ReorderBuffer) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.queue = make(packetMinHeap, 0, b.capacity)
	heap.Init(&b.queue)
	b.initialized = false
	b.expectedSeq = 0
}

