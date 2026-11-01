package transport

import (
	"errors"
)

// Magic bytes identifying DualFlow Transport Protocol ("DFTP")
var MagicBytes = [4]byte{'D', 'F', 'T', 'P'}

const (
	ProtocolVersion1       uint8  = 1
	ProtocolVersion3       uint8  = 3
	CurrentProtocolVersion uint8  = ProtocolVersion3
	HeaderLength           uint16 = 32
	MaxPayloadLength       uint32 = 65535

	// Precise MTU and Frame Budgeting
	AssumedPathMTU       = 1420
	MaxCompleteIPv4Size  = 1372
	MaxCompleteIPv6Size  = 1392
	MaxCompleteUDPSize   = 1352
	MaxDFTPFrameSize     = 1344
	MaxStreamChunkSize   = 1280
	InnerStreamHeaderLen = 16
	AEADTagLength        = 16
	MaxSackBlocks        = 8
)

// Message Types
const (
	MsgTypeData          uint8 = 0x01
	MsgTypePing          uint8 = 0x02
	MsgTypePong          uint8 = 0x03
	MsgTypeFlowOpen      uint8 = 0x04
	MsgTypeClose         uint8 = 0x05
	MsgTypeStreamAck     uint8 = 0x06
	MsgTypePathChallenge uint8 = 0x07
	MsgTypePathResponse  uint8 = 0x08
	MsgTypeFlowOpenAck   uint8 = 0x09
	MsgTypeHandshakeInit uint8 = 0x10
	MsgTypeHandshakeResp uint8 = 0x11
)

// Transport Header Flags
const (
	FlagCompressed uint16 = 1 << 0
	FlagEncrypted  uint16 = 1 << 1
	FlagUrgent     uint16 = 1 << 2
	FlagTestFrame  uint16 = 1 << 3
	FlagDuplicate  uint16 = 1 << 4
)

// Stream Header Flags
const (
	FlagStreamSYN uint16 = 0x0001
	FlagStreamFIN uint16 = 0x0002
	FlagStreamRST uint16 = 0x0004
	FlagStreamACK uint16 = 0x0008
)

// SOCKS5 / Flow Address Types
const (
	AddrTypeIPv4   uint8 = 0x01
	AddrTypeDomain uint8 = 0x03
	AddrTypeIPv6   uint8 = 0x04
)

// SOCKS5 Status Codes
const (
	StatusSuccess         uint8 = 0x00
	StatusGeneralFailure  uint8 = 0x01
	StatusNetworkUnreach  uint8 = 0x03
	StatusHostUnreachable uint8 = 0x04
	StatusConnRefused     uint8 = 0x05
	StatusTTLExpired      uint8 = 0x06
)

// Connection States
type TransportState string

const (
	StateDisconnected TransportState = "DISCONNECTED"
	StateConnecting   TransportState = "CONNECTING"
	StateConnected    TransportState = "CONNECTED"
	StateError        TransportState = "ERROR"
)

var (
	ErrInvalidMagic           = errors.New("invalid transport magic bytes (expected 'DFTP')")
	ErrUnsupportedVersion     = errors.New("unsupported transport protocol version")
	ErrInvalidHeaderLength    = errors.New("invalid header length in transport frame")
	ErrPayloadTooLarge        = errors.New("frame payload exceeds maximum 65535 bytes")
	ErrChunkTooLarge          = errors.New("stream chunk exceeds maximum 1280 bytes")
	ErrChecksumMismatch       = errors.New("frame CRC32 payload checksum mismatch")
	ErrIncompleteHeader       = errors.New("buffer does not contain a full 32-byte header")
	ErrIncompletePayload      = errors.New("buffer does not contain full frame payload")
	ErrIncompleteStreamHeader = errors.New("buffer does not contain full 16-byte inner stream header")
	ErrMalformedSackPayload   = errors.New("malformed stream ack sack payload")
	ErrMalformedFlowOpen      = errors.New("malformed flow open payload")
	ErrMalformedPathChallenge = errors.New("malformed path challenge/response payload")
	ErrTooManySackBlocks      = errors.New("sack blocks exceed maximum limit of 8")
)

// FrameHeader represents the 32-byte binary tunnel transport header
type FrameHeader struct {
	Magic          [4]byte `json:"magic"`
	Version        uint8   `json:"version"`
	MsgType        uint8   `json:"msg_type"`
	Flags          uint16  `json:"flags"`
	HeaderLength   uint16  `json:"header_length"`
	Reserved       uint16  `json:"reserved"`
	SessionID      uint32  `json:"session_id"`
	SequenceNumber uint64  `json:"sequence_number"`
	PayloadLength  uint32  `json:"payload_length"`
	PathID         uint16  `json:"path_id"`
	Reserved2      uint16  `json:"reserved2"`
	Checksum       uint32  `json:"checksum,omitempty"` // For Version 1 compatibility
}

// Frame represents a full decoded transport frame
type Frame struct {
	Header  FrameHeader `json:"header"`
	Payload []byte      `json:"payload"`
}

// StreamHeader represents the 16-byte inner stream framing header
type StreamHeader struct {
	FlowID        uint32 `json:"flow_id"`
	StreamOffset  uint64 `json:"stream_offset"`
	StreamFlags   uint16 `json:"stream_flags"`
	PayloadLength uint16 `json:"payload_length"`
}

// StreamFrame represents an inner application stream chunk
type StreamFrame struct {
	Header StreamHeader `json:"header"`
	Data   []byte       `json:"data"`
}

// SackBlock represents a contiguous half-open stream byte range [StartOffset, EndOffset)
type SackBlock struct {
	StartOffset uint64 `json:"start_offset"`
	EndOffset   uint64 `json:"end_offset"`
}

// StreamAckPayload represents MsgTypeStreamAck selective acknowledgment data
type StreamAckPayload struct {
	FlowID              uint32      `json:"flow_id"`
	CumulativeAckOffset uint64      `json:"cumulative_ack_offset"`
	NumSackBlocks       uint16      `json:"num_sack_blocks"`
	Reserved            uint16      `json:"reserved"`
	SackBlocks          []SackBlock `json:"sack_blocks"`
}

// FlowOpenPayload represents MsgTypeFlowOpen connection request
type FlowOpenPayload struct {
	FlowID   uint32 `json:"flow_id"`
	Port     uint16 `json:"port"`
	AddrType uint8  `json:"addr_type"`
	Addr     string `json:"addr"`
}

// FlowOpenAckPayload represents MsgTypeFlowOpenAck result confirmation
type FlowOpenAckPayload struct {
	FlowID uint32 `json:"flow_id"`
	Status uint8  `json:"status"`
}

// PathChallengePayload represents MsgTypePathChallenge and MsgTypePathResponse
type PathChallengePayload struct {
	PathID uint16  `json:"path_id"`
	Nonce  [8]byte `json:"nonce"`
}

// TransportStats contains telemetry metrics for the transport layer
type TransportStats struct {
	State          TransportState `json:"state"`
	IsTest         bool           `json:"is_test"`
	TxPackets      uint64         `json:"tx_packets"`
	RxPackets      uint64         `json:"rx_packets"`
	TxBytes        uint64         `json:"tx_bytes"`
	RxBytes        uint64         `json:"rx_bytes"`
	DroppedPackets uint64         `json:"dropped_packets"`
	LastError      string         `json:"last_error,omitempty"`
}
