package transport

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"dualflow-core/internal/packet"
)

func TestFrameCodec(t *testing.T) {
	payload := []byte("DualFlow Packet Transport Test Payload 1234567890")
	sessionID := uint32(0xCAFE1234)
	seq := uint64(42)

	encoded, err := EncodeFrame(MsgTypeData, FlagTestFrame, sessionID, seq, payload)
	if err != nil {
		t.Fatalf("EncodeFrame failed: %v", err)
	}

	if len(encoded) != int(HeaderLength)+len(payload) {
		t.Fatalf("Encoded frame size mismatch: %d != %d", len(encoded), int(HeaderLength)+len(payload))
	}

	decoded, err := DecodeFrame(encoded)
	if err != nil {
		t.Fatalf("DecodeFrame failed: %v", err)
	}

	if decoded.Header.MsgType != MsgTypeData {
		t.Errorf("MsgType mismatch: %d != %d", decoded.Header.MsgType, MsgTypeData)
	}
	if decoded.Header.SessionID != sessionID {
		t.Errorf("SessionID mismatch: %x != %x", decoded.Header.SessionID, sessionID)
	}
	if decoded.Header.SequenceNumber != seq {
		t.Errorf("SequenceNumber mismatch: %d != %d", decoded.Header.SequenceNumber, seq)
	}
	if string(decoded.Payload) != string(payload) {
		t.Errorf("Payload mismatch: %s != %s", string(decoded.Payload), string(payload))
	}

	// Corrupt Checksum
	corrupt := make([]byte, len(encoded))
	copy(corrupt, encoded)
	corrupt[30] ^= 0xFF
	_, err = DecodeFrame(corrupt)
	if err != ErrChecksumMismatch {
		t.Errorf("Expected ErrChecksumMismatch, got: %v", err)
	}
}

func TestStreamDecoderPartialAndCoalesced(t *testing.T) {
	decoder := NewStreamDecoder()

	p1 := []byte("Packet One")
	p2 := []byte("Packet Two - Longer Data Stream")

	f1, _ := EncodeFrame(MsgTypeData, 0, 1, 100, p1)
	f2, _ := EncodeFrame(MsgTypeData, 0, 1, 101, p2)

	// Combine both frames
	combined := append(f1, f2...)

	// 1. Feed partial header (15 bytes)
	frames, err := decoder.Feed(combined[:15])
	if err != nil {
		t.Fatalf("Feed error: %v", err)
	}
	if len(frames) != 0 {
		t.Fatalf("Expected 0 frames on partial header, got %d", len(frames))
	}

	// 2. Feed rest of first frame + part of second frame
	frames, err = decoder.Feed(combined[15 : len(f1)+10])
	if err != nil {
		t.Fatalf("Feed error: %v", err)
	}
	if len(frames) != 1 {
		t.Fatalf("Expected 1 frame, got %d", len(frames))
	}
	if string(frames[0].Payload) != string(p1) {
		t.Errorf("Decoded payload mismatch: %s != %s", string(frames[0].Payload), string(p1))
	}

	// 3. Feed remainder
	frames, err = decoder.Feed(combined[len(f1)+10:])
	if err != nil {
		t.Fatalf("Feed error: %v", err)
	}
	if len(frames) != 1 {
		t.Fatalf("Expected 1 frame for second packet, got %d", len(frames))
	}
	if string(frames[0].Payload) != string(p2) {
		t.Errorf("Decoded second payload mismatch: %s != %s", string(frames[0].Payload), string(p2))
	}
}

func TestStreamDecoderResynchronization(t *testing.T) {
	decoder := NewStreamDecoder()

	p := []byte("Valid Payload After Noise")
	f, _ := EncodeFrame(MsgTypeData, 0, 1, 1, p)

	// Inject arbitrary noise before valid frame
	noise := []byte("random noise garbage bytes that do not have DFTP magic")
	stream := append(noise, f...)

	frames, err := decoder.Feed(stream)
	if err != nil {
		t.Fatalf("Feed error on stream with noise: %v", err)
	}
	if len(frames) != 1 {
		t.Fatalf("Expected 1 synchronized frame, got %d", len(frames))
	}
	if string(frames[0].Payload) != string(p) {
		t.Errorf("Payload mismatch: %s != %s", string(frames[0].Payload), string(p))
	}
}

func TestInMemoryTransportLoopback(t *testing.T) {
	trans := NewInMemoryTransport(0x11223344, true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := trans.Start(ctx); err != nil {
		t.Fatalf("Failed to start transport: %v", err)
	}
	defer trans.Stop()

	var wg sync.WaitGroup
	wg.Add(1)

	var receivedData []byte
	trans.SetInboundSink(func(rawPacket []byte) {
		receivedData = rawPacket
		wg.Done()
	})

	testPkt := &packet.Packet{
		Raw:         []byte("DualFlow Test IP Packet Through In-Memory Tunnel"),
		Version:     4,
		TotalLength: 48,
		SrcIP:       net.ParseIP("10.0.0.1"),
		DstIP:       net.ParseIP("10.0.0.2"),
	}

	if err := trans.SendPacket(testPkt); err != nil {
		t.Fatalf("SendPacket failed: %v", err)
	}

	// Wait for loopback receive
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Fatalf("Timeout waiting for in-memory loopback delivery")
	}

	if string(receivedData) != string(testPkt.Raw) {
		t.Errorf("Received data mismatch: %s != %s", string(receivedData), string(testPkt.Raw))
	}

	stats := trans.GetStats()
	if stats.TxPackets != 1 {
		t.Errorf("Expected TxPackets 1, got %d", stats.TxPackets)
	}
	if stats.RxPackets != 1 {
		t.Errorf("Expected RxPackets 1, got %d", stats.RxPackets)
	}
	if !stats.IsTest {
		t.Errorf("Expected IsTest to be true")
	}
}


func TestStreamDecoderFuzzRobustness(t *testing.T) {
	decoder := NewStreamDecoder()

	// Randomized pseudo-noise generator
	prng := uint32(0x1337C0DE)
	nextRand := func() byte {
		prng = prng*1664525 + 1013904223
		return byte(prng >> 24)
	}

	// Feed 100 randomized chunks with pseudo-noise
	for i := 0; i < 100; i++ {
		chunkLen := int(nextRand()%128) + 1
		chunk := make([]byte, chunkLen)
		for j := 0; j < chunkLen; j++ {
			chunk[j] = nextRand()
		}

		// Inject valid frame occasionally
		if i%10 == 0 {
			validFrame, _ := EncodeFrame(MsgTypeData, 0, 1, uint64(i), []byte("Embedded In Fuzz"))
			chunk = append(chunk, validFrame...)
		}

		// Must not panic or hang
		_, _ = decoder.Feed(chunk)
	}
}
