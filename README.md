# DualFlow

High-Performance Multipath Connection Manager, Dynamic Link Aggregation & Secure Traffic Routing Engine for Windows 10/11 x64.

---

## 1. Project Overview

DualFlow is an experimental, high-performance networking system designed to bond and optimize multiple physical Internet connections (e.g., Ethernet + 4G/5G Android USB Tethering) on Windows workstations. It provides both an unprivileged User-Space TCP Stream Proxy (Phase 7) and an L3 Virtual Network Adapter engine based on WireGuard's Wintun driver (Phase 4–6).

### Key Architectural Components
- **Client Network Core Daemon (`dualflow-core`):** High-throughput Go daemon managing Windows adapter discovery via the IP Helper API (`iphlpapi.dll`), interface socket pinning via Winsock `IP_UNICAST_IF`, RTT tracking, and user-space SOCKS5 proxying.
- **DualFlow Secure Relay (`dualflow-relay`):** Low-overhead UDP relay terminating the DFTP v3 protocol, providing Noise_IKpsk2 cryptographic handshakes, ChaCha20-Poly1305 AEAD payload encryption, sliding-window stream reassembly, and target TCP connection forwarding.
- **DFTP v3 Multipath Transport Protocol:** Custom UDP-based transport featuring a 32-byte outer header, cryptographic session multiplexing, explicit-sequence AEAD encryption, zero-skip cumulative ACK + SACK range tracking, and fast retransmission (3 duplicate ACKs).
- **Desktop Instrument Panel:** Native Windows desktop application built with React 19, TypeScript, Vite, Tailwind CSS v4, and Electron.

---

## 2. Current Implementation Status

**Status:** `PHASE 7 COMPLETE — PHYSICAL VALIDATION PENDING`

| Subsystem | Milestone | Verification Level | Status |
|---|---|:---:|:---:|
| **Desktop UI & Shell** | Phase 1 | UI / Visual Testing | **Complete** |
| **Windows Network Discovery** | Phase 2 | Integration Test (`iphlpapi.dll`) | **Complete** |
| **Go Core Daemon & IPC** | Phase 3 | IPC Named Pipe Integration | **Complete** |
| **Wintun L3 Foundation** | Phase 4 | Driver Loading & Ring Buffer | **Complete** |
| **DFTP Framing & Transport** | Phase 5 | Unit & Protocol Tests | **Complete** |
| **Bonding Scheduler Engine** | Phase 6 | RTT / Loss Metric Tracking | **Complete** |
| **User-Space TCP Stream Proxy** | Phase 7 | End-to-End Loopback Tests | **Complete** |
| **Physical Multi-Path WAN Aggregation** | Phase 7 | Multi-Device Hardware Harness | **Pending External Hardware** |

> **Important Transparency Notice:** All protocol framing, Noise encryption, sliding window reassembly, SACK feedback, session rekey with in-flight drain (25s), and dynamic path failover are **100% verified** in code and local integration tests. However, physical multi-path WAN bandwidth aggregation across two independent physical gateways is **strictly marked as pending validation against a secondary physical host**. Loopback execution does not and cannot claim physical link aggregation.

---

## 3. Repository Structure

```
Dualflow/
├── .gitignore                          # Git exclusion specification
├── README.md                           # This technical documentation
├── package.json                        # Root workspace configuration
├── PHASE6_*.md                         # Phase 6 specifications & hardware test reports
├── PHASE7_*.md                         # Phase 7 specifications, security design & completion report
├── wintun/                             # WireGuard Wintun C include headers & license
│   └── include/wintun.h
└── desktop/
    ├── package.json                    # Electron & UI dependencies
    ├── vite.config.ts                  # Vite build configuration
    ├── bin/                            # Runtime driver dependencies (wintun.dll)
    ├── wintun-dist/                    # Upstream Wintun redistributable binaries (amd64, arm, x86)
    ├── src/                            # React 19 frontend UI
    ├── electron/                       # Electron main process & IPC bridges
    └── network-core/                   # Go Networking Subsystem (dualflow-core)
        ├── go.mod                      # Go module definition (Go 1.22+)
        ├── go.sum                      # Go dependency checksums
        ├── wintun.dll                  # Signed Windows Wintun driver DLL (amd64)
        ├── .env.example                # Template for configuration environment variables
        ├── cmd/
        │   ├── dualflow-core/          # Client daemon entry point
        │   └── dualflow-relay/         # Secure Relay server entry point
        ├── internal/
        │   ├── app/                    # Daemon state machine & IPC JSON-RPC pipe
        │   ├── diagnostics/            # Socket diagnostics & MTU probing
        │   ├── discovery/              # Windows network adapter discovery
        │   ├── multipath/              # Dynamic path manager & Winsock IP_UNICAST_IF
        │   ├── packet/                 # Raw IPv4/IPv6 packet parsing & checksums
        │   ├── paths/                  # Interface state models & metrics
        │   ├── protocol/               # Framing primitives & packet types
        │   ├── proxy/                  # SOCKS5 TCP Stream Proxy
        │   ├── relay/                  # Noise_IKpsk2 relay, session table & rekey
        │   ├── telemetry/              # Interface throughput sampling
        │   ├── transport/              # DFTP v3 reliable stream engine
        │   └── wintun/                 # Wintun driver wrapper
        └── tests/                      # Integration test suite & measurement harnesses
```

---

## 4. Prerequisites

- **Operating System:** Windows 10 or Windows 11 (64-bit AMD64).
- **Go Toolchain:** Go 1.22 or higher (`go version`).
- **Node.js & npm:** Node.js 18+ and npm 9+ (required for the Electron desktop frontend).
- **Network Interfaces:** At least one active physical network adapter for local testing; two independent physical gateways (e.g. Ethernet + USB Tethering) for multi-path physical validation.
- **Privileges:**
  - Running the **SOCKS5 Stream Proxy & Secure Relay** requires standard, unprivileged user permissions.
  - Creating **Wintun Virtual L3 Adapters** requires Windows Administrator privileges.

---

## 5. Build Instructions

### 5.1 Building the Go Network Subsystem

Open PowerShell and navigate to the Go module directory:

```powershell
cd desktop\network-core

# Build the Client Network Core Daemon
go build -o dualflow-core.exe ./cmd/dualflow-core

# Build the Secure Relay Server
go build -o dualflow-relay.exe ./cmd/dualflow-relay
```

### 5.2 Building the Desktop Electron Application

```powershell
cd desktop

# Install npm dependencies
npm install

# Build Go core, TypeScript, Vite frontend, and Electron packages
npm run build
```

---

## 6. Running Tests & Static Analysis

All tests must be executed from `desktop/network-core`:

```powershell
cd desktop\network-core
```

### 6.1 Static Analysis (`go vet`)

```powershell
go vet ./...
```
*Expected Result:* Exit code `0` with zero warnings or errors.

### 6.2 Running All Unit & Local Integration Tests

```powershell
go test -v ./...
```

### 6.3 Test Suite Classification

| Test Name | Type | Description | Result |
|---|:---:|---|:---:|
| `TestProtocolValidation` | Unit | Verifies DFTP v3 32-byte header encoding, magic bytes, and checksums. | **PASS** |
| `TestDiscoveryEngine` | Integration | Enumerates Windows network adapters via IP Helper API & Registry. | **PASS** |
| `TestThroughputCalculation` | Unit | Validates delta byte calculation and throughput rate estimation. | **PASS** |
| `TestPathManager` | Integration | Verifies path tracking, state transitions, and socket registration. | **PASS** |
| `TestSourceBoundProbe` | Integration | Tests source-IP bound UDP socket probing across active interfaces. | **PASS** |
| `TestWintunProtocolIntegration` | Integration | Verifies `wintun.dll` driver presence and L3 packet encapsulation. | **PASS** |
| `TestPacketEngineAndTransportProtocol` | Integration | Verifies end-to-end packet processing and DFTP framing. | **PASS** |
| `TestPhase7EndToEndStreamProxying` | Integration | Complete proxy loopback: SOCKS5 client -> Relay -> Echo server (SHA-256 byte match). | **PASS** |
| `TestPhase7OutOfOrderReassemblyAndSACK` | Unit | Verifies zero-skip reassembly of out-of-order chunks and SACK range generation. | **PASS** |
| `TestPhase7FastRetransmitOnThreeDupAcks` | Unit | Verifies that receiving 3 duplicate ACKs triggers immediate retransmission without waiting for RTO. | **PASS** |
| `TestPhase7SessionRekeyWithInFlightDrain` | Integration | Verifies Noise rekey, 25s drain window, endpoint inheritance, and active flow migration with zero data loss. | **PASS** |
| `TestPhase7PathTrackingAndFailover` | Integration | Simulates Path 1 failure, automatic reroute to Path 2, and Path 1 recovery with zero stream corruption. | **PASS** |
| `TestPhase7ConcurrentTCPStreamsAndLargeTransfer` | Integration | Streams 4096-byte blocks across 3 concurrent TCP flows through the proxy simultaneously with zero cross-talk. | **PASS** |
| `TestPhase7StatisticalFormulaVerificationOnly_Synthetic` | Mathematical | Validates Student's t distribution, confidence interval, and paired-difference math algorithms using synthetic test constants. *(Not a network speed benchmark).* | **PASS** |
| `TestPhase7PhysicalThroughputMeasurementHarness` | Hardware | Live physical network throughput benchmark against a remote relay. Requires external host. | **SKIP** *(Awaiting Second Device)* |

> **Note on `go test -race` on Windows:** The Go race detector on Windows requires CGO and a MinGW-w64 GCC compiler installed on `%PATH%`. If running without GCC, Go will output `go: -race requires cgo; enable cgo by setting CGO_ENABLED=1`.

---

## 7. Configuration & Environment Variables

A template file is available at [`desktop/network-core/.env.example`](desktop/network-core/.env.example).

| Variable | Required | Description |
|---|:---:|---|
| `DUALFLOW_RELAY_PSK` | Optional | 32-byte hex-encoded Pre-Shared Key for Noise_IKpsk2 handshake. If omitted, an ephemeral key is generated. |
| `DUALFLOW_RELAY_STATIC_KEY` | Optional | 32-byte hex-encoded private static key for the Relay. If omitted, an ephemeral keypair is generated. |
| `DUALFLOW_RELAY_LISTEN` | Optional | UDP listen address for the Relay (Default: `0.0.0.0:51820`). |
| `DUALFLOW_TEST_REMOTE_RELAY` | Optional | `IP:Port` of an external DualFlow Relay (e.g. `192.168.1.50:51820`). Required only to run `TestPhase7PhysicalThroughputMeasurementHarness`. |

---

## 8. Known Limitations & Scope Boundaries

1. **SOCKS5 Scope:** The User-Space Stream Proxy in Phase 7 strictly implements `CMD 0x01` (**TCP CONNECT**), supporting IPv4 (`0x01`), Domain Names (`0x03`), and IPv6 (`0x04`). It does **NOT** support `CMD 0x02` (TCP BIND) or `CMD 0x03` (UDP ASSOCIATE). Whole-system UDP and native gaming traffic requiring raw UDP encapsulation must route through the Wintun L3 packet engine (Phase 4 / Phase 8), not the SOCKS5 proxy.
2. **Physical Aggregation Validation:** Multi-path physical egress and true bandwidth aggregation can only be validated when the Client and Relay run on physically separate machines connected via distinct networks. Loopback or single-host tests cannot validate physical WAN aggregation.
3. **Host Route Integrity:** DualFlow strictly avoids modifying the Windows host default route (`0.0.0.0/0`), DNS configuration, Windows Firewall, NAT, or Internet Connection Sharing (ICS). SOCKS5 applications configure their proxy endpoint explicitly (`127.0.0.1:1080`).

---

## 9. License

- DualFlow Core: Proprietary / Work-in-Progress.
- Wintun Driver: Copyright (C) 2018-2021 WireGuard LLC. Licensed under the MIT License (see [`wintun/LICENSE.txt`](wintun/LICENSE.txt) and [`desktop/network-core/WINTUN_LICENSE.txt`](desktop/network-core/WINTUN_LICENSE.txt)).
