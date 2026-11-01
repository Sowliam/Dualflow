package telemetry

import (
	"bytes"
	"fmt"
	"math"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"dualflow-core/internal/diagnostics"
)

type InterfaceThroughput struct {
	Name        string  `json:"name"`
	RxBps       float64 `json:"rxBps"`
	TxBps       float64 `json:"txBps"`
	RxMbps      float64 `json:"rxMbps"`
	TxMbps      float64 `json:"txMbps"`
	IsConnected bool    `json:"isConnected"`
	TimestampMs int64   `json:"timestampMs"`
}

type ProbeTelemetry struct {
	PathID             string   `json:"pathId"`
	SourceIp           *string  `json:"sourceIp"`
	Target             string   `json:"target"`
	RttMs              *float64 `json:"rttMs"`
	RttMinMs           *float64 `json:"rttMinMs"`
	RttMaxMs           *float64 `json:"rttMaxMs"`
	JitterMs           *float64 `json:"jitterMs"`
	LossPercent        *float64 `json:"lossPercent"`
	LastProbeTimestamp string   `json:"lastProbeTimestamp"`
}

type PathTelemetryPoint struct {
	Timestamp int64    `json:"timestamp"`
	RttMs     *float64 `json:"rttMs"`
	RxMbps    float64  `json:"rxMbps"`
	TxMbps    float64  `json:"txMbps"`
	Loss      float64  `json:"loss"`
}

type CounterSnapshot struct {
	BytesIn   uint64
	BytesOut  uint64
	Timestamp int64
	State     int // 1 = connected, 5 = disconnected
}

type TelemetryEngine struct {
	mu           sync.Mutex
	lastCounters map[string]CounterSnapshot
	probeHistory map[string][]probeSample
	ewmaJitter   map[string]float64
	lastRtt      map[string]float64
}

type probeSample struct {
	Timestamp int64
	Success   bool
	RttMs     float64
}

func NewTelemetryEngine() *TelemetryEngine {
	return &TelemetryEngine{
		lastCounters: make(map[string]CounterSnapshot),
		probeHistory: make(map[string][]probeSample),
		ewmaJitter:   make(map[string]float64),
		lastRtt:      make(map[string]float64),
	}
}

// SampleThroughput queries Windows interface counters and computes delta Mbps
func (e *TelemetryEngine) SampleThroughput() map[string]InterfaceThroughput {
	e.mu.Lock()
	defer e.mu.Unlock()

	results := make(map[string]InterfaceThroughput)
	out, err := execHiddenCmd("netsh", "interface", "ipv4", "show", "subinterfaces")
	if err != nil {
		diagnostics.Warn("Failed to query subinterfaces: %v", err)
		return results
	}

	now := time.Now().UnixMilli()
	lines := strings.Split(out, "\n")
	isTable := false

	for _, l := range lines {
		l = strings.TrimSpace(l)
		if strings.Contains(l, "---") {
			isTable = true
			continue
		}
		if !isTable || l == "" {
			continue
		}

		fields := strings.Fields(l)
		if len(fields) >= 5 {
			state, _ := strconv.Atoi(fields[1])
			inB, _ := strconv.ParseUint(fields[2], 10, 64)
			outB, _ := strconv.ParseUint(fields[3], 10, 64)
			ifName := strings.Join(fields[4:], " ")
			key := strings.ToLower(ifName)
			isConnected := state == 1

			prev, hasPrev := e.lastCounters[key]
			var rxBps, txBps float64

			if hasPrev && isConnected {
				elapsedSec := float64(now-prev.Timestamp) / 1000.0
				if elapsedSec > 0.2 {
					if inB >= prev.BytesIn && outB >= prev.BytesOut {
						deltaIn := inB - prev.BytesIn
						deltaOut := outB - prev.BytesOut
						rxBps = float64(deltaIn) / elapsedSec
						txBps = float64(deltaOut) / elapsedSec
					}
				}
			}

			e.lastCounters[key] = CounterSnapshot{
				BytesIn:   inB,
				BytesOut:  outB,
				Timestamp: now,
				State:     state,
			}

			rxMbps := (rxBps * 8) / 1_000_000.0
			txMbps := (txBps * 8) / 1_000_000.0

			results[key] = InterfaceThroughput{
				Name:        ifName,
				RxBps:       rxBps,
				TxBps:       txBps,
				RxMbps:      rxMbps,
				TxMbps:      txMbps,
				IsConnected: isConnected,
				TimestampMs: now,
			}
		}
	}

	return results
}

// ProbePath performs a source-bound ICMP ping probe without falling back to other interfaces
func (e *TelemetryEngine) ProbePath(pathID string, sourceIp *string, target string) ProbeTelemetry {
	e.mu.Lock()
	defer e.mu.Unlock()

	now := time.Now().UnixMilli()
	if target == "" {
		target = "1.1.1.1"
	}

	if sourceIp == nil || *sourceIp == "" {
		return ProbeTelemetry{
			PathID:             pathID,
			SourceIp:           nil,
			Target:             target + ":53",
			RttMs:              nil,
			RttMinMs:           nil,
			RttMaxMs:           nil,
			JitterMs:           nil,
			LossPercent:        nil,
			LastProbeTimestamp: "",
		}
	}

	src := *sourceIp
	cmdStr := fmt.Sprintf("ping.exe -n 1 -w 1000 -S %s %s", src, target)
	out, err := execHiddenCmd("cmd.exe", "/c", cmdStr)

	var currentRtt *float64
	success := false

	if err == nil {
		re := regexp.MustCompile(`(?i)time[=<]([0-9]+)ms`)
		m := re.FindStringSubmatch(out)
		if len(m) >= 2 {
			rttVal, _ := strconv.ParseFloat(m[1], 64)
			currentRtt = &rttVal
			success = true
		}
	}

	// Update sliding window history (20 samples)
	history := e.probeHistory[pathID]
	rttVal := 0.0
	if currentRtt != nil {
		rttVal = *currentRtt
	}
	history = append(history, probeSample{
		Timestamp: now,
		Success:   success,
		RttMs:     rttVal,
	})
	if len(history) > 20 {
		history = history[len(history)-20:]
	}
	e.probeHistory[pathID] = history

	// Calculate loss percentage from actual probes
	total := len(history)
	failed := 0
	var successfulRtts []float64
	for _, s := range history {
		if !s.Success {
			failed++
		} else {
			successfulRtts = append(successfulRtts, s.RttMs)
		}
	}
	loss := (float64(failed) / float64(total)) * 100.0

	var minRtt, maxRtt *float64
	if len(successfulRtts) > 0 {
		minV := successfulRtts[0]
		maxV := successfulRtts[0]
		for _, v := range successfulRtts {
			if v < minV {
				minV = v
			}
			if v > maxV {
				maxV = v
			}
		}
		minRtt = &minV
		maxRtt = &maxV
	}

	// RFC 3550 EWMA Jitter
	var jitter *float64
	if currentRtt != nil {
		rtt := *currentRtt
		prev, hasPrev := e.lastRtt[pathID]
		curJitter, hasJitter := e.ewmaJitter[pathID]

		if hasPrev {
			d := math.Abs(rtt - prev)
			if !hasJitter {
				curJitter = d
			} else {
				curJitter = curJitter + (d-curJitter)/16.0
			}
		} else {
			curJitter = 0.0
		}

		e.lastRtt[pathID] = rtt
		e.ewmaJitter[pathID] = curJitter
		jitter = &curJitter
	}

	return ProbeTelemetry{
		PathID:             pathID,
		SourceIp:           sourceIp,
		Target:             target + ":53",
		RttMs:              currentRtt,
		RttMinMs:           minRtt,
		RttMaxMs:           maxRtt,
		JitterMs:           jitter,
		LossPercent:        &loss,
		LastProbeTimestamp: time.Now().Format("15:04:05"),
	}
}

func execHiddenCmd(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = nil
	err := cmd.Run()
	return out.String(), err
}

