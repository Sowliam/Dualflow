import { exec } from 'node:child_process'
import { promisify } from 'node:util'
import type { PathTelemetryUpdate, TelemetryPoint } from '../../src/types'

const execAsync = promisify(exec)

interface InterfaceCounterSnapshot {
  bytesIn: number
  bytesOut: number
  timestampMs: number
  mediaSenseState: number
}

interface ProbeHistoryEntry {
  timestamp: number
  success: boolean
  rttMs: number | null
}

export class TelemetryMonitorService {
  private counterSnapshots = new Map<string, InterfaceCounterSnapshot>()
  private probeHistories = new Map<string, ProbeHistoryEntry[]>()
  private ewmaJitter = new Map<string, number>()
  private lastSuccessfulRtt = new Map<string, number>()
  private isSampling = false

  /**
   * Sample Windows network interface byte counters from GetIfTable2 / netsh subinterfaces.
   * Returns a map of interfaceName -> { rxBps, txBps, rxMbps, txMbps, isConnected }.
   */
  public async sampleInterfaceThroughput(): Promise<
    Map<string, { rxBps: number; txBps: number; rxMbps: number; txMbps: number; isConnected: boolean }>
  > {
    const results = new Map<
      string,
      { rxBps: number; txBps: number; rxMbps: number; txMbps: number; isConnected: boolean }
    >()

    try {
      const { stdout } = await execAsync('netsh interface ipv4 show subinterfaces', {
        windowsHide: true,
      })
      const now = Date.now()

      const lines = stdout.trim().split(/\r?\n/)
      let isTable = false

      for (const line of lines) {
        if (line.includes('---')) {
          isTable = true
          continue
        }
        if (!isTable) continue

        const match = line.trim().match(/^(\d+)\s+(\d+)\s+(\d+)\s+(\d+)\s+(.+)$/)
        if (!match) continue

        const [, mtuStr, stateStr, inBytesStr, outBytesStr, ifNameRaw] = match
        const ifName = ifNameRaw.trim()
        const ifKey = ifName.toLowerCase()
        const mediaSenseState = parseInt(stateStr, 10) // 1 = connected, 5 = disconnected
        const currentBytesIn = parseInt(inBytesStr, 10)
        const currentBytesOut = parseInt(outBytesStr, 10)
        const isConnected = mediaSenseState === 1

        const prev = this.counterSnapshots.get(ifKey)
        let rxBps = 0
        let txBps = 0

        if (prev && isConnected) {
          const elapsedSec = (now - prev.timestampMs) / 1000
          if (elapsedSec > 0.2) {
            const deltaIn = currentBytesIn - prev.bytesIn
            const deltaOut = currentBytesOut - prev.bytesOut

            // Check for counter reset, adapter disconnect/reconnect, or overflow
            if (deltaIn >= 0 && deltaOut >= 0) {
              rxBps = deltaIn / elapsedSec
              txBps = deltaOut / elapsedSec
            }
          }
        }

        this.counterSnapshots.set(ifKey, {
          bytesIn: currentBytesIn,
          bytesOut: currentBytesOut,
          timestampMs: now,
          mediaSenseState,
        })

        const rxMbps = (rxBps * 8) / 1_000_000
        const txMbps = (txBps * 8) / 1_000_000

        results.set(ifKey, {
          rxBps,
          txBps,
          rxMbps,
          txMbps,
          isConnected,
        })
      }
    } catch (err) {
      console.warn('[TelemetryMonitor] Error reading interface subinterfaces:', err)
    }

    return results
  }

  /**
   * Execute an independent, source-bound ICMP RTT probe using Windows ping.exe -S <sourceIp>.
   * Strictly prevents silent fallback to other network interfaces.
   */
  public async probePath(
    pathId: 'modem' | 'android',
    sourceIp: string | null,
    target = '1.1.1.1'
  ): Promise<PathTelemetryUpdate> {
    const timestamp = Date.now()

    if (!sourceIp) {
      return {
        pathId,
        rxBps: 0,
        txBps: 0,
        rttMs: null,
        rttMinMs: null,
        rttMaxMs: null,
        jitterMs: null,
        lossPercent: null,
        probeTarget: target,
        lastProbeTimestamp: null,
      }
    }

    let currentRtt: number | null = null
    let success = false

    try {
      // Windows ping with strict source address binding (-S) and 1000ms timeout (-w)
      const cmd = `ping.exe -n 1 -w 1000 -S ${sourceIp} ${target}`
      const { stdout } = await execAsync(cmd, { windowsHide: true })

      const timeMatch = stdout.match(/time[=<]([0-9]+)ms/i)
      if (timeMatch) {
        currentRtt = parseInt(timeMatch[1], 10)
        success = true
      }
    } catch {
      // Ping failed: host unreachable, general failure, or timeout
      success = false
      currentRtt = null
    }

    // Update probe history window (last 20 probes)
    let history = this.probeHistories.get(pathId) || []
    history.push({ timestamp, success, rttMs: currentRtt })
    if (history.length > 20) {
      history = history.slice(-20)
    }
    this.probeHistories.set(pathId, history)

    // Calculate packet loss from actual sent probes
    const totalProbes = history.length
    const failedProbes = history.filter((p) => !p.success).length
    const lossPercent = totalProbes > 0 ? (failedProbes / totalProbes) * 100 : 0

    // Calculate min/max RTT from successful probes in window
    const successfulRtts = history.map((p) => p.rttMs).filter((r): r is number => r !== null)
    const rttMinMs = successfulRtts.length > 0 ? Math.min(...successfulRtts) : null
    const rttMaxMs = successfulRtts.length > 0 ? Math.max(...successfulRtts) : null

    // Calculate RFC 3550 EWMA Jitter
    let jitterMs = this.ewmaJitter.get(pathId) ?? null
    if (currentRtt !== null) {
      const prevRtt = this.lastSuccessfulRtt.get(pathId)
      if (prevRtt !== undefined) {
        const d = Math.abs(currentRtt - prevRtt)
        if (jitterMs === null) {
          jitterMs = d
        } else {
          // RFC 3550: J(i) = J(i-1) + (|D(i-1, i)| - J(i-1)) / 16
          jitterMs = jitterMs + (d - jitterMs) / 16
        }
      } else {
        jitterMs = 0
      }
      this.lastSuccessfulRtt.set(pathId, currentRtt)
      this.ewmaJitter.set(pathId, jitterMs)
    }

    return {
      pathId,
      rxBps: 0, // Injected by caller
      txBps: 0,
      rttMs: currentRtt,
      rttMinMs,
      rttMaxMs,
      jitterMs: jitterMs !== null ? Math.round(jitterMs * 10) / 10 : null,
      lossPercent: Math.round(lossPercent * 10) / 10,
      probeTarget: `${target}:53`,
      lastProbeTimestamp: new Date(timestamp).toLocaleTimeString(),
      telemetryPoint: {
        timestamp,
        rttMs: currentRtt,
        rxMbps: 0,
        txMbps: 0,
        loss: lossPercent,
      },
    }
  }

  /**
   * Reset state for an interface or path.
   */
  public resetPath(pathId: 'modem' | 'android'): void {
    this.probeHistories.delete(pathId)
    this.ewmaJitter.delete(pathId)
    this.lastSuccessfulRtt.delete(pathId)
  }
}

