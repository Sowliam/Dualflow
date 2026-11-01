package packet

import (
	"encoding/binary"
	"net"
	"testing"
	"time"
)

// Helper to construct a synthetic IPv4 packet with optional checksum
func makeIPv4(src, dst net.IP, proto uint8, payload []byte) []byte {
	totalLen := 20 + len(payload)
	b := make([]byte, totalLen)
	b[0] = 0x45 // Version 4, IHL 5 (20 bytes)
	b[1] = 0x00 // DSCP / ECN
	binary.BigEndian.PutUint16(b[2:4], uint16(totalLen))
	binary.BigEndian.PutUint16(b[4:6], 12345) // Identification
	binary.BigEndian.PutUint16(b[6:8], 0x4000) // Flags: Don't Fragment
	b[8] = 64                                 // TTL
	b[9] = proto
	copy(b[12:16], src.To4())
	copy(b[16:20], dst.To4())

	// Compute header checksum
	csum := InternetChecksum(b[:20])
	binary.BigEndian.PutUint16(b[10:12], csum)

	copy(b[20:], payload)
	return b
}

// Helper to construct a synthetic IPv6 packet
func makeIPv6(src, dst net.IP, nextHdr uint8, payload []byte) []byte {
	payloadLen := len(payload)
	totalLen := 40 + payloadLen
	b := make([]byte, totalLen)
	b[0] = 0x60 // Version 6
	binary.BigEndian.PutUint16(b[4:6], uint16(payloadLen))
	b[6] = nextHdr
	b[7] = 64 // Hop Limit
	copy(b[8:24], src.To16())
	copy(b[24:40], dst.To16())
	copy(b[40:], payload)
	return b
}

func TestIPv4Validation(t *testing.T) {
	src := net.ParseIP("192.168.1.100")
	dst := net.ParseIP("1.1.1.1")
	payload := []byte("Hello DualFlow IPv4")

	raw := makeIPv4(src, dst, ProtoUDP, payload)

	pkt, err := ValidateIPPacket(raw, true)
	if err != nil {
		t.Fatalf("Expected valid IPv4 packet, got error: %v", err)
	}
	if pkt.Version != 4 {
		t.Errorf("Expected version 4, got %d", pkt.Version)
	}
	if pkt.Protocol != ProtoUDP {
		t.Errorf("Expected protocol UDP (17), got %d", pkt.Protocol)
	}
	if !pkt.SrcIP.Equal(src) {
		t.Errorf("SrcIP mismatch: %v != %v", pkt.SrcIP, src)
	}
	if !pkt.DstIP.Equal(dst) {
		t.Errorf("DstIP mismatch: %v != %v", pkt.DstIP, dst)
	}
	if pkt.TotalLength != len(raw) {
		t.Errorf("TotalLength mismatch: %d != %d", pkt.TotalLength, len(raw))
	}

	// Truncated packet
	_, err = ValidateIPPacket(raw[:15], false)
	if err != ErrPacketTooShort {
		t.Errorf("Expected ErrPacketTooShort, got: %v", err)
	}

	// Corrupt IHL
	corruptIHL := make([]byte, len(raw))
	copy(corruptIHL, raw)
	corruptIHL[0] = 0x43 // IHL = 3 (< 5)
	_, err = ValidateIPPacket(corruptIHL, false)
	if err != ErrInvalidIHL {
		t.Errorf("Expected ErrInvalidIHL, got: %v", err)
	}

	// Corrupt Checksum
	corruptChecksum := make([]byte, len(raw))
	copy(corruptChecksum, raw)
	corruptChecksum[10] ^= 0xFF
	_, err = ValidateIPPacket(corruptChecksum, true)
	if err != ErrInvalidIPv4Checksum {
		t.Errorf("Expected ErrInvalidIPv4Checksum, got: %v", err)
	}
}

func TestIPv6Validation(t *testing.T) {
	src := net.ParseIP("2001:db8::1")
	dst := net.ParseIP("2606:4700:4700::1111")
	payload := []byte("DualFlow IPv6 Test Stream")

	raw := makeIPv6(src, dst, ProtoTCP, payload)

	pkt, err := ValidateIPPacket(raw, false)
	if err != nil {
		t.Fatalf("Expected valid IPv6 packet, got: %v", err)
	}
	if pkt.Version != 6 {
		t.Errorf("Expected version 6, got %d", pkt.Version)
	}
	if pkt.Protocol != ProtoTCP {
		t.Errorf("Expected TCP protocol, got %d", pkt.Protocol)
	}
	if pkt.TotalLength != len(raw) {
		t.Errorf("Total length mismatch: %d != %d", pkt.TotalLength, len(raw))
	}

	// Truncated IPv6 header
	_, err = ValidateIPPacket(raw[:30], false)
	if err != ErrPacketTooShort {
		t.Errorf("Expected ErrPacketTooShort, got: %v", err)
	}

	// Payload length mismatch
	corruptLen := make([]byte, len(raw))
	copy(corruptLen, raw)
	binary.BigEndian.PutUint16(corruptLen[4:6], uint16(len(payload)+50)) // Exceeds buffer
	_, err = ValidateIPPacket(corruptLen, false)
	if err != ErrIPv6PayloadMismatch {
		t.Errorf("Expected ErrIPv6PayloadMismatch, got: %v", err)
	}
}

func TestIPv6ExtensionHeaderWalking(t *testing.T) {
	src := net.ParseIP("2001:db8::1")
	dst := net.ParseIP("2001:db8::2")
	payload := []byte("Payload behind Hop-by-Hop")

	// IPv6 base (40 bytes) + Hop-by-Hop (8 bytes) + payload
	totalLen := 40 + 8 + len(payload)
	raw := make([]byte, totalLen)
	raw[0] = 0x60 // IPv6
	binary.BigEndian.PutUint16(raw[4:6], uint16(8+len(payload)))
	raw[6] = IPv6HopByHop // NextHeader = 0
	raw[7] = 64
	copy(raw[8:24], src.To16())
	copy(raw[24:40], dst.To16())

	// Hop-by-Hop Header
	raw[40] = ProtoUDP // NextHeader inside extension is UDP
	raw[41] = 0        // Length in 8-byte units minus 1 -> 0 means 8 bytes total
	copy(raw[48:], payload)

	pkt, err := ValidateIPPacket(raw, false)
	if err != nil {
		t.Fatalf("Expected valid IPv6 with Hop-by-Hop, got: %v", err)
	}
	if pkt.Protocol != ProtoUDP {
		t.Errorf("Expected resolved protocol UDP (17), got %d", pkt.Protocol)
	}
	if pkt.HeaderLength != 48 {
		t.Errorf("Expected total header length 48, got %d", pkt.HeaderLength)
	}
}

func TestBoundedQueueSaturation(t *testing.T) {
	q := NewBoundedQueue(4)
	pkt := &Packet{Version: 4, TotalLength: 20}

	for i := 0; i < 4; i++ {
		if !q.Push(pkt) {
			t.Fatalf("Failed to push packet %d into queue", i)
		}
	}

	// 5th push must fail (queue full)
	if q.Push(pkt) {
		t.Fatalf("Expected 5th push to fail due to capacity saturation")
	}

	if q.Len() != 4 {
		t.Errorf("Expected queue len 4, got %d", q.Len())
	}
	q.Close()
}

type mockOutbound struct {
	available bool
	sentCount int
	lastPkt   *Packet
}

func (m *mockOutbound) SendPacket(pkt *Packet) error {
	m.sentCount++
	m.lastPkt = pkt
	return nil
}

func (m *mockOutbound) IsAvailable() bool {
	return m.available
}

func TestPipelineIngestAndDropAccounting(t *testing.T) {
	pipe := NewPipeline(10)
	mock := &mockOutbound{available: true}
	pipe.SetOutboundHandler(mock)

	if err := pipe.Start(); err != nil {
		t.Fatalf("Failed to start pipeline: %v", err)
	}
	defer pipe.Stop()

	src := net.ParseIP("10.0.0.1")
	dst := net.ParseIP("10.0.0.2")
	validPkt := makeIPv4(src, dst, ProtoICMP, []byte("Ping!"))

	// Ingest valid packet
	p, err := pipe.IngestRawPacket(validPkt)
	if err != nil {
		t.Fatalf("Expected valid ingest, got: %v", err)
	}
	if p.Protocol != ProtoICMP {
		t.Errorf("Expected ICMP protocol, got %d", p.Protocol)
	}

	// Ingest invalid packet (garbage)
	invalidPkt := []byte{0x41, 0x00, 0x00}
	_, err = pipe.IngestRawPacket(invalidPkt)
	if err == nil {
		t.Fatalf("Expected invalid packet to be rejected")
	}

	// Allow worker loop to process
	time.Sleep(50 * time.Millisecond)

	stats := pipe.GetStats()
	if stats.ValidatedPackets != 1 {
		t.Errorf("Expected 1 validated packet, got %d", stats.ValidatedPackets)
	}
	if stats.RejectedPackets != 1 {
		t.Errorf("Expected 1 rejected packet, got %d", stats.RejectedPackets)
	}
	if stats.DropsInvalidPacket != 1 {
		t.Errorf("Expected 1 DropInvalidPacket, got %d", stats.DropsInvalidPacket)
	}
}

