package multipath

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"sync"
	"syscall"
	"time"
)

// Windows Winsock Option Constants
// On Windows, SO_BINDTODEVICE does NOT exist.
// Winsock provides IP_UNICAST_IF and IPV6_UNICAST_IF for binding a socket to an interface index.
const (
	IPPROTO_IP      = 0
	IPPROTO_IPV6    = 41
	IP_UNICAST_IF   = 31
	IPV6_UNICAST_IF = 31
)

var (
	ErrSocketClosed       = errors.New("socket is closed")
	ErrInterfaceMismatch  = errors.New("source IP does not belong to specified interface index")
	ErrInterfaceDown      = errors.New("specified network interface is down or disabled")
	ErrInvalidAddress     = errors.New("invalid local IP address specified for path socket")
)

// htonl converts a 32-bit integer to network byte order (big-endian) on little-endian hosts
func htonl(val uint32) uint32 {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, val)
	return binary.LittleEndian.Uint32(b)
}

// PathSocket manages a dedicated UDP socket pinned strictly to a specific local IP and interface
type PathSocket struct {
	mu            sync.RWMutex
	pathID        string
	localIP       string
	ifaceIndex    int
	ifaceName     string
	remoteAddr    *net.UDPAddr
	conn          *net.UDPConn
	isOpen        bool
	lastError     string
	inboundSink   func(data []byte, from *net.UDPAddr)
	quitChan      chan struct{}
	rxWorkerWg    sync.WaitGroup
	bytesSent     uint64
	bytesReceived uint64
}

// NewPathSocket initializes a PathSocket binding configuration
func NewPathSocket(pathID, localIP string, ifaceIndex int, ifaceName string) *PathSocket {
	return &PathSocket{
		pathID:     pathID,
		localIP:    localIP,
		ifaceIndex: ifaceIndex,
		ifaceName:  ifaceName,
	}
}

// NewMockPathSocket creates an in-memory mock socket for testing
func NewMockPathSocket(pathID string, isOpen bool) *PathSocket {
	return &PathSocket{
		pathID: pathID,
		isOpen: isOpen,
	}
}

// SetInboundSink registers the callback hook for packets arriving on this socket
func (s *PathSocket) SetInboundSink(sink func(data []byte, from *net.UDPAddr)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inboundSink = sink
}

// SetRemoteEndpoint configures the remote target address (reflector or relay)
func (s *PathSocket) SetRemoteEndpoint(addr *net.UDPAddr) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.remoteAddr = addr
}

// Open validates the interface and binds the UDP socket with Windows IP_UNICAST_IF pinning
func (s *PathSocket) Open() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.isOpen {
		return nil
	}

	// 1. Validate that the source IP actually belongs to the assigned interface index
	if err := validateInterfaceIP(s.localIP, s.ifaceIndex); err != nil {
		s.lastError = err.Error()
		return fmt.Errorf("interface validation failed for %s: %w", s.pathID, err)
	}

	// 2. Resolve local UDP address (ephemeral port)
	localUDP, err := net.ResolveUDPAddr("udp4", fmt.Sprintf("%s:0", s.localIP))
	if err != nil {
		s.lastError = err.Error()
		return fmt.Errorf("cannot resolve local UDP address %s: %w", s.localIP, err)
	}

	// 3. Bind socket to local source IP
	conn, err := net.ListenUDP("udp4", localUDP)
	if err != nil {
		s.lastError = err.Error()
		return fmt.Errorf("cannot listen on local UDP %s: %w", s.localIP, err)
	}

	// 4. Pin socket to Windows Interface Index via IP_UNICAST_IF
	if s.ifaceIndex > 0 {
		rawConn, rawErr := conn.SyscallConn()
		if rawErr == nil {
			var sockOptErr error
			_ = rawConn.Control(func(fd uintptr) {
				// Winsock IP_UNICAST_IF requires interface index in network byte order
				netIdx := int(htonl(uint32(s.ifaceIndex)))
				sockOptErr = syscall.SetsockoptInt(syscall.Handle(fd), IPPROTO_IP, IP_UNICAST_IF, netIdx)
			})
			if sockOptErr != nil {
				// Non-fatal if system driver doesn't support unicast if, but record in lastError
				s.lastError = fmt.Sprintf("IP_UNICAST_IF notice: %v", sockOptErr)
			}
		}
	}

	s.conn = conn
	s.isOpen = true
	s.quitChan = make(chan struct{})

	// 5. Start background receiver
	s.rxWorkerWg.Add(1)
	go s.rxLoop(conn, s.quitChan)

	return nil
}

// Close gracefully closes the socket and terminates receiver workers
func (s *PathSocket) Close() error {
	s.mu.Lock()
	if !s.isOpen {
		s.mu.Unlock()
		return nil
	}

	s.isOpen = false
	close(s.quitChan)
	if s.conn != nil {
		_ = s.conn.Close()
	}
	s.mu.Unlock()

	s.rxWorkerWg.Wait()
	return nil
}

// IsOpen returns whether the socket is currently operational
func (s *PathSocket) IsOpen() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.isOpen
}

// LocalPort returns the local ephemeral port assigned to this socket
func (s *PathSocket) LocalPort() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.conn == nil {
		return 0
	}
	if addr, ok := s.conn.LocalAddr().(*net.UDPAddr); ok {
		return addr.Port
	}
	return 0
}

// Send transmits a buffer to the configured remote endpoint or specific target
func (s *PathSocket) Send(data []byte, target *net.UDPAddr) error {
	s.mu.RLock()
	conn := s.conn
	isOpen := s.isOpen
	defaultTarget := s.remoteAddr
	s.mu.RUnlock()

	if !isOpen {
		return ErrSocketClosed
	}

	if conn == nil {
		// Mock / simulated socket mode
		s.mu.Lock()
		s.bytesSent += uint64(len(data))
		s.mu.Unlock()
		return nil
	}

	dst := target
	if dst == nil {
		dst = defaultTarget
	}
	if dst == nil {
		return errors.New("no remote target destination configured for path socket")
	}

	// Set write deadline to prevent indefinite blockage on socket congestion
	_ = conn.SetWriteDeadline(time.Now().Add(500 * time.Millisecond))
	n, err := conn.WriteToUDP(data, dst)
	if err != nil {
		s.mu.Lock()
		s.lastError = err.Error()
		s.mu.Unlock()
		return err
	}

	s.mu.Lock()
	s.bytesSent += uint64(n)
	s.mu.Unlock()
	return nil
}

// rxLoop continuously reads incoming datagrams from the bound socket
func (s *PathSocket) rxLoop(conn *net.UDPConn, quit <-chan struct{}) {
	defer s.rxWorkerWg.Done()

	buf := make([]byte, 65535)

	for {
		select {
		case <-quit:
			return
		default:
		}

		_ = conn.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
		n, from, err := conn.ReadFromUDP(buf)
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue
			}
			// If socket closed, terminate
			select {
			case <-quit:
				return
			default:
				continue
			}
		}

		if n > 0 {
			s.mu.Lock()
			s.bytesReceived += uint64(n)
			sink := s.inboundSink
			s.mu.Unlock()

			if sink != nil {
				pktCopy := make([]byte, n)
				copy(pktCopy, buf[:n])
				sink(pktCopy, from)
			}
		}
	}
}

// validateInterfaceIP verifies that the specified local IP is active on the given interface index
func validateInterfaceIP(ipStr string, ifIndex int) error {
	targetIP := net.ParseIP(ipStr)
	if targetIP == nil {
		return ErrInvalidAddress
	}

	ifaces, err := net.Interfaces()
	if err != nil {
		return err
	}

	var matchedIface *net.Interface
	for _, ifc := range ifaces {
		if ifc.Index == ifIndex {
			matchedIface = &ifc
			break
		}
	}

	if matchedIface == nil {
		return fmt.Errorf("interface index %d not found on Windows host", ifIndex)
	}

	if (matchedIface.Flags & net.FlagUp) == 0 {
		return ErrInterfaceDown
	}

	addrs, err := matchedIface.Addrs()
	if err != nil {
		return err
	}

	for _, a := range addrs {
		var ip net.IP
		switch v := a.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		if ip != nil && ip.Equal(targetIP) {
			return nil // Validated match
		}
	}

	return fmt.Errorf("%w (IP %s not found on interface %s index %d)",
		ErrInterfaceMismatch, ipStr, matchedIface.Name, ifIndex)
}
