package relay

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// Config defines the configuration parameters for the DualFlow Relay Server.
type Config struct {
	ListenAddr         string        `json:"listen_addr"`
	PSKHex             string        `json:"-"` // Never serialized in logs/json
	ServerStaticKeyHex string        `json:"-"`
	HandshakeRateLimit int           `json:"handshake_rate_limit"` // default 20/sec
	SessionIdleTimeout time.Duration `json:"session_idle_timeout"` // default 30s
	FlowIdleTimeout    time.Duration `json:"flow_idle_timeout"`    // default 10s
	MaxSessions        int           `json:"max_sessions"`         // default 256
	MaxFlowsPerSession int           `json:"max_flows_per_session"`// default 1024

	// Parsed keys
	PSK             [32]byte `json:"-"`
	ServerStaticKey [32]byte `json:"-"`
}

// DefaultConfig returns safe defaults for local development.
func DefaultConfig() *Config {
	return &Config{
		ListenAddr:         "127.0.0.1:51820",
		HandshakeRateLimit: 20,
		SessionIdleTimeout: 30 * time.Second,
		FlowIdleTimeout:    10 * time.Second,
		MaxSessions:        256,
		MaxFlowsPerSession: 1024,
	}
}

// LoadFromEnv loads keys and configuration overrides from environment variables.
// Crucially, secrets are never logged or exposed.
func (c *Config) LoadFromEnv() error {
	if addr := os.Getenv("DUALFLOW_RELAY_LISTEN"); addr != "" {
		c.ListenAddr = addr
	}

	pskEnv := os.Getenv("DUALFLOW_RELAY_PSK")
	if pskEnv != "" {
		pskBytes, err := hex.DecodeString(strings.TrimSpace(pskEnv))
		if err != nil || len(pskBytes) != 32 {
			return errors.New("DUALFLOW_RELAY_PSK must be a valid 64-character hex string (32 bytes)")
		}
		copy(c.PSK[:], pskBytes)
	}

	staticKeyEnv := os.Getenv("DUALFLOW_RELAY_STATIC_KEY")
	if staticKeyEnv != "" {
		keyBytes, err := hex.DecodeString(strings.TrimSpace(staticKeyEnv))
		if err != nil || len(keyBytes) != 32 {
			return errors.New("DUALFLOW_RELAY_STATIC_KEY must be a valid 64-character hex string (32 bytes)")
		}
		copy(c.ServerStaticKey[:], keyBytes)
	}

	return nil
}

// EnsureKeys ensures valid PSK and ServerStaticKey exist, generating ephemeral ones if needed for test runs.
func (c *Config) EnsureKeys() error {
	zeroKey := [32]byte{}
	if c.PSK == zeroKey {
		if _, err := rand.Read(c.PSK[:]); err != nil {
			return fmt.Errorf("failed generating ephemeral PSK: %w", err)
		}
	}
	if c.ServerStaticKey == zeroKey {
		pair, err := GenerateStaticKeypair()
		if err != nil {
			return fmt.Errorf("failed generating ephemeral static key: %w", err)
		}
		copy(c.ServerStaticKey[:], pair.Private)
	}
	return nil
}

// RedactedSummary returns a string summary safe for logging that guarantees no key material is leaked.
func (c *Config) RedactedSummary() string {
	pskStatus := "[NOT CONFIGURED]"
	zeroKey := [32]byte{}
	if c.PSK != zeroKey {
		pskStatus = "[CONFIGURED]"
	}

	staticKeyStatus := "[NOT CONFIGURED]"
	if c.ServerStaticKey != zeroKey {
		staticKeyStatus = "[CONFIGURED]"
	}

	return fmt.Sprintf("ListenAddr: %s | PSK: %s | StaticKey: %s | RateLimit: %d/s | MaxSessions: %d",
		c.ListenAddr, pskStatus, staticKeyStatus, c.HandshakeRateLimit, c.MaxSessions)
}
