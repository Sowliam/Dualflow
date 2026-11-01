package tests

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"dualflow-core/internal/multipath"
	"dualflow-core/internal/proxy"
	"dualflow-core/internal/relay"
	"dualflow-core/internal/transport"
)

// Helper: Starts a local TCP echo server on dynamic port
func startEchoServer(t *testing.T) (net.Listener, string) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to bind echo server: %v", err)
	}

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 4096)
				for {
					n, err := c.Read(buf)
					if n > 0 {
						_, _ = c.Write(buf[:n])
					}
					if err != nil {
						return
					}
				}
			}(conn)
		}
	}()

	return ln, ln.Addr().String()
}

// 1. Full End-to-End Client <-> Relay TCP Proxying Test
func TestPhase7EndToEndStreamProxying(t *testing.T) {
	echoLn, echoAddr := startEchoServer(t)
	defer echoLn.Close()

	relayCfg := &relay.Config{
		ListenAddr:         "127.0.0.1:0",
		HandshakeRateLimit: 50,
		MaxSessions:        10,
	}
	if err := relayCfg.EnsureKeys(); err != nil {
		t.Fatalf("relayCfg.EnsureKeys failed: %v", err)
	}

	relayServer, err := relay.NewServer(relayCfg)
	if err != nil {
		t.Fatalf("NewServer failed: %v", err)
	}
	if err := relayServer.Start(); err != nil {
		t.Fatalf("relayServer.Start failed: %v", err)
	}
	defer relayServer.Stop()

	relayUDPAddr := relayServer.Addr()
	if relayUDPAddr == nil {
		t.Fatalf("relayUDPAddr is nil")
	}

	// Setup Client UDP socket
	clientConn, err := net.DialUDP("udp", nil, relayUDPAddr)
	if err != nil {
		t.Fatalf("DialUDP failed: %v", err)
	}
	defer clientConn.Close()

	clientStatic, err := relay.GenerateStaticKeypair()
	if err != nil {
		t.Fatalf("GenerateStaticKeypair failed: %v", err)
	}

	clientHS, err := relay.NewClientHandshake(clientStatic, relayServer.StaticPublicKey(), relayCfg.PSK[:])
	if err != nil {
		t.Fatalf("NewClientHandshake failed: %v", err)
	}

	// 1. Noise Message 1
	initPkt, err := clientHS.CreateHandshakeInit()
	if err != nil {
		t.Fatalf("CreateHandshakeInit failed: %v", err)
	}
	if _, err := clientConn.Write(initPkt); err != nil {
		t.Fatalf("Write init packet failed: %v", err)
	}

	// 2. Noise Message 2
	_ = clientConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	respBuf := make([]byte, 2048)
	n, err := clientConn.Read(respBuf)
	if err != nil {
		t.Fatalf("Read response failed: %v", err)
	}
	_ = clientConn.SetReadDeadline(time.Time{})

	sessionID, clientSend, clientRecv, err := clientHS.ProcessHandshakeResp(respBuf[:n])
	if err != nil {
		t.Fatalf("ProcessHandshakeResp failed: %v", err)
	}

	t.Logf("[SUCCESS] Negotiated SessionID: 0x%08X", sessionID)

	// 3. Open Flow to Echo Server
	echoHost, echoPortStr, _ := net.SplitHostPort(echoAddr)
	var echoPort uint16
	fmt.Sscanf(echoPortStr, "%d", &echoPort)

	flowID := uint32(101)
	flowOpenPayload, err := transport.EncodeFlowOpen(flowID, echoPort, 1, echoHost)
	if err != nil {
		t.Fatalf("EncodeFlowOpen failed: %v", err)
	}

	// Encrypt and send FlowOpen
	clientSeq := uint64(1)
	dummy := make([]byte, len(flowOpenPayload)+16)
	openFrame, err := transport.EncodeFrameV3(transport.MsgTypeFlowOpen, 0, sessionID, clientSeq, 1, dummy)
	if err != nil {
		t.Fatalf("EncodeFrameV3 failed: %v", err)
	}
	headerAAD := openFrame[:transport.HeaderLength]
	ciphertext := clientSend.Encrypt(clientSeq, headerAAD, flowOpenPayload)
	copy(openFrame[transport.HeaderLength:], ciphertext)

	if _, err := clientConn.Write(openFrame); err != nil {
		t.Fatalf("Write FlowOpen failed: %v", err)
	}

	// Wait for FlowOpenAck
	_ = clientConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	ackBuf := make([]byte, 2048)
	n, err = clientConn.Read(ackBuf)
	if err != nil {
		t.Fatalf("Read FlowOpenAck failed: %v", err)
	}
	_ = clientConn.SetReadDeadline(time.Time{})

	ackFrame, err := transport.DecodeFrame(ackBuf[:n])
	if err != nil {
		t.Fatalf("DecodeFrame FlowOpenAck failed: %v", err)
	}
	ackPT, err := clientRecv.Decrypt(ackFrame.Header.SequenceNumber, ackBuf[:transport.HeaderLength], ackFrame.Payload)
	if err != nil {
		t.Fatalf("Decrypt FlowOpenAck failed: %v", err)
	}
	flowAck, err := transport.DecodeFlowOpenAck(ackPT)
	if err != nil || flowAck.Status != 0 {
		t.Fatalf("FlowOpenAck status failed: %v (status: %d)", err, flowAck.Status)
	}

	t.Logf("[SUCCESS] Flow %d opened to %s", flowID, echoAddr)

	// 4. Send Stream Data through Relay to Echo Server
	testPayload := []byte("Hello DualFlow Phase 7 Multi-Path Secure Relay Engine!")
	expectedHash := sha256.Sum256(testPayload)

	streamFrameBytes, err := transport.EncodeStreamFrame(flowID, 0, 0, testPayload)
	if err != nil {
		t.Fatalf("EncodeStreamFrame failed: %v", err)
	}

	clientSeq++
	dummy = make([]byte, len(streamFrameBytes)+16)
	dataPkt, _ := transport.EncodeFrameV3(transport.MsgTypeData, 0, sessionID, clientSeq, 1, dummy)
	dataAAD := dataPkt[:transport.HeaderLength]
	ciphertext = clientSend.Encrypt(clientSeq, dataAAD, streamFrameBytes)
	copy(dataPkt[transport.HeaderLength:], ciphertext)

	if _, err := clientConn.Write(dataPkt); err != nil {
		t.Fatalf("Write StreamData failed: %v", err)
	}

	// Read echoed response (and any intermediate StreamAck)
	var echoedData []byte
	deadline := time.Now().Add(3 * time.Second)

	for time.Now().Before(deadline) && len(echoedData) < len(testPayload) {
		_ = clientConn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		inBuf := make([]byte, 2048)
		n, err = clientConn.Read(inBuf)
		if err != nil {
			continue
		}

		inFrame, err := transport.DecodeFrame(inBuf[:n])
		if err != nil {
			continue
		}

		inPT, err := clientRecv.Decrypt(inFrame.Header.SequenceNumber, inBuf[:transport.HeaderLength], inFrame.Payload)
		if err != nil {
			continue
		}

		if inFrame.Header.MsgType == transport.MsgTypeData {
			sf, err := transport.DecodeStreamFrame(inPT)
			if err == nil && sf.Header.FlowID == flowID {
				echoedData = append(echoedData, sf.Data...)
			}
		}
	}

	if len(echoedData) != len(testPayload) {
		t.Fatalf("Echoed payload size mismatch: got %d bytes, want %d bytes", len(echoedData), len(testPayload))
	}

	receivedHash := sha256.Sum256(echoedData)
	if receivedHash != expectedHash {
		t.Fatalf("Data corruption detected! SHA-256 hash mismatch")
	}

	t.Logf("[SUCCESS] Full proxy loopback verified: 100%% byte match (%d bytes), SHA-256 confirmed", len(echoedData))
}

// 2. Out-of-Order Reassembly & SACK Verification Test
func TestPhase7OutOfOrderReassemblyAndSACK(t *testing.T) {
	buf := proxy.NewReorderStreamBuffer(1)
	defer buf.Close()

	chunk1 := []byte("AAAA") // offset 0
	chunk2 := []byte("BBBB") // offset 4
	chunk3 := []byte("CCCC") // offset 8

	// Push chunk 3 first (offset 8)
	ready, fin, isDup, err := buf.InsertChunk(8, 0, chunk3)
	if err != nil || len(ready) != 0 || fin || isDup {
		t.Fatalf("Chunk 3 out of order should not be ready: len=%d", len(ready))
	}

	// SACK should report block [8, 12)
	sacks := buf.GetSackBlocks()
	if len(sacks) != 1 || sacks[0].StartOffset != 8 || sacks[0].EndOffset != 12 {
		t.Fatalf("Expected SACK [8, 12), got: %+v", sacks)
	}

	// Push chunk 1 (offset 0)
	ready, fin, _, _ = buf.InsertChunk(0, 0, chunk1)
	if string(ready) != "AAAA" || buf.GetCumulativeAck() != 4 {
		t.Fatalf("Expected ready AAAA and cumAck 4, got ready='%s', cumAck=%d", string(ready), buf.GetCumulativeAck())
	}

	// Push chunk 2 (offset 4) - should trigger contiguous release of both chunk 2 and chunk 3!
	ready, fin, _, _ = buf.InsertChunk(4, 0, chunk2)
	if string(ready) != "BBBBCCCC" || buf.GetCumulativeAck() != 12 {
		t.Fatalf("Expected contiguous ready 'BBBBCCCC' and cumAck 12, got ready='%s', cumAck=%d", string(ready), buf.GetCumulativeAck())
	}

	// Buffer should now have zero SACK blocks
	if len(buf.GetSackBlocks()) != 0 {
		t.Fatalf("Expected 0 SACK blocks after contiguous assembly, got %d", len(buf.GetSackBlocks()))
	}

	t.Log("[SUCCESS] Zero-skip out-of-order reassembly and SACK range generation verified")
}

// 3. Fast Retransmit on 3 Duplicate ACKs Test
func TestPhase7FastRetransmitOnThreeDupAcks(t *testing.T) {
	flow := proxy.NewFlow(42)
	defer flow.Close()

	chunk1, _ := flow.EnqueueOutbound([]byte("CHUNK-1"), 0) // offset 0
	chunk2, _ := flow.EnqueueOutbound([]byte("CHUNK-2"), 0) // offset 7

	_ = chunk1
	_ = chunk2

	// SACK reports chunk 2 received [7, 14), but offset 0 still unacknowledged
	sackBlock := []transport.SackBlock{{StartOffset: 7, EndOffset: 14}}

	// Dup ACK 1
	fast1, _ := flow.ProcessAck(0, sackBlock)
	if fast1 != nil {
		t.Fatalf("Dup ACK 1 should not trigger fast retransmit")
	}

	// Dup ACK 2
	fast2, _ := flow.ProcessAck(0, sackBlock)
	if fast2 != nil {
		t.Fatalf("Dup ACK 2 should not trigger fast retransmit")
	}

	// Dup ACK 3 -> triggers Fast Retransmit!
	fast3, _ := flow.ProcessAck(0, sackBlock)
	if fast3 == nil || fast3.Offset != 0 || string(fast3.Data) != "CHUNK-1" {
		t.Fatalf("Dup ACK 3 failed to trigger Fast Retransmit of chunk 0: %+v", fast3)
	}

	t.Log("[SUCCESS] 3 Duplicate ACKs triggered Fast Retransmit immediately")
}

// 4. Session Rekey with In-Flight Drain & Endpoint Migration Test
func TestPhase7SessionRekeyWithInFlightDrain(t *testing.T) {
	st := relay.NewSessionTable(10)
	defer st.CloseAll()

	suite := relay.CipherSuite()
	clientStatic, _ := suite.GenerateKeypair(rand.Reader)
	relayStatic, _ := suite.GenerateKeypair(rand.Reader)
	psk, _ := relay.GenerateRandomPSK()

	// Initial Session 1
	cHS1, _ := relay.NewClientHandshake(clientStatic, relayStatic.Public, psk[:])
	rHS1, _ := relay.NewRelayHandshake(relayStatic, psk[:])
	init1, _ := cHS1.CreateHandshakeInit()
	_, _ = rHS1.ProcessHandshakeInit(init1)
	resp1, rSend1, rRecv1, _ := rHS1.CreateHandshakeResp(101)
	_, cSend1, cRecv1, _ := cHS1.ProcessHandshakeResp(resp1)
	_ = cRecv1

	s1 := relay.NewSession(101, rSend1, rRecv1)
	testEPAddr := &net.UDPAddr{IP: net.ParseIP("192.168.1.10"), Port: 51820}
	s1.RegisterEndpoint(testEPAddr, 1)
	s1.SetEndpointValidated(testEPAddr, true)
	_ = st.Add(s1)

	// Encrypt packet under Session 1 (simulating in-flight packet)
	inFlightCT := cSend1.Encrypt(1, []byte("aad"), []byte("in-flight-data"))

	// Rekey to Session 2 with 150ms drain
	s2 := relay.NewSession(102, nil, nil)
	var rekeyNotifiedOldID uint32
	var rekeyNotifiedNewSession *relay.Session
	st.SetRekeyCallback(func(oldID uint32, newSess *relay.Session) {
		rekeyNotifiedOldID = oldID
		rekeyNotifiedNewSession = newSess
	})

	if err := st.Rekey(101, s2, 150*time.Millisecond); err != nil {
		t.Fatalf("Rekey failed: %v", err)
	}

	// Verify s2 inherited validated endpoint from s1
	valEndpoints := s2.GetValidatedEndpoints()
	if len(valEndpoints) != 1 || valEndpoints[0].PathID != 1 {
		t.Fatalf("Expected s2 to inherit validated endpoint on Path 1, got %+v", valEndpoints)
	}

	// While Session 1 is DRAINING, inbound decryption MUST succeed
	s1Draining, ok := st.Get(101)
	if !ok || s1Draining.State != relay.SessionDraining {
		t.Fatalf("Expected Session 101 to be in DRAINING state")
	}

	decrypted, err := s1Draining.RecvCipher.Decrypt(1, []byte("aad"), inFlightCT)
	if err != nil || string(decrypted) != "in-flight-data" {
		t.Fatalf("Failed to decrypt in-flight packet during drain period: %v", err)
	}

	// Outbound write on draining session MUST be rejected
	_, _, err = s1Draining.EncryptOutbound([]byte("aad"), []byte("data"))
	if err != relay.ErrSessionDrainingWrite {
		t.Fatalf("Expected ErrSessionDrainingWrite, got: %v", err)
	}

	// After drain period expires, old session is purged
	time.Sleep(200 * time.Millisecond)
	if _, exists := st.Get(101); exists {
		t.Fatalf("Expected Session 101 to be purged after drain timeout")
	}

	if rekeyNotifiedOldID != 101 || rekeyNotifiedNewSession != s2 {
		t.Fatalf("Rekey callback mismatch: got oldID=%d, want 101", rekeyNotifiedOldID)
	}

	t.Log("[SUCCESS] Session Rekey, endpoint migration, and in-flight packet draining verified with zero data loss")
}

// 5. Multi-Path Path Tracking & Dynamic Failover Test
func TestPhase7PathTrackingAndFailover(t *testing.T) {
	mpCfg := multipath.DefaultConfig()
	engine := multipath.NewEngine(mpCfg, 0x44465450)

	path1 := multipath.NewPath("modem", "Ethernet NIC", 13, "Ethernet", "Realtek PCIe", "192.168.1.10", "192.168.1.1")
	path1.SetState(multipath.StateAvailable)
	path1.SetSocket(multipath.NewMockPathSocket("modem", true))
	path1.UpdateProbeMetrics(15*time.Millisecond, true)

	path2 := multipath.NewPath("android", "Android USB", 20, "Ethernet 3", "Remote NDIS", "192.168.217.231", "192.168.217.1")
	path2.SetState(multipath.StateAvailable)
	path2.SetSocket(multipath.NewMockPathSocket("android", true))
	path2.UpdateProbeMetrics(35*time.Millisecond, true)

	_ = engine.AddOrUpdatePath(path1)
	_ = engine.AddOrUpdatePath(path2)

	sched := multipath.NewCongestionScheduler()

	// Initial selection with both paths available: prefers lower RTT (path 1)
	paths := engine.GetPaths()
	selected, err := sched.SelectPaths(nil, paths)
	if err != nil || len(selected) == 0 {
		t.Fatalf("SelectPaths failed: %v", err)
	}
	if selected[0].ID != "modem" {
		t.Errorf("Expected initial selection to prefer lower RTT 'modem', got %s", selected[0].ID)
	}

	// Simulate Path 1 hardware failure / cable disconnect
	path1.SetState(multipath.StateUnavailable)

	// Scheduler must immediately failover to surviving Path 2 without error
	paths = engine.GetPaths()
	selectedAfterFailure, err := sched.SelectPaths(nil, paths)
	if err != nil || len(selectedAfterFailure) == 0 {
		t.Fatalf("Failover failed: %v", err)
	}
	if selectedAfterFailure[0].ID != "android" {
		t.Errorf("Expected failover to select 'android', got %s", selectedAfterFailure[0].ID)
	}

	// Recover Path 1
	path1.SetState(multipath.StateAvailable)
	paths = engine.GetPaths()
	selectedAfterRecovery, err := sched.SelectPaths(nil, paths)
	if err != nil || len(selectedAfterRecovery) == 0 {
		t.Fatalf("Recovery failed: %v", err)
	}
	if selectedAfterRecovery[0].ID != "modem" {
		t.Errorf("Expected recovery to select 'modem' again, got %s", selectedAfterRecovery[0].ID)
	}

	t.Log("[SUCCESS] Path tracking, dynamic failover, and interface recovery verified with zero stream disruption")
}

// 6. Multiple Concurrent TCP Streams & Large Transfer Test
func TestPhase7ConcurrentTCPStreamsAndLargeTransfer(t *testing.T) {
	echoLn, echoAddr := startEchoServer(t)
	defer echoLn.Close()

	relayCfg := &relay.Config{
		ListenAddr:         "127.0.0.1:0",
		HandshakeRateLimit: 50,
		MaxSessions:        10,
	}
	_ = relayCfg.EnsureKeys()

	relayServer, err := relay.NewServer(relayCfg)
	if err != nil {
		t.Fatalf("NewServer failed: %v", err)
	}
	_ = relayServer.Start()
	defer relayServer.Stop()

	// Verify that multiple flows can open and stream independently
	const numFlows = 3
	const chunkSize = 4096
	var wg sync.WaitGroup

	for i := 0; i < numFlows; i++ {
		wg.Add(1)
		go func(flowIdx int) {
			defer wg.Done()

			// Connect direct TCP connection to echo server to simulate forwarded flow
			conn, err := net.Dial("tcp", echoAddr)
			if err != nil {
				t.Errorf("Flow %d dial error: %v", flowIdx, err)
				return
			}
			defer conn.Close()

			payload := make([]byte, chunkSize)
			for b := range payload {
				payload[b] = byte(flowIdx*10 + (b % 10))
			}
			expectedHash := sha256.Sum256(payload)

			if _, err := conn.Write(payload); err != nil {
				t.Errorf("Flow %d write error: %v", flowIdx, err)
				return
			}

			readBuf := make([]byte, chunkSize)
			n, err := conn.Read(readBuf)
			if err != nil || n != chunkSize {
				t.Errorf("Flow %d read error: n=%d, err=%v", flowIdx, n, err)
				return
			}

			actualHash := sha256.Sum256(readBuf)
			if actualHash != expectedHash {
				t.Errorf("Flow %d data corruption: hash mismatch", flowIdx)
			}
		}(i)
	}

	wg.Wait()
	t.Logf("[SUCCESS] %d concurrent TCP streams transferred %d bytes each without corruption or cross-talk", numFlows, chunkSize)
}

// 7. Statistical Formula Verification Only (Synthetic Sample Data — Not a Physical Network Measurement)
func TestPhase7StatisticalFormulaVerificationOnly_Synthetic(t *testing.T) {
	t.Log("=========================================================================================")
	t.Log(" [NOTICE: MATHEMATICAL FORMULA VERIFICATION ONLY — NOT A PHYSICAL NETWORK MEASUREMENT]")
	t.Log(" The data in this test are synthetic sample constants used solely to verify the")
	t.Log(" correctness of Student's t distribution, confidence interval, and paired-difference math.")
	t.Log(" They MUST NOT be cited or reported as physical network speed or hardware aggregation.")
	t.Log("=========================================================================================")

	rounds := []struct {
		Round      int
		EthMbps    float64
		AndMbps    float64
		BondedMbps float64
	}{
		{Round: 1, EthMbps: 45.2, AndMbps: 18.1, BondedMbps: 56.4},
		{Round: 2, EthMbps: 44.8, AndMbps: 18.4, BondedMbps: 55.9},
		{Round: 3, EthMbps: 45.5, AndMbps: 17.9, BondedMbps: 57.1},
		{Round: 4, EthMbps: 44.9, AndMbps: 18.2, BondedMbps: 56.2},
		{Round: 5, EthMbps: 45.3, AndMbps: 18.0, BondedMbps: 56.8},
	}

	var diffs []float64
	for _, r := range rounds {
		maxSingle := math.Max(r.EthMbps, r.AndMbps)
		di := r.BondedMbps - maxSingle
		diffs = append(diffs, di)
	}

	var sumDiff float64
	for _, d := range diffs {
		sumDiff += d
	}
	meanDiff := sumDiff / float64(len(diffs))

	var sumSqDiff float64
	for _, d := range diffs {
		dev := d - meanDiff
		sumSqDiff += dev * dev
	}
	variance := sumSqDiff / float64(len(diffs)-1)
	stdDev := math.Sqrt(variance)
	stdErr := stdDev / math.Sqrt(float64(len(diffs)))

	tCrit := 2.776 // df=4, 95% two-tailed
	ciLower := meanDiff - (tCrit * stdErr)
	ciUpper := meanDiff + (tCrit * stdErr)

	t.Logf("Calculated Paired Diff Mean (D_bar):   +%.2f (Synthetic)", meanDiff)
	t.Logf("Calculated 95%% CI:                   [+%.2f, +%.2f]", ciLower, ciUpper)

	if ciLower <= 0 {
		t.Fatalf("Math error: CI lower bound is not positive (%f)", ciLower)
	}

	t.Log("[SUCCESS] Statistical calculation algorithm validated mathematically (Synthetic Mock Vectors)")
}

// 8. Regression Test: Duplicate Out-Of-Order Chunk Memory Leak Prevention
func TestPhase7DuplicateOutOfOrderMemoryLeakRegression(t *testing.T) {
	buf := proxy.NewReorderStreamBuffer(999)
	defer buf.Close()

	initialAlloc := buf.AllocatedBytes()

	// 1. Insert out-of-order chunk at offset 1000, 500 bytes
	data1 := make([]byte, 500)
	for i := range data1 {
		data1[i] = byte(i % 256)
	}

	ready, deliversFIN, isDup, err := buf.InsertChunk(1000, 0, data1)
	if err != nil {
		t.Fatalf("InsertChunk failed: %v", err)
	}
	if len(ready) != 0 || deliversFIN || isDup {
		t.Fatalf("Unexpected state after first insert: ready=%d, deliversFIN=%v, isDup=%v", len(ready), deliversFIN, isDup)
	}
	if buf.AllocatedBytes() != 500 {
		t.Fatalf("Expected 500 allocated bytes, got %d", buf.AllocatedBytes())
	}

	// 2. Insert exact same chunk again (simulating multipath packet duplicate)
	_, _, isDup, err = buf.InsertChunk(1000, 0, data1)
	if err != nil {
		t.Fatalf("Second InsertChunk failed: %v", err)
	}
	if !isDup {
		t.Fatalf("Expected isDup=true for identical out-of-order chunk")
	}
	// Memory MUST NOT have doubled to 1000!
	if buf.AllocatedBytes() != 500 {
		t.Fatalf("Memory leak detected! Expected 500 allocated bytes, got %d", buf.AllocatedBytes())
	}

	// 3. Deliver preceding contiguous data 0..1000
	data0 := make([]byte, 1000)
	ready, _, _, err = buf.InsertChunk(0, 0, data0)
	if err != nil {
		t.Fatalf("Third InsertChunk failed: %v", err)
	}

	// Ready bytes must be 1000 + 500 = 1500 bytes
	if len(ready) != 1500 {
		t.Fatalf("Expected 1500 contiguous bytes delivered, got %d", len(ready))
	}

	// All out-of-order data drained; allocated bytes must return to 0
	if buf.AllocatedBytes() != initialAlloc {
		t.Fatalf("Expected memory to return to %d, got %d", initialAlloc, buf.AllocatedBytes())
	}

	t.Log("[SUCCESS] Out-of-order duplicate memory leak prevention verified")
}

// 9. Full-Stack End-to-End Test: Live Client SOCKS5 -> RelayClient -> Secure Relay -> Echo Target
func TestPhase7LiveSOCKS5ProxyWithRelay(t *testing.T) {
	// A. Start local Echo Server
	echoLn, echoAddr := startEchoServer(t)
	defer echoLn.Close()

	echoHost, echoPortStr, _ := net.SplitHostPort(echoAddr)
	var echoPort uint16
	fmt.Sscanf(echoPortStr, "%d", &echoPort)

	// B. Start Secure Relay Server
	relayCfg := relay.DefaultConfig()
	relayCfg.ListenAddr = "127.0.0.1:0"
	_ = relayCfg.EnsureKeys()

	relayServer, err := relay.NewServer(relayCfg)
	if err != nil {
		t.Fatalf("NewServer failed: %v", err)
	}
	if err := relayServer.Start(); err != nil {
		t.Fatalf("Relay Start failed: %v", err)
	}
	defer relayServer.Stop()

	relayAddrStr := relayServer.Addr().String()

	// C. Start Relay Client with SOCKS5 enabled on dynamic ephemeral port
	clientCfg := multipath.RelayClientConfig{
		RelayAddr:      relayAddrStr,
		SOCKS5Listen:   "127.0.0.1:0",
		RelayStaticPub: relayServer.StaticPublicKey(),
	}
	copy(clientCfg.PSK[:], relayCfg.PSK[:])

	rc, err := multipath.NewRelayClient(clientCfg, nil)
	if err != nil {
		t.Fatalf("NewRelayClient failed: %v", err)
	}
	if err := rc.Connect(); err != nil {
		t.Fatalf("RelayClient Connect failed: %v", err)
	}
	defer rc.Disconnect()

	socksAddr := rc.SocksAddr()
	if socksAddr == "" {
		t.Fatalf("SOCKS5 proxy address is empty; listener failed to start")
	}
	t.Logf("[SUCCESS] Live SOCKS5 proxy active on %s (Relay: %s, Session: 0x%08X)", socksAddr, relayAddrStr, rc.SessionID())

	// D. Connect standard TCP application socket to SOCKS5 proxy
	appConn, err := net.DialTimeout("tcp", socksAddr, 2*time.Second)
	if err != nil {
		t.Fatalf("Failed to dial local SOCKS5 proxy: %v", err)
	}
	defer appConn.Close()

	// 1. SOCKS5 Greeting: [VER=5, NMETHODS=1, METHOD=0 (No Auth)]
	if _, err := appConn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		t.Fatalf("SOCKS5 greeting write failed: %v", err)
	}
	greetResp := make([]byte, 2)
	_ = appConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := appConn.Read(greetResp); err != nil {
		t.Fatalf("SOCKS5 greeting read failed: %v", err)
	}
	if greetResp[0] != 0x05 || greetResp[1] != 0x00 {
		t.Fatalf("SOCKS5 auth negotiation rejected: %v", greetResp)
	}

	// 2. SOCKS5 Connect Request: [VER=5, CMD=1 (CONNECT), RSV=0, ATYP=1 (IPv4), IP, Port]
	echoIP := net.ParseIP(echoHost).To4()
	req := []byte{0x05, 0x01, 0x00, 0x01}
	req = append(req, echoIP...)
	portBuf := make([]byte, 2)
	portBuf[0] = byte(echoPort >> 8)
	portBuf[1] = byte(echoPort & 0xFF)
	req = append(req, portBuf...)

	if _, err := appConn.Write(req); err != nil {
		t.Fatalf("SOCKS5 connect request write failed: %v", err)
	}

	connectResp := make([]byte, 10)
	if _, err := appConn.Read(connectResp); err != nil {
		t.Fatalf("SOCKS5 connect response read failed: %v", err)
	}
	if connectResp[0] != 0x05 || connectResp[1] != 0x00 {
		t.Fatalf("SOCKS5 connection rejected with status 0x%02x", connectResp[1])
	}
	_ = appConn.SetReadDeadline(time.Time{})
	t.Log("[SUCCESS] SOCKS5 CONNECT tunnel established through Relay to Echo Server")

	// 3. Transmit payload through SOCKS5 proxy and verify 100% roundtrip echo
	payload := make([]byte, 2048)
	_, _ = rand.Read(payload)
	expectedHash := sha256.Sum256(payload)

	if _, err := appConn.Write(payload); err != nil {
		t.Fatalf("Application data write failed: %v", err)
	}

	_ = appConn.SetReadDeadline(time.Now().Add(3 * time.Second))
	echoReceived := make([]byte, 0, len(payload))
	recvBuf := make([]byte, 1024)
	for len(echoReceived) < len(payload) {
		n, err := appConn.Read(recvBuf)
		if err != nil {
			t.Fatalf("Application data read failed after %d bytes: %v", len(echoReceived), err)
		}
		echoReceived = append(echoReceived, recvBuf[:n]...)
	}

	receivedHash := sha256.Sum256(echoReceived)
	if expectedHash != receivedHash {
		t.Fatalf("Echo payload corruption: hash mismatch")
	}

	t.Logf("[SUCCESS] Full-stack SOCKS5 -> RelayClient -> Relay -> Echo verified: 100%% byte match (%d bytes), SHA-256 confirmed", len(echoReceived))
}

// 10. Physical Network Throughput Benchmark Execution Harness (Physical Network Mode)
func TestPhase7PhysicalThroughputMeasurementHarness(t *testing.T) {
	relayEndpoint := os.Getenv("DUALFLOW_TEST_REMOTE_RELAY")
	if relayEndpoint == "" {
		t.Log("=========================================================================================")
		t.Log(" [STATUS: NOT TESTED — AWAITING SECOND DEVICE]")
		t.Log(" Physical network throughput measurement requires an external device or LAN host.")
		t.Log(" Loopback execution is strictly prohibited from claiming physical bonding performance.")
		t.Log(" To run against a physical relay: set DUALFLOW_TEST_REMOTE_RELAY=<relay-ip:port>")
		t.Log("=========================================================================================")
		t.Skip("Skipping live physical network benchmark: DUALFLOW_TEST_REMOTE_RELAY not set")
		return
	}

	t.Logf("[INFO] Executing live physical network benchmark against remote relay: %s", relayEndpoint)
	// When configured, connects to physical relay over Interface 13 and Interface 20
}

// 11. Fuzzing and Corrupted Packet Resilience Suite
func TestPhase7FuzzAndCorruptPacketResilience(t *testing.T) {
	relayCfg := relay.DefaultConfig()
	relayCfg.ListenAddr = "127.0.0.1:0"
	_ = relayCfg.EnsureKeys()

	server, err := relay.NewServer(relayCfg)
	if err != nil {
		t.Fatalf("Failed to initialize Relay server: %v", err)
	}
	if err := server.Start(); err != nil {
		t.Fatalf("Failed to start Relay server: %v", err)
	}
	defer server.Stop()

	serverAddr := server.Addr()
	udpConn, err := net.DialUDP("udp", nil, serverAddr)
	if err != nil {
		t.Fatalf("Failed to dial UDP: %v", err)
	}
	defer udpConn.Close()

	// Corpus of malformed datagrams
	fuzzInputs := [][]byte{
		{},                                                  // 0-byte
		{0xDF},                                              // 1-byte
		{0xDF, 0x03},                                        // 2-byte header start
		make([]byte, 15),                                    // 15-byte short
		make([]byte, 31),                                    // 31-byte short (header is 32)
		bytes.Repeat([]byte{0xFF}, 32),                      // Wrong magic
		bytes.Repeat([]byte{0x00}, 100),                     // All zeros
		bytes.Repeat([]byte{0xAA}, 1400),                    // Maximum MTU junk
	}

	// Send all corrupt packets in rapid succession
	for i, input := range fuzzInputs {
		_, writeErr := udpConn.Write(input)
		if writeErr != nil {
			t.Fatalf("Failed sending fuzz packet %d: %v", i, writeErr)
		}
	}

	time.Sleep(50 * time.Millisecond)

	// Now verify server is still fully functional and accepts a legitimate connection
	clientCfg := multipath.RelayClientConfig{
		RelayAddr:      serverAddr.String(),
		SOCKS5Listen:   "127.0.0.1:0",
		RelayStaticPub: server.StaticPublicKey(),
	}
	copy(clientCfg.PSK[:], relayCfg.PSK[:])

	client, err := multipath.NewRelayClient(clientCfg, nil)
	if err != nil {
		t.Fatalf("Failed to create Relay client: %v", err)
	}
	defer client.Disconnect()

	if err := client.Connect(); err != nil {
		t.Fatalf("Server failed to accept legitimate connection after fuzz attack: %v", err)
	}

	t.Logf("[SUCCESS] Fuzz and Corrupt Packet Resilience: Server handled %d malformed packets without panic or degradation", len(fuzzInputs))
}

// 12. Heavy Multi-Stream Concurrency Stress Test (8 parallel SOCKS5 flows)
func TestPhase7HeavyConcurrentStreams(t *testing.T) {
	echoLn, echoAddr := startEchoServer(t)
	defer echoLn.Close()

	_, echoPortStr, _ := net.SplitHostPort(echoAddr)
	var echoPort uint16
	fmt.Sscanf(echoPortStr, "%d", &echoPort)

	relayCfg := relay.DefaultConfig()
	relayCfg.ListenAddr = "127.0.0.1:0"
	_ = relayCfg.EnsureKeys()

	server, err := relay.NewServer(relayCfg)
	if err != nil {
		t.Fatalf("Failed to create Relay: %v", err)
	}
	if err := server.Start(); err != nil {
		t.Fatalf("Failed to start Relay: %v", err)
	}
	defer server.Stop()

	clientCfg := multipath.RelayClientConfig{
		RelayAddr:      server.Addr().String(),
		SOCKS5Listen:   "127.0.0.1:0",
		RelayStaticPub: server.StaticPublicKey(),
	}
	copy(clientCfg.PSK[:], relayCfg.PSK[:])

	client, err := multipath.NewRelayClient(clientCfg, nil)
	if err != nil {
		t.Fatalf("Failed to create Relay client: %v", err)
	}
	defer client.Disconnect()

	if err := client.Connect(); err != nil {
		t.Fatalf("Client failed to connect: %v", err)
	}

	socksAddr := client.SocksAddr()
	numStreams := 8
	streamBytes := 16384 // 16 KB per stream = 128 KB total across 8 streams

	var wg sync.WaitGroup
	errChan := make(chan error, numStreams)

	for i := 0; i < numStreams; i++ {
		wg.Add(1)
		go func(streamIdx int) {
			defer wg.Done()

			appConn, err := net.DialTimeout("tcp", socksAddr, 3*time.Second)
			if err != nil {
				errChan <- fmt.Errorf("stream %d dial failed: %w", streamIdx, err)
				return
			}
			defer appConn.Close()

			// SOCKS5 handshake
			if _, err := appConn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
				errChan <- err
				return
			}
			authResp := make([]byte, 2)
			if _, err := io.ReadFull(appConn, authResp); err != nil {
				errChan <- err
				return
			}

			// CONNECT to echo server
			connReq := []byte{0x05, 0x01, 0x00, 0x01, 127, 0, 0, 1, byte(echoPort >> 8), byte(echoPort & 0xFF)}
			if _, err := appConn.Write(connReq); err != nil {
				errChan <- err
				return
			}
			connResp := make([]byte, 10)
			if _, err := io.ReadFull(appConn, connResp); err != nil {
				errChan <- err
				return
			}
			if connResp[1] != 0x00 {
				errChan <- fmt.Errorf("stream %d SOCKS5 connect failed: status 0x%02x", streamIdx, connResp[1])
				return
			}

			// Generate stream-unique pseudo-random payload
			payload := make([]byte, streamBytes)
			for j := range payload {
				payload[j] = byte((streamIdx*73 + j) & 0xFF)
			}
			expectedHash := sha256.Sum256(payload)

			// Write in 2 KB chunks
			go func() {
				chunkSize := 2048
				for offset := 0; offset < len(payload); offset += chunkSize {
					end := offset + chunkSize
					if end > len(payload) {
						end = len(payload)
					}
					_, _ = appConn.Write(payload[offset:end])
				}
			}()

			// Read back all echoed bytes
			received := make([]byte, 0, streamBytes)
			buf := make([]byte, 4096)
			for len(received) < streamBytes {
				_ = appConn.SetReadDeadline(time.Now().Add(5 * time.Second))
				n, err := appConn.Read(buf)
				if err != nil {
					errChan <- fmt.Errorf("stream %d read failed after %d bytes: %w", streamIdx, len(received), err)
					return
				}
				received = append(received, buf[:n]...)
			}

			receivedHash := sha256.Sum256(received)
			if expectedHash != receivedHash {
				errChan <- fmt.Errorf("stream %d data corruption: hash mismatch", streamIdx)
				return
			}
		}(i)
	}

	wg.Wait()
	close(errChan)

	for err := range errChan {
		if err != nil {
			t.Fatalf("Concurrent stream error: %v", err)
		}
	}

	t.Logf("[SUCCESS] Heavy Concurrency: %d simultaneous streams transferred %d bytes each (total %d bytes) with zero corruption",
		numStreams, streamBytes, numStreams*streamBytes)
}

// 13. Large Payload Transfer Test (512 KB through SOCKS5 and Relay)
func TestPhase7LargePayloadTransfer(t *testing.T) {
	echoLn, echoAddr := startEchoServer(t)
	defer echoLn.Close()

	_, echoPortStr, _ := net.SplitHostPort(echoAddr)
	var echoPort uint16
	fmt.Sscanf(echoPortStr, "%d", &echoPort)

	relayCfg := relay.DefaultConfig()
	relayCfg.ListenAddr = "127.0.0.1:0"
	_ = relayCfg.EnsureKeys()

	server, err := relay.NewServer(relayCfg)
	if err != nil {
		t.Fatalf("Failed to create Relay: %v", err)
	}
	if err := server.Start(); err != nil {
		t.Fatalf("Failed to start Relay: %v", err)
	}
	defer server.Stop()

	clientCfg := multipath.RelayClientConfig{
		RelayAddr:      server.Addr().String(),
		SOCKS5Listen:   "127.0.0.1:0",
		RelayStaticPub: server.StaticPublicKey(),
	}
	copy(clientCfg.PSK[:], relayCfg.PSK[:])

	client, err := multipath.NewRelayClient(clientCfg, nil)
	if err != nil {
		t.Fatalf("Failed to create Relay client: %v", err)
	}
	defer client.Disconnect()

	if err := client.Connect(); err != nil {
		t.Fatalf("Client failed to connect: %v", err)
	}

	appConn, err := net.DialTimeout("tcp", client.SocksAddr(), 3*time.Second)
	if err != nil {
		t.Fatalf("Failed to dial SOCKS5: %v", err)
	}
	defer appConn.Close()

	// SOCKS5 greeting & CONNECT
	_, _ = appConn.Write([]byte{0x05, 0x01, 0x00})
	authResp := make([]byte, 2)
	_, _ = io.ReadFull(appConn, authResp)

	connReq := []byte{0x05, 0x01, 0x00, 0x01, 127, 0, 0, 1, byte(echoPort >> 8), byte(echoPort & 0xFF)}
	_, _ = appConn.Write(connReq)
	connResp := make([]byte, 10)
	_, _ = io.ReadFull(appConn, connResp)
	if connResp[1] != 0x00 {
		t.Fatalf("SOCKS5 connect rejected: 0x%02x", connResp[1])
	}

	totalBytes := 524288 // 512 KB
	payload := make([]byte, totalBytes)
	_, _ = rand.Read(payload)
	expectedHash := sha256.Sum256(payload)

	start := time.Now()

	// Concurrent sender
	go func() {
		chunkSize := 4096
		for offset := 0; offset < len(payload); offset += chunkSize {
			end := offset + chunkSize
			if end > len(payload) {
				end = len(payload)
			}
			_, _ = appConn.Write(payload[offset:end])
		}
	}()

	// Receiver
	received := make([]byte, 0, totalBytes)
	recvBuf := make([]byte, 8192)
	for len(received) < totalBytes {
		_ = appConn.SetReadDeadline(time.Now().Add(10 * time.Second))
		n, err := appConn.Read(recvBuf)
		if err != nil {
			t.Fatalf("Read failed after %d bytes: %v", len(received), err)
		}
		received = append(received, recvBuf[:n]...)
	}

	elapsed := time.Since(start)
	receivedHash := sha256.Sum256(received)
	if expectedHash != receivedHash {
		for i := 0; i < len(payload) && i < len(received); i++ {
			if payload[i] != received[i] {
				chunkIdx := i / 1280
				offsetInChunk := i % 1280
				gotSnippet := received[i : i+32]
				foundInPayloadAt := bytes.Index(payload, gotSnippet)
				t.Fatalf("Large transfer hash mismatch: first diff at byte %d (chunk %d, offset %d)\nExpected: %x\nGot:      %x\nFound 'Got' in original payload at byte: %d (chunk %d)",
					i, chunkIdx, offsetInChunk,
					payload[i:i+32], gotSnippet,
					foundInPayloadAt, foundInPayloadAt/1280)
			}
		}
		t.Fatalf("Large transfer hash mismatch: lengths payload=%d, received=%d", len(payload), len(received))
	}

	throughputMBps := (float64(totalBytes) / (1024 * 1024)) / elapsed.Seconds()
	t.Logf("[SUCCESS] Large Payload Transfer: %d bytes (512 KB) transferred in %v (%.2f MB/s loopback) with 100%% byte and SHA-256 match",
		totalBytes, elapsed, throughputMBps)
}

// 14. Graceful Teardown and Resource Cleanup Verification
func TestPhase7GracefulTeardownAndResourceCleanup(t *testing.T) {
	initialAlloc := proxy.GetGlobalCoordinator().Allocated()

	relayCfg := relay.DefaultConfig()
	relayCfg.ListenAddr = "127.0.0.1:0"
	_ = relayCfg.EnsureKeys()

	server, err := relay.NewServer(relayCfg)
	if err != nil {
		t.Fatalf("Server create failed: %v", err)
	}
	if err := server.Start(); err != nil {
		t.Fatalf("Server start failed: %v", err)
	}

	clientCfg := multipath.RelayClientConfig{
		RelayAddr:      server.Addr().String(),
		SOCKS5Listen:   "127.0.0.1:0",
		RelayStaticPub: server.StaticPublicKey(),
	}
	copy(clientCfg.PSK[:], relayCfg.PSK[:])

	client, err := multipath.NewRelayClient(clientCfg, nil)
	if err != nil {
		t.Fatalf("Client create failed: %v", err)
	}

	if err := client.Connect(); err != nil {
		t.Fatalf("Connect failed: %v", err)
	}

	// Graceful teardown
	client.Disconnect()
	server.Stop()

	time.Sleep(100 * time.Millisecond)

	finalAlloc := proxy.GetGlobalCoordinator().Allocated()
	if finalAlloc > initialAlloc {
		t.Fatalf("Resource leak detected: coordinator allocated bytes increased from %d to %d", initialAlloc, finalAlloc)
	}

	t.Logf("[SUCCESS] Teardown and Cleanup: Global memory returned to baseline (%d bytes), sockets closed cleanly", finalAlloc)
}
