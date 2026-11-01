import { useState, useEffect, type FC } from 'react'
import {
  ArrowRightLeft,
  CheckCircle2,
  AlertTriangle,
  XCircle,
  Clock,
  Shuffle,
} from 'lucide-react'
import type { MultipathStatus, MultipathPathSnapshot, BondingCoreStatus } from '../types'

interface AggregationEngineCardProps {
  coreStatus?: BondingCoreStatus
  totalRxBps?: number
  totalTxBps?: number
}

const defaultStatus: MultipathStatus = {
  state: 'STOPPED',
  scheduler_mode: 'round_robin',
  paths: [],
  paths_count: 0,
  stats: {
    total_logical_packets: 0,
    path_tx_packets: {},
    path_rx_packets: {},
    successful_sends: 0,
    failed_sends: 0,
    duplicates_dropped: 0,
    reordered_delivered: 0,
    late_packets_dropped: 0,
    timed_out_packets: 0,
    total_drops: 0,
  },
  reorder_queue_depth: 0,
  reorder_reordered: 0,
  reorder_dropped_late: 0,
  reorder_timed_out: 0,
  is_test_mode: true,
}

export const AggregationEngineCard: FC<AggregationEngineCardProps> = () => {
  const [status, setStatus] = useState<MultipathStatus>(defaultStatus)
  const [isSwitching, setIsSwitching] = useState(false)
  const [errorMsg, setErrorMsg] = useState<string | null>(null)

  const fetchStatus = async () => {
    if (window.dualflowAPI?.multipath) {
      try {
        const s = await window.dualflowAPI.multipath.getStatus()
        if (s) setStatus(s)
      } catch (err: any) {
        // Silently retain last known status or log
      }
    }
  }

  useEffect(() => {
    fetchStatus()

    if (window.dualflowAPI?.multipath?.onStatsUpdated) {
      const unsub = window.dualflowAPI.multipath.onStatsUpdated((fresh) => {
        if (fresh) setStatus(fresh)
      })
      return unsub
    }
  }, [])

  const handleModeChange = async (mode: 'round_robin' | 'weighted_rr' | 'lowest_latency' | 'redundant') => {
    if (!window.dualflowAPI?.multipath) return
    setIsSwitching(true)
    setErrorMsg(null)
    try {
      const updated = await window.dualflowAPI.multipath.setMode(mode)
      if (updated) setStatus(updated)
    } catch (err: any) {
      setErrorMsg(err.message || 'Failed to switch scheduler mode')
    } finally {
      setIsSwitching(false)
    }
  }

  const formatBytes = (bytes: number) => {
    if (!bytes || bytes === 0) return '0 B'
    const k = 1024
    const sizes = ['B', 'KB', 'MB', 'GB']
    const i = Math.floor(Math.log(bytes) / Math.log(k))
    return `${(bytes / Math.pow(k, i)).toFixed(1)} ${sizes[i]}`
  }

  const paths = status.paths || []
  const stats = status.stats || defaultStatus.stats

  return (
    <div className="bg-[#1B1E22] border border-[#2C3238] rounded-[2px] p-3 text-xs font-mono">
      {/* Header */}
      <div className="flex items-center justify-between border-b border-[#2C3238] pb-2 mb-3">
        <div className="flex items-center gap-2">
          <ArrowRightLeft className="w-4 h-4 text-[#D99B32]" strokeWidth={1.5} />
          <span className="font-semibold text-xs text-[#E2E5E9] tracking-wide">
            MULTI-PATH BONDING & PACKET SCHEDULER
          </span>
          <span className="text-[10px] text-[#5E6670] bg-[#141618] border border-[#2C3238] px-1.5 py-0.5 rounded-[2px]">
            Phase 6 Core
          </span>
        </div>

        {/* Engine Status Badge */}
        <div className="flex items-center gap-2">
          <span className="text-[10px] text-[#5E6670] uppercase">Engine State:</span>
          <span
            className={`px-2 py-0.5 rounded-[2px] text-[10px] font-semibold border flex items-center gap-1.5 ${
              status.state === 'RUNNING'
                ? 'bg-[#1E2721] border-[#4EA869]/50 text-[#4EA869]'
                : status.state === 'DEGRADED'
                ? 'bg-[#23251D] border-[#D99B32]/50 text-[#D99B32]'
                : status.state === 'ERROR'
                ? 'bg-[#2A1D1D] border-[#C45347]/50 text-[#C45347]'
                : 'bg-[#141618] border-[#2C3238] text-[#5E6670]'
            }`}
          >
            {status.state === 'RUNNING' && <CheckCircle2 className="w-3 h-3 text-[#4EA869]" />}
            {status.state === 'DEGRADED' && <AlertTriangle className="w-3 h-3 text-[#D99B32]" />}
            {status.state === 'ERROR' && <XCircle className="w-3 h-3 text-[#C45347]" />}
            {status.state}
          </span>
        </div>
      </div>

      {/* Scheduler Mode Selector */}
      <div className="bg-[#141618] border border-[#2C3238] p-2.5 rounded-[2px] mb-3">
        <div className="flex items-center justify-between mb-2">
          <span className="text-[10px] text-[#5E6670] uppercase tracking-wider flex items-center gap-1.5">
            <Shuffle className="w-3.5 h-3.5 text-[#9098A2]" />
            Scheduling Algorithm
          </span>
          <span className="text-[10px] text-[#5E6670]">
            Dynamic zero-downtime runtime switching
          </span>
        </div>

        <div className="grid grid-cols-4 gap-2">
          {/* Round-Robin (Default) */}
          <button
            onClick={() => handleModeChange('round_robin')}
            disabled={isSwitching}
            className={`p-2 rounded-[2px] border text-left transition-colors ${
              status.scheduler_mode === 'round_robin'
                ? 'bg-[#1E2721] border-[#4EA869] text-[#E2E5E9]'
                : 'bg-[#1B1E22] border-[#2C3238] text-[#9098A2] hover:border-[#5E6670]'
            }`}
          >
            <div className="flex items-center justify-between">
              <span className="text-[11px] font-semibold">Round-Robin</span>
              {status.scheduler_mode === 'round_robin' && (
                <span className="w-1.5 h-1.5 rounded-full bg-[#4EA869]" />
              )}
            </div>
            <span className="text-[9px] text-[#5E6670] block mt-0.5">
              Deterministic 1:1 distribution
            </span>
          </button>

          {/* Weighted RR */}
          <button
            onClick={() => handleModeChange('weighted_rr')}
            disabled={isSwitching}
            className={`p-2 rounded-[2px] border text-left transition-colors ${
              status.scheduler_mode === 'weighted_rr'
                ? 'bg-[#1E2721] border-[#4EA869] text-[#E2E5E9]'
                : 'bg-[#1B1E22] border-[#2C3238] text-[#9098A2] hover:border-[#5E6670]'
            }`}
          >
            <div className="flex items-center justify-between">
              <span className="text-[11px] font-semibold">Weighted RR</span>
              {status.scheduler_mode === 'weighted_rr' && (
                <span className="w-1.5 h-1.5 rounded-full bg-[#4EA869]" />
              )}
            </div>
            <span className="text-[9px] text-[#5E6670] block mt-0.5">
              Proportional to link weights
            </span>
          </button>

          {/* Lowest Latency */}
          <button
            onClick={() => handleModeChange('lowest_latency')}
            disabled={isSwitching}
            className={`p-2 rounded-[2px] border text-left transition-colors ${
              status.scheduler_mode === 'lowest_latency'
                ? 'bg-[#1E2721] border-[#4EA869] text-[#E2E5E9]'
                : 'bg-[#1B1E22] border-[#2C3238] text-[#9098A2] hover:border-[#5E6670]'
            }`}
          >
            <div className="flex items-center justify-between">
              <span className="text-[11px] font-semibold">Lowest Latency</span>
              {status.scheduler_mode === 'lowest_latency' && (
                <span className="w-1.5 h-1.5 rounded-full bg-[#4EA869]" />
              )}
            </div>
            <span className="text-[9px] text-[#5E6670] block mt-0.5">
              Live RTT + 10ms hysteresis
            </span>
          </button>

          {/* Redundant */}
          <button
            onClick={() => handleModeChange('redundant')}
            disabled={isSwitching}
            className={`p-2 rounded-[2px] border text-left transition-colors ${
              status.scheduler_mode === 'redundant'
                ? 'bg-[#23251D] border-[#D99B32] text-[#E2E5E9]'
                : 'bg-[#1B1E22] border-[#2C3238] text-[#9098A2] hover:border-[#5E6670]'
            }`}
          >
            <div className="flex items-center justify-between">
              <span className="text-[11px] font-semibold">Redundant</span>
              {status.scheduler_mode === 'redundant' && (
                <span className="w-1.5 h-1.5 rounded-full bg-[#D99B32]" />
              )}
            </div>
            <span className="text-[9px] text-[#5E6670] block mt-0.5">
              Dual-path broadcast (Loss reduction)
            </span>
          </button>
        </div>

        {errorMsg && (
          <div className="mt-2 text-[10px] text-[#C45347] flex items-center gap-1">
            <AlertTriangle className="w-3 h-3" />
            {errorMsg}
          </div>
        )}
      </div>

      {/* Real Network Paths Cards Grid */}
      <div className="grid grid-cols-2 gap-3 mb-3">
        {paths.length === 0 ? (
          <div className="col-span-2 bg-[#141618] border border-[#2C3238] p-3 rounded-[2px] text-center text-[#5E6670]">
            No physical paths registered yet. Connect network adapters to initialize paths.
          </div>
        ) : (
          paths.map((p: MultipathPathSnapshot) => (
            <div
              key={p.id}
              className="bg-[#141618] border border-[#2C3238] p-2.5 rounded-[2px]"
            >
              <div className="flex items-center justify-between border-b border-[#2C3238] pb-1.5 mb-2">
                <div>
                  <span className="text-[11px] font-semibold text-[#E2E5E9] block">
                    {p.name.toUpperCase()}
                  </span>
                  <span className="text-[9px] text-[#5E6670]">
                    {p.description || p.iface_name} (ifIndex: {p.iface_index})
                  </span>
                </div>

                <span
                  className={`px-1.5 py-0.5 rounded-[2px] text-[9px] font-semibold border ${
                    p.state === 'AVAILABLE'
                      ? 'bg-[#1E2721] border-[#4EA869]/50 text-[#4EA869]'
                      : p.state === 'DEGRADED'
                      ? 'bg-[#23251D] border-[#D99B32]/50 text-[#D99B32]'
                      : 'bg-[#1B1E22] border-[#2C3238] text-[#5E6670]'
                  }`}
                >
                  {p.state}
                </span>
              </div>

              {/* Path Properties Grid */}
              <div className="grid grid-cols-4 gap-2 mb-2 text-[10px]">
                <div>
                  <span className="text-[#5E6670] block text-[9px]">SOURCE IP</span>
                  <span className="text-[#E2E5E9] font-medium">{p.local_ip || '—'}</span>
                </div>
                <div>
                  <span className="text-[#5E6670] block text-[9px]">LIVE RTT</span>
                  <span className="text-[#E2E5E9] font-medium tabular-num">
                    {p.rtt_ms > 0 ? `${p.rtt_ms.toFixed(1)} ms` : '—'}
                  </span>
                </div>
                <div>
                  <span className="text-[#5E6670] block text-[9px]">RTT JITTER (EWMA)</span>
                  <span className="text-[#E2E5E9] font-medium tabular-num">
                    {p.rtt_jitter_ms > 0 ? `±${p.rtt_jitter_ms.toFixed(1)} ms` : '—'}
                  </span>
                </div>
                <div>
                  <span className="text-[#5E6670] block text-[9px]">LOSS ESTIMATE</span>
                  <span className={`font-medium tabular-num ${p.packet_loss > 0 ? 'text-[#C45347]' : 'text-[#4EA869]'}`}>
                    {p.rtt_ms > 0 ? `${p.packet_loss.toFixed(1)}%` : '—'}
                  </span>
                </div>
              </div>

              {/* Path Traffic Breakdown */}
              <div className="flex items-center justify-between text-[10px] bg-[#1B1E22] px-2 py-1 rounded-[2px] border border-[#2C3238]/60 text-[#9098A2]">
                <span>
                  TX: <strong className="text-[#E2E5E9]">{p.tx_packets.toLocaleString()}</strong> pkts ({formatBytes(p.tx_bytes)})
                </span>
                <span>
                  RX: <strong className="text-[#E2E5E9]">{p.rx_packets.toLocaleString()}</strong> pkts ({formatBytes(p.rx_bytes)})
                </span>
                <span>
                  Drops: <strong className={p.drops > 0 ? 'text-[#C45347]' : 'text-[#5E6670]'}>{p.drops}</strong>
                </span>
              </div>
            </div>
          ))
        )}
      </div>

      {/* Reordering Buffer & Loss Mitigation Metrics */}
      <div className="grid grid-cols-4 gap-2 mb-3">
        <div className="bg-[#141618] border border-[#2C3238] p-2 rounded-[2px]">
          <span className="text-[9px] text-[#5E6670] uppercase block">Logical Packets</span>
          <span className="text-sm font-semibold text-[#E2E5E9] tabular-num block mt-0.5">
            {stats.total_logical_packets.toLocaleString()}
          </span>
          <span className="text-[9px] text-[#5E6670]">Allocated sequence IDs</span>
        </div>

        <div className="bg-[#141618] border border-[#2C3238] p-2 rounded-[2px]">
          <span className="text-[9px] text-[#5E6670] uppercase block">Deduplication Drops</span>
          <span className="text-sm font-semibold text-[#D99B32] tabular-num block mt-0.5">
            {stats.duplicates_dropped.toLocaleString()}
          </span>
          <span className="text-[9px] text-[#5E6670]">Suppressed duplicate frames</span>
        </div>

        <div className="bg-[#141618] border border-[#2C3238] p-2 rounded-[2px]">
          <span className="text-[9px] text-[#5E6670] uppercase block">Reordered Delivered</span>
          <span className="text-sm font-semibold text-[#4EA869] tabular-num block mt-0.5">
            {stats.reordered_delivered.toLocaleString()}
          </span>
          <span className="text-[9px] text-[#5E6670]">Out-of-order reassembled</span>
        </div>

        <div className="bg-[#141618] border border-[#2C3238] p-2 rounded-[2px]">
          <span className="text-[9px] text-[#5E6670] uppercase block">Reorder Queue Depth</span>
          <span className="text-sm font-semibold text-[#E2E5E9] tabular-num block mt-0.5">
            {status.reorder_queue_depth} / 256
          </span>
          <span className="text-[9px] text-[#5E6670]">
            Timeouts: {status.reorder_timed_out}
          </span>
        </div>
      </div>

      {/* Technical Reality Footer */}
      <div className="flex items-center justify-between text-[10px] text-[#5E6670] pt-2 border-t border-[#2C3238]">
        <div className="flex items-center gap-1.5">
          <Clock className="w-3.5 h-3.5 text-[#5E6670]" />
          <span>Prober: 1000ms heartbeat | Winsock IP_UNICAST_IF Pinned | DFTP v1 Framing</span>
        </div>
        <span>Safe Mode: Host Route 0.0.0.0 & DNS 100% Preserved</span>
      </div>
    </div>
  )
}
