# DualFlow Phase 7 — Implementation Plan v3.0
## Release-Ready Engineering Plan: Local Self-Hosted Secure Relay & Real Bonding

**Document Version:** 3.0.0 (Consistency Patched)  
**Target Environment:** Windows 11 Pro x64 (Local Development) & Linux VPS (Future Production)  
**Parent Foundation:** Phases 1–6 Go Network Core & Wintun Integration  
**Roadmap Alignment:** Strictly Phase 7 of 8. No additional phases introduced.  
**Safety Invariant:** Zero modifications to host default routes (`0.0.0.0/0`), DNS, firewall, NAT, or ICS.

---

## 1. Executive Summary & Core Architectural Decisions

This document establishes the release-ready implementation plan for DualFlow Phase 7. As the final technical specification, it resolves all architectural ambiguities:

1. **Primary Forwarding Architecture:** **User-Space TCP Stream Proxy (Layer 4 Stream Bonding)**.
   - Raw IP packet tunneling through Wintun remains in the codebase as the Phase 4/5 foundation, but is explicitly separated from Phase 7 stream bonding.
   - **Destination Contract:** Standard **SOCKS5 (RFC 1928)** proxy protocol on `127.0.0.1:10800` with an optional Fixed-Destination mode (`--target <ip:port>`) for direct benchmark harnesses (e.g. `iperf3`). Distinguishes single TCP data streams from control connections.
   - **Stream Reliability Invariant:** No stream gap is ever skipped upon timeout. Missing chunks are actively recovered via selective retransmission (`MsgTypeStreamAck`). Flows experiencing unrecoverable packet loss terminate gracefully (`StreamRST`) rather than delivering corrupted byte streams.
2. **Formal Cryptographic Standard:** **`Noise_IKpsk2_25519_AESGCM_SHA256` via `github.com/flynn/noise v1.1.0`**.
   - Standard Noise Protocol implementation without bespoke HKDF replacements.
   - Out-of-order packet encryption utilizes the native `cs.Cipher().Encrypt(out, seq, ad, plaintext)` and `cs.Cipher().Decrypt(out, seq, ad, ciphertext)` APIs, passing explicit 64-bit sequence numbers.
   - **Nonce Construction:** Standard Noise AESGCM 12-byte nonce: 4 bytes of zero (`0x00, 0x00, 0x00, 0x00`) followed by 8 bytes big-endian sequence number. Directional key separation guarantees no nonce is ever reused under the same key.
   - **Rekeying & Session Lifecycle:** Rekeying triggers a fresh standard Noise handshake establishing a new Session ID and fresh keys. Old sessions drain during a 5-second grace period.
3. **Anti-Replay & Path Validation Hardening:**
   - **Two-Phase Concurrency-Safe Locking:** Replay check uses **RLock pre-check $\to$ unlock $\to$ AEAD authentication $\to$ Lock $\to$ re-check $\to$ commit**. Eliminates Go data races while ensuring unauthenticated packets cannot advance replay state.
   - **Replay Window Sizing:** Scaled to **2048 packets** (32 $\times$ `uint64`), providing a defined reordering budget: at 20,000 pps, accommodates up to 102.4 ms of path RTT divergence; at 10,000 pps, accommodates 204.8 ms.
   - **Cryptographic Path Challenge/Response:** Newly observed `RemoteAddr` endpoints remain in `PathStatePending` until confirmed via `MsgTypePathChallenge` and `MsgTypePathResponse`. Return traffic is never routed to unvalidated endpoints.
4. **Strict Datagram Sizing & MTU Accounting:**
   - **Assumed Path MTU:** **1420 bytes** (conservative cellular minimum).
   - **Complete IP Packet Size:** Strictly **1372 bytes** on IPv4 and **1392 bytes** on IPv6 (both $\le 1420$ bytes).
   - **Complete UDP Datagram Size:** Strictly **1352 bytes** for both IPv4 and IPv6.
   - **UDP Payload / DFTP Frame Size:** Strictly **1344 bytes**.
   - **Stream Chunk Data Size:** Strictly **1280 bytes**, guaranteeing zero IP-level fragmentation across both IPv4 and IPv6 on mobile cellular (1420 MTU) and Ethernet links.
5. **Operational Modes & Zero Paid VPS:**
   - **Local Development Mode:** Single-workstation testing over `127.0.0.1:51820`. Verifies proxying, handshake, cryptography, and reassembly. **Explicitly prohibited from claiming physical egress or Internet aggregation.**
   - **Real Network Validation Mode:** Multi-device testing with a separate physical endpoint (e.g. secondary PC on LAN). Validates physical NIC egress, multi-path arrival, and real throughput aggregation.

---

## 2. Shared Source-of-Truth Parameter Table

This table is the normative specification across all Phase 7 documentation. Every document, component, and implementation artifact must conform strictly to these parameters:

| Parameter Category | Specification Parameter | Normative Value | Architectural Justification |
|---|---|---|---|
| **Protocol Identity** | Protocol Version | `3` (DFTP v3) | Distinct major version reflecting out-of-order AEAD and stream SACK framing. |
| | Magic Bytes | `0x44465450` (`"DFTP"`) | 4-byte ASCII magic identifier for datagram demarcation. |
| | Header Length | `32` bytes | 64-bit aligned fixed header covering transport routing and security. |
| **Datagram Limits** | Assumed Path MTU | `1420` bytes | Conservative path MTU for mobile cellular (4G/5G LTE) and PPPoE networks. |
| | Max Complete IP Packet | `1372` bytes (IPv4) / `1392` bytes (IPv6) | Enforces 28-byte safety margin below the 1420-byte path MTU on IPv6 (48B on IPv4). |
| | Max Complete UDP Datagram | `1352` bytes (both IPv4 and IPv6) | Maximum size of complete UDP datagram (8B UDP header + 1344B DFTP frame). |
| | UDP Payload / DFTP Frame | `1344` bytes | Total size of outer header (32B) + inner header (16B) + chunk (1280B) + tag (16B). |
| | Max Stream Chunk Data | `1280` bytes | $1344 - 32\text{ (DFTP)} - 16\text{ (Inner Hdr)} - 16\text{ (Tag)} = 1280$ bytes clean power-of-two data. |
| | Minimum Valid Datagram | `32` bytes | Outer DFTP header minimum size (handshake/control messages). |
| **Cryptography** | Noise Handshake Pattern | `Noise_IKpsk2_25519_AESGCM_SHA256` | Formally specified 1-RTT mutual authentication with Pre-Shared Key. |
| | Pinned Library | `github.com/flynn/noise v1.1.0` | Reputable, maintained Go implementation of Noise Protocol Framework. |
| | Cipher Interface API | `cs.Cipher().Encrypt(out, seq, ad, pt)` | Flynn Noise explicit-sequence API; bypasses sequential internal counter. |
| | AEAD Algorithm | AES-256-GCM (NIST SP 800-38D) | Hardware-accelerated (AES-NI) authenticated encryption with 128-bit tag. |
| | AEAD Nonce Construction | 4B Zero (`0x00000000`) + 8B `seq` (Big-Endian) | Standard Noise AESGCM 12-byte nonce format per Noise specification section 5.1. |
| | Retransmission Nonce Rule | New Sequence Number on Every Retransmit | Retransmissions NEVER reuse old sequence numbers; prevents nonce reuse. |
| | Associated Data (AAD) | Full 32-byte DFTP Header | Authenticates all routing fields; any header mutation causes decryption failure. |
| | Rekey Triggers | $2^{32}$ packets or 1 hour | Client-initiated fresh Noise handshake creating new SessionID; 5s old session grace drain. |
| **Anti-Replay** | Anti-Replay Window Size | `2048` packets | 32 $\times$ `uint64` words. Accommodates 102.4 ms reordering budget at 20k pps. |
| | Locking Protocol | RLock $\to$ AEAD $\to$ Lock Commit | RLock pre-check $\to$ unlock $\to$ AEAD decrypt $\to$ Lock re-check $\to$ commit. |
| **Stream Reliability**| Stream Reliability Rule | Zero-Skip Byte Invariant | Missing chunks are never skipped; gaps are actively SACK retransmitted. |
| | Destination Contract | SOCKS5 (RFC 1928) & Fixed `--target` | Standard proxy handshake with remote DNS (`--socks5-hostname`) or direct port forward. |
| | SACK Delay Timer | `15` ms | Bounded delay before emitting `MsgTypeStreamAck` with missing ranges. |
| | Max Retransmit Attempts| `5` retries per chunk | RFC 6298 RTO with backoff; exhaustion aborts flow via `StreamRST`, not corrupted data. |
| | Max Flow Inactivity | `10` seconds | Flow timeout threshold triggering connection teardown. |
| | Max Memory Per Flow | `2` MB | Clamps maximum unacknowledged buffering per flow; exerts backpressure on local socket. |
| | Max Concurrent Flows | `1024` flows per session | Upper bound on simultaneous TCP connections. |
| | Global Proxy Memory Cap| `64` MB | Hard admission-control ceiling across all active flow buffers. |
| **Path Management** | Path Challenge Nonce | `8` bytes (cryptographic random) | Generated via `crypto/rand` for endpoint reachability validation. |
| | Path Validation Timeout| `3` seconds (max 3 retries) | Unvalidated endpoints are purged and never receive return traffic. |
| | Keepalive Interval | `5` seconds | Periodic `MsgTypePing` / `MsgTypePong` to measure path RTT and loss. |
| **Networking Defaults**| Default Relay Address | `127.0.0.1:51820` | Strict loopback binding; binding to `0.0.0.0` requires explicit configuration. |
| | Default Client Proxy | `127.0.0.1:10800` | Local TCP listener port for applications and benchmark harnesses. |
| | Secret Provisioning | Env `DUALFLOW_PSK` or JSON file | Passing `--psk` on command line is prohibited; PSK and hash never logged. |
| | Statistical Benchmark | 5 rounds, Randomized / Paired | Paired diff $CI_{lower}(D) > 0$ via Student's t ($t=2.776$), $\ge 15\%$ gain required. |

---

## 3. Package Structure & Code Organization

```
desktop/network-core/
├── cmd/
│   ├── dualflow-core/            # Client Network Core Daemon (Existing)
│   │   └── main.go
│   └── dualflow-relay/           # Independent Relay Server Binary (Phase 7)
│       └── main.go
├── internal/
│   ├── app/                      # Client Application Orchestrator
│   ├── multipath/                # Multi-Path Engine (Phase 6 Foundation)
│   │   ├── path_validator.go     # Cryptographic Path Challenge/Response (v3.0)
│   │   ├── relay_client.go       # Client-side relay session & proxy manager
│   │   └── scheduler_congestion.go# Path congestion control & rate limiting
│   ├── proxy/                    # User-Space Stream Proxy (v3.0 Primary)
│   │   ├── socks5.go             # SOCKS5 (RFC 1928) handshake parser
│   │   ├── client_listener.go    # Local TCP listener & flow allocator
│   │   ├── flow.go               # Flow state & per-flow SACK tracking
│   │   ├── protocol.go           # Inner stream framing (FlowID, StreamOffset)
│   │   └── reorder_stream.go     # Zero-skip stream reassembly buffer
│   ├── relay/                    # Relay Engine (Phase 7 New)
│   │   ├── config.go             # Server config (default 127.0.0.1:51820)
│   │   ├── server.go             # UDP event loop & packet demux
│   │   ├── session.go            # Multi-path session table & path registry
│   │   ├── noise.go              # Noise_IKpsk2 wrapper via flynn/noise v1.1.0
│   │   ├── crypto.go             # Explicit-sequence Cipher() AEAD management
│   │   ├── replay.go             # 2048-packet RLock-Lock anti-replay window
│   │   ├── forwarder.go          # Outbound TCP connection forwarder (net.DialTCP)
│   │   └── stats.go              # Telemetry & session health reporting
│   ├── transport/                # DFTP Codec (Updated Wire Format v3.0)
│   │   ├── codec.go
│   │   └── types.go
│   └── wintun/                   # Kernel Adapter & Driver Interface (Phase 4/5)
```

---

## 4. Phased Implementation Roadmap

### Phase 7.1: Cryptographic Core & DFTP v3 Wire Format
- Add dependency `github.com/flynn/noise v1.1.0` to `go.mod`.
- Verify handshake state machine against official Noise test vectors for `Noise_IKpsk2_25519_AESGCM_SHA256`.
- Implement `internal/relay/crypto.go`: out-of-order encryption via `cs.Cipher().Encrypt(out, seq, ad, pt)` with explicit sequence allocation.
- Implement `internal/relay/replay.go`: 2048-packet sliding window with `RLock pre-check` $\to$ AEAD decrypt $\to$ `Lock re-check & commit`.
- Standardize `internal/transport/types.go` and `codec.go` with 32-byte header, 1372/1392-byte IP packet budget, and 1352-byte UDP datagram limit.

### Phase 7.2: Reliable Stream Proxy Engine & SOCKS5 Contract
- Implement `internal/proxy` package:
  - Local TCP listener (`127.0.0.1:10800`).
  - SOCKS5 handshake (RFC 1928) with remote DNS resolution (`--socks5-hostname`) and Fixed-Target fallback (`--target`).
  - Inner stream framing: `FlowID` (4B), `StreamOffset` (8B), `StreamFlags` (2B), `PayloadLength` (2B).
  - Stream flags: `FlagStreamSYN (0x0001)`, `FlagStreamFIN (0x0002)`, `FlagStreamRST (0x0004)`, `FlagStreamACK (0x0008)`.
  - Wire format of `MsgTypeStreamAck`: `FlowID` (4B), `CumulativeAckOffset` (8B), `NumSackBlocks` (2B), `Reserved` (2B), plus up to 8 SACK blocks `[Start, End)`.
  - Cumulative ACK semantics, duplicate ACK fast retransmit (3 duplicate ACKs), and sender retransmit buffer release.
  - RFC 6298 RTO estimation, exponential backoff (200ms–3000ms, max 10s), and retry exhaustion (max 5 retries, then RST).
  - TCP half-close handling: `FlagStreamFIN` on write-close; full deallocation only after mutual FIN exchange.
  - Flow ID lifecycle: 32-bit monotonic allocation, 20-second `FlowStateTimeWait` before ID recycling.
  - Zero-skip stream reassembly buffer (`reorder_stream.go`): missing bytes trigger SACK, never skipped.
  - Strict admission control & backpressure: 2 MB per flow, 64 MB global limit, maximum 1024 flows; drops out-of-order chunks on buffer cap to exert TCP backpressure on client socket.

### Phase 7.3: Relay Server & Outbound Forwarder
- Implement `internal/relay` package:
  - Server listener bound strictly to `127.0.0.1:51820` by default.
  - Secure configuration loading from JSON or environment variable (no command-line PSK exposure; secret never logged).
  - Path challenge/response state machine (`MsgTypePathChallenge`, `MsgTypePathResponse`).
  - Outbound TCP forwarder (`net.DialTCP`), return stream chunking, and validated return striping.

### Phase 7.4: Multi-Path Congestion & Client Integration
- Implement `internal/multipath/scheduler_congestion.go`:
  - Path-level in-flight packet tracking to prevent bufferbloat on divergent links.
  - Smoothed RTT (SRTT) and RTT variation (RTTVAR) estimation.
- Hook `internal/multipath/relay_client.go` into `internal/app/app.go`.
- Implement RPC commands: `relay_connect`, `relay_disconnect`, `relay_status`.
- Connect Electron UI cards to display active session ID, physical paths, and per-flow bonding telemetry.

### Phase 7.5: Multi-Stage Empirical Verification
- Execute Stage A (Local Process Integration) on Windows 11 host.
- Execute Stage B (Physical Egress) with interface-specific `pktmon` captures on Interface 13 and Interface 20, correlating visible outer DFTP header fields (`SessionID`, `SessionSequenceNumber`, `PathID`) against the client send log, with NDIS miniport stats as supporting aggregate evidence.
- Execute Stage C (External Endpoint) verifying independent reachability over each physical interface (Ethernet and Android USB) before executing relay challenges.
- Execute Stage D (Single TCP Flow Validation) across 5 counterbalanced/randomized rounds, evaluating paired difference $D_i = T_{bonded, i} - \max(T_{eth, i}, T_{and, i})$ with Student's t 95% CI ($CI_{lower}(D) > 0$) and $\ge 15\%$ mean throughput gain.

---

## 5. Formal Acceptance Criteria (Strictly Separated)

| Acceptance Level | Scope | Explicit Verification Requirement |
|---|---|---|
| **1. Local Process Verified** | Localhost | `dualflow-core` and `dualflow-relay` run as separate processes with distinct PIDs; clean startup and shutdown. |
| **2. Local Protocol Verified** | Localhost | DFTP v3 32-byte header, inner stream framing, SACK framing, and validation order verified against test vectors. |
| **3. Local Cryptography Verified** | Localhost | `flynn/noise v1.1.0` mutual auth, explicit-sequence `cs.Cipher()` AEAD, and RLock-Lock replay commit verified. |
| **4. Local Reassembly Verified** | Localhost | Out-of-order stream chunks correctly reassembled per flow with zero stream skipping; SACK retransmits missing chunks; independent flows isolated. |
| **5. Physical Egress Verified** | Physical HW | Packet capture (`pktmon`) and NDIS stats show independent packet egress through Ethernet and Android USB with zero cross-leakage. |
| **6. Remote Relay Verified** | Multi-Device| Relay running on a separate physical machine observes independent path ingress from both interfaces; path challenge succeeds. |
| **7. Real TCP Flow Aggregation Verified** | Multi-Device| A single logical TCP flow achieves measured steady-state throughput where the 95% Student's t confidence interval of the paired difference strictly exceeds zero ($CI_{lower}(D) > 0$) and mean bonded throughput is $\ge 15\%$ higher than the faster individual baseline across 5 counterbalanced rounds. |

---

## 6. Known Limitations and What Localhost Cannot Prove

> [!WARNING]
> ### Crucial Architectural Limitations of Localhost Testing
> 1. **Windows Loopback Short-Circuit:** When Client and Relay communicate via `127.0.0.1`, Windows processes datagrams entirely in memory. Packets **never hit the physical Ethernet NIC or USB bus**. Localhost proves protocol and proxy logic, but **CANNOT prove physical transmission**.
> 2. **Artificial Network Idealism:** Local loopback exhibits sub-millisecond latencies ($< 0.1\text{ ms}$) with zero packet loss and zero jitter. Real-world WAN multi-path divergence (e.g. 35 ms DSL vs 65 ms LTE) cannot be accurately simulated over loopback.
> 3. **Bandwidth Aggregation Illusion:** Running an aggregation benchmark between two processes on the same CPU is bound by memory bus speed and CPU core scheduling—not network wire capacity. True network bonding can only be measured when traffic traverses independent physical WAN uplinks to an external host.
> 4. **Middlebox & NAT Invisibility:** Localhost tests bypass cellular CGNAT port mapping, mobile tower handover jitter, and router stateful firewall behavior.
