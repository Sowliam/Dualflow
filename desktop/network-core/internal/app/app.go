package app

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"

	"dualflow-core/internal/diagnostics"
	"dualflow-core/internal/discovery"
	"dualflow-core/internal/multipath"
	"dualflow-core/internal/packet"
	"dualflow-core/internal/paths"
	"dualflow-core/internal/protocol"
	"dualflow-core/internal/telemetry"
	"dualflow-core/internal/transport"
	"dualflow-core/internal/wintun"
)

type App struct {
	mu              sync.Mutex
	startTime       time.Time
	discovery       *discovery.DiscoveryEngine
	telemetry       *telemetry.TelemetryEngine
	paths           *paths.PathManager
	wintun          *wintun.Manager
	packetPipeline  *packet.Pipeline
	transport       *transport.InMemoryTransport
	multipathEngine *multipath.Engine
	relayClient     *multipath.RelayClient
	isSubscribed    bool
	stopChan        chan struct{}
	outWriter       *bufio.Writer
}

func NewApp() *App {
	wMgr := wintun.GetManager()
	pipe := packet.NewPipeline(1024)
	trans := transport.NewInMemoryTransport(0x44465450, true)
	mpCfg := multipath.DefaultConfig()
	mpEngine := multipath.NewEngine(mpCfg, 0x44465450)

	pipe.SetOutboundHandler(mpEngine)
	pipe.SetWintunSink(wMgr)
	pipe.SetTestMode(true)

	// Hook Wintun receive loop to pipe
	wMgr.SetPacketReceiver(func(pkt []byte) {
		pipe.IngestRawPacket(pkt)
	})

	// Hook Multi-Path Engine inbound loop to pipe
	mpEngine.SetInboundSink(func(raw []byte) {
		pipe.IngestTunnelPacket(raw)
	})

	// Start pipeline, multipath engine, and fallback transport
	pipe.Start()
	mpEngine.Start(context.Background())
	trans.Start(context.Background())

	return &App{
		startTime:       time.Now(),
		discovery:       discovery.NewDiscoveryEngine(),
		telemetry:       telemetry.NewTelemetryEngine(),
		paths:           paths.NewPathManager(),
		wintun:          wMgr,
		packetPipeline:  pipe,
		transport:       trans,
		multipathEngine: mpEngine,
		stopChan:        make(chan struct{}),
		outWriter:       bufio.NewWriter(os.Stdout),
	}
}

func (a *App) Run() error {
	diagnostics.Info("DualFlow Network Core Daemon initializing (v%s)...", protocol.ProtocolVersion)

	// Initial adapter discovery and path reconciliation
	adapters, err := a.discovery.Discover()
	if err == nil {
		a.paths.Reconcile(adapters)
		a.syncMultipathPaths()
	}

	// Send initial READY event to Electron
	a.sendEvent("ready", map[string]any{
		"version":        protocol.ProtocolVersion,
		"status":         "ready",
		"adapters_count": len(adapters),
		"uptime_ms":      0,
	})

	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var req protocol.Request
		if err := json.Unmarshal(line, &req); err != nil {
			diagnostics.Warn("Malformed JSON request received: %v", err)
			a.sendResponse(protocol.NewErrorResponse("unknown", protocol.ErrInvalidRequest, "Malformed JSON"))
			continue
		}

		if err := protocol.ValidateRequest(&req); err != nil {
			a.sendResponse(protocol.NewErrorResponse(req.RequestID, protocol.ErrInvalidRequest, err.Error()))
			continue
		}

		a.handleRequest(&req)
	}

	if err := scanner.Err(); err != nil && err != io.EOF {
		diagnostics.Error("Stdin scanner error: %v", err)
		return err
	}

	diagnostics.Info("Stdin closed. DualFlow Network Core Daemon shutting down cleanly.")
	a.wintun.CloseAdapter()
	a.packetPipeline.Stop()
	a.multipathEngine.Stop()
	a.transport.Stop()
	if a.relayClient != nil {
		a.relayClient.Disconnect()
	}
	close(a.stopChan)
	return nil
}

func (a *App) handleRequest(req *protocol.Request) {
	switch req.Type {
	case "get_status":
		p1, p2 := a.paths.GetPaths()
		a.sendResponse(protocol.NewSuccessResponse(req.RequestID, map[string]any{
			"version":     protocol.ProtocolVersion,
			"uptime_ms":   time.Since(a.startTime).Milliseconds(),
			"status":      "running",
			"path1_state": p1.State,
			"path2_state": p2.State,
		}))

	case "get_adapters":
		adapters, err := a.discovery.Discover()
		if err != nil {
			a.sendResponse(protocol.NewErrorResponse(req.RequestID, protocol.ErrInternalError, err.Error()))
			return
		}
		a.paths.Reconcile(adapters)
		a.sendResponse(protocol.NewSuccessResponse(req.RequestID, adapters))

	case "get_paths":
		p1, p2 := a.paths.GetPaths()
		a.sendResponse(protocol.NewSuccessResponse(req.RequestID, map[string]any{
			"path1": p1,
			"path2": p2,
		}))

	case "rescan":
		diagnostics.Info("Forced rescan requested by client")
		adapters, err := a.discovery.Discover()
		if err != nil {
			a.sendResponse(protocol.NewErrorResponse(req.RequestID, protocol.ErrInternalError, err.Error()))
			return
		}
		a.paths.Reconcile(adapters)
		a.syncMultipathPaths()
		p1, p2 := a.paths.GetPaths()
		a.sendResponse(protocol.NewSuccessResponse(req.RequestID, map[string]any{
			"adapters": adapters,
			"path1":    p1,
			"path2":    p2,
		}))

	case "assign_path":
		var payload protocol.AssignPathPayload
		if err := json.Unmarshal(req.Payload, &payload); err != nil {
			a.sendResponse(protocol.NewErrorResponse(req.RequestID, protocol.ErrInvalidRequest, "invalid assign_path payload"))
			return
		}
		a.paths.AssignAdapter(payload.PathID, payload.AdapterID)
		adapters, _ := a.discovery.Discover()
		a.paths.Reconcile(adapters)
		a.syncMultipathPaths()
		p1, p2 := a.paths.GetPaths()
		a.sendResponse(protocol.NewSuccessResponse(req.RequestID, map[string]any{
			"path1": p1,
			"path2": p2,
		}))

	case "get_telemetry":
		p1, p2 := a.paths.GetPaths()
		throughput := a.telemetry.SampleThroughput()
		modemProbe := a.telemetry.ProbePath("modem", p1.SourceIp, "1.1.1.1")
		androidProbe := a.telemetry.ProbePath("android", p2.SourceIp, "1.1.1.1")
		a.paths.UpdateTelemetry(throughput, modemProbe, androidProbe)

		up1, up2 := a.paths.GetPaths()
		a.sendResponse(protocol.NewSuccessResponse(req.RequestID, map[string]any{
			"throughput": throughput,
			"path1":      up1,
			"path2":      up2,
		}))

	case "subscribe":
		a.mu.Lock()
		if !a.isSubscribed {
			a.isSubscribed = true
			go a.startBackgroundTelemetryLoop()
		}
		a.mu.Unlock()
		a.sendResponse(protocol.NewSuccessResponse(req.RequestID, map[string]any{
			"subscribed": true,
		}))

	case "wintun_get_status":
		status := a.wintun.GetStatus()
		a.sendResponse(protocol.NewSuccessResponse(req.RequestID, status))

	case "wintun_get_stats":
		stats := a.wintun.GetStats()
		a.sendResponse(protocol.NewSuccessResponse(req.RequestID, stats))

	case "wintun_create_adapter":
		diagnostics.Info("Explicit request to create Wintun adapter '%s'", wintun.DefaultAdapterName)
		if err := a.wintun.CreateAdapter(); err != nil {
			diagnostics.Warn("CreateAdapter error: %v", err)
			a.sendResponse(protocol.NewErrorResponse(req.RequestID, protocol.ErrWintunError, err.Error()))
			return
		}
		a.sendResponse(protocol.NewSuccessResponse(req.RequestID, a.wintun.GetStatus()))

	case "wintun_open_adapter":
		diagnostics.Info("Opening Wintun adapter '%s'", wintun.DefaultAdapterName)
		if err := a.wintun.OpenAdapter(); err != nil {
			diagnostics.Warn("OpenAdapter error: %v", err)
			a.sendResponse(protocol.NewErrorResponse(req.RequestID, protocol.ErrWintunError, err.Error()))
			return
		}
		a.sendResponse(protocol.NewSuccessResponse(req.RequestID, a.wintun.GetStatus()))

	case "wintun_start_session":
		var payload protocol.WintunStartSessionPayload
		if len(req.Payload) > 0 {
			_ = json.Unmarshal(req.Payload, &payload)
		}
		diagnostics.Info("Starting Wintun packet session (capacity: %d)", payload.Capacity)
		if err := a.wintun.StartSession(payload.Capacity); err != nil {
			diagnostics.Warn("StartSession error: %v", err)
			a.sendResponse(protocol.NewErrorResponse(req.RequestID, protocol.ErrWintunError, err.Error()))
			return
		}
		a.sendResponse(protocol.NewSuccessResponse(req.RequestID, a.wintun.GetStatus()))

	case "wintun_stop_session":
		diagnostics.Info("Stopping Wintun packet session")
		if err := a.wintun.StopSession(); err != nil {
			diagnostics.Warn("StopSession error: %v", err)
			a.sendResponse(protocol.NewErrorResponse(req.RequestID, protocol.ErrWintunError, err.Error()))
			return
		}
		a.sendResponse(protocol.NewSuccessResponse(req.RequestID, a.wintun.GetStatus()))

	case "wintun_close_adapter":
		diagnostics.Info("Closing Wintun adapter handle")
		if err := a.wintun.CloseAdapter(); err != nil {
			diagnostics.Warn("CloseAdapter error: %v", err)
			a.sendResponse(protocol.NewErrorResponse(req.RequestID, protocol.ErrWintunError, err.Error()))
			return
		}
		a.sendResponse(protocol.NewSuccessResponse(req.RequestID, a.wintun.GetStatus()))

	case "wintun_delete_adapter":
		var payload protocol.WintunDeleteAdapterPayload
		if err := json.Unmarshal(req.Payload, &payload); err != nil || !payload.Confirm {
			a.sendResponse(protocol.NewErrorResponse(req.RequestID, protocol.ErrInvalidRequest, "explicit user confirmation (confirm: true) is required to delete adapter"))
			return
		}
		diagnostics.Warn("Explicit request to DELETE Wintun adapter '%s'", wintun.DefaultAdapterName)
		if err := a.wintun.DeleteAdapter(); err != nil {
			diagnostics.Error("DeleteAdapter error: %v", err)
			a.sendResponse(protocol.NewErrorResponse(req.RequestID, protocol.ErrWintunError, err.Error()))
			return
		}
		a.sendResponse(protocol.NewSuccessResponse(req.RequestID, a.wintun.GetStatus()))

	case "wintun_send_test_packet":
		var payload protocol.WintunSendTestPacketPayload
		if len(req.Payload) > 0 {
			_ = json.Unmarshal(req.Payload, &payload)
		}
		if payload.PayloadSize <= 0 {
			payload.PayloadSize = 32
		}
		testPkt := wintun.GenerateTestIPv4Packet(nil, nil, payload.PayloadSize)
		if err := a.wintun.SendPacket(testPkt); err != nil {
			a.sendResponse(protocol.NewErrorResponse(req.RequestID, protocol.ErrWintunError, err.Error()))
			return
		}
		stats := a.wintun.GetStats()
		a.sendResponse(protocol.NewSuccessResponse(req.RequestID, map[string]any{
			"sent":        true,
			"tx_packets":  stats.TxPackets,
			"tx_bytes":    stats.TxBytes,
			"packet_size": len(testPkt),
		}))

	case "packet_get_status":
		status := a.packetPipeline.Status()
		status["transport"] = a.transport.GetStats()
		a.sendResponse(protocol.NewSuccessResponse(req.RequestID, status))

	case "packet_get_stats":
		a.sendResponse(protocol.NewSuccessResponse(req.RequestID, map[string]any{
			"pipeline":  a.packetPipeline.GetStats(),
			"transport": a.transport.GetStats(),
		}))

	case "packet_set_test_mode":
		var payload protocol.PacketSetTestModePayload
		if len(req.Payload) > 0 {
			_ = json.Unmarshal(req.Payload, &payload)
		}
		a.packetPipeline.SetTestMode(payload.Enabled)
		status := a.packetPipeline.Status()
		status["transport"] = a.transport.GetStats()
		a.sendResponse(protocol.NewSuccessResponse(req.RequestID, status))

	case "packet_inject_test":
		var payload protocol.PacketInjectTestPayload
		if len(req.Payload) > 0 {
			_ = json.Unmarshal(req.Payload, &payload)
		}
		if payload.PayloadSize <= 0 {
			payload.PayloadSize = 32
		}
		var raw []byte
		if payload.IPVersion == 6 {
			// Generate test IPv6 packet
			src := net.ParseIP("2001:db8::1")
			dst := net.ParseIP("2606:4700:4700::1111")
			payloadBytes := make([]byte, payload.PayloadSize)
			for i := range payloadBytes {
				payloadBytes[i] = byte(i % 256)
			}
			raw = make([]byte, 40+len(payloadBytes))
			raw[0] = 0x60
			binary.BigEndian.PutUint16(raw[4:6], uint16(len(payloadBytes)))
			raw[6] = packet.ProtoTCP
			raw[7] = 64
			copy(raw[8:24], src.To16())
			copy(raw[24:40], dst.To16())
			copy(raw[40:], payloadBytes)
		} else {
			// Generate test IPv4 packet
			src := net.ParseIP("192.168.1.100")
			dst := net.ParseIP("1.1.1.1")
			raw = wintun.GenerateTestIPv4Packet(src, dst, payload.PayloadSize)
		}

		if payload.Corrupt {
			// Corrupt packet to exercise rejection and drop counting
			if len(raw) > 10 {
				raw[0] = 0x43 // Invalid IHL
			} else {
				raw = []byte{0x00, 0x01}
			}
		}

		pkt, err := a.packetPipeline.IngestRawPacket(raw)
		if err != nil {
			a.sendResponse(protocol.NewSuccessResponse(req.RequestID, map[string]any{
				"injected":   false,
				"error":      err.Error(),
				"stats":      a.packetPipeline.GetStats(),
				"transport":  a.transport.GetStats(),
			}))
			return
		}

		a.sendResponse(protocol.NewSuccessResponse(req.RequestID, map[string]any{
			"injected":     true,
			"version":      pkt.Version,
			"protocol":     pkt.Protocol,
			"total_length": pkt.TotalLength,
			"stats":        a.packetPipeline.GetStats(),
			"transport":    a.transport.GetStats(),
		}))

	case "packet_reset_stats":
		a.packetPipeline.ResetStats()
		a.transport.ResetStats()
		a.sendResponse(protocol.NewSuccessResponse(req.RequestID, map[string]any{
			"reset": true,
		}))

	case "multipath_get_status":
		status := a.multipathEngine.GetStatus()
		a.sendResponse(protocol.NewSuccessResponse(req.RequestID, status))

	case "multipath_set_mode":
		var payload protocol.MultipathSetModePayload
		if len(req.Payload) > 0 {
			_ = json.Unmarshal(req.Payload, &payload)
		}
		mode := multipath.SchedulerMode(payload.Mode)
		if err := a.multipathEngine.SetSchedulerMode(mode); err != nil {
			a.sendResponse(protocol.NewErrorResponse(req.RequestID, protocol.ErrPipelineError, err.Error()))
			return
		}
		a.sendResponse(protocol.NewSuccessResponse(req.RequestID, a.multipathEngine.GetStatus()))

	case "multipath_set_weights":
		var payload protocol.MultipathSetWeightsPayload
		if len(req.Payload) > 0 {
			_ = json.Unmarshal(req.Payload, &payload)
		}
		if err := a.multipathEngine.SetWeights(payload.Weights); err != nil {
			a.sendResponse(protocol.NewErrorResponse(req.RequestID, protocol.ErrPipelineError, err.Error()))
			return
		}
		a.sendResponse(protocol.NewSuccessResponse(req.RequestID, a.multipathEngine.GetStatus()))

	case "relay_connect":
		var payload protocol.RelayConnectPayload
		if len(req.Payload) > 0 {
			if err := json.Unmarshal(req.Payload, &payload); err != nil {
				a.sendResponse(protocol.NewErrorResponse(req.RequestID, protocol.ErrInvalidRequest, "invalid relay_connect payload"))
				return
			}
		}
		if payload.RelayAddr == "" {
			payload.RelayAddr = "127.0.0.1:51820"
		}
		pskBytes, err := hex.DecodeString(payload.PSKHex)
		if err != nil || len(pskBytes) != 32 {
			a.sendResponse(protocol.NewErrorResponse(req.RequestID, protocol.ErrInvalidRequest, "psk_hex must be 32 bytes hex"))
			return
		}
		relayPubBytes, err := hex.DecodeString(payload.RelayStaticPubHex)
		if err != nil || len(relayPubBytes) != 32 {
			a.sendResponse(protocol.NewErrorResponse(req.RequestID, protocol.ErrInvalidRequest, "relay_static_pub_hex must be 32 bytes hex"))
			return
		}

		cfg := multipath.RelayClientConfig{
			RelayAddr:      payload.RelayAddr,
			SOCKS5Listen:   payload.SOCKS5Listen,
			RelayStaticPub: relayPubBytes,
		}
		copy(cfg.PSK[:], pskBytes)

		client, err := multipath.NewRelayClient(cfg, a.multipathEngine)
		if err != nil {
			a.sendResponse(protocol.NewErrorResponse(req.RequestID, protocol.ErrRelayError, err.Error()))
			return
		}
		if err := client.Connect(); err != nil {
			a.sendResponse(protocol.NewErrorResponse(req.RequestID, protocol.ErrRelayError, err.Error()))
			return
		}
		a.mu.Lock()
		if a.relayClient != nil {
			a.relayClient.Disconnect()
		}
		a.relayClient = client
		a.mu.Unlock()

		a.sendResponse(protocol.NewSuccessResponse(req.RequestID, map[string]any{
			"status":     "connected",
			"session_id": fmt.Sprintf("0x%08X", client.SessionID()),
			"relay_addr": payload.RelayAddr,
		}))

	case "relay_disconnect":
		a.mu.Lock()
		if a.relayClient != nil {
			a.relayClient.Disconnect()
			a.relayClient = nil
		}
		a.mu.Unlock()
		a.sendResponse(protocol.NewSuccessResponse(req.RequestID, map[string]any{
			"status": "disconnected",
		}))

	case "relay_status":
		a.mu.Lock()
		client := a.relayClient
		a.mu.Unlock()

		if client == nil || !client.IsConnected() {
			a.sendResponse(protocol.NewSuccessResponse(req.RequestID, map[string]any{
				"status":    "disconnected",
				"connected": false,
			}))
			return
		}

		a.sendResponse(protocol.NewSuccessResponse(req.RequestID, map[string]any{
			"status":     "connected",
			"connected":  true,
			"session_id": fmt.Sprintf("0x%08X", client.SessionID()),
		}))

	default:
		a.sendResponse(protocol.NewErrorResponse(req.RequestID, protocol.ErrUnknownCommand, fmt.Sprintf("unknown command type: %s", req.Type)))
	}
}

func (a *App) startBackgroundTelemetryLoop() {
	ticker := time.NewTicker(1500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-a.stopChan:
			return
		case <-ticker.C:
			if adapters, err := a.discovery.Discover(); err == nil {
				a.paths.Reconcile(adapters)
				a.syncMultipathPaths()
			}

			p1, p2 := a.paths.GetPaths()
			throughput := a.telemetry.SampleThroughput()
			modemProbe := a.telemetry.ProbePath("modem", p1.SourceIp, "1.1.1.1")
			androidProbe := a.telemetry.ProbePath("android", p2.SourceIp, "1.1.1.1")
			a.paths.UpdateTelemetry(throughput, modemProbe, androidProbe)

			up1, up2 := a.paths.GetPaths()
			a.sendEvent("telemetry_update", map[string]any{
				"path1":     up1,
				"path2":     up2,
				"wintun":    a.wintun.GetStats(),
				"packet":    a.packetPipeline.GetStats(),
				"transport": a.transport.GetStats(),
				"multipath": a.multipathEngine.GetStatus(),
			})
		}
	}
}

func (a *App) syncMultipathPaths() {
	p1, p2 := a.paths.GetPaths()

	if p1.AssignedAdapter != nil && p1.SourceIp != nil {
		gw := ""
		if p1.Gateway != nil {
			gw = *p1.Gateway
		}
		path1 := multipath.NewPath("modem", p1.PathName, p1.AssignedAdapter.IfIndex, p1.AssignedAdapter.Name, p1.AssignedAdapter.Description, *p1.SourceIp, gw)
		if p1.State == paths.StateConnected {
			path1.SetState(multipath.StateAvailable)
		} else {
			path1.SetState(multipath.StateUnavailable)
		}
		_ = a.multipathEngine.AddOrUpdatePath(path1)
	}

	if p2.AssignedAdapter != nil && p2.SourceIp != nil {
		gw := ""
		if p2.Gateway != nil {
			gw = *p2.Gateway
		}
		path2 := multipath.NewPath("android", p2.PathName, p2.AssignedAdapter.IfIndex, p2.AssignedAdapter.Name, p2.AssignedAdapter.Description, *p2.SourceIp, gw)
		if p2.State == paths.StateConnected {
			path2.SetState(multipath.StateAvailable)
		} else {
			path2.SetState(multipath.StateUnavailable)
		}
		_ = a.multipathEngine.AddOrUpdatePath(path2)
	}
}

func (a *App) sendResponse(res protocol.Response) {
	a.mu.Lock()
	defer a.mu.Unlock()

	data, err := json.Marshal(res)
	if err != nil {
		diagnostics.Error("Failed to marshal response: %v", err)
		return
	}
	a.outWriter.Write(data)
	a.outWriter.WriteByte('\n')
	a.outWriter.Flush()
}

func (a *App) sendEvent(name string, data any) {
	a.mu.Lock()
	defer a.mu.Unlock()

	evt := protocol.NewEvent(name, data)
	b, err := json.Marshal(evt)
	if err != nil {
		diagnostics.Error("Failed to marshal event: %v", err)
		return
	}
	a.outWriter.Write(b)
	a.outWriter.WriteByte('\n')
	a.outWriter.Flush()
}

