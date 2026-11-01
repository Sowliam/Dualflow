package multipath

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"dualflow-core/internal/proxy"
	"dualflow-core/internal/relay"
	"dualflow-core/internal/transport"
	"github.com/flynn/noise"
)

var (
	ErrRelayNotConnected = errors.New("relay client is not connected")
	ErrFlowRejected      = errors.New("relay rejected TCP flow")
)

// RelayClientConfig holds the connection parameters for the Client -> Relay tunnel
type RelayClientConfig struct {
	RelayAddr          string `json:"relay_addr"`
	PSKHex             string `json:"-"`
	RelayStaticPubHex  string `json:"relay_static_pub"`
	SOCKS5Listen       string `json:"socks5_listen"` // e.g. "127.0.0.1:10800"
	AutoReconnect      bool   `json:"auto_reconnect"`

	PSK            [32]byte `json:"-"`
	RelayStaticPub []byte   `json:"-"`
}

// ClientFlow tracks a single local TCP connection mapped to a proxied DFTP stream
type ClientFlow struct {
	FlowID      uint32
	LocalConn   net.Conn
	Flow        *proxy.Flow
	LocalClosed bool
	CloseOnce   sync.Once
	stopChan    chan struct{}
}

// RelayClient coordinates the client-side session, path-striping, and local SOCKS5 listener
type RelayClient struct {
	cfg          RelayClientConfig
	engine       *Engine
	socksProxy   *proxy.ClientProxyListener
	scheduler    *CongestionScheduler
	clientStatic noise.DHKey

	// Session State
	mu         sync.RWMutex
	sessionID  uint32
	sendCipher *relay.TransportCipher
	recvCipher *relay.TransportCipher
	antiReplay *relay.AntiReplayWindow
	seqGen     *relay.MonotonicSequence
	connected  bool
	lastActive time.Time

	// Flow table
	flowsMu   sync.RWMutex
	flows     map[uint32]*ClientFlow
	nextFlow  uint32

	// UDP socket for communication with Relay (or per-path sockets)
	udpConn   *net.UDPConn
	relayAddr *net.UDPAddr

	// Metrics
	txBytes uint64
	rxBytes uint64

	stopChan  chan struct{}
	closeOnce sync.Once
}

// NewRelayClient initializes the client-side relay controller
func NewRelayClient(cfg RelayClientConfig, engine *Engine) (*RelayClient, error) {
	if cfg.SOCKS5Listen == "" {
		cfg.SOCKS5Listen = "127.0.0.1:10800"
	}

	clientKeypair, err := relay.GenerateStaticKeypair()
	if err != nil {
		return nil, fmt.Errorf("failed generating client static keypair: %w", err)
	}

	rc := &RelayClient{
		cfg:          cfg,
		engine:       engine,
		scheduler:    NewCongestionScheduler(),
		clientStatic: clientKeypair,
		antiReplay:   relay.NewAntiReplayWindow(),
		seqGen:       relay.NewMonotonicSequence(0),
		flows:        make(map[uint32]*ClientFlow),
		stopChan:     make(chan struct{}),
	}

	return rc, nil
}

// Connect initiates the Noise_IKpsk2 handshake with the Relay and starts the background loops
func (rc *RelayClient) Connect() error {
	rc.mu.Lock()
	defer rc.mu.Unlock()

	if rc.connected {
		return nil
	}

	raddr, err := net.ResolveUDPAddr("udp", rc.cfg.RelayAddr)
	if err != nil {
		return fmt.Errorf("invalid relay address: %w", err)
	}
	rc.relayAddr = raddr

	conn, err := net.ListenUDP("udp", nil)
	if err != nil {
		return fmt.Errorf("failed to open local UDP socket: %w", err)
	}
	rc.udpConn = conn

	// Handshake orchestration
	clientHS, err := relay.NewClientHandshake(rc.clientStatic, rc.cfg.RelayStaticPub, rc.cfg.PSK[:])
	if err != nil {
		conn.Close()
		return fmt.Errorf("handshake initialization failed: %w", err)
	}

	initPacket, err := clientHS.CreateHandshakeInit()
	if err != nil {
		conn.Close()
		return fmt.Errorf("failed to create Handshake Message 1: %w", err)
	}

	// Send Message 1
	if _, err := conn.WriteToUDP(initPacket, raddr); err != nil {
		conn.Close()
		return fmt.Errorf("failed to send Handshake Message 1: %w", err)
	}

	// Wait for Message 2
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	respBuf := make([]byte, 2048)
	n, _, err := conn.ReadFromUDP(respBuf)
	if err != nil {
		conn.Close()
		return fmt.Errorf("handshake response timeout: %w", err)
	}
	_ = conn.SetReadDeadline(time.Time{})

	sessionID, sendCipher, recvCipher, err := clientHS.ProcessHandshakeResp(respBuf[:n])
	if err != nil {
		conn.Close()
		return fmt.Errorf("handshake verification failed: %w", err)
	}

	rc.sessionID = sessionID
	rc.sendCipher = sendCipher
	rc.recvCipher = recvCipher
	rc.connected = true
	rc.lastActive = time.Now()

	log.Printf("[INFO] DualFlow Secure Relay connected successfully (SessionID: 0x%08X)", sessionID)

	// Start local SOCKS5 proxy listener if configured
	if rc.cfg.SOCKS5Listen != "" {
		rc.socksProxy = proxy.NewClientProxyListener(rc.cfg.SOCKS5Listen, "", rc)
		if err := rc.socksProxy.Start(context.Background()); err != nil {
			log.Printf("[WARN] Failed to start local SOCKS5 listener on %s: %v", rc.cfg.SOCKS5Listen, err)
		} else {
			log.Printf("[INFO] DualFlow SOCKS5 proxy listener active on %s", rc.cfg.SOCKS5Listen)
		}
	}

	go rc.readLoop()
	go rc.retransmitLoop()

	return nil
}

// Disconnect gracefully terminates the session
func (rc *RelayClient) Disconnect() {
	rc.mu.Lock()
	if !rc.connected {
		rc.mu.Unlock()
		return
	}
	rc.connected = false
	rc.mu.Unlock()

	// Send Close frame
	_ = rc.SendControlFrame(transport.MsgTypeClose, nil)

	rc.closeOnce.Do(func() {
		close(rc.stopChan)
		if rc.udpConn != nil {
			_ = rc.udpConn.Close()
		}
		if rc.socksProxy != nil {
			_ = rc.socksProxy.Stop()
		}

		rc.flowsMu.Lock()
		for _, cf := range rc.flows {
			cf.Close()
		}
		rc.flows = make(map[uint32]*ClientFlow)
		rc.flowsMu.Unlock()
	})
	log.Println("[INFO] DualFlow Secure Relay client disconnected.")
}

// IsConnected returns current session status
func (rc *RelayClient) IsConnected() bool {
	rc.mu.RLock()
	defer rc.mu.RUnlock()
	return rc.connected
}

// SessionID returns the negotiated 32-bit session ID
func (rc *RelayClient) SessionID() uint32 {
	rc.mu.RLock()
	defer rc.mu.RUnlock()
	return rc.sessionID
}

// SocksAddr returns the bound address of the local SOCKS5 proxy listener
func (rc *RelayClient) SocksAddr() string {
	rc.mu.RLock()
	defer rc.mu.RUnlock()
	if rc.socksProxy != nil {
		return rc.socksProxy.Addr()
	}
	return ""
}

// SendControlFrame sends an encrypted control message to the Relay
func (rc *RelayClient) SendControlFrame(msgType uint8, payload []byte) error {
	rc.mu.RLock()
	if !rc.connected {
		rc.mu.RUnlock()
		return ErrRelayNotConnected
	}
	sessionID := rc.sessionID
	sendCipher := rc.sendCipher
	conn := rc.udpConn
	raddr := rc.relayAddr
	rc.mu.RUnlock()

	seq, err := rc.seqGen.Next()
	if err != nil {
		return err
	}

	cipherLen := len(payload) + 16
	dummy := make([]byte, cipherLen)
	packet, err := transport.EncodeFrameV3(msgType, 0, sessionID, seq, 1, dummy)
	if err != nil {
		return err
	}

	headerAAD := packet[:transport.HeaderLength]
	ciphertext := sendCipher.Encrypt(seq, headerAAD, payload)
	copy(packet[transport.HeaderLength:], ciphertext)

	_, err = conn.WriteToUDP(packet, raddr)
	return err
}

// OpenFlow dials a new TCP connection through the Relay tunnel
func (rc *RelayClient) OpenFlow(targetAddr string, targetPort uint16) (*ClientFlow, error) {
	if !rc.IsConnected() {
		return nil, ErrRelayNotConnected
	}

	flowID := atomic.AddUint32(&rc.nextFlow, 1)
	flowModel := proxy.NewFlow(flowID)

	cf := &ClientFlow{
		FlowID:   flowID,
		Flow:     flowModel,
		stopChan: make(chan struct{}),
	}

	rc.flowsMu.Lock()
	rc.flows[flowID] = cf
	rc.flowsMu.Unlock()

	// Send FlowOpen frame
	openBytes, err := transport.EncodeFlowOpen(flowID, targetPort, 1, targetAddr)
	if err != nil {
		cf.Close()
		return nil, err
	}

	if err := rc.SendControlFrame(transport.MsgTypeFlowOpen, openBytes); err != nil {
		cf.Close()
		return nil, err
	}

	return cf, nil
}

// readLoop receives datagrams from the Relay UDP socket
func (rc *RelayClient) readLoop() {
	buf := make([]byte, 2048)

	for {
		select {
		case <-rc.stopChan:
			return
		default:
		}

		n, _, err := rc.udpConn.ReadFromUDP(buf)
		if err != nil {
			select {
			case <-rc.stopChan:
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

		go rc.dispatchPacket(rawPacket)
	}
}

// dispatchPacket decodes and processes an inbound DFTP v3 packet from the Relay
func (rc *RelayClient) dispatchPacket(packet []byte) {
	frame, err := transport.DecodeFrame(packet)
	if err != nil {
		return
	}

	rc.mu.RLock()
	sessionID := rc.sessionID
	recvCipher := rc.recvCipher
	rc.mu.RUnlock()

	if frame.Header.SessionID != sessionID {
		return
	}

	// Decrypt and anti-replay commit
	headerBytes := packet[:transport.HeaderLength]
	var plaintext []byte

	err = rc.antiReplay.VerifyAndCommit(frame.Header.SequenceNumber, func() error {
		pt, decErr := recvCipher.Decrypt(frame.Header.SequenceNumber, headerBytes, frame.Payload)
		if decErr != nil {
			return decErr
		}
		plaintext = pt
		return nil
	})
	if err != nil {
		return
	}

	atomic.AddUint64(&rc.rxBytes, uint64(len(packet)))

	switch frame.Header.MsgType {
	case transport.MsgTypeData:
		rc.handleStreamData(frame.Header.PathID, plaintext)

	case transport.MsgTypeStreamAck:
		rc.handleStreamAck(plaintext)

	case transport.MsgTypePathChallenge:
		rc.handlePathChallenge(frame.Header.PathID, plaintext)

	case transport.MsgTypeFlowOpenAck:
		rc.handleFlowOpenAck(plaintext)

	case transport.MsgTypeClose:
		rc.Disconnect()
	}
}

// handleStreamData processes incoming stream chunks, reassembles contiguous data, and writes to local TCP socket
func (rc *RelayClient) handleStreamData(pathID uint16, payload []byte) {
	streamFrame, err := transport.DecodeStreamFrame(payload)
	if err != nil {
		return
	}

	// Dispatch to active SOCKS5 proxy listener if running
	if rc.socksProxy != nil {
		_ = rc.socksProxy.HandleInboundStreamData(
			streamFrame.Header.FlowID,
			streamFrame.Header.StreamOffset,
			streamFrame.Header.StreamFlags,
			streamFrame.Data,
		)
	}

	rc.flowsMu.RLock()
	cf, exists := rc.flows[streamFrame.Header.FlowID]
	rc.flowsMu.RUnlock()

	if !exists {
		return
	}

	readyBytes, deliversFin, _, _ := cf.Flow.ReorderBuffer.InsertChunk(
		streamFrame.Header.StreamOffset,
		streamFrame.Header.StreamFlags,
		streamFrame.Data,
	)

	if len(readyBytes) > 0 && cf.LocalConn != nil {
		_, _ = cf.LocalConn.Write(readyBytes)
	}

	if deliversFin && cf.LocalConn != nil {
		cf.Flow.MarkRemoteFinDelivered()
		if tcpConn, ok := cf.LocalConn.(*net.TCPConn); ok {
			_ = tcpConn.CloseWrite()
		}
	}

	// Send StreamAck back to Relay
	ackPayload, err := transport.EncodeStreamAck(
		cf.Flow.ID,
		cf.Flow.ReorderBuffer.GetCumulativeAck(),
		cf.Flow.ReorderBuffer.GetSackBlocks(),
	)
	if err == nil {
		_ = rc.SendControlFrame(transport.MsgTypeStreamAck, ackPayload)
	}
}

// handleStreamAck updates the retransmission queue
func (rc *RelayClient) handleStreamAck(payload []byte) {
	ack, err := transport.DecodeStreamAck(payload)
	if err != nil {
		return
	}

	rc.flowsMu.RLock()
	cf, exists := rc.flows[ack.FlowID]
	rc.flowsMu.RUnlock()

	if !exists {
		return
	}

	fastRetransmitChunk, _ := cf.Flow.ProcessAck(ack.CumulativeAckOffset, ack.SackBlocks)
	if fastRetransmitChunk != nil {
		rc.sendStreamChunk(cf, fastRetransmitChunk.Offset, fastRetransmitChunk.Data, fastRetransmitChunk.Flags)
	}
}

// handlePathChallenge responds to cryptographic validation challenge sent by Relay
func (rc *RelayClient) handlePathChallenge(pathID uint16, payload []byte) {
	challenge, err := transport.DecodePathChallenge(payload)
	if err != nil {
		return
	}

	// Echo back matching nonce with MsgTypePathResponse
	respPayload := transport.EncodePathChallenge(challenge.PathID, challenge.Nonce)
	_ = rc.SendControlFrame(transport.MsgTypePathResponse, respPayload)
	log.Printf("[INFO] Answered cryptographic PathChallenge for PathID %d", challenge.PathID)
}

func (rc *RelayClient) handleFlowOpenAck(payload []byte) {
	ack, err := transport.DecodeFlowOpenAck(payload)
	if err != nil {
		return
	}

	if rc.socksProxy != nil {
		rc.socksProxy.HandleFlowOpenAck(ack.FlowID, ack.Status)
	}

	rc.flowsMu.RLock()
	cf, exists := rc.flows[ack.FlowID]
	rc.flowsMu.RUnlock()

	if exists && ack.Status != 0 {
		log.Printf("[WARN] Relay rejected flow %d (Status: %d)", ack.FlowID, ack.Status)
		cf.Close()
	}
}

// SendFlowOpen implements proxy.TunnelSender
func (rc *RelayClient) SendFlowOpen(flowID uint32, port uint16, addrType uint8, addr string) error {
	if !rc.IsConnected() {
		return ErrRelayNotConnected
	}

	var flowModel *proxy.Flow
	if rc.socksProxy != nil {
		flowModel, _ = rc.socksProxy.GetFlowTable().Get(flowID)
	}
	if flowModel == nil {
		flowModel = proxy.NewFlow(flowID)
	}

	cf := &ClientFlow{
		FlowID:   flowID,
		Flow:     flowModel,
		stopChan: make(chan struct{}),
	}

	rc.flowsMu.Lock()
	rc.flows[flowID] = cf
	rc.flowsMu.Unlock()

	openBytes, err := transport.EncodeFlowOpen(flowID, port, addrType, addr)
	if err != nil {
		cf.Close()
		return err
	}

	return rc.SendControlFrame(transport.MsgTypeFlowOpen, openBytes)
}

// SendStreamData implements proxy.TunnelSender
func (rc *RelayClient) SendStreamData(flowID uint32, offset uint64, flags uint16, data []byte) error {
	rc.flowsMu.RLock()
	cf, exists := rc.flows[flowID]
	rc.flowsMu.RUnlock()

	if !exists {
		return fmt.Errorf("unknown flow id: %d", flowID)
	}

	rc.sendStreamChunk(cf, offset, data, flags)
	return nil
}

// SendStreamAck implements proxy.TunnelSender
func (rc *RelayClient) SendStreamAck(flowID uint32, cumAck uint64, sackBlocks []transport.SackBlock) error {
	ackPayload, err := transport.EncodeStreamAck(flowID, cumAck, sackBlocks)
	if err != nil {
		return err
	}
	return rc.SendControlFrame(transport.MsgTypeStreamAck, ackPayload)
}

// SendFlowRST implements proxy.TunnelSender
func (rc *RelayClient) SendFlowRST(flowID uint32) error {
	rc.flowsMu.Lock()
	cf, exists := rc.flows[flowID]
	if exists {
		delete(rc.flows, flowID)
	}
	rc.flowsMu.Unlock()

	if exists {
		cf.Close()
	}
	return nil
}

// sendStreamChunk packages a chunk and transmits across chosen path
func (rc *RelayClient) sendStreamChunk(cf *ClientFlow, offset uint64, payload []byte, flags uint16) {
	streamBytes, err := transport.EncodeStreamFrame(cf.Flow.ID, offset, flags, payload)
	if err != nil {
		return
	}

	rc.mu.RLock()
	sessionID := rc.sessionID
	sendCipher := rc.sendCipher
	conn := rc.udpConn
	raddr := rc.relayAddr
	rc.mu.RUnlock()

	seq, err := rc.seqGen.Next()
	if err != nil {
		return
	}

	// Select path dynamically using scheduler across registered engine paths
	pathID := uint16(1)
	var activeSocket *PathSocket

	if rc.engine != nil {
		paths := rc.engine.GetPaths()
		selectedPaths, schedErr := rc.scheduler.SelectPaths(nil, paths)
		if schedErr == nil && len(selectedPaths) > 0 {
			chosen := selectedPaths[0]
			if chosen.ID == "android" {
				pathID = 2
			} else {
				pathID = 1
			}
			activeSocket = chosen.GetSocket()
			chosen.RecordTx(len(streamBytes))
			rc.scheduler.OnPacketSent(chosen.ID, uint32(len(payload)))
		}
	}

	cipherLen := len(streamBytes) + 16
	dummy := make([]byte, cipherLen)
	packet, err := transport.EncodeFrameV3(transport.MsgTypeData, 0, sessionID, seq, pathID, dummy)
	if err != nil {
		return
	}

	headerAAD := packet[:transport.HeaderLength]
	ciphertext := sendCipher.Encrypt(seq, headerAAD, streamBytes)
	copy(packet[transport.HeaderLength:], ciphertext)

	atomic.AddUint64(&rc.txBytes, uint64(len(packet)))

	// If chosen physical path has an active socket, send via that interface;
	// otherwise fall back gracefully to client connection to ensure zero data loss
	if activeSocket != nil && activeSocket.IsOpen() {
		if err := activeSocket.Send(packet, raddr); err == nil {
			return
		}
	}
	_, _ = conn.WriteToUDP(packet, raddr)
}

// retransmitLoop handles periodic timer retransmissions for client flows
func (rc *RelayClient) retransmitLoop() {
	ticker := time.NewTicker(40 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-rc.stopChan:
			return
		case <-ticker.C:
			rc.flowsMu.RLock()
			for _, cf := range rc.flows {
				timedOut, err := cf.Flow.GetTimedOutChunks()
				if err != nil {
					cf.Close()
					continue
				}
				for _, chunk := range timedOut {
					go rc.sendStreamChunk(cf, chunk.Offset, chunk.Data, chunk.Flags)
				}
			}
			rc.flowsMu.RUnlock()
		}
	}
}

// Close closes an individual client flow
func (cf *ClientFlow) Close() {
	cf.CloseOnce.Do(func() {
		close(cf.stopChan)
		if cf.LocalConn != nil {
			_ = cf.LocalConn.Close()
		}
		if cf.Flow != nil {
			cf.Flow.Close()
		}
	})
}
