package proxy

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"time"

	"dualflow-core/internal/diagnostics"
	"dualflow-core/internal/transport"
)

var (
	ErrSOCKS5UnsupportedVersion = errors.New("unsupported socks5 version")
	ErrSOCKS5UnsupportedCommand = errors.New("unsupported socks5 command (only TCP CONNECT supported)")
	ErrSOCKS5ConnectionFailed   = errors.New("remote target connection failed via relay")
)

// TunnelSender defines the interface for transmitting formatted DFTP frames across the multi-path tunnel
type TunnelSender interface {
	SendFlowOpen(flowID uint32, port uint16, addrType uint8, addr string) error
	SendStreamData(flowID uint32, offset uint64, flags uint16, data []byte) error
	SendStreamAck(flowID uint32, cumAck uint64, sackBlocks []transport.SackBlock) error
	SendFlowRST(flowID uint32) error
}

// ClientProxyListener manages the local SOCKS5 and Fixed-Target TCP listener
type ClientProxyListener struct {
	mu           sync.RWMutex
	listenAddr   string
	fixedTarget  string
	listener     net.Listener
	flowTable    *FlowTable
	tunnelSender TunnelSender
	flowOpenWait map[uint32]chan uint8 // flowID -> ack channel (status)
	isRunning    bool
	ctx          context.Context
	cancel       context.CancelFunc
}

// NewClientProxyListener creates an unstarted proxy listener
func NewClientProxyListener(listenAddr, fixedTarget string, sender TunnelSender) *ClientProxyListener {
	if listenAddr == "" {
		listenAddr = "127.0.0.1:10800"
	}
	return &ClientProxyListener{
		listenAddr:   listenAddr,
		fixedTarget:  fixedTarget,
		flowTable:    NewFlowTable(),
		tunnelSender: sender,
		flowOpenWait: make(map[uint32]chan uint8),
	}
}

// Start begins listening on the designated local TCP port
func (p *ClientProxyListener) Start(ctx context.Context) error {
	p.mu.Lock()
	if p.isRunning {
		p.mu.Unlock()
		return nil
	}

	l, err := net.Listen("tcp", p.listenAddr)
	if err != nil {
		p.mu.Unlock()
		return fmt.Errorf("failed to bind client proxy listener on %s: %w", p.listenAddr, err)
	}

	p.listener = l
	p.isRunning = true
	p.ctx, p.cancel = context.WithCancel(ctx)
	p.mu.Unlock()

	diagnostics.Info("Client Proxy Listener active on %s (Fixed Target: '%s')", p.listenAddr, p.fixedTarget)

	go p.acceptLoop()
	go p.flowCleanupLoop()

	return nil
}

// Stop terminates the listener and closes all active flows
func (p *ClientProxyListener) Stop() error {
	p.mu.Lock()
	if !p.isRunning {
		p.mu.Unlock()
		return nil
	}

	p.isRunning = false
	if p.cancel != nil {
		p.cancel()
	}
	if p.listener != nil {
		_ = p.listener.Close()
	}
	p.mu.Unlock()

	return nil
}

// GetFlowTable returns the active flow table
func (p *ClientProxyListener) GetFlowTable() *FlowTable {
	return p.flowTable
}

func (p *ClientProxyListener) acceptLoop() {
	for {
		conn, err := p.listener.Accept()
		if err != nil {
			p.mu.RLock()
			running := p.isRunning
			p.mu.RUnlock()
			if !running {
				return
			}
			diagnostics.Warn("Proxy listener accept error: %v", err)
			time.Sleep(50 * time.Millisecond)
			continue
		}

		go p.handleConnection(conn)
	}
}

func (p *ClientProxyListener) handleConnection(conn net.Conn) {
	defer conn.Close()

	var targetAddr string
	var targetPort uint16
	var addrType uint8

	p.mu.RLock()
	fixed := p.fixedTarget
	p.mu.RUnlock()

	if fixed != "" {
		// Fixed-Target Mode: parse host and port directly
		host, portStr, err := net.SplitHostPort(fixed)
		if err != nil {
			diagnostics.Error("Invalid fixed target format '%s': %v", fixed, err)
			return
		}
		pNum, _ := strconv.Atoi(portStr)
		targetPort = uint16(pNum)
		targetAddr = host
		addrType = transport.AddrTypeDomain
		if ip := net.ParseIP(host); ip != nil {
			if ip.To4() != nil {
				addrType = transport.AddrTypeIPv4
			} else {
				addrType = transport.AddrTypeIPv6
			}
		}
	} else {
		// SOCKS5 Negotiation Mode
		tAddr, tPort, aType, err := p.negotiateSOCKS5(conn)
		if err != nil {
			diagnostics.Debug("SOCKS5 negotiation failed: %v", err)
			return
		}
		targetAddr = tAddr
		targetPort = tPort
		addrType = aType
	}

	// 1. Allocate Flow
	flow, err := p.flowTable.AllocateFlow()
	if err != nil {
		diagnostics.Error("Failed to allocate flow: %v", err)
		if fixed == "" {
			_ = sendSOCKS5Reply(conn, transport.StatusGeneralFailure)
		}
		return
	}
	defer p.flowTable.Remove(flow.ID)

	// 2. Prepare FlowOpen Waiter
	ackChan := make(chan uint8, 1)
	p.mu.Lock()
	p.flowOpenWait[flow.ID] = ackChan
	p.mu.Unlock()

	defer func() {
		p.mu.Lock()
		delete(p.flowOpenWait, flow.ID)
		p.mu.Unlock()
	}()

	// 3. Dispatch MsgTypeFlowOpen across tunnel
	if err := p.tunnelSender.SendFlowOpen(flow.ID, targetPort, addrType, targetAddr); err != nil {
		diagnostics.Error("Failed to send FlowOpen for flow %d: %v", flow.ID, err)
		if fixed == "" {
			_ = sendSOCKS5Reply(conn, transport.StatusNetworkUnreach)
		}
		return
	}

	// 4. Wait for FlowOpenAck from Relay (timeout 5s)
	select {
	case status := <-ackChan:
		if status != transport.StatusSuccess {
			diagnostics.Warn("Relay rejected flow %d with status 0x%02x", flow.ID, status)
			if fixed == "" {
				_ = sendSOCKS5Reply(conn, status)
			}
			return
		}
	case <-time.After(5 * time.Second):
		diagnostics.Warn("Timeout awaiting FlowOpenAck for flow %d", flow.ID)
		if fixed == "" {
			_ = sendSOCKS5Reply(conn, transport.StatusHostUnreachable)
		}
		return
	case <-p.ctx.Done():
		return
	}

	// 5. Send SOCKS5 Success Confirmation
	if fixed == "" {
		if err := sendSOCKS5Reply(conn, transport.StatusSuccess); err != nil {
			return
		}
	}

	// 6. Start Bidirectional Data Pump
	p.pumpConnection(conn, flow)
}

func (p *ClientProxyListener) negotiateSOCKS5(conn net.Conn) (string, uint16, uint8, error) {
	// Greeting: [VER, NMETHODS, METHODS...]
	header := make([]byte, 2)
	if _, err := io.ReadFull(conn, header); err != nil {
		return "", 0, 0, err
	}
	if header[0] != 5 {
		return "", 0, 0, ErrSOCKS5UnsupportedVersion
	}

	numMethods := int(header[1])
	methods := make([]byte, numMethods)
	if _, err := io.ReadFull(conn, methods); err != nil {
		return "", 0, 0, err
	}

	// Reply: [0x05, 0x00] (No authentication required)
	if _, err := conn.Write([]byte{0x05, 0x00}); err != nil {
		return "", 0, 0, err
	}

	// Request: [VER, CMD, RSV, ATYP, ADDR, PORT]
	reqHeader := make([]byte, 4)
	if _, err := io.ReadFull(conn, reqHeader); err != nil {
		return "", 0, 0, err
	}

	if reqHeader[0] != 5 {
		return "", 0, 0, ErrSOCKS5UnsupportedVersion
	}
	if reqHeader[1] != 1 { // Only TCP CONNECT (1) supported
		_ = sendSOCKS5Reply(conn, 0x07) // Command not supported
		return "", 0, 0, ErrSOCKS5UnsupportedCommand
	}

	atyp := reqHeader[3]
	var targetAddr string

	switch atyp {
	case transport.AddrTypeIPv4:
		ipBuf := make([]byte, 4)
		if _, err := io.ReadFull(conn, ipBuf); err != nil {
			return "", 0, 0, err
		}
		targetAddr = net.IP(ipBuf).String()
	case transport.AddrTypeDomain:
		lenBuf := make([]byte, 1)
		if _, err := io.ReadFull(conn, lenBuf); err != nil {
			return "", 0, 0, err
		}
		dLen := int(lenBuf[0])
		domainBuf := make([]byte, dLen)
		if _, err := io.ReadFull(conn, domainBuf); err != nil {
			return "", 0, 0, err
		}
		targetAddr = string(domainBuf)
	case transport.AddrTypeIPv6:
		ipBuf := make([]byte, 16)
		if _, err := io.ReadFull(conn, ipBuf); err != nil {
			return "", 0, 0, err
		}
		targetAddr = net.IP(ipBuf).String()
	default:
		_ = sendSOCKS5Reply(conn, 0x08) // Address type not supported
		return "", 0, 0, fmt.Errorf("unsupported SOCKS5 ATYP: %d", atyp)
	}

	portBuf := make([]byte, 2)
	if _, err := io.ReadFull(conn, portBuf); err != nil {
		return "", 0, 0, err
	}
	targetPort := binary.BigEndian.Uint16(portBuf)

	return targetAddr, targetPort, atyp, nil
}

func sendSOCKS5Reply(conn net.Conn, status uint8) error {
	// [VER=5, REP=status, RSV=0, ATYP=1, 4 bytes IP (0.0.0.0), 2 bytes Port (0)]
	reply := []byte{0x05, status, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
	_, err := conn.Write(reply)
	return err
}

func (p *ClientProxyListener) pumpConnection(conn net.Conn, flow *Flow) {
	var wg sync.WaitGroup
	wg.Add(1)

	// Inbound pump: reading from local TCP socket -> chunking -> tunnel
	go func() {
		defer wg.Done()
		buf := make([]byte, transport.MaxStreamChunkSize) // 1280 bytes

		for {
			// Backpressure check: throttle read if reassembly buffer or global memory is saturated
			if flow.ReorderBuffer.IsBackpressured() {
				time.Sleep(10 * time.Millisecond)
				continue
			}

			n, err := conn.Read(buf)
			if n > 0 {
				chunk, qErr := flow.EnqueueOutbound(buf[:n], 0)
				if qErr == nil {
					_ = p.tunnelSender.SendStreamData(flow.ID, chunk.Offset, 0, chunk.Data)
				}
			}

			if err != nil {
				if err == io.EOF {
					// Client closed write side (half-close)
					chunk, _ := flow.EnqueueOutbound(nil, transport.FlagStreamFIN)
					if chunk != nil {
						_ = p.tunnelSender.SendStreamData(flow.ID, chunk.Offset, transport.FlagStreamFIN, nil)
					}
				} else {
					// Socket abort
					_ = p.tunnelSender.SendFlowRST(flow.ID)
				}
				return
			}
		}
	}()

	wg.Wait()
}

// HandleFlowOpenAck is invoked when a MsgTypeFlowOpenAck packet arrives from the relay
func (p *ClientProxyListener) HandleFlowOpenAck(flowID uint32, status uint8) {
	p.mu.RLock()
	ch, exists := p.flowOpenWait[flowID]
	p.mu.RUnlock()

	if exists {
		select {
		case ch <- status:
		default:
		}
	}
}

// HandleInboundStreamData processes data chunks arriving from the tunnel intended for a local flow
func (p *ClientProxyListener) HandleInboundStreamData(flowID uint32, offset uint64, flags uint16, data []byte, conn net.Conn) error {
	flow, exists := p.flowTable.Get(flowID)
	if !exists {
		return errors.New("stream chunk for unknown flow")
	}

	readyBytes, deliversFIN, _, err := flow.ReorderBuffer.InsertChunk(offset, flags, data)
	if err != nil {
		return err
	}

	// Write assembled contiguous bytes to the local client socket
	if len(readyBytes) > 0 && conn != nil {
		if _, writeErr := conn.Write(readyBytes); writeErr != nil {
			_ = p.tunnelSender.SendFlowRST(flowID)
			return writeErr
		}
	}

	// Handle TCP half-close on receiving remote FIN
	if deliversFIN {
		flow.MarkRemoteFinDelivered()
		if tcpConn, ok := conn.(*net.TCPConn); ok {
			_ = tcpConn.CloseWrite()
		}
	}

	// Emit updated SACK report across tunnel
	cumAck := flow.ReorderBuffer.GetCumulativeAck()
	sackBlocks := flow.ReorderBuffer.GetSackBlocks()
	return p.tunnelSender.SendStreamAck(flowID, cumAck, sackBlocks)
}

func (p *ClientProxyListener) flowCleanupLoop() {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			p.flowTable.CleanupStale()
		case <-p.ctx.Done():
			return
		}
	}
}

