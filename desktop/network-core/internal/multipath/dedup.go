package multipath

import (
	"sync"
)

// DedupFilter implements a sliding window bitmap to detect and suppress duplicate packets.
// Particularly crucial for Redundant scheduler mode and multi-path network duplication.
type DedupFilter struct {
	mu         sync.Mutex
	windowSize uint64
	maxSeq     uint64
	bitmap     []uint64
	initialized bool
}

// NewDedupFilter initializes a sliding window filter with the given window size (e.g. 1024 packets)
func NewDedupFilter(windowSize int) *DedupFilter {
	if windowSize <= 0 {
		windowSize = 1024
	}
	// Round up to multiple of 64
	words := (windowSize + 63) / 64
	return &DedupFilter{
		windowSize: uint64(words * 64),
		bitmap:     make([]uint64, words),
	}
}

// CheckAndRecord tests whether a sequence has already been observed.
// Returns true if the packet is a duplicate (should be dropped).
// Returns false if the packet is new, and atomically records its sequence in the window.
func (d *DedupFilter) CheckAndRecord(seq uint64) bool {
	d.mu.Lock()
	defer d.mu.Unlock()

	if !d.initialized {
		d.maxSeq = seq
		d.bitmap[0] = 1
		d.initialized = true
		return false
	}

	diff := SequenceDistance(seq, d.maxSeq)

	if diff > 0 {
		// New packet ahead of maxSeq: slide window forward
		shift := uint64(diff)
		d.advanceWindow(shift)
		d.maxSeq = seq
		d.setBit(0) // mark maxSeq at index 0
		return false
	}

	// Packet is behind or equal to maxSeq
	pos := uint64(-diff)
	if pos >= d.windowSize {
		// Too old, outside the tracking window: drop as stale/duplicate
		return true
	}

	// Check if bit is already set
	wordIdx := pos / 64
	bitIdx := pos % 64

	if (d.bitmap[wordIdx] & (1 << bitIdx)) != 0 {
		// Already seen -> DUPLICATE!
		return true
	}

	// First time seen -> mark bit
	d.bitmap[wordIdx] |= (1 << bitIdx)
	return false
}

// advanceWindow shifts the bitmap forward by 'shift' bits, clearing newly exposed bits
func (d *DedupFilter) advanceWindow(shift uint64) {
	if shift >= d.windowSize {
		// Complete window invalidation
		for i := range d.bitmap {
			d.bitmap[i] = 0
		}
		return
	}

	wordShift := shift / 64
	bitShift := shift % 64

	numWords := uint64(len(d.bitmap))

	if wordShift > 0 {
		for i := numWords - 1; i >= wordShift; i-- {
			d.bitmap[i] = d.bitmap[i-wordShift]
		}
		for i := uint64(0); i < wordShift; i++ {
			d.bitmap[i] = 0
		}
	}

	if bitShift > 0 {
		carry := uint64(0)
		for i := uint64(0); i < numWords; i++ {
			newCarry := d.bitmap[i] >> (64 - bitShift)
			d.bitmap[i] = (d.bitmap[i] << bitShift) | carry
			carry = newCarry
		}
	}
}

func (d *DedupFilter) setBit(pos uint64) {
	wordIdx := pos / 64
	bitIdx := pos % 64
	if wordIdx < uint64(len(d.bitmap)) {
		d.bitmap[wordIdx] |= (1 << bitIdx)
	}
}

// Reset clears the filter state
func (d *DedupFilter) Reset() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.initialized = false
	d.maxSeq = 0
	for i := range d.bitmap {
		d.bitmap[i] = 0
	}
}

