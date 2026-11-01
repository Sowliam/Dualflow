package relay

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"dualflow-core/internal/transport"
	"github.com/flynn/noise"
	"golang.org/x/crypto/curve25519"
)

var (
	ErrHandshakeTimestampExpired = errors.New("handshake timestamp outside allowed +/- 10s window")
	ErrInvalidHandshakeType      = errors.New("invalid handshake message type")
	ErrHandshakeIncomplete       = errors.New("handshake state machine incomplete")
	ErrPSKLengthMismatch         = errors.New("pre-shared key must be exactly 32 bytes")
)

var HandshakePrologue = []byte("DualFlow-v3.0")

const (
	// HandshakeTimestampTolerance is the replay tolerance window for handshake timestamps
	HandshakeTimestampTolerance = 10 * time.Second
)

// CipherSuite returns the standardized DualFlow Noise cipher suite
func CipherSuite() noise.CipherSuite {
	return noise.NewCipherSuite(noise.DH25519, noise.CipherAESGCM, noise.HashSHA256)
}

// GenerateStaticKeypair generates a fresh Curve25519 DH keypair for static identity
func GenerateStaticKeypair() (noise.DHKey, error) {
	return CipherSuite().GenerateKeypair(rand.Reader)
}

// DerivePublicKey computes the Curve25519 public key corresponding to a private key
func DerivePublicKey(privateKey [32]byte) ([]byte, error) {
	return curve25519.X25519(privateKey[:], curve25519.Basepoint)
}

// GenerateRandomPSK generates a random 32-byte pre-shared key
func GenerateRandomPSK() ([32]byte, error) {
	var psk [32]byte
	if _, err := io.ReadFull(rand.Reader, psk[:]); err != nil {
		return psk, err
	}
	return psk, nil
}

// ClientHandshake orchestrates Noise_IKpsk2 initiation from the client
type ClientHandshake struct {
	hs     *noise.HandshakeState
	static noise.DHKey
	peer   []byte
	psk    []byte
}

// NewClientHandshake initializes a client Noise_IKpsk2 handshake state
func NewClientHandshake(clientStatic noise.DHKey, relayStaticPub []byte, psk []byte) (*ClientHandshake, error) {
	if len(psk) != 32 {
		return nil, ErrPSKLengthMismatch
	}

	config := noise.Config{
		CipherSuite:           CipherSuite(),
		Random:                rand.Reader,
		Pattern:               noise.HandshakeIK,
		Initiator:             true,
		Prologue:              HandshakePrologue,
		StaticKeypair:         clientStatic,
		PeerStatic:            relayStaticPub,
		PresharedKey:          psk,
		PresharedKeyPlacement: 2, // IKpsk2
	}

	hs, err := noise.NewHandshakeState(config)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize client handshake: %w", err)
	}

	return &ClientHandshake{
		hs:     hs,
		static: clientStatic,
		peer:   relayStaticPub,
		psk:    psk,
	}, nil
}

// CreateHandshakeInit generates Handshake Message 1 (MsgTypeHandshakeInit)
func (c *ClientHandshake) CreateHandshakeInit() ([]byte, error) {
	// Construct 24-byte payload: 8-byte Unix nanosecond timestamp + 16-byte random nonce
	payload := make([]byte, 24)
	binary.BigEndian.PutUint64(payload[0:8], uint64(time.Now().UnixNano()))
	if _, err := io.ReadFull(rand.Reader, payload[8:24]); err != nil {
		return nil, err
	}

	// WriteMessage produces: e_c (32B) || encrypted s_c (48B) || encrypted payload (24B + 16B tag = 40B) = 120B
	msg1, _, _, err := c.hs.WriteMessage(nil, payload)
	if err != nil {
		return nil, fmt.Errorf("client failed to write handshake message 1: %w", err)
	}

	// Wrap in outer 32-byte DFTP v3 header
	// SessionID is 0 during initiation, SequenceNumber is 0
	return transport.EncodeFrameV3(transport.MsgTypeHandshakeInit, 0, 0, 0, 0, msg1)
}

// ProcessHandshakeResp processes Handshake Message 2 (MsgTypeHandshakeResp)
func (c *ClientHandshake) ProcessHandshakeResp(respPacket []byte) (sessionID uint32, sendCipher *TransportCipher, recvCipher *TransportCipher, err error) {
	frame, err := transport.DecodeFrame(respPacket)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("failed to decode response packet: %w", err)
	}

	if frame.Header.MsgType != transport.MsgTypeHandshakeResp {
		return 0, nil, nil, ErrInvalidHandshakeType
	}

	// ReadMessage finishes the handshake and returns csRecv and csSend
	payload2, csRecv, csSend, err := c.hs.ReadMessage(nil, frame.Payload)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("client failed to read handshake message 2: %w", err)
	}

	if csSend == nil || csRecv == nil {
		return 0, nil, nil, ErrHandshakeIncomplete
	}

	if len(payload2) < 4 {
		return 0, nil, nil, errors.New("handshake response payload missing 4-byte session id")
	}

	confirmedSessionID := binary.BigEndian.Uint32(payload2[0:4])
	if confirmedSessionID != frame.Header.SessionID {
		return 0, nil, nil, errors.New("handshake session id mismatch between header and payload")
	}

	// In Noise Initiator: csSend is for Client->Relay, csRecv is for Relay->Client
	sendCipher = NewTransportCipher(csSend.Cipher())
	recvCipher = NewTransportCipher(csRecv.Cipher())

	return confirmedSessionID, sendCipher, recvCipher, nil
}

// RelayHandshake orchestrates Noise_IKpsk2 reception at the relay
type RelayHandshake struct {
	hs     *noise.HandshakeState
	static noise.DHKey
	psk    []byte
}

// NewRelayHandshake initializes a relay Noise_IKpsk2 handshake state
func NewRelayHandshake(relayStatic noise.DHKey, psk []byte) (*RelayHandshake, error) {
	if len(psk) != 32 {
		return nil, ErrPSKLengthMismatch
	}

	config := noise.Config{
		CipherSuite:           CipherSuite(),
		Random:                rand.Reader,
		Pattern:               noise.HandshakeIK,
		Initiator:             false,
		Prologue:              HandshakePrologue,
		StaticKeypair:         relayStatic,
		PresharedKey:          psk,
		PresharedKeyPlacement: 2,
	}

	hs, err := noise.NewHandshakeState(config)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize relay handshake: %w", err)
	}

	return &RelayHandshake{
		hs:     hs,
		static: relayStatic,
		psk:    psk,
	}, nil
}

// ProcessHandshakeInit validates Handshake Message 1 from the client
func (r *RelayHandshake) ProcessHandshakeInit(initPacket []byte) (clientStaticPub []byte, err error) {
	frame, err := transport.DecodeFrame(initPacket)
	if err != nil {
		return nil, fmt.Errorf("failed to decode init packet: %w", err)
	}

	if frame.Header.MsgType != transport.MsgTypeHandshakeInit {
		return nil, ErrInvalidHandshakeType
	}

	payload1, _, _, err := r.hs.ReadMessage(nil, frame.Payload)
	if err != nil {
		return nil, fmt.Errorf("relay failed to read handshake message 1: %w", err)
	}

	if len(payload1) < 8 {
		return nil, errors.New("handshake init payload missing 8-byte timestamp")
	}

	// Verify timestamp is within +/- 10 seconds of relay clock
	tsNanos := int64(binary.BigEndian.Uint64(payload1[0:8]))
	pktTime := time.Unix(0, tsNanos)
	diff := time.Since(pktTime)
	if diff < -HandshakeTimestampTolerance || diff > HandshakeTimestampTolerance {
		return nil, ErrHandshakeTimestampExpired
	}

	return r.hs.PeerStatic(), nil
}

// CreateHandshakeResp generates Handshake Message 2 and completes the handshake
func (r *RelayHandshake) CreateHandshakeResp(assignedSessionID uint32) (packetBytes []byte, sendCipher *TransportCipher, recvCipher *TransportCipher, err error) {
	// Payload carries 4-byte confirmed assigned SessionID
	payload := make([]byte, 4)
	binary.BigEndian.PutUint32(payload[0:4], assignedSessionID)

	msg2, csSend, csRecv, err := r.hs.WriteMessage(nil, payload)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("relay failed to write handshake message 2: %w", err)
	}

	if csSend == nil || csRecv == nil {
		return nil, nil, nil, ErrHandshakeIncomplete
	}

	// In Noise Responder: csSend is for Relay->Client, csRecv is for Client->Relay
	sendCipher = NewTransportCipher(csSend.Cipher())
	recvCipher = NewTransportCipher(csRecv.Cipher())

	packetBytes, err = transport.EncodeFrameV3(transport.MsgTypeHandshakeResp, 0, assignedSessionID, 0, 0, msg2)
	if err != nil {
		return nil, nil, nil, err
	}

	return packetBytes, sendCipher, recvCipher, nil
}

// HandshakeCache protects against duplicate in-flight HandshakeInit packets
type HandshakeCache struct {
	mu      sync.Mutex
	entries map[string]cachedHandshake
}

type cachedHandshake struct {
	response  []byte
	timestamp time.Time
}

// NewHandshakeCache creates a cache with auto-expiration
func NewHandshakeCache() *HandshakeCache {
	return &HandshakeCache{
		entries: make(map[string]cachedHandshake),
	}
}

func (c *HandshakeCache) Get(clientEphemeralKey []byte) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	entry, ok := c.entries[string(clientEphemeralKey)]
	if !ok {
		return nil, false
	}

	if time.Since(entry.timestamp) > HandshakeTimestampTolerance {
		delete(c.entries, string(clientEphemeralKey))
		return nil, false
	}

	return entry.response, true
}

func (c *HandshakeCache) Put(clientEphemeralKey []byte, resp []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.entries[string(clientEphemeralKey)] = cachedHandshake{
		response:  resp,
		timestamp: time.Now(),
	}

	// Purge stale entries if cache grows
	if len(c.entries) > 256 {
		now := time.Now()
		for k, v := range c.entries {
			if now.Sub(v.timestamp) > HandshakeTimestampTolerance {
				delete(c.entries, k)
			}
		}
	}
}
