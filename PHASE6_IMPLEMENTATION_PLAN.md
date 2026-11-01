# DualFlow — Phase 6 Implementation Plan: Multi-Path Bonding Engine

## 1. Existing Architecture Findings & Audit

### 1.1 Go Network Core Daemon (`cmd/dualflow-core/`, `internal/app/`)
- **Lifecycle & Stdin/Stdout RPC:** The Go daemon runs as a child process of Electron, communicating via JSON-RPC protocol version `1.0.0` over Stdin/Stdout lines.
- **Graceful Shutdown:** Receives Stdin EOF when Electron closes, tears down active sessions, and terminates with exit code 0.
- **Provider Architecture:** Provides `go` as primary provider and `ts` as fallback.

### 1.2 Packet Pipeline (`internal/packet/`)
- **Validation:** Strict IPv4 (RFC 791 / RFC 1071) and IPv6 (RFC 8200) parsers validating checksums, IHL, version, total length, extension headers, and padding.
- **Buffering:** Thread-safe bounded FIFO ring queue (`queue.go`, capacity 1024) with non-blocking drop-tail push semantics.
- **Telemetry & Drop Accounting:** Atomic tracking of validated, rejected, queued, and dropped packets with exact root-cause classification (`invalid_packet`, `queue_full`, `transport_unavailable`, `transport_write_failed`, `wintun_send_failed`, `session_closed`).

### 1.3 Transport Codec & Session (`internal/transport/`)
- **Framing:** 32-byte binary DFTP v1 header (`Magic: DFTP`, `Version: 1`, `MsgType: uint8`, `Flags: uint16`, `HeaderLength: 32`, `SessionID: uint32`, `SequenceNumber: uint64`, `PayloadLength: uint32`, `Checksum: uint32` CRC32-IEEE).
- **Stream Decoder:** Robust state machine capable of reassembling split/partial frames, coalesced frames, and resynchronizing after stream noise.
- **In-Memory Harness:** `InMemoryTransport` provides an isolated loopback test harness (`is_test: true`) without touching host sockets.

### 1.4 Virtual Adapter (`internal/wintun/`)
- **Driver:** Official WireGuard `wintun.dll` (v0.14.1) DigiCert EV signed.
- **Lifecycle:** Dynamically loads procedures (`WintunCreateAdapter`, `WintunOpenAdapter`, `WintunStartSession`, `WintunReceivePacket`, `WintunSendPacket`, `WintunEndSession`, `WintunCloseAdapter`).
- **Elevation Detection:** Canonical token check (`TokenElevation = 20`) preventing crashes and reporting `Win32 ERROR_ACCESS_DENIED (5)` cleanly when non-elevated.
- **Verification:** Verified live adapter creation, ring buffer start/stop, and 92-byte packet transmission in Windows kernel with Administrator privileges.

### 1.5 Network Discovery & Telemetry (`internal/discovery/`, `internal/telemetry/`, `internal/paths/`)
- **Enumeration:** Real Windows adapters discovered via `iphlpapi.dll` (`GetAdaptersAddresses`) and Registry fallback.
- **Hardware Verified:** Realtek PCIe GbE Family Controller (`192.168.1.10`, Gateway `192.168.1.1`) mapped to Path 1 (Modem). Android USB Tethering RNDIS (`192.168.217.231`, Gateway `192.168.217.128`) mapped to Path 2 (Android).
- **Paths Engine:** `PathManager` coordinates adapter assignment, throughput metrics, and probe status.

### 1.6 Architectural Conflicts & Resolution
1. **Windows Socket Binding (`SO_BINDTODEVICE` does not exist on Windows):**
   - *Conflict:* Linux socket option `SO_BINDTODEVICE` is unavailable on Windows NT.
   - *Resolution:* Use `net.ResolveUDPAddr` with explicit local source IP (`localIP:0`). On Windows, binding a UDP socket to a specific local interface IP (`192.168.1.10` or `192.168.217.231`) ensures the Windows kernel routing stack assigns and outputs packets through that specific interface. For additional interface pinning, we evaluate `IP_UNICAST_IF` / `IPV6_UNICAST_IF` via `syscall.SetsockoptInt` on the socket file descriptor if available.
2. **DFTP Protocol Multi-Path Awareness:**
   - *Conflict:* Current DFTP v1 frames do not explicitly identify path IDs or redundant copies.
   - *Resolution:* Utilize existing `Flags` field: `FlagDuplicate = 1 << 4`, `FlagProbe = 1 << 5`. Utilize `Reserved` byte 10 as `PathID` (1 or 2). This maintains 100% backward compatibility with DFTP v1 32-byte binary framing.
3. **Out-of-Order Delivery & Delay Variance:**
   - *Conflict:* Multiplexing packets across disparate connections (e.g. Fiber vs 4G LTE) causes high packet reordering.
   - *Resolution:* Implement a bounded sliding-window Reorder Buffer in `internal/multipath/reorder.go` with configurable timeout release.

---

## 2. Proposed Package Structure (`internal/multipath/`)

```
desktop/network-core/internal/multipath/
├── config.go            # Engine, socket, and scheduler configuration parameters
├── dedup.go             # Sliding-window sequence deduplication filter
├── health.go            # Health state machine & threshold evaluation
├── manager.go           # High-level Multi-Path Engine orchestrator
├── path.go              # Path entity, interface binding metadata & atomic counters
├── prober.go            # Active RTT & EWMA jitter measurement worker
├── reorder.go           # Sliding-window reorder buffer with timeout release
├── scheduler.go         # PacketScheduler interface & registry
├── scheduler_latency.go # Lowest-Latency First (LLF) scheduler with hysteresis
├── scheduler_redundant.go# Redundant packet duplication scheduler
├── scheduler_rr.go      # Deterministic Round-Robin scheduler
├── scheduler_weighted.go# Weighted Round-Robin (WRR) scheduler
├── sequence.go          # Monotonic 64-bit sequence allocator with wraparound support
├── socket.go            # Windows interface-bound UDP socket implementation
├── stats.go             # Per-path and engine-wide atomic telemetry stats
└── multipath_test.go    # Comprehensive unit and simulated multi-path tests
```

---

## 3. Packet Flow Design

```
[Inbound from Wintun / Ingestion]
        │
        ▼
[Packet Validator (RFC 791/8200)]
        │
        ▼
[Sequence Number Assignment (Monotonic 64-bit)]
        │
        ▼
[Multi-Path Scheduler]
   ├── Round-Robin: Path 1 -> Path 2 -> Path 1...
   ├── Weighted RR: Weight-proportional selection
   ├── Lowest-Latency: Min RTT with hysteresis
   └── Redundant: Copy to BOTH Path 1 and Path 2
        │
   ┌────┴────────────────────────┐
   ▼                             ▼
[PathSocket 1 (Realtek)]     [PathSocket 2 (Android RNDIS)]
(Bind: 192.168.1.10)        (Bind: 192.168.217.231)
   │                             │
   ▼                             ▼
[DFTP Frame TX]              [DFTP Frame TX]
        │                             │
   ═══════════════════════════════════════ (Network / Reflector / Relay)
        │                             │
   ▼                             ▼
[DFTP Frame RX]              [DFTP Frame RX]
   └────┬────────────────────────┘
        │
        ▼
[Deduplication Filter (Bitmap / Circular Window)]
        │ (drops duplicates from Redundant mode)
        ▼
[Reorder Buffer (Bounded Priority Window)]
        │ (reorders frames arriving with latency differences)
        ▼
[Delivery to Wintun / Host Sink]
```

---

## 4. Socket Binding Strategy on Windows 11

1. **Local Address Resolution:**
   - Resolve local IP: `localAddr, err := net.ResolveUDPAddr("udp4", fmt.Sprintf("%s:0", path.LocalIP))`
   - Create listener/dialer: `conn, err := net.ListenUDP("udp4", localAddr)`
2. **Interface Validation:**
   - Verify that `path.LocalIP` is assigned to `path.InterfaceIndex` using `net.Interfaces()` and `net.InterfaceAddrs()`.
   - If interface index does not match or interface is down, reject socket initialization (`ErrInterfaceMismatch`).
3. **Interface Index Pinning (`IP_UNICAST_IF`):**
   - Extract raw Windows socket handle using `conn.SyscallConn()`.
   - Invoke `setsockopt` with `IP_UNICAST_IF` (IPv4) or `IPV6_UNICAST_IF` (IPv6) where supported by Windows 11 kernel, passing network byte order interface index.
   - If syscall is rejected by driver/platform, log warning and rely on explicit source-IP binding.
4. **Failure Detection & Recovery:**
   - If write fails with network unreachable, mark path `UNAVAILABLE`, close socket safely, and initiate backoff reconnection.

---

## 5. Scheduler Design

### 5.1 Interface
```go
type PacketScheduler interface {
    Name() string
    SelectPaths(pkt *packet.Packet, paths []*Path) ([]*Path, error)
    UpdateMetrics(pathID string, rtt time.Duration, loss float64)
    SetWeights(weights map[string]uint32) error
    Reset()
}
```

### 5.2 Schedulers
1. **Round-Robin (`RoundRobinScheduler`):**
   - Atomic index increment modulo number of healthy eligible paths.
   - Skips `DEGRADED`, `UNAVAILABLE`, or closed paths.
2. **Weighted Round-Robin (`WeightedRRScheduler`):**
   - Deficit Round Robin (DRR) or classic weight accumulation.
   - Rejects zero or negative weights (minimum weight: 1).
3. **Lowest-Latency First (`LowestLatencyScheduler`):**
   - Chooses path with minimum measured EWMA RTT.
   - Configurable hysteresis threshold (e.g. 10ms or 15%) to prevent route flapping / oscillation.
   - Fallback to Round-Robin if no valid RTT measurements are available.
4. **Redundant (`RedundantScheduler`):**
   - Returns ALL eligible paths.
   - Packet is assigned single logical sequence number; copies are flagged with `FlagDuplicate`.

---

## 6. Sequence, Reordering & Deduplication Design

1. **Sequence Space:** Monotonic 64-bit integer (`uint64`). Handles wraparound cleanly using unsigned delta arithmetic.
2. **Deduplication Filter:**
   - Sliding bitmap window (size: 1024 packets).
   - If `seq <= highestDelivered - WindowSize`, drop as stale duplicate.
   - If already marked in bitmap, drop as duplicate.
3. **Reorder Buffer:**
   - Min-heap or circular array bounded at 256 packets.
   - Maximum holding time: configurable (default: 40ms).
   - On timeout, release in-order available packets and advance expected sequence to prevent pipeline stalling.

---

## 7. Active Path Prober Design

1. **Heartbeat Probing:**
   - Configurable probe interval (default: 1000ms, not 250ms).
   - Lightweight 32-byte DFTP Ping packet with monotonic probe sequence and timestamp.
2. **Metric Calculation:**
   - RTT: measured delta between transmit timestamp and receive timestamp.
   - RTT Jitter (EWMA): `Jitter_new = Jitter_old + (|RTT_sample - RTT_avg| - Jitter_old) / 16`.
   - Packet Loss: rolling window of last 100 probe samples.
3. **Reflector:**
   - Local test loopback reflector provided for unit and integration testing without internet access.
   - Remote reflector support via configured endpoint.

---

## 8. UI Integration Plan (`AggregationEngineCard.tsx`)

1. **Engine Status Badge:** `STOPPED`, `STARTING`, `RUNNING`, `DEGRADED`, `ERROR`.
2. **Path Telemetry Cards:**
   - Realtek Ethernet (`192.168.1.10`) & Android RNDIS (`192.168.217.231`).
   - Displays real RTT, RTT Jitter (EWMA), Loss %, and TX/RX bytes.
   - Displays `—` when metrics are not available; zero fake data.
3. **Mode Selector:**
   - Buttons for `Round-Robin`, `Weighted RR`, `Lowest Latency`, `Redundant`.
   - Triggers IPC command `dualflow:multipath-set-mode`.
4. **Engine Statistics:**
   - Logical packets, scheduled per path, duplicates dropped, reordered frames.

---

## 9. Safety Constraints

1. Never modify Windows routing tables (`0.0.0.0` default route untouched).
2. Never modify Windows DNS configuration.
3. Never modify Windows Firewall or NAT/ICS.
4. Never assume an adapter is usable without validating interface index and link status.
5. Never leak goroutines on shutdown.
6. Strictly bound all queues, buffers, and timeouts.

---

## 10. Test Matrix

| Category | Tests | Verification Level |
| :--- | :--- | :--- |
| **Sockets** | Valid source IP, invalid source IP, wrong interface index, socket closure, recreation | Unit & Loopback |
| **Schedulers** | RR deterministic order, WRR capacity weights, LLF selection & hysteresis, Redundant copying | Unit |
| **Reorder/Dedup** | In-order delivery, out-of-order reordering, duplicate suppression, timeout release, wraparound | Unit |
| **Prober** | RTT measurement, EWMA jitter calculation, timeout handling, stale detection | Unit & Loopback |
| **Integration** | Two-path simulation, unequal latency reordering, path disconnection, dynamic mode switching | Simulated Multi-Path |
| **Race Detection** | `go test -race ./internal/multipath/...` | Race Verification |

