package main

import (
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"dualflow-core/internal/relay"
)

func main() {
	listenAddr := flag.String("listen", "127.0.0.1:51820", "UDP listen address for DualFlow Relay")
	pskHex := flag.String("psk", "", "32-byte hex Pre-Shared Key (prefer DUALFLOW_RELAY_PSK environment variable)")
	staticKeyHex := flag.String("static-key", "", "32-byte hex private static key (prefer DUALFLOW_RELAY_STATIC_KEY)")
	showPubkey := flag.Bool("show-pubkey", false, "Derive and print the static public key from private key and exit")
	rateLimit := flag.Int("rate-limit", 20, "Handshake rate limit (handshakes per second)")
	flag.Parse()

	cfg := relay.DefaultConfig()
	cfg.ListenAddr = *listenAddr
	cfg.HandshakeRateLimit = *rateLimit

	// Load from environment first
	if err := cfg.LoadFromEnv(); err != nil {
		log.Fatalf("[FATAL] Environment error: %v", err)
	}

	// CLI flags override environment
	if *pskHex != "" {
		pskBytes, err := hex.DecodeString(*pskHex)
		if err != nil || len(pskBytes) != 32 {
			log.Fatalf("[FATAL] Invalid --psk: must be a 64-character hex string (32 bytes)")
		}
		copy(cfg.PSK[:], pskBytes)
	}

	if *staticKeyHex != "" {
		keyBytes, err := hex.DecodeString(*staticKeyHex)
		if err != nil || len(keyBytes) != 32 {
			log.Fatalf("[FATAL] Invalid --static-key: must be a 64-character hex string (32 bytes)")
		}
		copy(cfg.ServerStaticKey[:], keyBytes)
	}

	if err := cfg.EnsureKeys(); err != nil {
		log.Fatalf("[FATAL] Failed to configure cryptographic keys: %v", err)
	}

	if *showPubkey {
		pub, err := relay.DerivePublicKey(cfg.ServerStaticKey)
		if err != nil {
			log.Fatalf("[FATAL] Failed to derive public key: %v", err)
		}
		fmt.Println(hex.EncodeToString(pub))
		return
	}

	log.Println("=====================================================")
	log.Println("  DualFlow Secure Relay Server — v3.0 (DFTP v3)")
	log.Println("=====================================================")

	server, err := relay.NewServer(cfg)
	if err != nil {
		log.Fatalf("[FATAL] Failed to initialize relay server: %v", err)
	}

	pubKeyHex := hex.EncodeToString(server.StaticPublicKey())
	log.Printf("[INFO] Server Static Public Key: %s", pubKeyHex)

	if err := server.Start(); err != nil {
		log.Fatalf("[FATAL] Failed to start relay listener: %v", err)
	}

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	<-sigChan
	log.Println("[INFO] Interrupt received, shutting down DualFlow Secure Relay gracefully...")
	server.Stop()
	log.Println("[INFO] DualFlow Secure Relay stopped.")
}

