package multipath

import (
	"context"
	"net"
	"testing"
	"time"

	"dualflow-core/internal/packet"
)

// 1. Test Sequence Generator
func TestSequenceGenerator(t *testing.T) {
	seq := NewSequenceGenerator(100)
	if seq.Current() != 100 {
		t.Fatalf("Expected initial sequence 100, got %d", seq.Current())
	}
	s1 := seq.Next()
	s2 := seq.Next()
	if s1 != 101 || s2 != 102 {
		t.Fatalf("Expected 101 and 102, got %d and %d", s1, s2)
	}

	// Test wraparound distance
	diff := SequenceDistance(10, 5)
	if diff != 5 {
		t.Fatalf("Expected diff 5, got %d", diff)
	}
	if !IsNewerSequence(10, 5) {
		t.Fatal("Expected 10 to be newer than 5")
	}
	if IsNewerSequence(5, 10) {
		t.Fatal("Expected 5 not to be newer than 10")
	}
}

// 2. Test Deduplication Filter
func TestDedupFilter(t *testing.T) {
	dedup := NewDedupFilter(64)

	// First time packet 1 arrives
	if dedup.CheckAndRecord(1) {
		t.Fatal("Packet 1 should not be duplicate on first receipt")
	}

	// Second time packet 1 arrives -> MUST BE DUPLICATE
	if !dedup.CheckAndRecord(1) {
		t.Fatal("Packet 1 must be flagged as duplicate on second receipt")
	}

	// Packet 2 arrives
	if dedup.CheckAndRecord(2) {
		t.Fatal("Packet 2 should not be duplicate")
	}

	// Duplicate packet 2
	if !dedup.CheckAndRecord(2) {
		t.Fatal("Packet 2 should be duplicate")
	}

	// Advance window with packet 70
	if dedup.CheckAndRecord(70) {
		t.Fatal("Packet 70 should not be duplicate")
	}

	// Very old packet (seq 1) is now outside window -> should be dropped as duplicate/stale
	if !dedup.CheckAndRecord(1) {
		t.Fatal("Packet 1 outside window should be dropped as stale duplicate")
	}
}

// 3. Test Reorder Buffer: In-Order Delivery
func TestReorderBufferInOrder(t *testing.T) {
	rb := NewReorderBuffer(64, 50*time.Millisecond)

	out1 := rb.Insert(1, []byte("pkt1"), "path1")
	if len(out1) != 1 || string(out1[0]) != "pkt1" {
		t.Fatalf("Expected immediate in-order delivery of pkt1, got %v", out1)
	}

	out2 := rb.Insert(2, []byte("pkt2"), "path1")
	if len(out2) != 1 || string(out2[0]) != "pkt2" {
		t.Fatalf("Expected immediate in-order delivery of pkt2, got %v", out2)
	}
}

// 4. Test Reorder Buffer: Out-of-Order Assembly
func TestReorderBufferOutOfOrder(t *testing.T) {
	rb := NewReorderBuffer(64, 50*time.Millisecond)

	// 1 arrives
	out1 := rb.Insert(1, []byte("p1"), "path1")
	if len(out1) != 1 {
		t.Fatalf("Expected 1, got %d", len(out1))
	}

	// 3 arrives before 2!
	out3 := rb.Insert(3, []byte("p3"), "path2")
	if len(out3) != 0 {
		t.Fatalf("Packet 3 should be buffered awaiting 2, but returned %d", len(out3))
	}

	// 4 arrives before 2!
	out4 := rb.Insert(4, []byte("p4"), "path2")
	if len(out4) != 0 {
		t.Fatalf("Packet 4 should be buffered, but returned %d", len(out4))
	}

	// 2 finally arrives -> Should flush 2, 3, 4 together!
	out2 := rb.Insert(2, []byte("p2"), "path1")
	if len(out2) != 3 {
		t.Fatalf("Expected batch flush of 3 packets [p2, p3, p4], got %d", len(out2))
	}

	if string(out2[0]) != "p2" || string(out2[1]) != "p3" || string(out2[2]) != "p4" {
		t.Fatalf("Packets delivered out of order: %s, %s, %s", out2[0], out2[1], out2[2])
	}
}

// 5. Test Reorder Buffer: Timeout Missing Packet Release
func TestReorderBufferTimeout(t *testing.T) {
	rb := NewReorderBuffer(64, 20*time.Millisecond)

	// 1 arrives
	rb.Insert(1, []byte("p1"), "path1")

	// Packet 2 is lost! Packet 3 arrives.
	out3 := rb.Insert(3, []byte("p3"), "path2")
	if len(out3) != 0 {
		t.Fatal("Packet 3 should be buffered")
	}

	// Wait for timeout
	time.Sleep(30 * time.Millisecond)

	// Check timeout release
	flushed := rb.CheckTimeout()
	if len(flushed) != 1 || string(flushed[0]) != "p3" {
		t.Fatalf("Expected packet 3 to be released after timeout, got %v", flushed)
	}
}

// 6. Test Round-Robin Scheduler
func TestRoundRobinScheduler(t *testing.T) {
	sched := NewRoundRobinScheduler()

	p1 := NewPath("modem", "Modem", 13, "eth", "desc", "192.168.1.10", "192.168.1.1")
	p1.SetState(StateAvailable)
	p1.socket = &PathSocket{isOpen: true}

	p2 := NewPath("android", "Android", 20, "rndis", "desc", "192.168.217.231", "192.168.217.128")
	p2.SetState(StateAvailable)
	p2.socket = &PathSocket{isOpen: true}

	paths := []*Path{p1, p2}
	pkt := &packet.Packet{}

	s1, err := sched.SelectPaths(pkt, paths)
	if err != nil || s1[0].ID != "modem" {
		t.Fatalf("Expected modem first, got %v (err: %v)", s1, err)
	}

	s2, err := sched.SelectPaths(pkt, paths)
	if err != nil || s2[0].ID != "android" {
		t.Fatalf("Expected android second, got %v (err: %v)", s2, err)
	}

	s3, err := sched.SelectPaths(pkt, paths)
	if err != nil || s3[0].ID != "modem" {
		t.Fatalf("Expected modem third, got %v (err: %v)", s3, err)
	}

	// Path 2 becomes unavailable -> should strictly route to Path 1
	p2.SetState(StateUnavailable)
	s4, err := sched.SelectPaths(pkt, paths)
	if err != nil || s4[0].ID != "modem" {
		t.Fatalf("Expected fallback to modem when android unavailable, got %v", s4)
	}
}

// 7. Test Weighted Round-Robin Scheduler
func TestWeightedRRScheduler(t *testing.T) {
	sched := NewWeightedRRScheduler(map[string]uint32{
		"modem":   3,
		"android": 1,
	})

	p1 := NewPath("modem", "Modem", 13, "eth", "desc", "192.168.1.10", "192.168.1.1")
	p1.SetState(StateAvailable)
	p1.socket = &PathSocket{isOpen: true}

	p2 := NewPath("android", "Android", 20, "rndis", "desc", "192.168.217.231", "192.168.217.128")
	p2.SetState(StateAvailable)
	p2.socket = &PathSocket{isOpen: true}

	paths := []*Path{p1, p2}
	pkt := &packet.Packet{}

	counts := make(map[string]int)
	for i := 0; i < 40; i++ {
		selected, err := sched.SelectPaths(pkt, paths)
		if err != nil {
			t.Fatalf("SelectPaths failed: %v", err)
		}
		counts[selected[0].ID]++
	}

	// With 3:1 ratio, modem should get roughly 30 packets, android roughly 10
	if counts["modem"] < 25 || counts["android"] < 8 {
		t.Fatalf("Expected ~3:1 distribution, got modem: %d, android: %d", counts["modem"], counts["android"])
	}
}

// 8. Test Lowest-Latency First Scheduler with Hysteresis
func TestLowestLatencyScheduler(t *testing.T) {
	sched := NewLowestLatencyScheduler(10.0) // 10ms hysteresis

	p1 := NewPath("modem", "Modem", 13, "eth", "desc", "192.168.1.10", "192.168.1.1")
	p1.SetState(StateAvailable)
	p1.socket = &PathSocket{isOpen: true}

	p2 := NewPath("android", "Android", 20, "rndis", "desc", "192.168.217.231", "192.168.217.128")
	p2.SetState(StateAvailable)
	p2.socket = &PathSocket{isOpen: true}

	paths := []*Path{p1, p2}
	pkt := &packet.Packet{}

	// Modem: 30ms, Android: 50ms -> Modem should be chosen
	sched.UpdateMetrics("modem", 30*time.Millisecond, 0)
	sched.UpdateMetrics("android", 50*time.Millisecond, 0)

	s1, _ := sched.SelectPaths(pkt, paths)
	if s1[0].ID != "modem" {
		t.Fatalf("Expected modem (30ms) to be selected, got %s", s1[0].ID)
	}

	// Android drops to 25ms (improvement of only 5ms, less than 10ms hysteresis)
	sched.UpdateMetrics("android", 25*time.Millisecond, 0)
	s2, _ := sched.SelectPaths(pkt, paths)
	if s2[0].ID != "modem" {
		t.Fatalf("Expected hysteresis to retain modem (delta 5ms < 10ms), got %s", s2[0].ID)
	}

	// Android drops to 15ms (improvement of 15ms > 10ms hysteresis) -> Switch to Android!
	sched.UpdateMetrics("android", 15*time.Millisecond, 0)
	s3, _ := sched.SelectPaths(pkt, paths)
	if s3[0].ID != "android" {
		t.Fatalf("Expected switch to android (delta 15ms > 10ms), got %s", s3[0].ID)
	}
}

// 9. Test Redundant Scheduler
func TestRedundantScheduler(t *testing.T) {
	sched := NewRedundantScheduler()

	p1 := NewPath("modem", "Modem", 13, "eth", "desc", "192.168.1.10", "192.168.1.1")
	p1.SetState(StateAvailable)
	p1.socket = &PathSocket{isOpen: true}

	p2 := NewPath("android", "Android", 20, "rndis", "desc", "192.168.217.231", "192.168.217.128")
	p2.SetState(StateAvailable)
	p2.socket = &PathSocket{isOpen: true}

	paths := []*Path{p1, p2}
	pkt := &packet.Packet{}

	selected, err := sched.SelectPaths(pkt, paths)
	if err != nil {
		t.Fatalf("SelectPaths failed: %v", err)
	}
	if len(selected) != 2 {
		t.Fatalf("Redundant mode must select ALL eligible paths (2), got %d", len(selected))
	}
}

// 10. Test Active Prober & Loopback Reflector
func TestProberAndReflector(t *testing.T) {
	reflector, err := NewLocalReflector("127.0.0.1")
	if err != nil {
		t.Fatalf("Failed to create local reflector: %v", err)
	}
	defer reflector.Close()

	prober := NewPathProber(50*time.Millisecond, 100*time.Millisecond)
	prober.SetTargetEndpoint(reflector.Addr())

	// Create path with loopback socket
	p := NewPath("loopback", "Loopback", 1, "lo", "loopback", "127.0.0.1", "")
	sock := &PathSocket{
		pathID:  "loopback",
		localIP: "127.0.0.1",
	}

	lAddr, _ := net.ResolveUDPAddr("udp4", "127.0.0.1:0")
	conn, err := net.ListenUDP("udp4", lAddr)
	if err != nil {
		t.Fatalf("ListenUDP failed: %v", err)
	}
	sock.conn = conn
	sock.isOpen = true
	p.socket = sock

	// Hook socket incoming data
	sock.SetInboundSink(func(data []byte, from *net.UDPAddr) {
		prober.HandleProbeReply(data, p)
	})
	sock.quitChan = make(chan struct{})
	sock.rxWorkerWg.Add(1)
	go sock.rxLoop(conn, sock.quitChan)
	defer sock.Close()

	p.SetState(StateAvailable)

	// Dispatch single probe
	prober.dispatchProbe(p)

	// Wait for reflection
	time.Sleep(30 * time.Millisecond)

	snap := p.Snapshot()
	if snap.ProbeSuccess == 0 {
		t.Fatalf("Expected probe success, got 0 (failures: %d)", snap.Drops)
	}
	if snap.RTTMs <= 0 {
		t.Fatalf("Expected positive RTT ms, got %f", snap.RTTMs)
	}
}

// 11. Test Multi-Path Engine Pipeline Integration
func TestMultiPathEnginePipeline(t *testing.T) {
	cfg := DefaultConfig()
	cfg.SchedulerMode = ModeRoundRobin
	cfg.ReorderTimeout = 30 * time.Millisecond

	engine := NewEngine(cfg, 0x44465450)

	var deliveredPackets [][]byte
	engine.SetInboundSink(func(raw []byte) {
		deliveredPackets = append(deliveredPackets, raw)
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := engine.Start(ctx); err != nil {
		t.Fatalf("Engine start failed: %v", err)
	}
	defer engine.Stop()

	status := engine.GetStatus()
	if status["state"] != "RUNNING" && status["state"] != "DEGRADED" {
		t.Fatalf("Expected running or degraded state, got %v", status["state"])
	}

	// Test dynamic mode switching
	if err := engine.SetSchedulerMode(ModeLowestLatency); err != nil {
		t.Fatalf("SetSchedulerMode failed: %v", err)
	}
	if engine.cfg.SchedulerMode != ModeLowestLatency {
		t.Fatalf("Expected ModeLowestLatency, got %v", engine.cfg.SchedulerMode)
	}

	if err := engine.SetSchedulerMode(ModeRedundant); err != nil {
		t.Fatalf("SetSchedulerMode failed: %v", err)
	}
	if engine.cfg.SchedulerMode != ModeRedundant {
		t.Fatalf("Expected ModeRedundant, got %v", engine.cfg.SchedulerMode)
	}
}

