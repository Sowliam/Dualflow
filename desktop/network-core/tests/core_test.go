package tests

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"dualflow-core/internal/discovery"
	"dualflow-core/internal/packet"
	"dualflow-core/internal/paths"
	"dualflow-core/internal/protocol"
	"dualflow-core/internal/telemetry"
	"dualflow-core/internal/transport"
	"dualflow-core/internal/wintun"
)

// 1. Test Protocol Serialization & Validation
func TestProtocolValidation(t *testing.T) {
	req := protocol.Request{
		ProtocolVersion: protocol.ProtocolVersion,
		RequestID:       "test-1",
		Type:            "get_status",
	}

	if err := protocol.ValidateRequest(&req); err != nil {
		t.Fatalf("Valid request failed validation: %v", err)
	}

	invalidReq := protocol.Request{
		RequestID: "test-2",
		Type:      "get_status",
	}
	if err := protocol.ValidateRequest(&invalidReq); err == nil {
		t.Fatal("Request without protocol_version should fail validation")
	}

	res := protocol.NewSuccessResponse("test-1", map[string]string{"foo": "bar"})
	data, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("Failed to serialize response: %v", err)
	}
	if !strings.Contains(string(data), `"protocol_version":"1.0.0"`) {
		t.Fatalf("Response missing protocol_version: %s", string(data))
	}
}

// 2. Test Discovery Engine with Actual Windows Stack
func TestDiscoveryEngine(t *testing.T) {
	engine := discovery.NewDiscoveryEngine()
	adapters, err := engine.Discover()
	if err != nil {
		t.Fatalf("Discover failed: %v", err)
	}

	if len(adapters) == 0 {
		t.Fatal("Expected at least 1 network adapter on Windows host")
	}

	foundEthernet := false
	for _, a := range adapters {
		if a.Name == "Ethernet" {
			foundEthernet = true
			if a.Status != "connected" {
				t.Errorf("Expected physical Ethernet to be connected, got %s", a.Status)
			}
			if a.IPv4 == nil || *a.IPv4 != "192.168.1.10" {
				t.Errorf("Expected IPv4 192.168.1.10, got %v", a.IPv4)
			}
			if a.Gateway == nil || *a.Gateway != "192.168.1.1" {
				t.Errorf("Expected Gateway 192.168.1.1, got %v", a.Gateway)
			}
			if a.Classification != "modem" {
				t.Errorf("Expected classification modem, got %s", a.Classification)
			}
			if a.ClassificationConfidence != "high" {
				t.Errorf("Expected high confidence, got %s", a.ClassificationConfidence)
			}
		}
	}

	if !foundEthernet {
		t.Fatal("Realtek PCIe GbE Family Controller / Ethernet was not enumerated")
	}
}

// 3. Test Throughput Calculation & Counter Reset Handling
func TestThroughputCalculation(t *testing.T) {
	engine := telemetry.NewTelemetryEngine()

	// Sample 1 (Baseline)
	s1 := engine.SampleThroughput()
	if len(s1) == 0 {
		t.Fatal("Expected subinterfaces to return interface throughput metrics")
	}

	// Sleep 500ms
	time.Sleep(500 * time.Millisecond)

	// Sample 2 (Delta)
	s2 := engine.SampleThroughput()
	eth, ok := s2["ethernet"]
	if !ok {
		t.Fatal("Expected Ethernet in throughput map")
	}

	if !eth.IsConnected {
		t.Fatal("Expected Ethernet to be reported as connected")
	}

	// Throughput should be non-negative
	if eth.RxMbps < 0 || eth.TxMbps < 0 {
		t.Fatalf("Negative throughput calculated: Rx=%f, Tx=%f", eth.RxMbps, eth.TxMbps)
	}
}

// 4. Test Path Manager Reconcile and Assignment
func TestPathManager(t *testing.T) {
	pm := paths.NewPathManager()
	engine := discovery.NewDiscoveryEngine()
	adapters, err := engine.Discover()
	if err != nil {
		t.Fatalf("Discover failed: %v", err)
	}

	pm.Reconcile(adapters)
	p1, p2 := pm.GetPaths()

	// Path 1 should auto-bind to Ethernet
	if p1.AssignedAdapter == nil || p1.AssignedAdapter.Name != "Ethernet" {
		t.Fatalf("Expected Path 1 assigned to Ethernet, got %v", p1.AssignedAdapter)
	}
	if p1.State != paths.StateConnected {
		t.Fatalf("Expected Path 1 connected, got %s", p1.State)
	}
	if p1.SourceIp == nil || *p1.SourceIp != "192.168.1.10" {
		t.Fatalf("Expected Path 1 source IP 192.168.1.10, got %v", p1.SourceIp)
	}

	// Path 2 should be disconnected (unless phone is plugged in)
	if p2.AssignedAdapter != nil {
		t.Logf("Path 2 assigned to: %s", p2.AssignedAdapter.Name)
	} else {
		if p2.State != paths.StateDisconnected {
			t.Fatalf("Expected Path 2 disconnected, got %s", p2.State)
		}
	}
}

// 5. Test Source-Bound Probe Failure on Unrouted Interface
func TestSourceBoundProbe(t *testing.T) {
	engine := telemetry.NewTelemetryEngine()

	// Probe from real Ethernet -> should succeed
	src := "192.168.1.10"
	p1 := engine.ProbePath("modem", &src, "1.1.1.1")
	if p1.RttMs == nil || *p1.RttMs <= 0 {
		t.Fatalf("Expected valid RTT from 192.168.1.10, got %v", p1.RttMs)
	}

	// Probe from Hyper-V virtual switch -> should fail without falling back
	vSrc := "172.17.96.1"
	pV := engine.ProbePath("virtual", &vSrc, "1.1.1.1")
	if pV.RttMs != nil {
		t.Fatalf("Expected probe from isolated Hyper-V IP 172.17.96.1 to fail, but got RTT: %v", *pV.RttMs)
	}
}

// 6. Test Wintun Integration Layer & Protocol Commands
func TestWintunProtocolIntegration(t *testing.T) {
	mgr := wintun.NewManager(wintun.DefaultAdapterName, wintun.DefaultTunnelType)
	status := mgr.GetStatus()

	if status.DriverStatus != wintun.DriverStatusAvailable {
		t.Fatalf("Expected driver status AVAILABLE, got %s (error: %s)", status.DriverStatus, status.LastError)
	}

	if status.AdapterName != "DualFlow Tunnel" {
		t.Fatalf("Expected adapter name 'DualFlow Tunnel', got %s", status.AdapterName)
	}

	stats := mgr.GetStats()
	if stats.RxPackets != 0 || stats.TxPackets != 0 {
		t.Fatalf("Expected zero initial packets, got RX=%d, TX=%d", stats.RxPackets, stats.TxPackets)
	}

	// Verify test packet synthesis
	testPkt := wintun.GenerateTestIPv4Packet(nil, nil, 48)
	if len(testPkt) != 20+8+48 {
		t.Fatalf("Expected 76 bytes, got %d", len(testPkt))
	}
	t.Logf("Wintun integration verified: Driver=%s, Elevated=%v, TestPacketLen=%d", status.DriverStatus, status.IsElevated, len(testPkt))
}



// 8. Test Packet Pipeline and Transport Protocol Integration
func TestPacketEngineAndTransportProtocol(t *testing.T) {
	pipe := packet.NewPipeline(256)
	trans := transport.NewInMemoryTransport(0x1337BEEF, true)

	pipe.SetOutboundHandler(trans)
	pipe.SetTestMode(true)

	if err := pipe.Start(); err != nil {
		t.Fatalf("Failed to start pipeline: %v", err)
	}
	defer pipe.Stop()

	if err := trans.Start(context.Background()); err != nil {
		t.Fatalf("Failed to start transport: %v", err)
	}
	defer trans.Stop()

	// Ingest valid synthetic IPv4
	validIPv4 := wintun.GenerateTestIPv4Packet(nil, nil, 32)
	pkt, err := pipe.IngestRawPacket(validIPv4)
	if err != nil {
		t.Fatalf("Failed to ingest valid IPv4: %v", err)
	}
	if pkt.Version != 4 {
		t.Errorf("Expected IPv4, got %d", pkt.Version)
	}

	// Ingest invalid truncated packet (should record rejection and DropInvalidPacket)
	invalidPkt := []byte{0x45, 0x00, 0x00}
	_, err = pipe.IngestRawPacket(invalidPkt)
	if err == nil {
		t.Fatalf("Expected truncated packet to fail ingestion")
	}

	// Wait for worker queue processing
	time.Sleep(60 * time.Millisecond)

	pStats := pipe.GetStats()
	if pStats.ValidatedPackets != 1 {
		t.Errorf("Expected 1 validated packet, got %d", pStats.ValidatedPackets)
	}
	if pStats.RejectedPackets != 1 {
		t.Errorf("Expected 1 rejected packet, got %d", pStats.RejectedPackets)
	}
	if pStats.DropsInvalidPacket != 1 {
		t.Errorf("Expected 1 drop by invalid packet, got %d", pStats.DropsInvalidPacket)
	}

	tStats := trans.GetStats()
	if tStats.TxPackets != 1 {
		t.Errorf("Expected 1 transport TxPacket, got %d", tStats.TxPackets)
	}
	if !tStats.IsTest {
		t.Errorf("Expected transport IsTest to be true")
	}

	// Test Reset
	pipe.ResetStats()
	trans.ResetStats()

	pStatsAfter := pipe.GetStats()
	if pStatsAfter.ValidatedPackets != 0 || pStatsAfter.TotalDrops != 0 {
		t.Errorf("Expected reset stats, got validated=%d drops=%d", pStatsAfter.ValidatedPackets, pStatsAfter.TotalDrops)
	}
}
