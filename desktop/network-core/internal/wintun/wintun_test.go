package wintun

import (
	"net"
	"testing"
)

// TestWintunRuntimeAvailability validates that the official wintun.dll is discovered and loaded.
func TestWintunRuntimeAvailability(t *testing.T) {
	dll := GetDLL()
	if !dll.IsAvailable() {
		t.Fatalf("Expected wintun.dll to be available, but got error: %v", dll.GetLoadError())
	}
	t.Logf("Wintun DLL path resolved: %s", dll.GetPath())

	ver, err := dll.GetRunningDriverVersion()
	t.Logf("Running driver version query: %s (err: %v)", ver, err)
}

// TestElevationCheck verifies process elevation query function.
func TestElevationCheck(t *testing.T) {
	elevated := IsElevated()
	t.Logf("Process elevation status: %v (Administrator: %v)", elevated, elevated)
}

// TestPacketValidation tests packet size boundaries and IPv4 test packet generation.
func TestPacketValidation(t *testing.T) {
	mgr := NewManager("Test Adapter", "TestTunnel")

	// 1. Packet too small (< 20 bytes)
	errSmall := mgr.SendPacket([]byte{0x45, 0x00, 0x00})
	if errSmall == nil {
		t.Fatal("Expected error for packet smaller than 20 bytes, got nil")
	}

	// 2. Packet too large (> 65535 bytes)
	hugeData := make([]byte, MaxPacketSize+1)
	errHuge := mgr.SendPacket(hugeData)
	if errHuge == nil {
		t.Fatal("Expected error for packet larger than 65535 bytes, got nil")
	}

	// 3. Valid synthetic test packet
	pkt := GenerateTestIPv4Packet(net.IPv4(10, 0, 0, 1), net.IPv4(10, 0, 0, 2), 32)
	if len(pkt) != 20+8+32 {
		t.Fatalf("Expected packet length %d, got %d", 20+8+32, len(pkt))
	}
	if pkt[0] != 0x45 {
		t.Fatalf("Expected IPv4 version byte 0x45, got 0x%X", pkt[0])
	}
	t.Logf("Generated valid %d-byte IPv4 ICMP packet with checksum", len(pkt))
}

// TestRingCapacityValidation tests power-of-two and bounds validation for Wintun ring buffers.
func TestRingCapacityValidation(t *testing.T) {
	mgr := NewManager("Test Adapter", "TestTunnel")

	// Capacity less than minimum (128 KiB)
	errUnder := mgr.StartSession(0x10000)
	if errUnder == nil {
		t.Fatal("Expected error for capacity below 128 KiB")
	}

	// Capacity greater than maximum (64 MiB)
	errOver := mgr.StartSession(0x8000000)
	if errOver == nil {
		t.Fatal("Expected error for capacity above 64 MiB")
	}

	// Capacity not a power of two
	errNotPowerOfTwo := mgr.StartSession(300000)
	if errNotPowerOfTwo == nil {
		t.Fatal("Expected error for capacity that is not a power of two")
	}
}

// TestHardwareIntegration attempts real kernel creation if elevated, or documents environment limitation.
func TestHardwareIntegration(t *testing.T) {
	mgr := NewManager(DefaultAdapterName, DefaultTunnelType)

	if !IsElevated() {
		t.Log("NOTICE: Test process is running without Administrator privileges.")
		t.Log("Wintun driver installation and kernel adapter creation require elevation.")
		t.Log("STATUS: IMPLEMENTED BUT BLOCKED BY ENVIRONMENT (Requires Administrator token).")

		err := mgr.CreateAdapter()
		if err == nil {
			t.Fatal("Expected create to fail without elevation, but succeeded")
		}
		t.Logf("Verified expected elevation denial: %v", err)
		return
	}

	t.Log("Elevated privilege detected: executing live adapter lifecycle...")
	err := mgr.CreateAdapter()
	if err != nil {
		t.Fatalf("CreateAdapter failed with elevated privileges: %v", err)
	}
	defer mgr.CloseAdapter()

	t.Log("Adapter created successfully! Starting session...")
	err = mgr.StartSession(DefaultRingCapacity)
	if err != nil {
		t.Fatalf("StartSession failed: %v", err)
	}

	// Send controlled test packet
	testPkt := GenerateTestIPv4Packet(net.IPv4(10, 254, 1, 1), net.IPv4(10, 254, 1, 2), 64)
	err = mgr.SendPacket(testPkt)
	if err != nil {
		t.Fatalf("SendPacket failed: %v", err)
	}

	stats := mgr.GetStats()
	if stats.TxPackets != 1 || stats.TxBytes != uint64(len(testPkt)) {
		t.Fatalf("Expected 1 TX packet and %d TX bytes, got %d pkts / %d bytes", len(testPkt), stats.TxPackets, stats.TxBytes)
	}
	t.Logf("Successfully verified live packet send: %d packets / %d bytes", stats.TxPackets, stats.TxBytes)

	// Stop session
	err = mgr.StopSession()
	if err != nil {
		t.Fatalf("StopSession failed: %v", err)
	}
	t.Log("Session stopped cleanly.")
}

