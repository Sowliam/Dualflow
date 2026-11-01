package relay

import (
	"errors"
	"sync"
)

var (
	ErrReplayDetected      = errors.New("packet rejected: sequence is duplicate or outside anti-replay window")
	ErrConcurrentDuplicate = errors.New("concurrent worker duplicate sequence detected")
	ErrInvalidSequenceZero = errors.New("sequence zero is reserved exclusively for handshakes")
)

const (
	// WindowSize is 2048 packets (32 uint64 words = 256 bytes)
	ReplayWindowSize = 2048
	BitmapWords      = 32
)

// AntiReplayWindow implements a 2048-packet sliding replay window with two-phase RLock-Lock concurrency
type AntiReplayWindow struct {
	mu     sync.RWMutex
	seqMax uint64
	bitmap [BitmapWords]uint64
}

// NewAntiReplayWindow creates an initialized 2048-packet window.
func NewAntiReplayWindow() *AntiReplayWindow {
	return &AntiReplayWindow{}
}

// isBitSetLocked checks if a bit at offset from seqMax is set. Caller must hold at least RLock.
func (w *AntiReplayWindow) isBitSetLocked(offset uint64) bool {
	if offset >= ReplayWindowSize {
		return true // Outside window
	}
	wordIdx := offset / 64
	bitIdx := offset % 64
	return (w.bitmap[wordIdx] & (uint64(1) << bitIdx)) != 0
}

// commitBitLocked marks a bit as received. If seq > seqMax, advances the window.
// Caller must hold write Lock.
func (w *AntiReplayWindow) commitBitLocked(seq uint64) {
	if seq > w.seqMax {
		shift := seq - w.seqMax
		if shift >= ReplayWindowSize {
			// Window advanced completely beyond previous history; reset all bits
			for i := range w.bitmap {
				w.bitmap[i] = 0
			}
		} else {
			w.shiftBitmap(shift)
		}
		w.seqMax = seq
		w.bitmap[0] |= 1 // Bit 0 corresponds to seqMax
		return
	}

	offset := w.seqMax - seq
	if offset < ReplayWindowSize {
		wordIdx := offset / 64
		bitIdx := offset % 64
		w.bitmap[wordIdx] |= (uint64(1) << bitIdx)
	}
}

// shiftBitmap shifts the bitmap by 'shift' bits to accommodate advancing seqMax.
func (w *AntiReplayWindow) shiftBitmap(shift uint64) {
	wordShift := int(shift / 64)
	bitShift := shift % 64

	if wordShift > 0 {
		for i := BitmapWords - 1; i >= 0; i-- {
			if i >= wordShift {
				w.bitmap[i] = w.bitmap[i-wordShift]
			} else {
				w.bitmap[i] = 0
			}
		}
	}

	if bitShift > 0 {
		for i := BitmapWords - 1; i > 0; i-- {
			w.bitmap[i] = (w.bitmap[i] << bitShift) | (w.bitmap[i-1] >> (64 - bitShift))
		}
		w.bitmap[0] <<= bitShift
	}
}

// VerifyAndCommit performs concurrency-safe anti-replay verification:
// Phase 1: RLock pre-check (fast-drop duplicates and stale packets).
// Phase 2: AEAD Decrypt execution outside of locks.
// Phase 3: Write-lock double-check and atomic bitmap commit.
func (w *AntiReplayWindow) VerifyAndCommit(seq uint64, decryptFunc func() error) error {
	if seq == 0 {
		return ErrInvalidSequenceZero
	}

	// 1. Phase 1: RLock Pre-Check (Concurrent read)
	w.mu.RLock()
	if seq <= w.seqMax {
		diff := w.seqMax - seq
		if diff >= ReplayWindowSize || w.isBitSetLocked(diff) {
			w.mu.RUnlock()
			return ErrReplayDetected
		}
	}
	w.mu.RUnlock()

	// 2. Phase 2: AEAD Decryption outside of locks
	if decryptFunc != nil {
		if err := decryptFunc(); err != nil {
			return err // Authentication failed; replay window remains completely untouched
		}
	}

	// 3. Phase 3: Lock Re-Check & Commit (Exclusive write)
	w.mu.Lock()
	defer w.mu.Unlock()

	// Double-check under write lock to prevent race conditions with concurrent goroutines
	if seq <= w.seqMax {
		diff := w.seqMax - seq
		if diff >= ReplayWindowSize || w.isBitSetLocked(diff) {
			return ErrConcurrentDuplicate
		}
	}

	// Commit sequence
	w.commitBitLocked(seq)
	return nil
}

// GetSeqMax returns current highest received sequence number.
func (w *AntiReplayWindow) GetSeqMax() uint64 {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.seqMax
}

