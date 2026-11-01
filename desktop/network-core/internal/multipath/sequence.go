package multipath

import (
	"sync/atomic"
)

// SequenceGenerator generates monotonic 64-bit packet sequence IDs
type SequenceGenerator struct {
	current uint64
}

// NewSequenceGenerator creates a generator starting at a specified initial sequence number
func NewSequenceGenerator(initial uint64) *SequenceGenerator {
	return &SequenceGenerator{
		current: initial,
	}
}

// Next atomically returns the next sequence number in monotonic progression
func (s *SequenceGenerator) Next() uint64 {
	return atomic.AddUint64(&s.current, 1)
}

// Current returns the last assigned sequence number
func (s *SequenceGenerator) Current() uint64 {
	return atomic.LoadUint64(&s.current)
}

// SequenceDistance computes the signed difference (a - b) respecting 64-bit integer wraparound.
// A positive result indicates that sequence 'a' is newer than sequence 'b'.
func SequenceDistance(a, b uint64) int64 {
	return int64(a - b)
}

// IsNewerSequence returns true if sequence 'a' is considered newer than sequence 'b'
func IsNewerSequence(a, b uint64) bool {
	return int64(a-b) > 0
}

