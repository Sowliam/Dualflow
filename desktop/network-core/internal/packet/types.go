package packet

import (
	"errors"
	"net"
	"time"
)

// Supported IP versions
const (
	IPv4Version = 4
	IPv6Version = 6

	MinIPv4HeaderLen = 20
	MinIPv6HeaderLen = 40
	MaxPacketSize    = 65535
)

// Standard IP protocols
const (
	ProtoICMP   = 1
	ProtoTCP    = 6
	ProtoUDP    = 17
	ProtoIPv6ICMP = 58
)

// IPv6 Extension Header Types
const (
	IPv6HopByHop   = 0
	IPv6Routing    = 43
	IPv6Fragment   = 44
	IPv6ESP        = 50
	IPv6AH         = 51
	IPv6DestOpts   = 60
	IPv6NoNextHdr  = 59
)

// DropReason enumerates granular reasons for packet drops
type DropReason string

const (
	DropInvalidPacket        DropReason = "invalid_packet"
	DropQueueFull            DropReason = "queue_full"
	DropTransportUnavailable DropReason = "transport_unavailable"
	DropTransportWriteFailed DropReason = "transport_write_failed"
	DropWintunSendFailed     DropReason = "wintun_send_failed"
	DropSessionClosed        DropReason = "session_closed"
)

var (
	ErrPacketTooShort        = errors.New("packet is shorter than minimum header size")
	ErrPacketTooLarge        = errors.New("packet exceeds maximum allowed size (65535 bytes)")
	ErrUnsupportedIPVersion  = errors.New("unsupported IP version (must be IPv4 or IPv6)")
	ErrInvalidIHL            = errors.New("invalid IPv4 internet header length (IHL < 5 or exceeds packet)")
	ErrIPv4LengthMismatch    = errors.New("IPv4 total length does not match or exceeds buffer length")
	ErrInvalidIPv4Checksum   = errors.New("IPv4 header checksum verification failed")
	ErrIPv6PayloadMismatch   = errors.New("IPv6 payload length does not match or exceeds buffer length")
	ErrCorruptExtensionHdr   = errors.New("corrupted IPv6 extension header bounds")
	ErrExtensionHeaderLimit  = errors.New("exceeded maximum IPv6 extension header chain limit (loop guard)")
	ErrQueueFull             = errors.New("bounded queue capacity reached")
	ErrQueueClosed           = errors.New("bounded queue is closed")
)

// Packet represents a parsed and validated network layer packet
type Packet struct {
	Raw         []byte    `json:"-"`
	Version     uint8     `json:"version"`      // 4 or 6
	Protocol    uint8     `json:"protocol"`     // TCP, UDP, ICMP, etc.
	SrcIP       net.IP    `json:"src_ip"`
	DstIP       net.IP    `json:"dst_ip"`
	TotalLength int       `json:"total_length"`
	HeaderLength int      `json:"header_length"`
	CapturedAt  time.Time `json:"captured_at"`
}

