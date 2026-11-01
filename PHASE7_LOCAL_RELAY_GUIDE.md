# DualFlow Phase 7 — Local Relay Guide v3.0
## Developer Operations, Secret Management & Troubleshooting Manual

**Document Version:** 3.0.0  
**Target:** Local Windows 11 Workstation & Zero-Cost Test Devices  
**Binary:** `dualflow-relay.exe`  

---

## 1. Quick Start & Secret Provisioning

### Generating a Cryptographically Secure 32-Byte PSK
Never use weak, hardcoded strings. Generate a random 256-bit PSK using PowerShell:

```powershell
# Generate a cryptographically random 32-byte Base64 key
$bytes = New-Object byte[] 32
[Security.Cryptography.RNGCryptoServiceProvider]::Create().GetBytes($bytes)
$psk = [Convert]::ToBase64String($bytes)
Write-Host "Generated PSK: $psk"
```

### Storing and Passing Secrets Safely
> [!CAUTION]
> **Security Rule:** Do **not** pass secrets via command-line arguments like `--psk <secret>` in production. Command-line strings are visible to all local processes via Windows Task Manager, Process Explorer, and PowerShell `Get-Process`.

#### Option A: Environment Variable (Recommended for Sessions)
```powershell
$env:DUALFLOW_PSK = "YourGeneratedBase64SecretKeyHere"
```

#### Option B: Protected Configuration File
Create `dualflow-relay.json`:
```json
{
  "listen": "127.0.0.1:51820",
  "psk": "YourGeneratedBase64SecretKeyHere",
  "static_key_file": "relay_static.key",
  "log_level": "info",
  "timeout": "30s",
  "max_flows": 1024,
  "max_flow_memory_mb": 2,
  "global_memory_mb": 64
}
```
Restrict Windows file permissions so only your user account can read the file:
```powershell
icacls dualflow-relay.json /inheritance:r /grant:r "$($env:USERNAME):(R,W)"
```

---

## 2. Compiling and Launching the Relay Server

### Compiling the Binary
From the root of the project:

```powershell
# Navigate to network-core directory
cd desktop\network-core

# Build the Relay server binary
C:\go\bin\go.exe build -v -o dualflow-relay.exe ./cmd/dualflow-relay
```

### Launching in Local Development Mode (Default)
Run the server in a dedicated terminal (no Administrator elevation required):

```powershell
# Launch using protected environment variable or config file
.\dualflow-relay.exe --config dualflow-relay.json
```

> [!IMPORTANT]
> **Safety Invariant:** The Relay binds strictly to `127.0.0.1:51820` by default. It **never binds to `0.0.0.0` unless explicitly configured in the config file or commanded via `--listen 0.0.0.0:51820`**.

Expected Startup Console Output:
```text
[INFO] DualFlow Secure Relay Server starting (PID: 14892)
[INFO] Operating Mode: Local Development Mode (Loopback Only)
[INFO] Binding UDP listener strictly on 127.0.0.1:51820...
[INFO] Cryptographic engine initialized: flynn/noise v1.1.0 (Noise_IKpsk2_25519_AESGCM_SHA256)
[INFO] Relay Static Public Key: [ACTUAL PUBLIC KEY GENERATED AT RUNTIME]
[INFO] Relay Key Fingerprint: SHA256: [ACTUAL FINGERPRINT GENERATED AT RUNTIME]
[INFO] Pre-Shared Key (PSK) loaded: [CONFIGURED] (Secret material redacted)
[INFO] Relay ready for local client connections.
```

---

## 3. Shared Architectural & Framing Parameters

The Relay server and Client enforce the following unified network and cryptographic parameters:

| Parameter Key | Unified Value | Operational Description |
|---|---|---|
| **Assumed Path MTU** | `1420` bytes | Conservative cellular path MTU limit. |
| **Complete IPv4 Packet** | `1372` bytes | 20B IPv4 + 8B UDP + 1344B DFTP frame $\le 1420$ B. |
| **Complete IPv6 Packet** | `1392` bytes | 40B IPv6 + 8B UDP + 1344B DFTP frame $\le 1420$ B. |
| **Complete UDP Datagram** | `1352` bytes | 8B UDP header + 1344B DFTP frame (identical for IPv4/IPv6). |
| **DFTP Frame (UDP Payload)** | `1344` bytes | 32B outer header + 16B inner header + 1280B chunk + 16B tag. |
| **Stream Chunk Size** | `1280` bytes | Maximum TCP payload per frame. |
| **Noise Protocol Pattern** | `Noise_IKpsk2_25519_AESGCM_SHA256` | Pinned library `github.com/flynn/noise v1.1.0`. |
| **AES-GCM Nonce Format** | `[4B 0x00] \|\| [8B seq uint64]` | Flynn Noise standard `CipherAESGCM` nonce. |
| **Anti-Replay Window** | `2048` packets (32 words) | RLock pre-check + Lock double-checked commit. |
| **SOCKS5 Proxy Listener** | `127.0.0.1:10800` | RFC 1928 standard proxy for client apps. |
| **Fixed Target Mode** | `--target <ip:port>` | Raw TCP port forwarder on `:10800` for `iperf3`. |
| **Statistical Acceptance** | 5 rounds, Randomized / Paired | Paired diff $CI_{lower}(D) > 0$ via Student's t ($t=2.776$), $\ge 15\%$ gain. |

---

## 4. Connecting Applications: SOCKS5 & Fixed-Target Modes

The DualFlow client exposes a local TCP listener on `127.0.0.1:10800`. It operates in one of two distinct operational modes:

### Mode A: Dynamic SOCKS5 Proxy Mode (Default)
When launched without `--target`, the listener acts as a standard RFC 1928 SOCKS5 proxy server. Applications dynamically request TCP target endpoints:
```powershell
# Test via curl using remote DNS resolution (recommended)
curl --socks5-hostname 127.0.0.1:10800 http://httpbin.org/ip
```
> [!TIP]
> **SOCKS5 DNS Resolution:** Always use `--socks5-hostname` instead of `--socks5`. The `--socks5-hostname` flag delegates DNS resolution to the remote relay, preventing local DNS queries from leaking or failing if the local PC cannot resolve the destination domain.

### Mode B: Fixed-Destination Forwarding Mode (`--target`)
For benchmark applications like `iperf3` that do not natively speak SOCKS5 without third-party wrappers, the client can be started with an explicit target:
```powershell
.\dualflow-core.exe --target 192.168.1.50:5201
```
In this mode, any TCP connection established to `127.0.0.1:10800` is immediately forwarded across the bonded multi-path tunnel to `192.168.1.50:5201` without SOCKS5 handshake negotiation.

#### Running Single-Stream iperf3 Benchmark:
1. Start `iperf3` server on target machine (e.g., secondary laptop on LAN):
   ```bash
   iperf3 -s -p 5201
   ```
2. Launch DualFlow Client pointing to the target:
   ```powershell
   .\dualflow-core.exe --target 192.168.1.50:5201
   ```
3. Run `iperf3` client pointing to local proxy:
   ```powershell
   iperf3 -c 127.0.0.1 -p 10800 -P 1 -t 10
   ```
   *Note:* `iperf3` establishes a Control Connection (`FlowID 1`) and Data Stream Connection (`FlowID 2`). DualFlow transparently assigns distinct flow IDs and proxies both connections concurrently.

---

## 5. Running in Real Network Validation Mode (Zero-Cost Multi-Device)

When testing with a secondary device (e.g. laptop on LAN or phone on USB):

### Option 1: Secondary Laptop on LAN (Recommended)
Launch the relay bound to its local network IP:
```powershell
# Example on secondary laptop with IP 192.168.1.50
.\dualflow-relay.exe --listen 192.168.1.50:51820 --config relay-config.json
```

> [!WARNING]
> **Android Tethering Reachability Constraint:** Do NOT assume that Android USB tethering can reach a secondary PC on the local LAN. Android tethering commonly applies carrier NAT and firewalls or drops LAN subnets (`192.168.1.0/24`). Before executing Stage C, test reachability independently on BOTH interfaces:
> - Test UDP reachability over Ethernet to `192.168.1.50:51820`.
> - Test UDP reachability over Android USB to `192.168.1.50:51820`.
> If Android USB cannot reach the LAN IP, use Option 2 (Android Self-Relay) instead. Never modify Windows routing tables or firewall rules automatically.

Configure `dualflow-core` on the development PC:
```json
{
  "type": "relay_connect",
  "request_id": "req-002",
  "payload": {
    "relay_addr": "192.168.1.50:51820",
    "relay_pubkey": "7a8f...21c4"
  }
}
```

### Option 2: Android Phone (Self-Relay via Termux)
To run the relay directly on the tethered Android phone:
1. **Cross-Compile Static Linux ARM64 Binary:**
   ```powershell
   $env:CGO_ENABLED="0"; $env:GOOS="linux"; $env:GOARCH="arm64"; C:\go\bin\go.exe build -o dualflow-relay-arm64 ./cmd/dualflow-relay
   ```
   *Note on Termux Compatibility:* A pure static Go binary (`CGO_ENABLED=0 GOOS=linux GOARCH=arm64`) executes directly on the 64-bit Android Linux kernel inside the Termux user environment without external dynamic libc dependencies. If compiling with `GOOS=android`, CGO and the Android NDK toolchain are required. Verify execution in Termux by running `./dualflow-relay-arm64 --version`.
2. **Transfer to Phone & Execute via Termux:**
   ```bash
   chmod +x dualflow-relay-arm64
   export DUALFLOW_PSK="YourGeneratedBase64SecretKeyHere"
   ./dualflow-relay-arm64 --listen 0.0.0.0:51820
   ```
3. **Dual Reachability Requirement:** Before proceeding to Stage C, verify via active UDP probes that the Relay responds through BOTH:
   - The USB tethering address: `192.168.217.128:51820`
   - The phone's Wi-Fi LAN address: `192.168.1.45:51820`

---

## 6. Troubleshooting & Diagnostics

### Issue 1: "bind: address already in use"
- **Cause:** Another process holds UDP port 51820.
- **Resolution:**
  ```powershell
  # 1. Identify port owner PID
  Get-NetUDPEndpoint -LocalPort 51820 | Select-Object LocalAddress, LocalPort, OwningProcess

  # 2. Issue graceful termination to the specific process
  Stop-Process -Id <PID>
  ```
  *(Do not use `Stop-Process -Force` as first resort; verify the process name before terminating).* Or change the port in config: `"listen": "127.0.0.1:51822"`.

### Issue 2: "Noise Handshake Failed: Invalid PSK or Message Tampered"
- **Cause:** PSK string mismatch or outdated static key.
- **Resolution:** Ensure the client configuration and relay config pass the exact same PSK string and that the client pins the relay's current public key.

### Issue 3: "Packets Dropped by Anti-Replay Pre-Check"
- **Cause:** Packets arrived with sequence numbers older than $Seq_{max} - 2048$, or duplicate packets were delivered across paths.
- **Resolution:** Expected behavior if redundant scheduler is active or path jitter exceeds 2048 packets ($\approx 102.4\text{ ms}$ at 20k pps). Check `reorder_overhead` telemetry in logs.

---

## 7. Known Limitations and What Localhost Cannot Prove

> [!CAUTION]
> ### Crucial Operational Notice: Localhost vs Real Network
> 1. **Localhost Proves:** Software architecture, clean process separation, Noise_IKpsk2 handshake completion, AES-256-GCM authenticated encryption, two-phase double-checked anti-replay filtering, and per-flow stream reassembly logic.
> 2. **Localhost CANNOT Prove:** Physical network cable traversal, USB bus throughput, cellular network latency spikes, cellular CGNAT behavior, or real-world Internet bandwidth aggregation.
> 3. **Verification Standard:** Any claim of "Bandwidth Aggregation" or "Multi-Path Physical Egress" requires running in Real Network Validation Mode with evidence from kernel NDIS adapter statistics and interface packet capture.
