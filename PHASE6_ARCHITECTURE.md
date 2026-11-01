# DualFlow — Phase 6 Architecture: Multi-Path Bonding Engine

## 1. System Overview

The Multi-Path Bonding Engine in DualFlow enables concurrent packet scheduling across multiple independent physical and virtual network interfaces on Windows 11 x64 without modifying host routing tables (`0.0.0.0/0`), default gateways, DNS configurations, or Windows Defender Firewall rules.

The engine sits between the IP Packet Engine (`internal/packet/`) and physical network adapters, providing interface-pinned UDP socket transport, deterministic packet distribution, dynamic latency-aware routing, duplicate packet suppression, and adaptive out-of-order reassembly.

---

## 2. Component Topology

```
                  ┌────────────────────────────────────────┐
                  │       Windows 11 Applications          │
                  └───────────────────┬────────────────────┘
                                      │ IP Packets
                                      ▼
                  ┌────────────────────────────────────────┐
                  │   Wintun Adapter ("DualFlow Tunnel")   │
                  └───────────────────┬────────────────────┘
                                      │ WintunReceivePacket
                                      ▼
                  ┌────────────────────────────────────────┐
                  │     Packet Engine (RFC 791 / 8200)     │
                  │  - Strict checksum & boundary checks   │
                  │  - Bounded FIFO ring queue (cap 1024)  │
                  └───────────────────┬────────────────────┘
                                      │ Validated IP Packets
                                      ▼
═══════════════════════════════════════════════════════════════════════════════
                   MULTI-PATH BONDING ENGINE (internal/multipath/)
═══════════════════════════════════════════════════════════════════════════════
                                      │
                                      ▼
                  ┌────────────────────────────────────────┐
                  │    Sequence Generator (Monotonic 64-bit)│
                  └───────────────────┬────────────────────┘
                                      │
                                      ▼
                  ┌────────────────────────────────────────┐
                  │         Multi-Path Scheduler           │
                  │  ┌───────────────┬──────────────────┐  │
                  │  │  Round-Robin  │   Weighted RR    │  │
                  │  ├───────────────┼──────────────────┤  │
                  │  │Lowest Latency │    Redundant     │  │
                  │  └───────────────┴──────────────────┘  │
                  └───────────┬────────────────┬───────────┘
               Path 1 Selected│                │Path 2 Selected (or Duplicate)
                              ▼                ▼
                     ┌────────────────┐┌────────────────┐
                     │DFTP Encoder #1 ││DFTP Encoder #2 │
                     └───────┬────────┘└───────┬────────┘
                             │                 │
                             ▼                 ▼
                     ┌────────────────┐┌────────────────┐
                     │  PathSocket 1  ││  PathSocket 2  │
                     │ (192.168.1.10) ││(192.168.217.231)│
                     │  ifIndex: 13   ││  ifIndex: 20   │
                     │ IP_UNICAST_IF  ││ IP_UNICAST_IF  │
                     └───────┬────────┘└───────┬────────┘
                             │                 │
                             ▼                 ▼
═════════════════════════════╪═════════════════╪═══════════════════════════════
                 PHYSICAL EGRESS & REMOTE REFLECTOR / RELAY
═════════════════════════════╪═════════════════╪═══════════════════════════════
                             │                 │
                             ▼                 ▼
                     ┌────────────────┐┌────────────────┐
                     │Realtek GbE NIC ││Android RNDIS   │
                     │(Home Modem/ISP)││(USB Tethering) │
                     └───────┬────────┘└───────┬────────┘
                             │                 │
                             └────────┬────────┘
                                      ▼
                     ┌─────────────────────────────────┐
                     │ Inbound Receiver & Demux        │
                     └────────────────┬────────────────┘
                                      │
                                      ▼
                     ┌─────────────────────────────────┐
                     │ Deduplication Filter            │
                     │ (1024-packet sliding bitmap)    │
                     └────────────────┬────────────────┘
                                      │ Unique frames only
                                      ▼
                     ┌─────────────────────────────────┐
                     │ Bounded Reorder Buffer          │
                     │ (Min-heap, 40ms timeout release)│
                     └────────────────┬────────────────┘
                                      │ Sequential in-order delivery
                                      ▼
                     ┌─────────────────────────────────┐
                     │ Wintun Ingress Sink / Host Stack│
                     └─────────────────────────────────┘
```

---

## 3. Detailed Component Architecture

### 3.1 Windows Interface Socket Binding (`PathSocket`)
- **Platform Invariant:** Windows NT does NOT implement the Linux `SO_BINDTODEVICE` socket option. Binding a socket solely to `0.0.0.0` or a generic port delegates routing entirely to the Windows default gateway table.
- **DualFlow Implementation Strategy:**
  1. **Source-IP Binding:** Each socket is explicitly resolved and bound to the interface's local IPv4 address via `net.ResolveUDPAddr("udp4", "<localIP>:0")` and `net.ListenUDP`. This ensures local address assignment.
  2. **Interface Pinning (`IP_UNICAST_IF`):** To prevent routing fallback across disparate gateways, DualFlow queries the socket's raw file descriptor via `SyscallConn().Control()` and applies the Winsock `IP_UNICAST_IF` option (`level = IPPROTO_IP (0)`, `optname = 31`). On Windows, the 32-bit interface index is converted to network byte order using `htonl`.
  3. **Operational Validation:** Prior to socket binding, `validateInterfaceIP` cross-checks `net.Interfaces()` to verify that the local IP belongs to the interface index and that `net.FlagUp` is asserted.

### 3.2 Schedulers (`PacketScheduler`)
All schedulers implement a unified interface supporting runtime mode switching without engine restarts:
- **Round-Robin (`RoundRobinScheduler`):**
  - Distributes logical packets sequentially across all paths where `IsEligible() == true`.
  - Thread-safe atomic counter modulo eligible path count. Skips degraded or closed sockets.
- **Weighted Round-Robin (`WeightedRRScheduler`):**
  - Implements the NGINX smooth weighted round-robin algorithm.
  - Accumulates configured weights into per-path runtime registers, selects the maximum, and deducts total weight.
  - Guarantees proportional transmission without bursting or starving lower-weight paths.
- **Lowest-Latency First (`LowestLatencyScheduler`):**
  - Directs traffic to the path with lowest verified RTT.
  - Evaluates measurements with a 10ms hysteresis threshold to eliminate high-frequency route oscillation (route flapping).
  - Automatically rejects stale measurements (> 5000ms old) and falls back gracefully to Round-Robin when metrics are missing.
- **Redundant (`RedundantScheduler`):**
  - Duplicates logical packets across all eligible paths concurrently.
  - All copies share the identical 64-bit sequence number. Copies beyond the primary path are marked with `FlagDuplicate = 1 << 4`.
  - Minimizes packet loss probability for competitive gaming and latency-critical interactive streams.

### 3.3 Sequence Management, Deduplication & Reordering
- **Monotonic Sequence Generator:** 64-bit atomic unsigned integer counter with unsigned delta arithmetic (`int64(a - b)`) to handle wraparound transparently.
- **Deduplication Filter (`DedupFilter`):**
  - 1024-packet sliding window bitmap.
  - Automatically identifies and suppresses duplicate frames arriving over secondary paths in Redundant mode or due to network reflections.
  - Drops packets outside the tracking window as stale duplicates.
- **Reorder Buffer (`ReorderBuffer`):**
  - Priority min-heap tracking sequence IDs with capacity bounded at 256 packets.
  - Delivers strictly in-order packets immediately (`diff == 0`).
  - Buffers out-of-order packets (`diff > 0`) caused by transit delay differences between paths (e.g. 15ms fiber vs 45ms LTE).
  - Background 10ms ticker enforces a 40ms timeout release: if a packet is missing, subsequent buffered packets are released to prevent pipeline stalling.

### 3.4 Active Path Prober & Health Monitoring
- **Probe Dispatch:** Sends lightweight 32-byte binary datagrams (`DFPB` magic + 64-bit probe ID + 64-bit Tx timestamp + Path tag) through the interface-bound socket.
- **Frequency:** Configurable interval (default: 1000ms), timeout (default: 1500ms).
- **Metric Formulation:**
  - `RTT`: Measured round-trip delta between transmit and reflection receipt.
  - `RTT Jitter (EWMA)`: `Jitter_new = Jitter_old + (|RTT_sample - RTT_avg| - Jitter_old) / 16`.
  - `Packet Loss`: Measured over rolling window of recent probe attempts.
- **Local Reflector:** Integrated in-process UDP loopback reflector for automated testing and local verification without external network dependency.

### 3.5 Protocol & IPC Integration
- **DFTP v1 Frame Header:** 32-byte binary framing preserved. Byte 5: `MsgType`, Bytes 6:8: `Flags` (`FlagCompressed`, `FlagEncrypted`, `FlagUrgent`, `FlagTestFrame`, `FlagDuplicate`), Bytes 16:24: `SequenceNumber`, Bytes 28:32: `Checksum` (CRC32-IEEE).
- **Go Core JSON-RPC Commands:**
  - `multipath_get_status`
  - `multipath_set_mode`
  - `multipath_set_weights`
- **Electron IPC Push:** Broadcasts `multipath-stats-updated` to renderer at 1500ms intervals.
- **Renderer UI (`AggregationEngineCard.tsx`):** Displays engine status, live path cards, scheduler selector buttons, and reordering/drop metrics with zero mock telemetry.

