import { useState, useEffect, type FC } from 'react'
import {
  Boxes,
  Activity,
  Send,
  RotateCcw,
  AlertTriangle,
  CheckCircle2,
  XCircle,
  HelpCircle,
  Radio,
  FileCheck,
} from 'lucide-react'
import type { PacketStatus, PacketStats, TransportStats } from '../types'

const defaultStats: PacketStats = {
  validated_packets: 0,
  rejected_packets: 0,
  queued_packets: 0,
  processed_bytes: 0,
  total_drops: 0,
  drops_invalid_packet: 0,
  drops_queue_full: 0,
  drops_transport_unavailable: 0,
  drops_transport_write_failed: 0,
  drops_wintun_send_failed: 0,
  drops_session_closed: 0,
}

const defaultTransport: TransportStats = {
  state: 'CONNECTED',
  is_test: true,
  tx_packets: 0,
  rx_packets: 0,
  tx_bytes: 0,
  rx_bytes: 0,
  dropped_packets: 0,
}

export const PacketEngineCard: FC = () => {
  const [status, setStatus] = useState<PacketStatus>({
    is_running: true,
    is_test_mode: true,
    queue_cap: 1024,
    queue_depth: 0,
    timestamp: Date.now(),
    stats: {
      validated_packets: 0,
      rejected_packets: 0,
      queued_packets: 0,
      processed_bytes: 0,
      total_drops: 0,
      drops_invalid_packet: 0,
      drops_queue_full: 0,
      drops_transport_unavailable: 0,
      drops_transport_write_failed: 0,
      drops_wintun_send_failed: 0,
      drops_session_closed: 0,
    },
    transport: {
      state: 'CONNECTED',
      is_test: true,
      tx_packets: 0,
      rx_packets: 0,
      tx_bytes: 0,
      rx_bytes: 0,
      dropped_packets: 0,
    },
  })

  const [isLoading, setIsLoading] = useState(false)
  const [actionError, setActionError] = useState<string | null>(null)
  const [actionSuccess, setActionSuccess] = useState<string | null>(null)

  const fetchStatus = async () => {
    if (window.dualflowAPI?.packet) {
      try {
        const s = await window.dualflowAPI.packet.getStatus()
        setStatus(s)
      } catch (err: any) {
        setActionError(err.message || String(err))
      }
    }
  }

  useEffect(() => {
    fetchStatus()

    if (window.dualflowAPI?.packet?.onStatsUpdated) {
      const unsub = window.dualflowAPI.packet.onStatsUpdated((freshData) => {
        setStatus((prev) => ({
          ...prev,
          stats: freshData.pipeline || prev.stats,
          transport: freshData.transport || prev.transport,
        }))
      })
      return unsub
    }
  }, [])

  const handleInject = async (version: 4 | 6, corrupt = false) => {
    setIsLoading(true)
    setActionError(null)
    setActionSuccess(null)
    try {
      if (window.dualflowAPI?.packet) {
        const res = await window.dualflowAPI.packet.injectTestPacket({
          ipVersion: version,
          payloadSize: 48,
          corrupt,
        })
        if (res.injected) {
          setActionSuccess(
            `Injected valid IPv${version} packet (${res.total_length} bytes, Proto: ${res.protocol})`
          )
        } else {
          setActionError(`Packet rejected as expected: ${res.error}`)
        }
        if (res.stats) {
          setStatus((prev) => ({
            ...prev,
            stats: res.stats,
            transport: res.transport || prev.transport,
          }))
        }
      }
    } catch (err: any) {
      setActionError(err.message || String(err))
    } finally {
      setIsLoading(false)
      fetchStatus()
    }
  }

  const handleToggleTestMode = async () => {
    setIsLoading(true)
    setActionError(null)
    setActionSuccess(null)
    try {
      if (window.dualflowAPI?.packet) {
        const newMode = !status.is_test_mode
        const updated = await window.dualflowAPI.packet.setTestMode(newMode)
        setStatus(updated)
        setActionSuccess(`Test mode ${newMode ? 'ENABLED (Safe In-Memory)' : 'DISABLED'}`)
      }
    } catch (err: any) {
      setActionError(err.message || String(err))
    } finally {
      setIsLoading(false)
    }
  }

  const handleReset = async () => {
    setIsLoading(true)
    setActionError(null)
    setActionSuccess(null)
    try {
      if (window.dualflowAPI?.packet) {
        await window.dualflowAPI.packet.resetStats()
        setActionSuccess('Packet engine and transport counters reset to zero')
        fetchStatus()
      }
    } catch (err: any) {
      setActionError(err.message || String(err))
    } finally {
      setIsLoading(false)
    }
  }

  const formatBytes = (bytes: number): string => {
    if (!bytes || bytes === 0) return '0 B'
    const k = 1024
    const sizes = ['B', 'KB', 'MB', 'GB']
    const i = Math.floor(Math.log(bytes) / Math.log(k))
    return `${(bytes / Math.pow(k, i)).toFixed(1)} ${sizes[i]}`
  }

  const stats: PacketStats = status.stats || defaultStats
  const trans: TransportStats = status.transport || defaultTransport

  return (
    <div className="bg-[#1B1E22] border border-[#2C3238] rounded-md p-4 text-[#D8DEE9]">
      {/* Header */}
      <div className="flex flex-wrap items-center justify-between gap-3 pb-3 border-b border-[#2C3238]">
        <div className="flex items-center gap-2.5">
          <Boxes className="w-5 h-5 text-indigo-400" />
          <div>
            <div className="flex items-center gap-2">
              <span className="font-semibold text-sm tracking-wide text-white uppercase">
                Packet Processing &amp; Tunnel Transport
              </span>
              <span className="px-1.5 py-0.5 text-[10px] font-mono font-medium rounded bg-indigo-500/10 text-indigo-400 border border-indigo-500/30">
                Phase 5 Core
              </span>
            </div>
            <p className="text-[11px] text-[#8892B0]">
              Strict IPv4/IPv6 RFC validation, bounded FIFO queueing, and versioned binary DFTP transport.
            </p>
          </div>
        </div>

        {/* Status Badges */}
        <div className="flex items-center gap-2">
          {status.is_test_mode ? (
            <span className="inline-flex items-center gap-1.5 px-2 py-0.5 text-[10px] font-mono font-semibold rounded bg-amber-500/10 text-amber-400 border border-amber-500/30">
              <Radio className="w-3 h-3 text-amber-400" />
              SAFE IN-MEMORY TEST TRANSPORT
            </span>
          ) : (
            <span className="inline-flex items-center gap-1.5 px-2 py-0.5 text-[10px] font-mono font-semibold rounded bg-emerald-500/10 text-emerald-400 border border-emerald-500/30">
              <Activity className="w-3 h-3 text-emerald-400" />
              LIVE PIPELINE
            </span>
          )}

          <span
            className={`inline-flex items-center gap-1 px-2 py-0.5 text-[10px] font-mono rounded border ${
              trans.state === 'CONNECTED'
                ? 'bg-emerald-500/10 text-emerald-400 border-emerald-500/30'
                : 'bg-zinc-800 text-zinc-400 border-zinc-700'
            }`}
          >
            TRANSPORT: {trans.state || 'DISCONNECTED'}
          </span>
        </div>
      </div>

      {/* Action Messages */}
      {actionSuccess && (
        <div className="mt-3 p-2 bg-emerald-950/40 border border-emerald-800/60 rounded text-[11px] text-emerald-300 flex items-center gap-2">
          <CheckCircle2 className="w-4 h-4 text-emerald-400 shrink-0" />
          <span>{actionSuccess}</span>
        </div>
      )}

      {actionError && (
        <div className="mt-3 p-2 bg-red-950/40 border border-red-800/60 rounded text-[11px] text-red-300 flex items-center gap-2">
          <AlertTriangle className="w-4 h-4 text-red-400 shrink-0" />
          <span>{actionError}</span>
        </div>
      )}

      {/* Metrics Grid */}
      <div className="grid grid-cols-2 sm:grid-cols-4 gap-3 mt-4">
        {/* Validated Packets */}
        <div className="bg-[#141618] border border-[#24292E] p-2.5 rounded">
          <div className="flex items-center justify-between text-[11px] text-[#8892B0]">
            <span>Validated Packets</span>
            <FileCheck className="w-3.5 h-3.5 text-emerald-400" />
          </div>
          <div className="mt-1 font-mono text-base font-bold text-white">
            {stats.validated_packets?.toLocaleString() ?? 0}
          </div>
          <div className="text-[10px] font-mono text-[#6A737D] mt-0.5">
            {formatBytes(stats.processed_bytes ?? 0)}
          </div>
        </div>

        {/* Rejected Packets */}
        <div className="bg-[#141618] border border-[#24292E] p-2.5 rounded">
          <div className="flex items-center justify-between text-[11px] text-[#8892B0]">
            <span>Validation Rejections</span>
            <XCircle className="w-3.5 h-3.5 text-rose-400" />
          </div>
          <div className="mt-1 font-mono text-base font-bold text-rose-400">
            {stats.rejected_packets?.toLocaleString() ?? 0}
          </div>
          <div className="text-[10px] font-mono text-[#6A737D] mt-0.5">
            Strict RFC checksum/bounds
          </div>
        </div>

        {/* Queue Buffer */}
        <div className="bg-[#141618] border border-[#24292E] p-2.5 rounded">
          <div className="flex items-center justify-between text-[11px] text-[#8892B0]">
            <span>Queue Depth / Cap</span>
            <Boxes className="w-3.5 h-3.5 text-sky-400" />
          </div>
          <div className="mt-1 font-mono text-base font-bold text-white">
            {status.queue_depth ?? 0}{' '}
            <span className="text-xs font-normal text-[#6A737D]">/ {status.queue_cap ?? 1024}</span>
          </div>
          <div className="text-[10px] font-mono text-[#6A737D] mt-0.5">
            FIFO bounded queue
          </div>
        </div>

        {/* Transport Telemetry */}
        <div className="bg-[#141618] border border-[#24292E] p-2.5 rounded">
          <div className="flex items-center justify-between text-[11px] text-[#8892B0]">
            <span>Transport Frames TX/RX</span>
            <Activity className="w-3.5 h-3.5 text-indigo-400" />
          </div>
          <div className="mt-1 font-mono text-base font-bold text-white">
            {trans.tx_packets?.toLocaleString() ?? 0}{' '}
            <span className="text-xs font-normal text-[#6A737D]">TX</span> /{' '}
            {trans.rx_packets?.toLocaleString() ?? 0}{' '}
            <span className="text-xs font-normal text-[#6A737D]">RX</span>
          </div>
          <div className="text-[10px] font-mono text-[#6A737D] mt-0.5">
            {formatBytes(trans.tx_bytes ?? 0)} TX
          </div>
        </div>
      </div>

      {/* Drops by Reason Breakdown */}
      <div className="mt-3 bg-[#141618] border border-[#24292E] p-3 rounded">
        <div className="flex items-center justify-between pb-2 border-b border-[#20252A]">
          <span className="text-[11px] font-semibold text-[#8892B0] uppercase tracking-wider">
            Packet Drop Accounting (Total: {stats.total_drops ?? 0})
          </span>
          {stats.last_drop_reason && (
            <span className="text-[10px] font-mono text-amber-400">
              Last Drop: {stats.last_drop_reason}
            </span>
          )}
        </div>

        <div className="grid grid-cols-3 sm:grid-cols-6 gap-2 mt-2 text-[11px] font-mono">
          <div className="p-1.5 rounded bg-[#1B1E22] border border-[#2C3238]">
            <div className="text-[9px] text-[#6A737D] uppercase">Invalid Pkt</div>
            <div className="font-bold text-white mt-0.5">{stats.drops_invalid_packet ?? 0}</div>
          </div>
          <div className="p-1.5 rounded bg-[#1B1E22] border border-[#2C3238]">
            <div className="text-[9px] text-[#6A737D] uppercase">Queue Full</div>
            <div className="font-bold text-white mt-0.5">{stats.drops_queue_full ?? 0}</div>
          </div>
          <div className="p-1.5 rounded bg-[#1B1E22] border border-[#2C3238]">
            <div className="text-[9px] text-[#6A737D] uppercase">Trans Unavail</div>
            <div className="font-bold text-white mt-0.5">{stats.drops_transport_unavailable ?? 0}</div>
          </div>
          <div className="p-1.5 rounded bg-[#1B1E22] border border-[#2C3238]">
            <div className="text-[9px] text-[#6A737D] uppercase">Write Failed</div>
            <div className="font-bold text-white mt-0.5">{stats.drops_transport_write_failed ?? 0}</div>
          </div>
          <div className="p-1.5 rounded bg-[#1B1E22] border border-[#2C3238]">
            <div className="text-[9px] text-[#6A737D] uppercase">Wintun Fail</div>
            <div className="font-bold text-white mt-0.5">{stats.drops_wintun_send_failed ?? 0}</div>
          </div>
          <div className="p-1.5 rounded bg-[#1B1E22] border border-[#2C3238]">
            <div className="text-[9px] text-[#6A737D] uppercase">Sess Closed</div>
            <div className="font-bold text-white mt-0.5">{stats.drops_session_closed ?? 0}</div>
          </div>
        </div>
      </div>

      {/* Protocol Framing Info & Safe Test Harness Actions */}
      <div className="mt-3 flex flex-wrap items-center justify-between gap-2 pt-2 border-t border-[#2C3238]">
        <div className="flex items-center gap-1.5 text-[11px] text-[#6A737D] font-mono">
          <HelpCircle className="w-3.5 h-3.5 text-[#8892B0]" />
          <span>DFTP Framing: 32-Byte Header | Magic: DFTP | CRC32-IEEE Checksum</span>
        </div>

        <div className="flex flex-wrap items-center gap-2">
          <button
            onClick={() => handleInject(4, false)}
            disabled={isLoading}
            className="inline-flex items-center gap-1.5 px-2.5 py-1 text-xs font-medium rounded bg-[#2C3238] hover:bg-[#343B43] text-white border border-[#3E4651] disabled:opacity-50 transition-colors"
          >
            <Send className="w-3 h-3 text-emerald-400" />
            Inject IPv4 Test
          </button>

          <button
            onClick={() => handleInject(6, false)}
            disabled={isLoading}
            className="inline-flex items-center gap-1.5 px-2.5 py-1 text-xs font-medium rounded bg-[#2C3238] hover:bg-[#343B43] text-white border border-[#3E4651] disabled:opacity-50 transition-colors"
          >
            <Send className="w-3 h-3 text-sky-400" />
            Inject IPv6 Test
          </button>

          <button
            onClick={() => handleInject(4, true)}
            disabled={isLoading}
            className="inline-flex items-center gap-1.5 px-2.5 py-1 text-xs font-medium rounded bg-[#2C3238] hover:bg-[#343B43] text-rose-300 border border-rose-900/60 disabled:opacity-50 transition-colors"
          >
            <AlertTriangle className="w-3 h-3 text-rose-400" />
            Inject Corrupt
          </button>

          <button
            onClick={handleToggleTestMode}
            disabled={isLoading}
            className="inline-flex items-center gap-1.5 px-2.5 py-1 text-xs font-medium rounded bg-[#2C3238] hover:bg-[#343B43] text-[#D8DEE9] border border-[#3E4651] disabled:opacity-50 transition-colors"
          >
            <Radio className="w-3 h-3 text-amber-400" />
            {status.is_test_mode ? 'Switch Live' : 'Switch Test'}
          </button>

          <button
            onClick={handleReset}
            disabled={isLoading}
            className="inline-flex items-center gap-1.5 px-2.5 py-1 text-xs font-medium rounded bg-[#2C3238] hover:bg-[#343B43] text-[#D8DEE9] border border-[#3E4651] disabled:opacity-50 transition-colors"
          >
            <RotateCcw className="w-3 h-3 text-zinc-400" />
            Reset
          </button>
        </div>
      </div>
    </div>
  )
}
