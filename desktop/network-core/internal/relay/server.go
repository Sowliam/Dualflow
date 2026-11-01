package relay

import (
	"crypto/rand"
	"errors"
	"fmt"
	"log"
	"net"
	"sync"
	"time"

	"dualflow-core/internal/transport"
	"github.com/flynn/noise"
)

// TokenBucket implements a simple concurrency-safe token bucket rate limiter
type TokenBucket struct {
	mu         sync.Mutex
	rate       float64
	capacity   float64
	tokens     float64
	lastRefill time.Time
}

func NewTokenBucket(rate, capacity float64) *TokenBucket {
	return &TokenBucket{
		rate:       rate,
		capacity:   capacity,
		tokens:     capacity,
		lastRefill: time.Now(),
	}
}

func (tb *TokenBucket) Allow() bool {
	tb.mu.Lock()
	defer tb.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(tb.lastRefill).Seconds()
	tb.lastRefill = now

	tb.tokens += elapsed * tb.rate
	if tb.tokens > tb.capacity {
		tb.tokens = tb.capacity
	}

	if tb.tokens >= 1.0 {
		tb.tokens -= 1.0
		return true
	}
	return false
}

type incomingPacket struct {
	packet []byte
	raddr  *net.UDPAddr
}

// Server is the DualFlow Secure Relay server instance
type Server struct {
	cfg            *Config
	conn           *net.UDPConn
	sessions       *SessionTable
	forwarder      *RelayForwarder
	staticKey      noise.DHKey
	limiter        *TokenBucket
	handshakeCache *HandshakeCache
	packetChan     chan incomingPacket
	workersWg      sync.WaitGroup
	running        bool
	mu             sync.Mutex
	stopChan       chan struct{}
	closeOnce      sync.Once
}

// NewServer creates a new Relay server from configuration
func NewServer(cfg *Config) (*Server, error) {
	if cfg == nil {
		cfg = DefaultConfig()
	}
	if err := cfg.EnsureKeys(); err != nil {
		return nil, err
	}

	var staticKeypair noise.DHKey
	if cfg.ServerStaticKey == [32]byte{} {
		var err error
		staticKeypair, err = GenerateStaticKeypair()
		if err != nil {
			return nil, fmt.Errorf("failed generating static keypair: %w", err)
		}
		copy(cfg.ServerStaticKey[:], staticKeypair.Private)
	} else {
		pub, err := DerivePublicKey(cfg.ServerStaticKey)
		if err != nil {
			return nil, fmt.Errorf("failed deriving static public key: %w", err)
		}
		staticKeypair = noise.DHKey{
			Private: cfg.ServerStaticKey[:],
			Public:  pub,
		}
	}

	srv := &Server{
		cfg:            cfg,
		sessions:       NewSessionTable(cfg.MaxSessions),
		staticKey:      staticKeypair,
		limiter:        NewTokenBucket(float64(cfg.HandshakeRateLimit), float64(cfg.HandshakeRateLimit)),
		handshakeCache: NewHandshakeCache(),
		packetChan:     make(chan incomingPacket, 2048),
		stopChan:       make(chan struct{}),
	}

	srv.forwarder = NewRelayForwarder(srv, cfg.MaxFlowsPerSession)
	srv.sessions.SetRekeyCallback(func(oldID uint32, newSession *Session) {
		srv.forwarder.UpdateSession(oldID, newSession)
	})
	return srv, nil
}

// Start binds the UDP listener and starts the packet handling loop
func (s *Server) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.running {
		return errors.New("server already running")
	}

	laddr, err := net.ResolveUDPAddr("udp", s.cfg.ListenAddr)
	if err != nil {
		return fmt.Errorf("invalid listen address: %w", err)
	}

	conn, err := net.ListenUDP("udp", laddr)
	if err != nil {
		return fmt.Errorf("failed to bind UDP socket: %w", err)
	}

	s.conn = conn
	s.running = true

	log.Printf("[INFO] DualFlow Secure Relay listening on %s (UDP)", conn.LocalAddr().String())
	log.Printf("[INFO] Configuration: %s", s.cfg.RedactedSummary())

	// Start bounded worker pool to prevent goroutine explosion under packet flood
	for i := 0; i < 16; i++ {
		s.workersWg.Add(1)
		go s.workerLoop()
	}

	go s.readLoop()
	return nil
}

// Stop gracefully shuts down the server, forwarder, and network socket
func (s *Server) Stop() {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.running = false
		close(s.stopChan)
		if s.conn != nil {
			_ = s.conn.Close()
		}
		s.mu.Unlock()

		s.workersWg.Wait()

		if s.forwarder != nil {
			s.forwarder.Close()
		}
		if s.sessions != nil {
			s.sessions.CloseAll()
		}
	})
}

// Addr returns the local bound UDP address
func (s *Server) Addr() *net.UDPAddr {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn != nil {
		return s.conn.LocalAddr().(*net.UDPAddr)
	}
	return nil
}

// StaticPublicKey returns the 32-byte public key of the server
func (s *Server) StaticPublicKey() []byte {
	return s.staticKey.Public
}

// SendFrame constructs a DFTP v3 outer header, encrypts the payload, and sends the datagram
func (s *Server) SendFrame(session *Session, raddr *net.UDPAddr, pathID uint16, msgType uint8, flags uint16, payload []byte) error {
	s.mu.Lock()
	conn := s.conn
	s.mu.Unlock()

	if conn == nil {
		return errors.New("server not listening")
	}
	if session == nil {
		return ErrSessionNotFound
	}

	session.mu.RLock()
	if session.State == SessionClosed || session.SendCipher == nil {
		session.mu.RUnlock()
		return ErrSessionNotFound
	}
	sendCipher := session.SendCipher
	session.mu.RUnlock()

	seq, err := session.SeqGen.Next()
	if err != nil {
		return err
	}

	// 1. Allocate buffer with 32-byte header + ciphertext (payload + 16B tag)
	cipherLen := len(payload) + 16
	dummy := make([]byte, cipherLen)

	packet, err := transport.EncodeFrameV3(msgType, flags, session.ID, seq, pathID, dummy)
	if err != nil {
		return err
	}

	headerAAD := packet[:transport.HeaderLength]
	ciphertext := sendCipher.Encrypt(seq, headerAAD, payload)
	if ciphertext == nil {
		return errors.New("encryption failed or cipher closed")
	}
	copy(packet[transport.HeaderLength:], ciphertext)

	session.Touch()
	_, err = conn.WriteToUDP(packet, raddr)
	return err
}

func (s *Server) workerLoop() {
	defer s.workersWg.Done()
	for {
		select {
		case <-s.stopChan:
			return
		case pkt := <-s.packetChan:
			s.dispatchPacket(pkt.packet, pkt.raddr)
		}
	}
}

// readLoop reads datagrams from the network socket and routes them
func (s *Server) readLoop() {
	buf := make([]byte, 2048)

	for {
		select {
		case <-s.stopChan:
			return
		default:
		}

		n, raddr, err := s.conn.ReadFromUDP(buf)
		if err != nil {
			select {
			case <-s.stopChan:
				return
			default:
				continue
			}
		}

		if n < int(transport.HeaderLength) {
			continue
		}

		rawPacket := make([]byte, n)
		copy(rawPacket, buf[:n])

		// Non-blocking dispatch to worker pool; drops under extreme burst to prevent OOM
		select {
		case s.packetChan <- incomingPacket{packet: rawPacket, raddr: raddr}:
		default:
		}
	}
}

// dispatchPacket decodes and routes an individual packet
func (s *Server) dispatchPacket(packet []byte, raddr *net.UDPAddr) {
	frame, err := transport.DecodeFrame(packet)
	if err != nil {
		return
	}

	if frame.Header.MsgType == transport.MsgTypeHandshakeInit {
		s.handleHandshake(frame, packet, raddr)
		return
	}

	session, ok := s.sessions.Get(frame.Header.SessionID)
	if !ok {
		return
	}

	// Authenticate and decrypt using outer 32-byte header as AAD
	headerBytes := packet[:transport.HeaderLength]
	plaintext, err := session.DecryptInbound(frame.Header.SequenceNumber, headerBytes, frame.Payload)
	if err != nil {
		return
	}

	// Register or update endpoint tracking
	s.checkPathValidation(session, raddr, frame.Header.PathID)

	// Dispatch message based on type
	switch frame.Header.MsgType {
	case transport.MsgTypeData:
		_ = s.forwarder.HandleStreamData(session, raddr, frame.Header.PathID, plaintext)

	case transport.MsgTypeStreamAck:
		_ = s.forwarder.HandleStreamAck(plaintext)

	case transport.MsgTypeFlowOpen:
		_ = s.forwarder.HandleFlowOpen(session, raddr, frame.Header.PathID, plaintext)

	case transport.MsgTypePathResponse:
		s.handlePathResponse(session, raddr, frame.Header.PathID, plaintext)

	case transport.MsgTypeClose:
		s.forwarder.CloseSessionFlows(session.ID)
		s.sessions.Remove(session.ID)

	case transport.MsgTypePing:
		// Pong response (echo)
		_ = s.SendFrame(session, raddr, frame.Header.PathID, transport.MsgTypePong, 0, plaintext)
	}
}

// handleHandshake processes incoming Noise_IKpsk2 Message 1
func (s *Server) handleHandshake(frame *transport.Frame, rawPacket []byte, raddr *net.UDPAddr) {
	// Rate limiting token bucket check
	if !s.limiter.Allow() {
		return // Drop silently to mitigate handshake flooding
	}

	relayHs, err := NewRelayHandshake(s.staticKey, s.cfg.PSK[:])
	if err != nil {
		return
	}

	_, err = relayHs.ProcessHandshakeInit(rawPacket)
	if err != nil {
		return
	}

	// Allocate unique session ID
	sessionID, err := s.sessions.AllocateSessionID()
	if err != nil {
		return
	}

	// Generate Handshake Message 2
	respPacket, sendCipher, recvCipher, err := relayHs.CreateHandshakeResp(sessionID)
	if err != nil {
		return
	}

	session := NewSession(sessionID, sendCipher, recvCipher)
	if err := s.sessions.Add(session); err != nil {
		return
	}

	// Initial handshake endpoint is registered and marked validated for Path 1
	session.RegisterEndpoint(raddr, 1)
	session.SetEndpointValidated(raddr, true)

	// Send handshake response
	s.mu.Lock()
	conn := s.conn
	s.mu.Unlock()
	if conn != nil {
		_, _ = conn.WriteToUDP(respPacket, raddr)
	}
}

// checkPathValidation tests if the source endpoint is validated for return traffic; if not, triggers a PathChallenge
func (s *Server) checkPathValidation(session *Session, raddr *net.UDPAddr, pathID uint16) {
	session.mu.Lock()
	key := raddr.String()
	ep, exists := session.Endpoints[key]
	if !exists {
		ep = &SessionEndpoint{
			RemoteAddr: raddr,
			PathID:     pathID,
			LastSeen:   time.Now(),
		}
		session.Endpoints[key] = ep
	} else {
		ep.LastSeen = time.Now()
		ep.PathID = pathID
	}

	if ep.IsValidated {
		session.mu.Unlock()
		return
	}

	// If challenge was sent recently (< 1s), don't spam
	if time.Since(ep.ChallengeSentAt) < 1*time.Second {
		session.mu.Unlock()
		return
	}

	var challengeNonce [8]byte
	_, _ = rand.Read(challengeNonce[:])
	ep.PendingChallenge = challengeNonce
	ep.ChallengeSentAt = time.Now()
	session.mu.Unlock()

	// Send PathChallenge frame
	challengePayload := transport.EncodePathChallenge(pathID, challengeNonce)
	_ = s.SendFrame(session, raddr, pathID, transport.MsgTypePathChallenge, 0, challengePayload)
}

// handlePathResponse validates the client's answer to a PathChallenge
func (s *Server) handlePathResponse(session *Session, raddr *net.UDPAddr, pathID uint16, payload []byte) {
	resp, err := transport.DecodePathChallenge(payload)
	if err != nil {
		return
	}

	if session.VerifyChallenge(raddr, resp.Nonce) {
		log.Printf("[INFO] Remote path %s (PathID %d) cryptographically validated", raddr.String(), pathID)
	}
}
