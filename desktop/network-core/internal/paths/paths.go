package paths

import (
	"strings"
	"sync"
	"time"

	"dualflow-core/internal/diagnostics"
	"dualflow-core/internal/discovery"
	"dualflow-core/internal/telemetry"
)

type PathState string

const (
	StateConnected    PathState = "connected"
	StateDisconnected PathState = "disconnected"
	StateStandby      PathState = "standby"
)

type ManagedPath struct {
	PathID             string                    `json:"pathId"`
	PathName           string                    `json:"pathName"`
	AssignedAdapter    *discovery.NetworkAdapter `json:"adapter"`
	SourceIp           *string                   `json:"sourceIp"`
	Gateway            *string                   `json:"gateway"`
	State              PathState                 `json:"state"`
	RxBps              float64                   `json:"rxBps"`
	TxBps              float64                   `json:"txBps"`
	RxMbps             float64                   `json:"rxMbps"`
	TxMbps             float64                   `json:"txMbps"`
	RttMs              *float64                  `json:"rttMs"`
	RttMinMs           *float64                  `json:"rttMinMs"`
	RttMaxMs           *float64                  `json:"rttMaxMs"`
	JitterMs           *float64                  `json:"jitterMs"`
	LossPercent        *float64                  `json:"lossPercent"`
	LastUpdate         int64                     `json:"lastUpdate"`
	DataSource         string                    `json:"dataSource"`
	AvailabilityStatus string                    `json:"availabilityStatus"`
}

type PathManager struct {
	mu           sync.RWMutex
	path1        ManagedPath
	path2        ManagedPath
	manualModem  string // adapter ID override
	manualTether string // adapter ID override
}

func NewPathManager() *PathManager {
	now := time.Now().UnixMilli()
	return &PathManager{
		path1: ManagedPath{
			PathID:             "modem",
			PathName:           "PATH 1: HOME MODEM / ISP",
			AssignedAdapter:    nil,
			SourceIp:           nil,
			Gateway:            nil,
			State:              StateDisconnected,
			RxBps:              0,
			TxBps:              0,
			RxMbps:             0,
			TxMbps:             0,
			RttMs:              nil,
			RttMinMs:           nil,
			RttMaxMs:           nil,
			JitterMs:           nil,
			LossPercent:        nil,
			LastUpdate:         now,
			DataSource:         "go_iphlpapi_core",
			AvailabilityStatus: "unavailable",
		},
		path2: ManagedPath{
			PathID:             "android",
			PathName:           "PATH 2: ANDROID USB TETHER",
			AssignedAdapter:    nil,
			SourceIp:           nil,
			Gateway:            nil,
			State:              StateDisconnected,
			RxBps:              0,
			TxBps:              0,
			RxMbps:             0,
			TxMbps:             0,
			RttMs:              nil,
			RttMinMs:           nil,
			RttMaxMs:           nil,
			JitterMs:           nil,
			LossPercent:        nil,
			LastUpdate:         now,
			DataSource:         "go_iphlpapi_core",
			AvailabilityStatus: "unavailable",
		},
	}
}

// Reconcile updates path assignments against the latest set of enumerated Windows adapters
func (m *PathManager) Reconcile(adapters []discovery.NetworkAdapter) {
	m.mu.Lock()
	defer m.mu.Unlock()

	adapterMap := make(map[string]discovery.NetworkAdapter)
	for _, a := range adapters {
		adapterMap[a.ID] = a
	}

	// 1. Reconcile Path 1 (Modem)
	var primary *discovery.NetworkAdapter
	if m.manualModem != "" {
		if a, ok := adapterMap[m.manualModem]; ok {
			primary = &a
		}
	}
	if primary == nil {
		// Auto-discover modem: classification == "modem" && connected, or physical ethernet connected
		for _, a := range adapters {
			if a.Classification == "modem" && a.Status == "connected" {
				cpy := a
				primary = &cpy
				break
			}
		}
		if primary == nil {
			for _, a := range adapters {
				if a.MediaType == "ethernet" && a.Status == "connected" {
					cpy := a
					primary = &cpy
					break
				}
			}
		}
	}

	m.path1.AssignedAdapter = primary
	if primary != nil && primary.Status == "connected" {
		m.path1.SourceIp = primary.IPv4
		m.path1.Gateway = primary.Gateway
		m.path1.State = StateConnected
		m.path1.AvailabilityStatus = "available"
	} else {
		m.path1.SourceIp = nil
		m.path1.Gateway = nil
		m.path1.State = StateDisconnected
		m.path1.AvailabilityStatus = "unavailable"
	}

	// 2. Reconcile Path 2 (Android Tethering)
	var tether *discovery.NetworkAdapter
	if m.manualTether != "" {
		if a, ok := adapterMap[m.manualTether]; ok {
			tether = &a
		}
	}
	if tether == nil {
		for _, a := range adapters {
			if a.Classification == "android" {
				cpy := a
				tether = &cpy
				break
			}
		}
	}

	m.path2.AssignedAdapter = tether
	if tether != nil {
		if tether.Status == "connected" {
			m.path2.SourceIp = tether.IPv4
			m.path2.Gateway = tether.Gateway
			m.path2.State = StateConnected
			m.path2.AvailabilityStatus = "available"
		} else {
			m.path2.SourceIp = nil
			m.path2.Gateway = nil
			m.path2.State = StateStandby
			m.path2.AvailabilityStatus = "unavailable"
		}
	} else {
		m.path2.SourceIp = nil
		m.path2.Gateway = nil
		m.path2.State = StateDisconnected
		m.path2.AvailabilityStatus = "unavailable"
	}

	m.path1.LastUpdate = time.Now().UnixMilli()
	m.path2.LastUpdate = time.Now().UnixMilli()
}

// UpdateTelemetry binds latest throughput and probe values to paths
func (m *PathManager) UpdateTelemetry(throughput map[string]telemetry.InterfaceThroughput, modemProbe telemetry.ProbeTelemetry, androidProbe telemetry.ProbeTelemetry) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now().UnixMilli()

	// Update Path 1
	if m.path1.AssignedAdapter != nil {
		name := strings.ToLower(m.path1.AssignedAdapter.Name)
		if tp, ok := throughput[name]; ok {
			m.path1.RxBps = tp.RxBps
			m.path1.TxBps = tp.TxBps
			m.path1.RxMbps = tp.RxMbps
			m.path1.TxMbps = tp.TxMbps
		}
	}
	m.path1.RttMs = modemProbe.RttMs
	m.path1.RttMinMs = modemProbe.RttMinMs
	m.path1.RttMaxMs = modemProbe.RttMaxMs
	m.path1.JitterMs = modemProbe.JitterMs
	m.path1.LossPercent = modemProbe.LossPercent
	m.path1.LastUpdate = now

	// Update Path 2
	if m.path2.AssignedAdapter != nil {
		name := strings.ToLower(m.path2.AssignedAdapter.Name)
		if tp, ok := throughput[name]; ok {
			m.path2.RxBps = tp.RxBps
			m.path2.TxBps = tp.TxBps
			m.path2.RxMbps = tp.RxMbps
			m.path2.TxMbps = tp.TxMbps
		}
	}
	m.path2.RttMs = androidProbe.RttMs
	m.path2.RttMinMs = androidProbe.RttMinMs
	m.path2.RttMaxMs = androidProbe.RttMaxMs
	m.path2.JitterMs = androidProbe.JitterMs
	m.path2.LossPercent = androidProbe.LossPercent
	m.path2.LastUpdate = now
}

// AssignAdapter sets manual override
func (m *PathManager) AssignAdapter(pathID, adapterID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if pathID == "modem" {
		m.manualModem = adapterID
		diagnostics.Info("Path 1 manual override set to: %s", adapterID)
	} else if pathID == "android" {
		m.manualTether = adapterID
		diagnostics.Info("Path 2 manual override set to: %s", adapterID)
	}
}

// GetPaths returns a thread-safe snapshot of both paths
func (m *PathManager) GetPaths() (ManagedPath, ManagedPath) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.path1, m.path2
}
