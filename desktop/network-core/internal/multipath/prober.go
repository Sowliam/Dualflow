package multipath

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"sync"
	"time"
)

var ProbeMagic = [4]byte{'D', 'F', 'P', 'B'} // DualFlow ProBe Magic

const ProbePacketSize = 32

// PendingProbe tracks an in-flight probe awaiting reflection
type PendingProbe struct {
	ID        uint64
	PathID    string
	TxTime    time.Time
	timer     *time.Timer
	completed bool
}

// PathProber coordinates active RTT and packet loss measurements per path
type PathProber struct {
	mu           sync.RWMutex
	interval     time.Duration
	timeout      time.Duration
	targetAddr   *net.UDPAddr
	pending      map[uint64]*PendingProbe
	probeSeq     uint64
	evaluator    *HealthEvaluator
	ctx          context.Context
	cancel       context.CancelFunc
	workerWg     sync.WaitGroup
	isRunning    bool
	onMetricHook func(pathID string, rtt time.Duration, loss float64)
}

// NewPathProber initializes an active probe monitor
func NewPathProber(interval, timeout time.Duration) *PathProber {
	if interval <= 0 {
		interval = 1000 * time.Millisecond
	}
	if timeout <= 0 {
		timeout = 1500 * time.Millisecond
	}
	return &PathProber{
		interval:   interval,
		timeout:    timeout,
		pending:    make(map[uint64]*PendingProbe),
		evaluator:  NewHealthEvaluator(3, 5*time.Second),
	}
}

// SetMetricHook registers a callback to update schedulers with fresh RTT data
func (pr *PathProber) SetMetricHook(fn func(pathID string, rtt time.Duration, loss float64)) {
	pr.mu.Lock()
	defer pr.mu.Unlock()
	pr.onMetricHook = fn
}

// SetTargetEndpoint configures the reflector endpoint address
func (pr *PathProber) SetTargetEndpoint(addr *net.UDPAddr) {
	pr.mu.Lock()
	defer pr.mu.Unlock()
	pr.targetAddr = addr
}

// Start begins periodic background probing across all registered paths
func (pr *PathProber) Start(paths []*Path) error {
	pr.mu.Lock()
	defer pr.mu.Unlock()

	if pr.isRunning {
		return nil
	}

	pr.ctx, pr.cancel = context.WithCancel(context.Background())
	pr.isRunning = true

	for _, p := range paths {
		pr.workerWg.Add(1)
		go pr.pathProbeWorker(pr.ctx, p)
	}

	return nil
}

// Stop terminates all probe workers and drains pending timers
func (pr *PathProber) Stop() {
	pr.mu.Lock()
	if !pr.isRunning {
		pr.mu.Unlock()
		return
	}
	pr.isRunning = false
	pr.cancel()

	// Drain pending
	for _, p := range pr.pending {
		if p.timer != nil {
			p.timer.Stop()
		}
	}
	pr.pending = make(map[uint64]*PendingProbe)
	pr.mu.Unlock()

	pr.workerWg.Wait()
}

// pathProbeWorker executes periodic probe dispatch for a single path
func (pr *PathProber) pathProbeWorker(ctx context.Context, p *Path) {
	defer pr.workerWg.Done()

	ticker := time.NewTicker(pr.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pr.dispatchProbe(p)
		}
	}
}

// dispatchProbe constructs and transmits a timestamped probe datagram
func (pr *PathProber) dispatchProbe(p *Path) {
	if p == nil || p.socket == nil || !p.socket.IsOpen() {
		return
	}

	pr.mu.Lock()
	target := pr.targetAddr
	if target == nil {
		pr.mu.Unlock()
		return
	}

	pr.probeSeq++
	probeID := pr.probeSeq
	now := time.Now()

	// Build 32-byte probe packet:
	// [0:4] Magic (DFPB)
	// [4:12] Probe ID (uint64)
	// [12:20] Transmit UnixNano (int64)
	// [20:32] Path ID tag
	data := make([]byte, ProbePacketSize)
	copy(data[0:4], ProbeMagic[:])
	binary.BigEndian.PutUint64(data[4:12], probeID)
	binary.BigEndian.PutUint64(data[12:20], uint64(now.UnixNano()))
	copy(data[20:32], []byte(p.ID))

	pending := &PendingProbe{
		ID:     probeID,
		PathID: p.ID,
		TxTime: now,
	}

	// Schedule timeout
	pending.timer = time.AfterFunc(pr.timeout, func() {
		pr.handleProbeTimeout(probeID, p)
	})

	pr.pending[probeID] = pending
	pr.mu.Unlock()

	// Transmit probe through interface-bound socket
	if err := p.socket.Send(data, target); err != nil {
		pr.mu.Lock()
		if pp, ok := pr.pending[probeID]; ok {
			pp.timer.Stop()
			delete(pr.pending, probeID)
		}
		pr.mu.Unlock()

		p.UpdateProbeMetrics(0, false)
		pr.evaluator.Evaluate(p)
	}
}

// handleProbeTimeout triggers when a probe does not receive reflection within timeout
func (pr *PathProber) handleProbeTimeout(probeID uint64, p *Path) {
	pr.mu.Lock()
	pp, ok := pr.pending[probeID]
	if !ok || pp.completed {
		pr.mu.Unlock()
		return
	}
	delete(pr.pending, probeID)
	pr.mu.Unlock()

	p.UpdateProbeMetrics(0, false)
	pr.evaluator.Evaluate(p)

	pr.mu.RLock()
	hook := pr.onMetricHook
	pr.mu.RUnlock()
	if hook != nil {
		hook(p.ID, p.RTT, p.PacketLoss)
	}
}

// HandleProbeReply processes reflected probe datagrams
func (pr *PathProber) HandleProbeReply(data []byte, p *Path) bool {
	if len(data) < ProbePacketSize {
		return false
	}
	if data[0] != ProbeMagic[0] || data[1] != ProbeMagic[1] || data[2] != ProbeMagic[2] || data[3] != ProbeMagic[3] {
		return false // Not a probe packet
	}

	probeID := binary.BigEndian.Uint64(data[4:12])
	now := time.Now()

	pr.mu.Lock()
	pending, ok := pr.pending[probeID]
	if !ok || pending.completed {
		pr.mu.Unlock()
		return false
	}
	pending.completed = true
	pending.timer.Stop()
	delete(pr.pending, probeID)
	pr.mu.Unlock()

	rtt := now.Sub(pending.TxTime)
	if rtt <= 0 {
		rtt = time.Microsecond
	}

	p.UpdateProbeMetrics(rtt, true)
	pr.evaluator.Evaluate(p)

	pr.mu.RLock()
	hook := pr.onMetricHook
	pr.mu.RUnlock()
	if hook != nil {
		hook(p.ID, p.RTT, p.PacketLoss)
	}

	return true
}

// LocalReflector implements a test loopback reflector that echoes probe packets back to senders
type LocalReflector struct {
	conn       *net.UDPConn
	targetAddr *net.UDPAddr
	quitChan   chan struct{}
	wg         sync.WaitGroup
}

// NewLocalReflector creates and binds an in-process local UDP reflector
func NewLocalReflector(bindIP string) (*LocalReflector, error) {
	addr, err := net.ResolveUDPAddr("udp4", fmt.Sprintf("%s:0", bindIP))
	if err != nil {
		return nil, err
	}
	conn, err := net.ListenUDP("udp4", addr)
	if err != nil {
		return nil, err
	}

	ref := &LocalReflector{
		conn:       conn,
		targetAddr: conn.LocalAddr().(*net.UDPAddr),
		quitChan:   make(chan struct{}),
	}

	ref.wg.Add(1)
	go ref.serve()

	return ref, nil
}

func (r *LocalReflector) Addr() *net.UDPAddr {
	return r.targetAddr
}

func (r *LocalReflector) Close() {
	close(r.quitChan)
	if r.conn != nil {
		_ = r.conn.Close()
	}
	r.wg.Wait()
}

func (r *LocalReflector) serve() {
	defer r.wg.Done()
	buf := make([]byte, 2048)

	for {
		select {
		case <-r.quitChan:
			return
		default:
		}

		_ = r.conn.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
		n, from, err := r.conn.ReadFromUDP(buf)
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue
			}
			select {
			case <-r.quitChan:
				return
			default:
				continue
			}
		}

		if n >= ProbePacketSize && buf[0] == ProbeMagic[0] && buf[1] == ProbeMagic[1] {
			// Echo probe packet directly back to sender
			_, _ = r.conn.WriteToUDP(buf[:n], from)
		}
	}
}
