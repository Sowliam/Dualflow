package relay

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"dualflow-core/internal/proxy"
	"dualflow-core/internal/transport"
)

// ForwarderPacketSender abstracts sending encoded/encrypted DFTP frames out via UDP
type ForwarderPacketSender interface {
	SendFrame(session *Session, raddr *net.UDPAddr, pathID uint16, msgType uint8, flags uint16, payload []byte) error
}

// RelayFlow wraps a proxied TCP connection and its flow state machine on the Relay
type RelayFlow struct {
	Flow        *proxy.Flow
	Session     *Session
	TCPConn     *net.TCPConn
	TargetAddr  string
	LocalClosed bool
	CloseOnce   sync.Once
	stopChan    chan struct{}
}

// RelayForwarder coordinates multiple TCP flows proxied through the Relay
type RelayForwarder struct {
	mu        sync.RWMutex
	flows     map[uint32]*RelayFlow
	sender    ForwarderPacketSender
	maxFlows  int
	pathIndex uint32 // atomic counter for round-robin path striping
	stopChan  chan struct{}
	closeOnce sync.Once
}

// NewRelayForwarder creates an initialized forwarder
func NewRelayForwarder(sender ForwarderPacketSender, maxFlows int) *RelayForwarder {
	if maxFlows <= 0 {
		maxFlows = 1024
	}
	f := &RelayForwarder{
		flows:    make(map[uint32]*RelayFlow),
		sender:   sender,
		maxFlows: maxFlows,
		stopChan: make(chan struct{}),
	}
	go f.retransmissionAndCleanupLoop()
	return f
}

// Close terminates all active flows and stops background routines
func (f *RelayForwarder) Close() {
	f.closeOnce.Do(func() {
		close(f.stopChan)
		f.mu.Lock()
		defer f.mu.Unlock()
		for _, rf := range f.flows {
			rf.Close()
		}
		f.flows = make(map[uint32]*RelayFlow)
	})
}

// UpdateSession points all active flows using oldSessionID to newSession after a rekey
func (f *RelayForwarder) UpdateSession(oldID uint32, newSession *Session) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, rf := range f.flows {
		if rf.Session != nil && rf.Session.ID == oldID {
			rf.Session = newSession
		}
	}
}

// CloseSessionFlows gracefully closes and removes all active flows belonging to a session
func (f *RelayForwarder) CloseSessionFlows(sessionID uint32) {
	f.mu.Lock()
	var toClose []*RelayFlow
	for flowID, rf := range f.flows {
		if rf.Session != nil && rf.Session.ID == sessionID {
			toClose = append(toClose, rf)
			delete(f.flows, flowID)
		}
	}
	f.mu.Unlock()

	for _, rf := range toClose {
		rf.Close()
	}
}

// HandleFlowOpen processes a client MsgTypeFlowOpen request, dials the target TCP endpoint,
// and sends MsgTypeFlowOpenAck back.
func (f *RelayForwarder) HandleFlowOpen(session *Session, clientAddr *net.UDPAddr, pathID uint16, payload []byte) error {
	openPayload, err := transport.DecodeFlowOpen(payload)
	if err != nil {
		return fmt.Errorf("malformed FlowOpen payload: %w", err)
	}

	f.mu.Lock()
	if len(f.flows) >= f.maxFlows {
		f.mu.Unlock()
		ackPayload := transport.EncodeFlowOpenAck(openPayload.FlowID, 1) // status 1: rejected
		return f.sender.SendFrame(session, clientAddr, pathID, transport.MsgTypeFlowOpenAck, 0, ackPayload)
	}
	f.mu.Unlock()

	target := net.JoinHostPort(openPayload.Addr, strconv.Itoa(int(openPayload.Port)))
	dialer := net.Dialer{Timeout: 10 * time.Second}
	rawConn, err := dialer.Dial("tcp", target)
	if err != nil {
		ackPayload := transport.EncodeFlowOpenAck(openPayload.FlowID, 2) // status 2: dial failed
		return f.sender.SendFrame(session, clientAddr, pathID, transport.MsgTypeFlowOpenAck, 0, ackPayload)
	}

	tcpConn, ok := rawConn.(*net.TCPConn)
	if !ok {
		rawConn.Close()
		return errors.New("underlying connection is not TCP")
	}

	_ = tcpConn.SetNoDelay(true)

	flowModel := proxy.NewFlow(openPayload.FlowID)
	rf := &RelayFlow{
		Flow:       flowModel,
		Session:    session,
		TCPConn:    tcpConn,
		TargetAddr: target,
		stopChan:   make(chan struct{}),
	}

	f.mu.Lock()
	f.flows[openPayload.FlowID] = rf
	f.mu.Unlock()

	// Send success ACK
	ackPayload := transport.EncodeFlowOpenAck(openPayload.FlowID, 0)
	if err := f.sender.SendFrame(session, clientAddr, pathID, transport.MsgTypeFlowOpenAck, 0, ackPayload); err != nil {
		rf.Close()
		return err
	}

	// Start reading from TCP connection -> packetize and send to client
	go f.pumpOutboundTCP(rf)

	return nil
}

// pumpOutboundTCP reads from the remote TCP target and streams chunks back to the client
func (f *RelayForwarder) pumpOutboundTCP(rf *RelayFlow) {
	buf := make([]byte, transport.MaxStreamChunkSize)

	defer func() {
		// Send FIN to client only if flow and session remain active
		select {
		case <-rf.stopChan:
			return
		case <-f.stopChan:
			return
		default:
		}
		if rf.Session != nil {
			rf.Session.mu.RLock()
			active := rf.Session.State == SessionActive && rf.Session.SendCipher != nil
			rf.Session.mu.RUnlock()
			if active {
				chunk, err := rf.Flow.EnqueueOutbound(nil, transport.FlagStreamFIN)
				if err == nil && chunk != nil {
					f.sendStreamChunk(rf, chunk.Offset, nil, transport.FlagStreamFIN)
				}
			}
		}
	}()

	for {
		select {
		case <-rf.stopChan:
			return
		case <-f.stopChan:
			return
		default:
		}

		n, err := rf.TCPConn.Read(buf)
		if n > 0 {
			chunkData := make([]byte, n)
			copy(chunkData, buf[:n])

			chunk, enqueueErr := rf.Flow.EnqueueOutbound(chunkData, 0)
			if enqueueErr == nil && chunk != nil {
				f.sendStreamChunk(rf, chunk.Offset, chunk.Data, 0)
			}
		}

		if err != nil {
			if errors.Is(err, io.EOF) {
				return
			}
			log.Printf("[DEBUG] Relay TCP read error on flow %d: %v", rf.Flow.ID, err)
			return
		}
	}
}

// sendStreamChunk packages a stream chunk into a DFTP frame and transmits it over an active path
func (f *RelayForwarder) sendStreamChunk(rf *RelayFlow, offset uint64, payload []byte, flags uint16) {
	streamBytes, err := transport.EncodeStreamFrame(rf.Flow.ID, offset, flags, payload)
	if err != nil {
		return
	}

	targetAddr, pathID := f.selectOutboundPath(rf.Session)
	if targetAddr == nil {
		return
	}

	_ = f.sender.SendFrame(rf.Session, targetAddr, pathID, transport.MsgTypeData, 0, streamBytes)
}

// selectOutboundPath chooses an endpoint and pathID for return traffic, round-robining across validated endpoints
func (f *RelayForwarder) selectOutboundPath(session *Session) (*net.UDPAddr, uint16) {
	endpoints := session.GetValidatedEndpoints()
	if len(endpoints) == 0 {
		// Fallback to any registered endpoint if not yet validated
		session.mu.RLock()
		for _, ep := range session.Endpoints {
			session.mu.RUnlock()
			return ep.RemoteAddr, ep.PathID
		}
		session.mu.RUnlock()
		return nil, 0
	}

	idx := atomic.AddUint32(&f.pathIndex, 1)
	chosen := endpoints[int(idx)%len(endpoints)]
	return chosen.RemoteAddr, chosen.PathID
}

// HandleStreamData processes inbound client data destined for the remote TCP socket
func (f *RelayForwarder) HandleStreamData(session *Session, clientAddr *net.UDPAddr, pathID uint16, payload []byte) error {
	streamFrame, err := transport.DecodeStreamFrame(payload)
	if err != nil {
		return fmt.Errorf("malformed stream frame: %w", err)
	}

	f.mu.RLock()
	rf, exists := f.flows[streamFrame.Header.FlowID]
	f.mu.RUnlock()

	if !exists {
		return nil
	}

	readyBytes, deliversFin, _, _ := rf.Flow.ReorderBuffer.InsertChunk(
		streamFrame.Header.StreamOffset,
		streamFrame.Header.StreamFlags,
		streamFrame.Data,
	)

	// Write contiguous data to TCP target
	if len(readyBytes) > 0 {
		if _, writeErr := rf.TCPConn.Write(readyBytes); writeErr != nil {
			log.Printf("[DEBUG] Relay TCP write error on flow %d: %v", rf.Flow.ID, writeErr)
		}
	}

	// Check if FIN was received and delivered
	if deliversFin {
		if !rf.LocalClosed {
			rf.LocalClosed = true
			_ = rf.TCPConn.CloseWrite()
			rf.Flow.MarkRemoteFinDelivered()
		}
	}

	// Send cumulative ACK + SACK back to client
	ackPayload, err := transport.EncodeStreamAck(
		rf.Flow.ID,
		rf.Flow.ReorderBuffer.GetCumulativeAck(),
		rf.Flow.ReorderBuffer.GetSackBlocks(),
	)
	if err == nil {
		_ = f.sender.SendFrame(session, clientAddr, pathID, transport.MsgTypeStreamAck, 0, ackPayload)
	}

	return nil
}

// HandleStreamAck processes an ACK/SACK sent by the client acknowledging outbound stream chunks
func (f *RelayForwarder) HandleStreamAck(payload []byte) error {
	ack, err := transport.DecodeStreamAck(payload)
	if err != nil {
		return fmt.Errorf("malformed StreamAck: %w", err)
	}

	f.mu.RLock()
	rf, exists := f.flows[ack.FlowID]
	f.mu.RUnlock()

	if !exists {
		return nil
	}

	fastRetransmitChunk, _ := rf.Flow.ProcessAck(ack.CumulativeAckOffset, ack.SackBlocks)
	if fastRetransmitChunk != nil {
		// Fast Retransmit: immediate retransmission of missing chunk
		f.sendStreamChunk(rf, fastRetransmitChunk.Offset, fastRetransmitChunk.Data, fastRetransmitChunk.Flags)
	}

	return nil
}

// retransmissionAndCleanupLoop handles periodic retransmissions and idle flow teardown
func (f *RelayForwarder) retransmissionAndCleanupLoop() {
	ticker := time.NewTicker(40 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-f.stopChan:
			return
		case <-ticker.C:
			f.checkRetransmissionsAndIdle()
		}
	}
}

func (f *RelayForwarder) checkRetransmissionsAndIdle() {
	f.mu.Lock()
	var toDelete []uint32

	for flowID, rf := range f.flows {
		timedOut, err := rf.Flow.GetTimedOutChunks()
		if err != nil {
			// Max retries exceeded
			rf.Close()
			toDelete = append(toDelete, flowID)
			continue
		}

		for _, chunk := range timedOut {
			go f.sendStreamChunk(rf, chunk.Offset, chunk.Data, chunk.Flags)
		}

		if rf.Flow.State == proxy.FlowStateClosed {
			rf.Close()
			toDelete = append(toDelete, flowID)
		}
	}

	for _, flowID := range toDelete {
		delete(f.flows, flowID)
	}
	f.mu.Unlock()
}

// Close terminates this specific flow
func (rf *RelayFlow) Close() {
	rf.CloseOnce.Do(func() {
		close(rf.stopChan)
		if rf.TCPConn != nil {
			_ = rf.TCPConn.Close()
		}
		if rf.Flow != nil {
			rf.Flow.Close()
		}
	})
}
