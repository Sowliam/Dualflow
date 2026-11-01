# DualFlow Phase 7 — Security Design v3.0
## Cryptographic Specification, Out-of-Order AEAD & Key Management

**Document Version:** 3.0.0  
**Standard Compliance:** Noise Protocol Framework (Noise_IKpsk2), RFC 7748 (Curve25519), RFC 5869 (HKDF), NIST SP 800-38D (AES-256-GCM), RFC 6479 (Anti-Replay Window)  
**Implementation Library:** `github.com/flynn/noise v1.1.0` (Pinned Go Module)

---

## 1. Protocol Selection, Handshake Envelope & Exact Noise v1.1.0 API Usage

DualFlow Phase 7 strictly implements the standardized **`Noise_IKpsk2_25519_AESGCM_SHA256`** handshake pattern using the official public API of `github.com/flynn/noise v1.1.0`.

### Handshake Outer Envelope & Wire Framing
All handshake messages are encapsulated within the standard 32-byte outer DFTP v3 header:
- **`MsgTypeHandshakeInit (0x10)` (Client -> Relay):**
  - `SessionID = 0x00000000` (Session not yet assigned).
  - `SessionSequenceNumber = 0`.
  - `PayloadLength = 120` bytes:
    - Ephemeral public key $e_c$: 32 bytes (unencrypted).
    - Encrypted static public key $s_c$: 48 bytes (32B key + 16B AEAD tag).
    - Encrypted handshake payload: 40 bytes (8B Unix nanosecond timestamp + 16B client nonce + 16B AEAD tag).
  - Handshake Prologue: `[]byte("DualFlow-v3.0")`.
- **`MsgTypeHandshakeResp (0x11)` (Relay -> Client):**
  - `SessionID = assignedSessionID` (32-bit random session identifier chosen by Relay).
  - `SessionSequenceNumber = 0`.
  - `PayloadLength = 52` bytes:
    - Ephemeral public key $e_r$: 32 bytes (unencrypted).
    - Encrypted handshake payload: 20 bytes (4B assigned `SessionID` confirmation + 16B AEAD tag).

### Handshake Replay Protection, Recovery & Demultiplexing
1. **Timestamp Replay Window:** The Relay decrypts the handshake payload and verifies that the 8-byte Unix nanosecond timestamp is within $\pm 10$ seconds of the Relay's clock. Outdated or future timestamps are silently dropped.
2. **Duplicate Handshake Mitigation:** If a duplicate `MsgTypeHandshakeInit` arrives within the 10-second window containing an identical client ephemeral public key ($e_c$), the Relay retransmits the cached `MsgTypeHandshakeResp` without recomputing Diffie-Hellman operations, preventing CPU exhaustion attacks.
3. **Lost Handshake Recovery:** The Client starts a 1.0-second retransmission timer upon transmitting `MsgTypeHandshakeInit`. If no response arrives, the Client retransmits the packet (up to 3 total attempts). If unacknowledged after 3 attempts, the handshake is aborted and a connection error is reported.
4. **Pre-Completion Demultiplexing:**
   - Unauthenticated `MsgTypeHandshakeInit` datagrams (`SessionID == 0`) are routed to the global Handshake Listener.
   - Incoming `MsgTypeHandshakeResp` datagrams are matched by the Client to the pending handshake state associated with the Relay's remote endpoint.
   - The session enters `SessionActive` in the active session map only after `MsgTypeHandshakeResp` has been authenticated and decrypted.

### Exact API Configuration & Handshake Implementation
```go
import (
    "crypto/rand"
    "github.com/flynn/noise"
)

// 1. Initialize Handshake Configuration
suite := noise.NewCipherSuite(noise.DH25519, noise.CipherAESGCM, noise.HashSHA256)

// Client (Initiator) Configuration
clientConfig := noise.Config{
    CipherSuite:           suite,
    Random:                rand.Reader,
    Pattern:               noise.HandshakeIK,
    Initiator:             true,
    Prologue:              []byte("DualFlow-v3.0"),
    StaticKeypair:         clientStaticDHKeypair,
    PeerStatic:            relayStaticPubKey,
    PresharedKey:          psk32Bytes,
    PresharedKeyPlacement: 2, // psk2 pattern
}
clientHS, _ := noise.NewHandshakeState(clientConfig)

// 2. Handshake Message 1 (Client -> Relay)
// e_c.pub (32B) || encrypted s_c.pub (48B) || encrypted payload (Timestamp: 8B || Nonce_c: 16B || Tag: 16B)
msg1, _, _, err := clientHS.WriteMessage(nil, clientPayload)

// Relay (Responder) Processes Message 1
relayConfig := noise.Config{
    CipherSuite:           suite,
    Random:                rand.Reader,
    Pattern:               noise.HandshakeIK,
    Initiator:             false,
    Prologue:              []byte("DualFlow-v3.0"),
    StaticKeypair:         relayStaticDHKeypair,
    PresharedKey:          psk32Bytes,
    PresharedKeyPlacement: 2,
}
relayHS, _ := noise.NewHandshakeState(relayConfig)
payload1, _, _, err := relayHS.ReadMessage(nil, msg1)

// 3. Handshake Message 2 (Relay -> Client)
// e_r.pub (32B) || encrypted payload (SessionID: 4B || Tag: 16B)
// WriteMessage returns csSend (Relay->Client) and csRecv (Client->Relay) upon handshake completion
msg2, csRelaySend, csRelayRecv, err := relayHS.WriteMessage(nil, sessionIDPayload)

// Client Processes Message 2 and Completes Handshake
// ReadMessage returns csRecv (Relay->Client) and csSend (Client->Relay) upon completion
payload2, csClientRecv, csClientSend, err := clientHS.ReadMessage(nil, msg2)
```

### Directional CipherState Mapping & Verification
Noise `Split()` outputs two distinct cipher states:
- **Client (Initiator):** `clientSendCipher` derives from `csClientSend` ($K_{C \to R}$); `clientRecvCipher` derives from `csClientRecv` ($K_{R \to C}$).
- **Relay (Responder):** `relayRecvCipher` derives from `csRelayRecv` ($K_{C \to R}$); `relaySendCipher` derives from `csRelaySend` ($K_{R \to C}$).
- **Automated Verification:** Unit tests (`TestDirectionalCipherSeparation`) explicitly assert that ciphertext generated by `clientSendCipher` can only be decrypted by `relayRecvCipher`, and that attempting to decrypt with `relaySendCipher` or `clientRecvCipher` fails with an authentication error.

---

## 2. Multi-Path Out-of-Order Transport AEAD via `Cipher()`

### The Out-of-Order Decryption Challenge & Solution
Standard `CipherState.Encrypt()` and `CipherState.Decrypt()` maintain an internal monotonic counter $n$. In a multi-path architecture where packets arrive out-of-order across independent physical paths, sequential decryption fails.

DualFlow solves this using the official **`CipherState.Cipher()`** interface of `flynn/noise v1.1.0`:
```go
// noise.Cipher interface exported by flynn/noise
type Cipher interface {
    Encrypt(out []byte, seq uint64, ad, plaintext []byte) []byte
    Decrypt(out []byte, seq uint64, ad, ciphertext []byte) ([]byte, error)
}
```

### Transport Encryption Pseudocode:
```go
// Extract the underlying Cipher from CipherState
sendCipher := csClientSend.Cipher()
recvCipher := csClientRecv.Cipher()

// Encrypting an Outbound Datagram (Client -> Relay)
// Explicit SessionSequenceNumber is allocated atomically
seq := atomic.AddUint64(&session.txSeq, 1)
aad := dftpOuterHeader[0:32] // Entire 32-byte DFTP header as AAD

// Encrypt with explicit sequence number
ciphertextWithTag := sendCipher.Encrypt(nil, seq, aad, plaintextPayload)

// Decrypting an Inbound Datagram (Independent of Arrival Order)
// seq is read directly from the received outer DFTP header bytes [16:24]
plaintext, err := recvCipher.Decrypt(nil, seqFromHeader, aadFromHeader, ciphertextWithTag)
```

> [!IMPORTANT]
> **CipherState Invariant:** Once `cs.Cipher()` is extracted for transport framing, `cs.Encrypt()` and `cs.Decrypt()` **must never be called subsequently**. This guarantees that the sequential internal counter is bypassed, and all nonces are explicitly managed via atomic 64-bit sequence numbers.

---

## 3. Exact Nonce Construction & Mathematical Proof of Nonce Uniqueness

To strictly satisfy NIST SP 800-38D requirements and guarantee that no AES-GCM nonce can ever repeat under the same encryption key:

### 1. Exact 12-Byte Nonce Construction
DualFlow relies on the official `cipherAESGCM` implementation in `github.com/flynn/noise v1.1.0`. The 12-byte (96-bit) nonce passed to `crypto/cipher.AEAD` is constructed deterministically from the 64-bit sequence number:
- **Bytes 0–3:** 4 zero bytes (`0x00, 0x00, 0x00, 0x00`)
- **Bytes 4–11:** 8 bytes containing the 64-bit unsigned sequence number in big-endian byte order (`binary.BigEndian.PutUint64(nonce[4:], seq)`)

Verified directly against upstream `github.com/flynn/noise/cipher_aesgcm.go`:
```go
func (c *cipherAESGCM) Encrypt(out []byte, seq uint64, ad, plaintext []byte) []byte {
    var nonce [12]byte
    binary.BigEndian.PutUint64(nonce[4:], seq)
    return c.aead.Seal(out, nonce[:], plaintext, ad)
}

func (c *cipherAESGCM) Decrypt(out []byte, seq uint64, ad, ciphertext []byte) ([]byte, error) {
    var nonce [12]byte
    binary.BigEndian.PutUint64(nonce[4:], seq)
    return c.aead.Open(out, nonce[:], ciphertext, ad)
}
```

### 2. Directional Key Separation
Noise `Split()` outputs two independent cipher states. The client-to-relay cipher and relay-to-client cipher possess distinct 256-bit AES keys ($K_{C \to R} \ne K_{R \to C}$).
Even when both client and relay emit datagrams with the identical sequence number (e.g., $Seq = 1$), they are encrypted under completely different 256-bit keys. Therefore, cross-directional nonce collisions are cryptographically impossible.

### 3. Monotonic Sequence Allocation
Within each directional session flow, `SessionSequenceNumber` is allocated strictly via atomic 64-bit increments:
$$Seq_{next} = \text{atomic.AddUint64}(\&txSeq, 1)$$
Every transmitted packet receives a strictly increasing sequence number ($1, 2, 3, \dots$). No two packets transmitted under the same key can ever share a sequence number.

### 4. Retransmission Nonce Invariant
**Retransmission NEVER resends the original encrypted datagram or reuses its sequence number.**
When SACK indicates that a stream chunk must be retransmitted:
- The inner stream chunk (`FlowID`, `StreamOffset`, and data) is retained.
- The chunk is encapsulated into a **NEW outer DFTP v3 datagram**.
- A **NEW, freshly incremented `SessionSequenceNumber`** is allocated atomically.
- The datagram is encrypted with the fresh sequence number as its nonce.
- **Proof:** Because every retransmission consumes a new sequence number, nonce reuse during packet loss recovery is mathematically impossible.

### 5. Session Rekeying via Standard Noise Handshake (Option A)
DualFlow does NOT invent an ad-hoc in-band rekeying protocol or introduce an undefined `EpochID` field into the 32-byte DFTP outer header. Instead, rekeying is executed via a **Fresh Standard Noise Handshake**:
- **Trigger:** Either after transmitting $2^{32}$ packets (to stay well below the AES-GCM $2^{64}$ sequence ceiling) or after 1 hour of continuous session operation.
- **Client-Initiated Rekey ONLY:** The Client strictly initiates all rekey handshakes. The Relay is purely passive and never initiates a rekey independently. This eliminates simultaneous rekey race conditions or dual-initiator collisions entirely.
- **Handshake Initiation:** Client initiates a fresh Noise handshake (`MsgTypeHandshakeInit`) over the existing transport, establishing a brand-new session with a new `SessionID` ($SessionID_{new}$) and fresh directional cipher keys ($K'_{C \to R}, K'_{R \to C}$).
- **Traffic Continuity During Handshake:** Outbound data traffic continues flowing uninterrupted across the active session ($SessionID_{old}$) while the new handshake is pending.
- **Outbound Cutover:** Once the Client processes `MsgTypeHandshakeResp`, outbound traffic immediately switches to $SessionID_{new}$ with sequence numbers starting at 1 under the new key.
- **Grace Drain Period:** The previous session ($SessionID_{old}$) enters a `SessionDraining` state for exactly 5 seconds:
  - **Inbound:** Packets addressed to $SessionID_{old}$ are decrypted and verified using the old cipher state, allowing in-flight multi-path packets and SACKs to drain completely.
  - **Outbound:** New data transmission on $SessionID_{old}$ is strictly forbidden; outbound is permitted solely for in-flight SACKs tied directly to $SessionID_{old}$.
- **Best-Effort Memory Hygiene:** Upon expiration of the 5-second timer, the old session's cipher states and key material are overwritten with zero bytes in memory (`for i := range key { key[i] = 0 }`) and purged from the session table. DualFlow provides best-effort user-space memory hygiene within Go runtime constraints; it does not claim kernel-level physical RAM zeroization guarantees due to non-pinned garbage-collected memory allocations.
- **SessionID 32-Bit Collision Analysis:** With 32-bit random SessionIDs ($2^{32} \approx 4.29 \times 10^9$) and `MaxConcurrentSessions = 256`, the Birthday Paradox collision probability is:
  $$P \approx \frac{N^2}{2 \times 2^{32}} = \frac{256^2}{8.58 \times 10^9} \approx 7.6 \times 10^{-6} \ (0.00076\%)$$
  The Relay checks active sessions in an $O(1)$ hash map upon handshake; if an unlikely collision occurs, a new random identifier is drawn before replying. No header redesign is required.

### 6. Deterministic Nonce Test Specification
A unit test verifies nonce uniqueness per (Key, Nonce) pair across $1,000,000$ operations:
```go
func TestDeterministicNonceUniqueness(t *testing.T) {
    // Assert that no (KeyInstance, Nonce) pair is ever observed more than once
    // across 1,000,000 simulated packet transmissions, retransmissions, and session rekeys.
    seenNonces := make(map[string]struct{}) // key: hex(key) + ":" + hex(nonce)
    
    // 1. Simulate initial session (Key1)
    // 2. Perform 500,000 sequential transmissions and 50,000 retransmissions
    // 3. Trigger session rekey (Key2)
    // 4. Perform another 450,000 transmissions
    // 5. Verify len(seenNonces) == 1,000,000 with zero collisions detected
}
```

### 7. Secret Handling & Log Redaction Invariant
The Pre-Shared Key (PSK) and its SHA-256 hash must **never** be output to console logs, log files, or telemetry streams. Console output strictly logs:
```text
[INFO] Pre-Shared Key (PSK) loaded: [CONFIGURED] (Secret material redacted)
```

---

## 4. Concurrency-Safe Anti-Replay Window (2048 Packets)

To eliminate Go data races across concurrent worker routines while tolerating multi-path RTT divergence:

### Replay Window Budgeting:
- **Window Size:** **2048 packets** ($32 \times \text{uint64}$ words = 256 bytes memory).
- **Reordering Budget:**
  $$\text{At } 10,000\text{ pps } (100\text{ Mbps}): \Delta t = \frac{2048}{10,000} = 204.8\text{ ms}$$
  $$\text{At } 20,000\text{ pps } (200\text{ Mbps}): \Delta t = \frac{2048}{20,000} = 102.4\text{ ms}$$
- Packets arriving with delay exceeding the reordering budget ($Seq \le Seq_{max} - 2048$) are rejected by design as stale.

### RLock-Lock Double-Checked Implementation Pseudocode:
```go
type AntiReplayWindow struct {
    mu      sync.RWMutex
    seqMax  uint64
    bitmap  [32]uint64 // 2048 bits
}

func (w *AntiReplayWindow) VerifyAndCommit(seq uint64, decryptFunc func() error) error {
    // 1. RLock Pre-Check (Safe concurrent read)
    w.mu.RLock()
    if seq <= w.seqMax && (w.seqMax - seq >= 2048 || w.isBitSetLocked(w.seqMax - seq)) {
        w.mu.RUnlock()
        return ErrReplayDetected
    }
    w.mu.RUnlock()

    // 2. AEAD Decryption & Tag Verification (Executed outside locks)
    if err := decryptFunc(); err != nil {
        return err // State completely untouched on auth failure
    }

    // 3. Exclusive Lock Re-Check & Commit
    w.mu.Lock()
    defer w.mu.Unlock()

    // Double-check under write lock (detect concurrent workers that committed this seq)
    if seq <= w.seqMax && (w.seqMax - seq >= 2048 || w.isBitSetLocked(w.seqMax - seq)) {
        return ErrConcurrentDuplicate
    }

    // Commit valid sequence into bitmap
    w.commitBitLocked(seq)
    return nil
}
```

---

## 5. Path Validation State Machine (Cryptographic Challenge/Response)

To prevent spoofed UDP packets or malicious routing updates from hijacking return traffic:
1. When an authenticated packet arrives from an unseen `RemoteAddr` for a given `PathID`, the relay places that endpoint in `PathStatePending`.
2. The relay generates an 8-byte random cryptographic challenge nonce via `crypto/rand` and transmits `MsgTypePathChallenge` to that `RemoteAddr`.
3. The client echoes the nonce via `MsgTypePathResponse`.
4. Only upon receiving the valid response does the endpoint transition to `PathStateValidated`.
5. **Return traffic is routed exclusively across validated endpoints.**

---

## 6. Resource Limits & Denial-of-Service Protection Budget

The resource limits are unified and mathematically consistent:

| Resource Dimension | Budget Allocation | Enforcement Mechanism |
|---|---|---|
| **Max Concurrent Sessions** | `256` sessions | New handshakes rejected when limit reached. |
| **Max Flows Per Session** | `1,024` flows | New connections receive TCP RST if session flow cap reached. |
| **Max Buffer Per Flow** | `2` MB | Inbound data beyond 2 MB ahead of contiguous ACK is dropped. |
| **Global Proxy Memory Cap** | `64` MB | Strict admission control: new flows rejected if global memory $\ge 64$ MB until memory drops $< 56$ MB. |
| **Handshake Rate Limit** | `20` handshakes/sec | Token bucket limiter drops unauthenticated handshake packets. |
| **Max Datagram Rate** | `100,000` pps/session | Token bucket drops datagrams exceeding session throughput ceiling. |
| **Idle Flow Timeout** | `10` seconds | Flow terminated with `FlagStreamRST` and deallocated. |
| **Idle Session Timeout** | `30` seconds | Session purged after 30 seconds of complete inactivity. |

---

## 7. Known Limitations and What Localhost Cannot Prove

> [!CAUTION]
> ### Crucial Security Notice: Localhost vs Real Network
> 1. **No Real Wire Adversaries:** On localhost, packets never traverse untrusted networks, Wi-Fi radios, or ISP switches. Local tests prove that AES-GCM and Noise state machines function without software crashes, but **cannot simulate active network adversaries** (e.g. BGP hijacking, cellular MITM, or packet tampering by middleboxes).
> 2. **NAT & Middlebox Permeability:** Cellular carriers (CGNAT) and home routers frequently rewrite UDP headers, alter ephemeral source ports, or terminate inactive UDP flows after 30 seconds. Localhost has zero middleboxes and cannot validate real-world NAT persistence.
