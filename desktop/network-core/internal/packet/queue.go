package packet

import (
	"context"
	"sync"
)

// BoundedQueue provides a thread-safe, bounded FIFO packet channel with drop-tail handling
type BoundedQueue struct {
	ch     chan *Packet
	closed bool
	mu     sync.RWMutex
}

// NewBoundedQueue creates a queue with a maximum capacity
func NewBoundedQueue(capacity int) *BoundedQueue {
	if capacity <= 0 {
		capacity = 1024
	}
	return &BoundedQueue{
		ch: make(chan *Packet, capacity),
	}
}

// Push attempts to add a packet to the queue without blocking.
// Returns true if successfully queued, or false if the queue is full or closed.
func (q *BoundedQueue) Push(pkt *Packet) bool {
	q.mu.RLock()
	defer q.mu.RUnlock()

	if q.closed {
		return false
	}

	select {
	case q.ch <- pkt:
		return true
	default:
		// Queue full (drop-tail)
		return false
	}
}

// Pop waits for a packet or until the context is canceled.
func (q *BoundedQueue) Pop(ctx context.Context) (*Packet, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case pkt, ok := <-q.ch:
		if !ok {
			return nil, ErrQueueClosed
		}
		return pkt, nil
	}
}

// Close gracefully closes the queue channel
func (q *BoundedQueue) Close() {
	q.mu.Lock()
	defer q.mu.Unlock()

	if !q.closed {
		q.closed = true
		close(q.ch)
	}
}

// Len returns the current number of buffered packets
func (q *BoundedQueue) Len() int {
	return len(q.ch)
}

// Cap returns the maximum capacity of the queue
func (q *BoundedQueue) Cap() int {
	return cap(q.ch)
}

