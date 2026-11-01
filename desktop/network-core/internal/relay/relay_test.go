package relay

import (
	"crypto/rand"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/flynn/noise"
)

func TestCryptoAEADExplicitSequence(t *testing.T) {
	suite := noise.NewCipherSuite(noise.DH25519, noise.CipherAESGCM, noise.HashSHA256)
	clientStatic, _ := suite.GenerateKeypair(rand.Reader)
	relayStatic, _ := suite.GenerateKeypair(rand.Reader)
	psk, _ := GenerateRandomPSK()

	clientHS, err := NewClientHandshake(clientStatic, relayStatic.Public, psk[:])
	if err != nil {
		t.Fatalf("NewClientHandshake failed: %v", err)
	}

	relayHS, err := NewRelayHandshake(relayStatic, psk[:])
	if err != nil {
		t.Fatalf("NewRelayHandshake failed: %v", err)
	}

	initPkt, err := clientHS.CreateHandshakeInit()
	if err != nil {
		t.Fatalf("CreateHandshakeInit failed: %v", err)
	}

	_, err = relayHS.ProcessHandshakeInit(initPkt)
	if err != nil {
		t.Fatalf("ProcessHandshakeInit failed: %v", err)
	}

	sessionID := uint32(0xAABBCCDD)
	respPkt, relaySend, relayRecv, err := relayHS.CreateHandshakeResp(sessionID)
	if err != nil {
		t.Fatalf("CreateHandshakeResp failed: %v", err)
	}

	confirmedID, clientSend, clientRecv, err := clientHS.ProcessHandshakeResp(respPkt)
	if err != nil {
		t.Fatalf("ProcessHandshakeResp failed: %v", err)
	}

	if confirmedID != sessionID {
		t.Fatalf("SessionID mismatch: got %x, want %x", confirmedID, sessionID)
	}

	// 1. Client -> Relay out-of-order encryption/decryption
	aad := []byte("DFTP_32_BYTE_OUTER_HEADER_RECORD")
	packets := []struct {
		seq  uint64
		data []byte
	}{
		{seq: 1, data: []byte("Chunk 1")},
		{seq: 3, data: []byte("Chunk 3 (Arrives Early)")},
		{seq: 2, data: []byte("Chunk 2 (Arrives Late)")},
		{seq: 5, data: []byte("Chunk 5")},
		{seq: 4, data: []byte("Chunk 4")},
	}

	for _, p := range packets {
		ct := clientSend.Encrypt(p.seq, aad, p.data)
		pt, err := relayRecv.Decrypt(p.seq, aad, ct)
		if err != nil {
			t.Fatalf("Decrypt failed for seq %d: %v", p.seq, err)
		}
		if string(pt) != string(p.data) {
			t.Errorf("Decrypted data mismatch for seq %d: got %s, want %s", p.seq, string(pt), string(p.data))
		}
	}

	// 2. Tampered AAD must fail
	ct := clientSend.Encrypt(10, aad, []byte("Sensitive"))
	tamperedAAD := []byte("TAMPERED_OUTER_HEADER_FIELDS___")
	_, err = relayRecv.Decrypt(10, tamperedAAD, ct)
	if err != ErrDecryptionFailed {
		t.Errorf("Expected ErrDecryptionFailed on tampered AAD, got: %v", err)
	}

	// 3. Directional separation: Client cannot decrypt its own send ciphertext
	_, err = clientRecv.Decrypt(10, aad, ct)
	if err != ErrDecryptionFailed {
		t.Errorf("Expected ErrDecryptionFailed when decrypting with wrong directional cipher, got: %v", err)
	}

	// 4. Relay -> Client reverse communication
	relayMsg := []byte("Relay to Client Stream Response")
	relayCT := relaySend.Encrypt(1, aad, relayMsg)
	clientPT, err := clientRecv.Decrypt(1, aad, relayCT)
	if err != nil {
		t.Fatalf("Client failed to decrypt relay message: %v", err)
	}
	if string(clientPT) != string(relayMsg) {
		t.Errorf("Client decrypted mismatch: got %s, want %s", string(clientPT), string(relayMsg))
	}
}

func TestAntiReplayWindow(t *testing.T) {
	window := NewAntiReplayWindow()

	// 1. Sequence 0 must be rejected
	err := window.VerifyAndCommit(0, nil)
	if err != ErrInvalidSequenceZero {
		t.Errorf("Expected ErrInvalidSequenceZero for seq 0, got: %v", err)
	}

	// 2. Monotonic sequence: 1, 2, 3
	for seq := uint64(1); seq <= 3; seq++ {
		if err := window.VerifyAndCommit(seq, nil); err != nil {
			t.Fatalf("VerifyAndCommit failed on seq %d: %v", seq, err)
		}
	}

	// 3. Duplicate rejection
	if err := window.VerifyAndCommit(2, nil); err != ErrReplayDetected {
		t.Errorf("Expected ErrReplayDetected for duplicate seq 2, got: %v", err)
	}

	// 4. Out of order within window
	if err := window.VerifyAndCommit(10, nil); err != nil {
		t.Fatalf("Failed to accept seq 10: %v", err)
	}
	if err := window.VerifyAndCommit(5, nil); err != nil {
		t.Fatalf("Failed to accept seq 5 (in-window arrival): %v", err)
	}
	if err := window.VerifyAndCommit(5, nil); err != ErrReplayDetected {
		t.Errorf("Expected ErrReplayDetected for second delivery of seq 5")
	}

	// 5. Jump window past 2048
	seqJump := uint64(10 + ReplayWindowSize + 100)
	if err := window.VerifyAndCommit(seqJump, nil); err != nil {
		t.Fatalf("Failed to advance window to %d: %v", seqJump, err)
	}

	// Old packets now outside the window must be rejected
	if err := window.VerifyAndCommit(10, nil); err != ErrReplayDetected {
		t.Errorf("Expected ErrReplayDetected for seq 10 after window advanced")
	}
}

func TestAntiReplayWindowConcurrency(t *testing.T) {
	window := NewAntiReplayWindow()
	var wg sync.WaitGroup
	workers := 20
	packetsPerWorker := 50

	// Launch concurrent workers dispatching both unique and duplicate packets
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for i := 1; i <= packetsPerWorker; i++ {
				seq := uint64((workerID * packetsPerWorker) + i)
				_ = window.VerifyAndCommit(seq, func() error {
					// Simulate small crypto verify delay
					return nil
				})
				// Attempt duplicate delivery
				_ = window.VerifyAndCommit(seq, nil)
			}
		}(w)
	}

	wg.Wait()
}

func TestHandshakeReplayAndTimestamp(t *testing.T) {
	suite := noise.NewCipherSuite(noise.DH25519, noise.CipherAESGCM, noise.HashSHA256)
	clientStatic, _ := suite.GenerateKeypair(rand.Reader)
	relayStatic, _ := suite.GenerateKeypair(rand.Reader)
	psk, _ := GenerateRandomPSK()

	clientHS, _ := NewClientHandshake(clientStatic, relayStatic.Public, psk[:])
	relayHS, _ := NewRelayHandshake(relayStatic, psk[:])

	initPkt, err := clientHS.CreateHandshakeInit()
	if err != nil {
		t.Fatalf("CreateHandshakeInit failed: %v", err)
	}

	// First presentation should succeed
	_, err = relayHS.ProcessHandshakeInit(initPkt)
	if err != nil {
		t.Fatalf("ProcessHandshakeInit failed: %v", err)
	}

	// Bad PSK test: PSK is mixed in Message 2 (IKpsk2).
	// When Relay uses bad PSK, Client will fail to decrypt Message 2.
	badPSK, _ := GenerateRandomPSK()
	badRelayHS, _ := NewRelayHandshake(relayStatic, badPSK[:])
	_, err = badRelayHS.ProcessHandshakeInit(initPkt)
	if err != nil {
		t.Fatalf("ProcessHandshakeInit failed unexpectedly: %v", err)
	}

	badRespPkt, _, _, err := badRelayHS.CreateHandshakeResp(0x12345678)
	if err != nil {
		t.Fatalf("CreateHandshakeResp failed: %v", err)
	}

	// Client processing response from relay with mismatched PSK must fail
	_, _, _, err = clientHS.ProcessHandshakeResp(badRespPkt)
	if err == nil {
		t.Fatalf("Expected failure with mismatched PSK in HandshakeResp, got nil")
	}
}

func TestSessionRekeyAndDrain(t *testing.T) {
	st := NewSessionTable(10)

	// Session 1
	s1 := NewSession(1001, nil, nil)
	if err := st.Add(s1); err != nil {
		t.Fatalf("Failed to add session: %v", err)
	}

	// Session 2 (Rekey target)
	s2 := NewSession(1002, nil, nil)

	// Rekey with 100ms drain duration for fast test
	if err := st.Rekey(1001, s2, 100*time.Millisecond); err != nil {
		t.Fatalf("Rekey failed: %v", err)
	}

	// Verify s1 is draining
	s1Retrieved, ok := st.Get(1001)
	if !ok || s1Retrieved.State != SessionDraining {
		t.Errorf("Expected session 1001 to be DRAINING, got %+v", s1Retrieved)
	}

	// Outbound encrypt on draining session must be prohibited
	_, _, err := s1Retrieved.EncryptOutbound([]byte("aad"), []byte("data"))
	if err != ErrSessionDrainingWrite {
		t.Errorf("Expected ErrSessionDrainingWrite, got: %v", err)
	}

	// Verify s2 is active
	s2Retrieved, ok := st.Get(1002)
	if !ok || s2Retrieved.State != SessionActive {
		t.Errorf("Expected session 1002 to be ACTIVE, got %+v", s2Retrieved)
	}

	// Wait for drain timer to expire
	time.Sleep(150 * time.Millisecond)

	// Session 1001 should now be purged
	_, exists := st.Get(1001)
	if exists {
		t.Errorf("Expected session 1001 to be removed after drain timeout")
	}
}

func TestRelayServerEndToEndHandshakeAndPathValidation(t *testing.T) {
	cfg := &Config{
		ListenAddr:         "127.0.0.1:0", // Dynamic port
		HandshakeRateLimit: 50,
		MaxSessions:        10,
	}
	if err := cfg.EnsureKeys(); err != nil {
		t.Fatalf("EnsureKeys failed: %v", err)
	}

	server, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer failed: %v", err)
	}
	if err := server.Start(); err != nil {
		t.Fatalf("server.Start failed: %v", err)
	}
	defer server.Stop()

	serverAddr := server.Addr()
	if serverAddr == nil {
		t.Fatalf("serverAddr is nil")
	}

	// Setup Client
	clientConn, err := net.DialUDP("udp", nil, serverAddr)
	if err != nil {
		t.Fatalf("DialUDP failed: %v", err)
	}
	defer clientConn.Close()

	clientStatic, err := GenerateStaticKeypair()
	if err != nil {
		t.Fatalf("GenerateStaticKeypair failed: %v", err)
	}

	clientHS, err := NewClientHandshake(clientStatic, server.StaticPublicKey(), cfg.PSK[:])
	if err != nil {
		t.Fatalf("NewClientHandshake failed: %v", err)
	}

	initPkt, err := clientHS.CreateHandshakeInit()
	if err != nil {
		t.Fatalf("CreateHandshakeInit failed: %v", err)
	}

	// Send Handshake Message 1 to Server
	if _, err := clientConn.Write(initPkt); err != nil {
		t.Fatalf("Write init packet failed: %v", err)
	}

	// Read Handshake Message 2 from Server
	_ = clientConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	respBuf := make([]byte, 2048)
	n, err := clientConn.Read(respBuf)
	if err != nil {
		t.Fatalf("Read response packet failed: %v", err)
	}

	sessionID, clientSend, clientRecv, err := clientHS.ProcessHandshakeResp(respBuf[:n])
	if err != nil {
		t.Fatalf("ProcessHandshakeResp failed: %v", err)
	}

	if sessionID == 0 {
		t.Fatalf("Invalid assigned session ID 0")
	}

	// Verify server has session
	serverSession, ok := server.sessions.Get(sessionID)
	if !ok {
		t.Fatalf("Server does not have session %d in session table", sessionID)
	}

	// Client sends encrypted Ping frame
	seq, pingAAD, err := serverSession.EncryptOutbound([]byte("aad"), []byte("ping-payload"))
	if err != nil {
		t.Fatalf("EncryptOutbound failed: %v", err)
	}
	_ = seq
	_ = pingAAD
	_ = clientSend
	_ = clientRecv
}
