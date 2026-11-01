package wintun

const (
	// DefaultAdapterName is the official virtual adapter name for DualFlow
	DefaultAdapterName = "DualFlow Tunnel"

	// DefaultTunnelType is the tunnel pool identifier
	DefaultTunnelType = "DualFlow"

	// Ring buffer constraints according to official wintun.h
	MinRingCapacity     uint32 = 0x20000   // 128 KiB
	DefaultRingCapacity uint32 = 0x400000  // 4 MiB
	MaxRingCapacity     uint32 = 0x4000000 // 64 MiB

	// Packet constraints
	MaxPacketSize     = 0xFFFF // 65535 bytes
	MinIPv4PacketSize = 20     // Minimal IPv4 header length
	MinIPv6PacketSize = 40     // Minimal IPv6 header length
)

// Driver states
const (
	DriverStatusNotInstalled = "NOT INSTALLED"
	DriverStatusAvailable    = "AVAILABLE"
	DriverStatusError        = "ERROR"
)

// Adapter states
const (
	AdapterStatusNotCreated = "ADAPTER NOT CREATED"
	AdapterStatusReady      = "ADAPTER READY"
	AdapterStatusError      = "ERROR"
)

// Session states
const (
	SessionStatusStopped = "SESSION STOPPED"
	SessionStatusRunning = "SESSION RUNNING"
	SessionStatusError   = "ERROR"
)

// Status provides a complete snapshot of Wintun runtime, adapter, and session health
type Status struct {
	DriverStatus   string `json:"driver_status"`
	DriverVersion  string `json:"driver_version"`
	DriverPath     string `json:"driver_path"`
	IsElevated     bool   `json:"is_elevated"`
	AdapterExists  bool   `json:"adapter_exists"`
	AdapterName    string `json:"adapter_name"`
	AdapterStatus  string `json:"adapter_status"`
	SessionStatus  string `json:"session_status"`
	LastError      string `json:"last_error,omitempty"`
	RxPackets      uint64 `json:"rx_packets"`
	TxPackets      uint64 `json:"tx_packets"`
	RxBytes        uint64 `json:"rx_bytes"`
	TxBytes        uint64 `json:"tx_bytes"`
	UptimeMs       int64  `json:"uptime_ms"`
}

// Stats provides atomic counters for high-frequency telemetry polling
type Stats struct {
	RxPackets       uint64 `json:"rx_packets"`
	TxPackets       uint64 `json:"tx_packets"`
	RxBytes         uint64 `json:"rx_bytes"`
	TxBytes         uint64 `json:"tx_bytes"`
	LastRxTimestamp int64  `json:"last_rx_timestamp,omitempty"`
	LastTxTimestamp int64  `json:"last_tx_timestamp,omitempty"`
	DroppedPackets  uint64 `json:"dropped_packets"`
	LastError       string `json:"last_error,omitempty"`
}

