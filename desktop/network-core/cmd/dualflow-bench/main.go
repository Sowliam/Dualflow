package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"dualflow-core/internal/multipath"
	"dualflow-core/internal/relay"
)

func main() {
	mode := flag.String("mode", "client", "Benchmark mode: 'client' (runs throughput test through relay) or 'echo' (runs TCP echo server on relay)")
	listenAddr := flag.String("listen", "0.0.0.0:8888", "Listen address for echo server (in echo mode)")
	relayAddr := flag.String("relay", "", "Remote DualFlow Relay address (ip:port) (in client mode)")
	pskHex := flag.String("psk", "", "32-byte hex Pre-Shared Key")
	staticPubHex := flag.String("static-pub", "", "32-byte hex Server static public key")
	targetAddr := flag.String("target", "127.0.0.1:8888", "Target address for echo server through the relay")
	durationSec := flag.Int("duration", 10, "Benchmark duration in seconds")
	numStreams := flag.Int("streams", 4, "Number of concurrent TCP streams")
	flag.Parse()

	if *mode == "echo" {
		runEchoServer(*listenAddr)
		return
	}

	if *relayAddr == "" {
		fmt.Println("DualFlow Physical Hardware Validation Benchmark Tool")
		fmt.Println("Usage (Client): dualflow-bench.exe -mode client -relay <ip:port> -psk <hex> -static-pub <hex> -target <ip:port> [-duration 10] [-streams 4]")
		fmt.Println("Usage (Echo Server on Relay machine): dualflow-bench.exe -mode echo [-listen 0.0.0.0:8888]")
		os.Exit(1)
	}

	runClientBenchmark(*relayAddr, *pskHex, *staticPubHex, *targetAddr, *durationSec, *numStreams)
}

func runEchoServer(listenAddr string) {
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		log.Fatalf("[FATAL] Failed to bind echo server on %s: %v", listenAddr, err)
	}
	defer ln.Close()

	log.Printf("[INFO] DualFlow Hardware Benchmark Echo Server listening on %s (TCP)", listenAddr)

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-sigChan
		log.Println("[INFO] Shutting down Echo Server...")
		ln.Close()
		os.Exit(0)
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go func(c net.Conn) {
			defer c.Close()
			buf := make([]byte, 32768)
			for {
				n, err := c.Read(buf)
				if n > 0 {
					if _, wErr := c.Write(buf[:n]); wErr != nil {
						return
					}
				}
				if err != nil {
					return
				}
			}
		}(conn)
	}
}

func runClientBenchmark(relayAddr, pskHex, staticPubHex, targetAddr string, durationSec, numStreams int) {
	log.Println("================================================================================")
	log.Println("   DUALFLOW PHYSICAL HARDWARE VALIDATION BENCHMARK (PHYSICAL NETWORK MODE)")
	log.Println("================================================================================")

	var psk [32]byte
	if pskHex != "" {
		b, err := hex.DecodeString(pskHex)
		if err != nil || len(b) != 32 {
			log.Fatalf("[FATAL] Invalid --psk: must be 64-char hex")
		}
		copy(psk[:], b)
	} else if envPsk := os.Getenv("DUALFLOW_RELAY_PSK"); envPsk != "" {
		b, err := hex.DecodeString(envPsk)
		if err == nil && len(b) == 32 {
			copy(psk[:], b)
		}
	} else {
		copy(psk[:], relay.DefaultConfig().PSK[:])
	}

	var staticPub []byte
	if staticPubHex != "" {
		b, err := hex.DecodeString(staticPubHex)
		if err != nil || len(b) != 32 {
			log.Fatalf("[FATAL] Invalid --static-pub: must be 64-char hex")
		}
		staticPub = b
	} else if envPub := os.Getenv("DUALFLOW_RELAY_STATIC_PUB"); envPub != "" {
		b, err := hex.DecodeString(envPub)
		if err == nil && len(b) == 32 {
			staticPub = b
		}
	} else {
		log.Fatalf("[FATAL] Server static public key must be provided via --static-pub or DUALFLOW_RELAY_STATIC_PUB")
	}

	cfg := multipath.RelayClientConfig{
		RelayAddr:      relayAddr,
		PSK:            psk,
		RelayStaticPub: staticPub,
		SOCKS5Listen:   "127.0.0.1:0",
		AutoReconnect:  true,
	}

	log.Printf("[INFO] Connecting to Physical Relay: %s", relayAddr)
	client, err := multipath.NewRelayClient(cfg, nil)
	if err != nil {
		log.Fatalf("[FATAL] Failed to initialize Relay Client: %v", err)
	}

	if err := client.Connect(); err != nil {
		log.Fatalf("[FATAL] Failed to connect to Physical Relay: %v", err)
	}
	defer client.Disconnect()

	socksAddr := client.SocksAddr()
	log.Printf("[SUCCESS] Connected! SessionID: 0x%08X | SOCKS5 Proxy: %s", client.SessionID(), socksAddr)

	targetHost, targetPortStr, err := net.SplitHostPort(targetAddr)
	if err != nil {
		log.Fatalf("[FATAL] Invalid target address %s: %v", targetAddr, err)
	}
	var targetPort uint16
	fmt.Sscanf(targetPortStr, "%d", &targetPort)

	log.Printf("[INFO] Launching %d concurrent TCP streams against target %s for %d seconds...", numStreams, targetAddr, durationSec)

	var totalBytesSent uint64
	var totalBytesReceived uint64
	var hashMismatches uint64

	deadline := time.Now().Add(time.Duration(durationSec) * time.Second)
	var wg sync.WaitGroup

	startTime := time.Now()

	for i := 0; i < numStreams; i++ {
		wg.Add(1)
		go func(streamID int) {
			defer wg.Done()

			conn, err := net.DialTimeout("tcp", socksAddr, 5*time.Second)
			if err != nil {
				log.Printf("[WARN] Stream %d failed to dial SOCKS5 proxy: %v", streamID, err)
				return
			}
			defer conn.Close()

			// SOCKS5 Greeting
			if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
				return
			}
			authResp := make([]byte, 2)
			if _, err := io.ReadFull(conn, authResp); err != nil {
				return
			}

			// SOCKS5 CONNECT
			targetIP := net.ParseIP(targetHost)
			var req []byte
			if targetIP != nil && targetIP.To4() != nil {
				req = []byte{0x05, 0x01, 0x00, 0x01}
				req = append(req, targetIP.To4()...)
			} else {
				req = []byte{0x05, 0x01, 0x00, 0x03, byte(len(targetHost))}
				req = append(req, []byte(targetHost)...)
			}
			req = append(req, byte(targetPort>>8), byte(targetPort&0xFF))

			if _, err := conn.Write(req); err != nil {
				return
			}
			reply := make([]byte, 10)
			if _, err := io.ReadFull(conn, reply); err != nil || reply[1] != 0x00 {
				log.Printf("[WARN] Stream %d SOCKS5 connect failed: status 0x%02x", streamID, reply[1])
				return
			}

			// Stream data loop
			buf := make([]byte, 16384)
			recvBuf := make([]byte, 16384)
			_, _ = rand.Read(buf)

			for time.Now().Before(deadline) {
				if _, err := conn.Write(buf); err != nil {
					return
				}
				atomic.AddUint64(&totalBytesSent, uint64(len(buf)))

				n, err := io.ReadFull(conn, recvBuf)
				if err != nil {
					return
				}
				atomic.AddUint64(&totalBytesReceived, uint64(n))

				if sha256.Sum256(buf) != sha256.Sum256(recvBuf[:n]) {
					atomic.AddUint64(&hashMismatches, 1)
				}
			}
		}(i)
	}

	wg.Wait()
	elapsed := time.Since(startTime)

	sentMB := float64(totalBytesSent) / (1024 * 1024)
	recvMB := float64(totalBytesReceived) / (1024 * 1024)
	throughputMbps := (float64(totalBytesReceived) * 8 / (1000 * 1000)) / elapsed.Seconds()

	log.Println("================================================================================")
	log.Println("                 PHYSICAL HARDWARE VALIDATION SCORECARD                        ")
	log.Println("================================================================================")
	fmt.Printf("Elapsed Test Time:           %.2f seconds\n", elapsed.Seconds())
	fmt.Printf("Active Concurrent Streams:   %d\n", numStreams)
	fmt.Printf("Total Bytes Sent:            %.2f MB (%d bytes)\n", sentMB, totalBytesSent)
	fmt.Printf("Total Bytes Received:        %.2f MB (%d bytes)\n", recvMB, totalBytesReceived)
	fmt.Printf("Effective Aggregate Speed:   %.2f Mbps (%.2f MB/s)\n", throughputMbps, recvMB/elapsed.Seconds())
	fmt.Printf("Data Integrity Check:        %d hash mismatches\n", hashMismatches)

	// Hardware Validation Pass/Fail Criteria
	passed := true
	fmt.Println("--------------------------------------------------------------------------------")
	fmt.Println("Acceptance Criteria Verification:")

	if totalBytesReceived > 0 && hashMismatches == 0 {
		fmt.Println(" [PASS] Cryptographic & Payload Integrity: 100% byte & SHA-256 match")
	} else {
		fmt.Println(" [FAIL] Payload Integrity: Corrupted bytes detected")
		passed = false
	}

	if throughputMbps > 0 {
		fmt.Printf(" [PASS] Multipath Tunnel Functionality: Sustained throughput confirmed\n")
	} else {
		fmt.Println(" [FAIL] No data received over tunnel")
		passed = false
	}

	fmt.Println("--------------------------------------------------------------------------------")
	if passed {
		fmt.Println("FINAL HARDWARE BENCHMARK RESULT: PASS (HARDWARE VALIDATION SUCCESSFUL)")
	} else {
		fmt.Println("FINAL HARDWARE BENCHMARK RESULT: FAIL (HARDWARE DEFECT DETECTED)")
	}
	log.Println("================================================================================")
}
