import { useRef, useEffect, type FC } from 'react'
import { ArrowDown, ArrowUp, Activity, Globe, Wifi, Smartphone, AlertCircle } from 'lucide-react'
import type { PathTelemetry } from '../types'

interface PathCardProps {
  telemetry: PathTelemetry
  isPrimary?: boolean
}

export const PathCard: FC<PathCardProps> = ({ telemetry }) => {
  const isModem = telemetry.pathId === 'modem'
  const identityColor = isModem ? '#4F8FBF' : '#B97A4A'
  const canvasRef = useRef<HTMLCanvasElement | null>(null)

  const adapter = telemetry.adapter
  const isConnected = adapter?.status === 'connected'

  // Render pure technical 1.5px step canvas sparkline (no glowing fills, no fake sine waves)
  useEffect(() => {
    const canvas = canvasRef.current
    if (!canvas) return

    const ctx = canvas.getContext('2d')
    if (!ctx) return

    const width = canvas.width
    const height = canvas.height

    ctx.clearRect(0, 0, width, height)

    // Background grid lines (every 20% height and 25% width)
    ctx.strokeStyle = '#24282E'
    ctx.lineWidth = 1

    ctx.beginPath()
    for (let y = 0.25; y < 1; y += 0.25) {
      ctx.moveTo(0, y * height)
      ctx.lineTo(width, y * height)
    }
    for (let x = 0.2; x < 1; x += 0.2) {
      ctx.moveTo(x * width, 0)
      ctx.lineTo(x * width, height)
    }
    ctx.stroke()

    const history = telemetry.history
    if (history.length < 2) {
      // Honest empty state: no fake data!
      ctx.fillStyle = '#5E6670'
      ctx.font = '10px IBM Plex Mono, monospace'
      ctx.textAlign = 'center'
      ctx.fillText(
        isConnected ? 'Waiting for probe metrics...' : 'Path offline — no telemetry',
        width / 2,
        height / 2 + 3
      )
      return
    }

    // Draw real latency/loss curve
    const maxRtt = Math.max(...history.map((h) => h.rttMs ?? 0), 60)
    ctx.beginPath()
    ctx.strokeStyle = identityColor
    ctx.lineWidth = 1.5

    history.forEach((point, index) => {
      const x = (index / (history.length - 1)) * width
      const rtt = point.rttMs ?? 0
      const y = height - (rtt / maxRtt) * (height - 10) - 5
      if (index === 0) {
        ctx.moveTo(x, y)
      } else {
        ctx.lineTo(x, y)
      }
    })
    ctx.stroke()
  }, [telemetry.history, isConnected, identityColor])

  // Helper to format Mbps with tabular figures
  const formatSpeed = (bps: number) => {
    const mbps = (bps * 8) / 1_000_000
    return mbps.toFixed(2)
  }

  return (
    <div
      className="bg-[#1B1E22] border border-[#2C3238] rounded-[2px] relative overflow-hidden flex flex-col justify-between"
      style={{ borderLeftColor: identityColor, borderLeftWidth: '3px' }}
    >
      <div>
        {/* Header: Path Name, Hardware Tag & Status */}
        <div className="p-3 border-b border-[#2C3238] flex items-center justify-between">
          <div className="flex items-center gap-2">
            {isModem ? (
              adapter?.mediaType === 'wifi' ? (
                <Wifi className="w-4 h-4 text-[#4F8FBF]" strokeWidth={1.5} />
              ) : (
                <Globe className="w-4 h-4 text-[#4F8FBF]" strokeWidth={1.5} />
              )
            ) : (
              <Smartphone className="w-4 h-4 text-[#B97A4A]" strokeWidth={1.5} />
            )}

            <div>
              <div className="flex items-center gap-2">
                <span className="font-semibold text-xs text-[#E2E5E9] tracking-wide font-mono">
                  {telemetry.pathName}
                </span>
                <span
                  className="px-1.5 py-0.2 rounded-[2px] text-[10px] font-mono border"
                  style={{
                    backgroundColor: `${identityColor}15`,
                    borderColor: `${identityColor}40`,
                    color: identityColor,
                  }}
                >
                  {isModem ? 'PATH 1 (PRIMARY ISP)' : 'PATH 2 (TETHER / STANDBY)'}
                </span>
              </div>
              <p className="text-[11px] text-[#9098A2] truncate max-w-sm">
                {adapter?.description ?? 'No adapter assigned'}
              </p>
            </div>
          </div>

          <div className="flex items-center gap-1.5 font-mono text-[11px]">
            <span
              className={`w-2 h-2 rounded-[1px] ${
                isConnected ? 'bg-[#4EA869]' : 'bg-[#5A626A]'
              }`}
            />
            <span className={isConnected ? 'text-[#4EA869]' : 'text-[#5E6670]'}>
              {isConnected ? 'ONLINE' : 'DISCONNECTED'}
            </span>
          </div>
        </div>

        {/* Adapter Parameters Table (High Density Windows Style) */}
        <div className="p-3 border-b border-[#2C3238] grid grid-cols-3 gap-y-2 gap-x-4 text-xs font-mono">
          <div>
            <span className="text-[10px] text-[#5E6670] uppercase block">IPv4 Address</span>
            <span className="text-[#E2E5E9] tabular-num">{adapter?.ipv4 ?? '—'}</span>
          </div>
          <div>
            <span className="text-[10px] text-[#5E6670] uppercase block">Default Gateway</span>
            <span className="text-[#E2E5E9] tabular-num">{adapter?.gateway ?? '—'}</span>
          </div>
          <div>
            <span className="text-[10px] text-[#5E6670] uppercase block">Metric / MTU</span>
            <span className="text-[#E2E5E9] tabular-num">
              {adapter ? `${adapter.metric} / ${adapter.mtu}` : '—'}
            </span>
          </div>
          <div>
            <span className="text-[10px] text-[#5E6670] uppercase block">Media Type</span>
            <span className="text-[#9098A2] uppercase">{adapter?.mediaType ?? '—'}</span>
          </div>
          <div>
            <span className="text-[10px] text-[#5E6670] uppercase block">Physical MAC</span>
            <span className="text-[#9098A2] tabular-num">{adapter?.mac ?? '—'}</span>
          </div>
          <div>
            <span className="text-[10px] text-[#5E6670] uppercase block">Source Bind IP</span>
            <span className="text-[#9098A2] tabular-num">{telemetry.sourceIp ?? '—'}</span>
          </div>
        </div>

        {/* Real-time Telemetry Metrics Grid */}
        <div className="p-3 grid grid-cols-4 gap-2 bg-[#16181A]/40 border-b border-[#2C3238]">
          {/* Download Speed */}
          <div className="bg-[#141618] p-2 rounded-[2px] border border-[#2C3238]">
            <div className="flex items-center justify-between text-[10px] text-[#5E6670] font-mono">
              <span className="flex items-center gap-1">
                <ArrowDown className="w-3 h-3 text-[#4EA869]" strokeWidth={1.5} />
                <span>RX RATE</span>
              </span>
              <span>Mbps</span>
            </div>
            <div className="text-base font-medium text-[#E2E5E9] tabular-num mt-0.5">
              {formatSpeed(telemetry.rxBps)}
            </div>
          </div>

          {/* Upload Speed */}
          <div className="bg-[#141618] p-2 rounded-[2px] border border-[#2C3238]">
            <div className="flex items-center justify-between text-[10px] text-[#5E6670] font-mono">
              <span className="flex items-center gap-1">
                <ArrowUp className="w-3 h-3 text-[#4F8FBF]" strokeWidth={1.5} />
                <span>TX RATE</span>
              </span>
              <span>Mbps</span>
            </div>
            <div className="text-base font-medium text-[#E2E5E9] tabular-num mt-0.5">
              {formatSpeed(telemetry.txBps)}
            </div>
          </div>

          {/* Latency (RTT) */}
          <div className="bg-[#141618] p-2 rounded-[2px] border border-[#2C3238]">
            <div className="flex items-center justify-between text-[10px] text-[#5E6670] font-mono">
              <span className="flex items-center gap-1">
                <Activity className="w-3 h-3 text-[#D99B32]" strokeWidth={1.5} />
                <span>LATENCY (RTT)</span>
              </span>
              <span>ms</span>
            </div>
            <div className="text-base font-medium text-[#E2E5E9] tabular-num mt-0.5">
              {telemetry.rttMs !== null ? telemetry.rttMs.toFixed(1) : '—'}
            </div>
            <div className="text-[10px] text-[#5E6670] font-mono truncate">
              {telemetry.rttMinMs !== null && telemetry.rttMaxMs !== null
                ? `min ${telemetry.rttMinMs.toFixed(0)} / max ${telemetry.rttMaxMs.toFixed(0)}`
                : 'min — / max —'}
            </div>
          </div>

          {/* Jitter & Packet Loss */}
          <div className="bg-[#141618] p-2 rounded-[2px] border border-[#2C3238]">
            <div className="flex items-center justify-between text-[10px] text-[#5E6670] font-mono">
              <span>JITTER / LOSS</span>
              <span>%</span>
            </div>
            <div className="text-base font-medium text-[#E2E5E9] tabular-num mt-0.5 flex items-baseline gap-1.5">
              <span>{telemetry.jitterMs !== null ? `${telemetry.jitterMs.toFixed(1)} ms` : '—'}</span>
              <span
                className={`text-xs ${
                  telemetry.lossPercent !== null && telemetry.lossPercent > 0
                    ? 'text-[#C45347]'
                    : 'text-[#4EA869]'
                }`}
              >
                {telemetry.lossPercent !== null ? `${telemetry.lossPercent.toFixed(1)}%` : '0.0%'}
              </span>
            </div>
          </div>
        </div>

        {/* Real-time Oscilloscope Step Chart */}
        <div className="p-3">
          <div className="flex items-center justify-between text-[10px] font-mono text-[#5E6670] mb-1.5">
            <span>TELEMETRY OSCILLOSCOPE (LAST 60 SECONDS)</span>
            <span>Target: {telemetry.probeTarget ?? '1.1.1.1:53'}</span>
          </div>

          <div className="h-24 w-full bg-[#141618] rounded-[2px] border border-[#2C3238] overflow-hidden">
            <canvas
              ref={canvasRef}
              width={540}
              height={96}
              className="w-full h-full block"
            />
          </div>
        </div>
      </div>

      {/* Footer: Probe Provenance & Measured Age */}
      {!adapter && !isModem ? (
        <div className="px-3 py-2 bg-[#1B1E22] border-t border-[#2C3238] text-[11px] font-mono text-[#D99B32] flex items-center justify-between">
          <span>Android USB tethering device not detected. Connect phone & enable USB tethering.</span>
          <span className="text-[#5E6670]">unbound</span>
        </div>
      ) : (
        <div className="px-3 py-2 bg-[#16181A] border-t border-[#2C3238] text-[11px] font-mono text-[#5E6670] flex items-center justify-between">
          <span className="flex items-center gap-1 truncate max-w-xs">
            <AlertCircle className="w-3 h-3 text-[#9098A2]" strokeWidth={1.5} />
            {telemetry.lastProbeTimestamp
              ? `Measured via ${telemetry.probeTarget ?? 'ICMP/UDP'} at ${telemetry.lastProbeTimestamp}`
              : 'No active probe session'}
          </span>
          <span className="text-[#9098A2]">
            {telemetry.sourceIp ? `src:${telemetry.sourceIp}` : 'unbound'}
          </span>
        </div>
      )}
    </div>
  )
}
