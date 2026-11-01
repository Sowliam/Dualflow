package proxy

import (
	"bytes"
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"dualflow-core/internal/transport"
)

func TestReorderStreamBufferZeroSkip(t *testing.T) {
	GetGlobalCoordinator().Reset()
	buf := NewReorderStreamBuffer(1)
	defer buf.Close()

	chunk1 := []byte("AAAA_0000_1280")
	chunk2 := []byte("BBBB_1280_2560")
	chunk3 := []byte("CCCC_2560_3840")

	// 1. Insert Chunk 1 (Offset 0): Immediate delivery
	ready, fin, dup, err := buf.InsertChunk(0, 0, chunk1)
	if err != nil || fin || dup || !bytes.Equal(ready, chunk1) {
		t.Fatalf("Chunk 1 failed: ready=%s, err=%v", string(ready), err)
	}
	if buf.GetCumulativeAck() != uint64(len(chunk1)) {
		t.Errorf("CumAck mismatch: got %d, want %d", buf.GetCumulativeAck(), len(chunk1))
	}

	// 2. Insert Chunk 3 (Offset 28): Out-of-order gap!
	offset3 := uint64(len(chunk1) + len(chunk2))
	ready, fin, dup, err = buf.InsertChunk(offset3, 0, chunk3)
	if err != nil || fin || dup || len(ready) != 0 {
		t.Fatalf("Chunk 3 should be buffered: ready=%s, err=%v", string(ready), err)
	}

	// Cumulative ACK must remain at end of Chunk 1 (zero-skip invariant)
	if buf.GetCumulativeAck() != uint64(len(chunk1)) {
		t.Errorf("Zero-skip violation: CumAck advanced prematurely to %d", buf.GetCumulativeAck())
	}

	// SACK blocks must report Chunk 3 range
	sacks := buf.GetSackBlocks()
	if len(sacks) != 1 || sacks[0].StartOffset != offset3 || sacks[0].EndOffset != offset3+uint64(len(chunk3)) {
		t.Fatalf("SACK block mismatch: %+v", sacks)
	}

	// 3. Insert Chunk 2 (Offset 14): Fills the gap!
	offset2 := uint64(len(chunk1))
	ready, fin, dup, err = buf.InsertChunk(offset2, 0, chunk2)
	if err != nil || fin || dup {
		t.Fatalf("Chunk 2 insert failed: %v", err)
	}

	// Both Chunk 2 and Chunk 3 must now be delivered contiguously
	expectedCombined := append(append([]byte(nil), chunk2...), chunk3...)
	if !bytes.Equal(ready, expectedCombined) {
		t.Errorf("Delivered stream mismatch: got %s, want %s", string(ready), string(expectedCombined))
	}

	expectedFinalAck := uint64(len(chunk1) + len(chunk2) + len(chunk3))
	if buf.GetCumulativeAck() != expectedFinalAck {
		t.Errorf("Final CumAck mismatch: got %d, want %d", buf.GetCumulativeAck(), expectedFinalAck)
	}

	// SACK blocks should now be empty
	if len(buf.GetSackBlocks()) != 0 {
		t.Errorf("Expected 0 SACK blocks after gap filled, got %+v", buf.GetSackBlocks())
	}
}

func TestStreamFINSequenceAndAck(t *testing.T) {
	GetGlobalCoordinator().Reset()
	buf := NewReorderStreamBuffer(2)
	defer buf.Close()

	payload := []byte("Final Stream Data")
	finOffset := uint64(0)

	// Send final data chunk with FlagStreamFIN
	ready, deliversFIN, dup, err := buf.InsertChunk(finOffset, transport.FlagStreamFIN, payload)
	if err != nil || dup || !deliversFIN || !bytes.Equal(ready, payload) {
		t.Fatalf("FIN chunk processing failed: deliversFIN=%v, err=%v", deliversFIN, err)
	}

	// FIN consumes 1 byte in sequence space so its ACK is distinguishable
	expectedAck := uint64(len(payload) + 1)
	if buf.GetCumulativeAck() != expectedAck {
		t.Errorf("CumAck after FIN mismatch: got %d, want %d", buf.GetCumulativeAck(), expectedAck)
	}
}

func TestFastRetransmitThreeDupAcks(t *testing.T) {
	GetGlobalCoordinator().Reset()
	flow := NewFlow(10)
	defer flow.Close()

	// Enqueue 3 chunks
	data := []byte("1280_bytes_chunk_data")
	_, _ = flow.EnqueueOutbound(data, 0)
	c2, _ := flow.EnqueueOutbound(data, 0)
	_, _ = flow.EnqueueOutbound(data, 0)

	// Receiver acknowledges chunk 1
	fastChunk, _ := flow.ProcessAck(uint64(len(data)), nil)
	if fastChunk != nil {
		t.Fatalf("Did not expect fast retransmit on first ACK")
	}

	// Duplicate ACK 1
	fastChunk, _ = flow.ProcessAck(uint64(len(data)), nil)
	if fastChunk != nil {
		t.Fatalf("Did not expect fast retransmit on dup 1")
	}

	// Duplicate ACK 2
	fastChunk, _ = flow.ProcessAck(uint64(len(data)), nil)
	if fastChunk != nil {
		t.Fatalf("Did not expect fast retransmit on dup 2")
	}

	// Duplicate ACK 3: Fast Retransmit MUST trigger!
	fastChunk, _ = flow.ProcessAck(uint64(len(data)), nil)
	if fastChunk == nil {
		t.Fatalf("Expected Fast Retransmit chunk on 3rd duplicate ACK, got nil")
	}

	if fastChunk.Offset != c2.Offset {
		t.Errorf("Fast retransmit offset mismatch: got %d, want %d", fastChunk.Offset, c2.Offset)
	}
}

func TestKarnsAlgorithmRTO(t *testing.T) {
	rto := NewRTOCalculator()
	initialRTO := rto.CurrentRTO()

	if initialRTO != InitialRTO {
		t.Errorf("Initial RTO mismatch: got %v, want %v", initialRTO, InitialRTO)
	}

	// Record clean sample (50ms)
	rto.RecordSample(50 * time.Millisecond)
	sampleRTO := rto.CurrentRTO()
	if sampleRTO <= 0 || sampleRTO > MaxRTO {
		t.Errorf("Sample RTO out of bounds: %v", sampleRTO)
	}

	// Test Exponential Backoff
	backedOff := rto.Backoff()
	if backedOff != sampleRTO*2 {
		t.Errorf("Expected 2x backoff: got %v, want %v", backedOff, sampleRTO*2)
	}
}

func TestBackpressureWatermarks(t *testing.T) {
	GetGlobalCoordinator().Reset()
	buf := NewReorderStreamBuffer(20)
	defer buf.Close()

	// Fill buffer with out-of-order chunks past 1.6 MB
	chunkSize := 1024
	chunkData := make([]byte, chunkSize)

	// Start at offset 10000000 so nothing is delivered
	baseOffset := uint64(10000000)
	numChunks := 1605 // 1605 KB > 1600 KB (High Watermark)

	for i := 0; i < numChunks; i++ {
		off := baseOffset + uint64(i*chunkSize)
		_, _, _, err := buf.InsertChunk(off, 0, chunkData)
		if err != nil {
			t.Fatalf("Insert failed at chunk %d: %v", i, err)
		}
	}

	if !buf.IsBackpressured() {
		t.Errorf("Expected buffer to be backpressured after %d KB allocated", buf.AllocatedBytes()/1024)
	}
}

func TestGlobalMemoryCapEnforcement(t *testing.T) {
	coord := GetGlobalCoordinator()
	coord.Reset()

	// Fill global coordinator up to 64 MB
	chunk := int64(32 * 1024 * 1024)
	if !coord.TryAcquire(chunk) {
		t.Fatalf("Failed to acquire first 32MB")
	}
	if !coord.TryAcquire(chunk) {
		t.Fatalf("Failed to acquire second 32MB")
	}

	// Exceeding 64MB must be rejected
	if coord.TryAcquire(1024) {
		t.Errorf("Expected rejection when exceeding 64MB cap, but succeeded")
	}

	// Release 1MB and acquire should succeed
	coord.Release(1024 * 1024)
	if !coord.TryAcquire(512 * 1024) {
		t.Errorf("Expected acquire to succeed after release")
	}
}

type mockTunnelSender struct {
	mu           sync.Mutex
	flowOpenSent bool
	dataSent     int
	lastCumAck   uint64
}

func (m *mockTunnelSender) SendFlowOpen(flowID uint32, port uint16, addrType uint8, addr string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.flowOpenSent = true
	return nil
}

func (m *mockTunnelSender) SendStreamData(flowID uint32, offset uint64, flags uint16, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dataSent += len(data)
	return nil
}

func (m *mockTunnelSender) SendStreamAck(flowID uint32, cumAck uint64, sackBlocks []transport.SackBlock) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastCumAck = cumAck
	return nil
}

func (m *mockTunnelSender) SendFlowRST(flowID uint32) error {
	return nil
}

func TestSOCKS5GreetingAndConnect(t *testing.T) {
	sender := &mockTunnelSender{}
	listener := NewClientProxyListener("127.0.0.1:0", "", sender)
	if err := listener.Start(context.Background()); err != nil {
		t.Fatalf("Listener start failed: %v", err)
	}
	defer listener.Stop()

	// Connect to local listener
	conn, err := net.Dial("tcp", listener.listener.Addr().String())
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer conn.Close()

	// 1. SOCKS5 Greeting: [VER=5, NMETHODS=1, METHOD=0]
	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		t.Fatalf("Write greeting failed: %v", err)
	}

	resp := make([]byte, 2)
	if _, err := conn.Read(resp); err != nil {
		t.Fatalf("Read greeting response failed: %v", err)
	}
	if resp[0] != 0x05 || resp[1] != 0x00 {
		t.Fatalf("Greeting response mismatch: %v", resp)
	}

	// 2. Asynchronously acknowledge FlowOpen from mock relay
	go func() {
		time.Sleep(50 * time.Millisecond)
		// Send FlowOpenAck to flow 1
		listener.HandleFlowOpenAck(2, transport.StatusSuccess)
	}()

	// 3. SOCKS5 CONNECT Request: [VER=5, CMD=1, RSV=0, ATYP=1, IP=127.0.0.1, Port=8080]
	req := []byte{0x05, 0x01, 0x00, 0x01, 127, 0, 0, 1, 0x1F, 0x90}
	if _, err := conn.Write(req); err != nil {
		t.Fatalf("Write connect failed: %v", err)
	}

	connectReply := make([]byte, 10)
	if _, err := conn.Read(connectReply); err != nil {
		t.Fatalf("Read connect reply failed: %v", err)
	}

	if connectReply[0] != 0x05 || connectReply[1] != 0x00 {
		t.Fatalf("SOCKS5 connect rejected with status: 0x%02x", connectReply[1])
	}
}
