import { useState, useEffect, type FC } from 'react'
import {
  Layers,
  ShieldAlert,
  Play,
  Square,
  PlusCircle,
  Trash2,
  Send,
  AlertCircle,
  CheckCircle2,
} from 'lucide-react'
import type { WintunStatus, WintunStats } from '../types'

export const WintunCard: FC = () => {
  const [status, setStatus] = useState<WintunStatus>({
    driver_status: 'AVAILABLE',
    driver_version: '0.14.1',
    driver_path: 'bin/wintun.dll',
    is_elevated: false,
    adapter_exists: false,
    adapter_name: 'DualFlow Tunnel',
    adapter_status: 'ADAPTER NOT CREATED',
    session_status: 'SESSION STOPPED',
    rx_packets: 0,
    tx_packets: 0,
    rx_bytes: 0,
    tx_bytes: 0,
    uptime_ms: 0,
  })

  const [stats, setStats] = useState<WintunStats>({
    rx_packets: 0,
    tx_packets: 0,
    rx_bytes: 0,
    tx_bytes: 0,
    dropped_packets: 0,
  })

  const [isLoading, setIsLoading] = useState(false)
  const [actionError, setActionError] = useState<string | null>(null)
  const [showDeleteModal, setShowDeleteModal] = useState(false)
  const [testResult, setTestResult] = useState<string | null>(null)

  const fetchStatus = async () => {
    if (window.dualflowAPI?.wintun) {
      try {
        const s = await window.dualflowAPI.wintun.getStatus()
        setStatus(s)
        if (s.last_error) {
          setActionError(s.last_error)
        }
      } catch (err: any) {
        setActionError(err.message || String(err))
      }
    }
  }

  useEffect(() => {
    fetchStatus()

    if (window.dualflowAPI?.wintun?.onStatsUpdated) {
      const unsub = window.dualflowAPI.wintun.onStatsUpdated((freshStats) => {
        setStats(freshStats)
      })
      return unsub
    }
  }, [])

  const handleCreateAdapter = async () => {
    setIsLoading(true)
    setActionError(null)
    setTestResult(null)
    try {
      if (window.dualflowAPI?.wintun) {
        const res = await window.dualflowAPI.wintun.createAdapter()
        setStatus(res)
      }
    } catch (err: any) {
      setActionError(err.message || String(err))
    } finally {
      setIsLoading(false)
      fetchStatus()
    }
  }

  const handleStartSession = async () => {
    setIsLoading(true)
    setActionError(null)
    setTestResult(null)
    try {
      if (window.dualflowAPI?.wintun) {
        const res = await window.dualflowAPI.wintun.startSession(0x400000)
        setStatus(res)
      }
    } catch (err: any) {
      setActionError(err.message || String(err))
    } finally {
      setIsLoading(false)
      fetchStatus()
    }
  }

  const handleStopSession = async () => {
    setIsLoading(true)
    setActionError(null)
    setTestResult(null)
    try {
      if (window.dualflowAPI?.wintun) {
        const res = await window.dualflowAPI.wintun.stopSession()
        setStatus(res)
      }
    } catch (err: any) {
      setActionError(err.message || String(err))
    } finally {
      setIsLoading(false)
      fetchStatus()
    }
  }

  const handleDeleteAdapter = async () => {
    setShowDeleteModal(false)
    setIsLoading(true)
    setActionError(null)
    setTestResult(null)
    try {
      if (window.dualflowAPI?.wintun) {
        const res = await window.dualflowAPI.wintun.deleteAdapter(true)
        setStatus(res)
      }
    } catch (err: any) {
      setActionError(err.message || String(err))
    } finally {
      setIsLoading(false)
      fetchStatus()
    }
  }

  const handleSendTestPacket = async () => {
    setIsLoading(true)
    setActionError(null)
    setTestResult(null)
    try {
      if (window.dualflowAPI?.wintun) {
        const res = await window.dualflowAPI.wintun.sendTestPacket(64)
        setTestResult(`Injected test IPv4 ICMP packet (${res.packet_size} bytes). Total TX: ${res.tx_packets}`)
      }
    } catch (err: any) {
      setActionError(err.message || String(err))
    } finally {
      setIsLoading(false)
      fetchStatus()
    }
  }

  const isSessionRunning = status.session_status === 'SESSION RUNNING'
  const isAdapterReady = status.adapter_status === 'ADAPTER READY' || status.adapter_exists

  return (
    <div className="bg-[#1B1E22] border border-[#2C3238] rounded-[2px] p-3 text-xs font-mono relative">
      {/* Header */}
      <div className="flex items-center justify-between border-b border-[#2C3238] pb-2 mb-3">
        <div className="flex items-center gap-2">
          <Layers className="w-4 h-4 text-[#38BDF8]" strokeWidth={1.5} />
          <span className="font-semibold text-xs text-[#E2E5E9] tracking-wide">
            WINTUN VIRTUAL NETWORK ADAPTER (TUN NEL ENGINE)
          </span>
        </div>

        <div className="flex items-center gap-2">
          {/* Elevation Badge */}
          <span
            className={`px-2 py-0.5 rounded-[2px] text-[10px] font-semibold border flex items-center gap-1 ${
              status.is_elevated
                ? 'bg-[#1E2721] border-[#4EA869]/50 text-[#4EA869]'
                : 'bg-[#2A1E1E] border-[#C45347]/50 text-[#C45347]'
            }`}
          >
            {status.is_elevated ? (
              <>
                <CheckCircle2 className="w-3 h-3" />
                <span>ELEVATED (ADMIN)</span>
              </>
            ) : (
              <>
                <ShieldAlert className="w-3 h-3" />
                <span>NON-ELEVATED (USER)</span>
              </>
            )}
          </span>

          {/* Session Status Badge */}
          <span
            className={`px-2 py-0.5 rounded-[2px] text-[10px] font-semibold border ${
              isSessionRunning
                ? 'bg-[#1E2721] border-[#4EA869]/50 text-[#4EA869]'
                : isAdapterReady
                ? 'bg-[#1B2836] border-[#38BDF8]/50 text-[#38BDF8]'
                : 'bg-[#141618] border-[#2C3238] text-[#5E6670]'
            }`}
          >
            {status.session_status}
          </span>
        </div>
      </div>

      {/* Grid: 4 Diagnostic Panels */}
      <div className="grid grid-cols-4 gap-3 mb-3">
        {/* 1. Driver Runtime */}
        <div className="bg-[#141618] p-2.5 rounded-[2px] border border-[#2C3238] space-y-1">
          <span className="text-[10px] text-[#5E6670] uppercase block">Wintun Runtime DLL</span>
          <div className="flex items-center gap-1.5">
            <span
              className={`w-2 h-2 rounded-full ${
                status.driver_status === 'AVAILABLE' ? 'bg-[#4EA869]' : 'bg-[#C45347]'
              }`}
            />
            <span className="text-xs font-semibold text-[#E2E5E9]">{status.driver_status}</span>
          </div>
          <span className="text-[10px] text-[#9098A2] block truncate" title={status.driver_path}>
            Version: {status.driver_version}
          </span>
        </div>

        {/* 2. Virtual Adapter State */}
        <div className="bg-[#141618] p-2.5 rounded-[2px] border border-[#2C3238] space-y-1">
          <span className="text-[10px] text-[#5E6670] uppercase block">Virtual Interface</span>
          <span className="text-xs font-semibold text-[#E2E5E9] block truncate">
            {status.adapter_name}
          </span>
          <span
            className={`text-[10px] font-medium block ${
              isAdapterReady ? 'text-[#4EA869]' : 'text-[#D99B32]'
            }`}
          >
            {status.adapter_status}
          </span>
        </div>

        {/* 3. Received Packets (RX) */}
        <div className="bg-[#141618] p-2.5 rounded-[2px] border border-[#2C3238] space-y-1">
          <span className="text-[10px] text-[#5E6670] uppercase block">Kernel RX Buffer</span>
          <div className="flex items-baseline gap-2">
            <span className="text-sm font-bold text-[#E2E5E9]">
              {(stats.rx_packets || status.rx_packets).toLocaleString()}
            </span>
            <span className="text-[10px] text-[#5E6670]">pkts</span>
          </div>
          <span className="text-[10px] text-[#9098A2] block">
            {((stats.rx_bytes || status.rx_bytes) / 1024).toFixed(1)} KB received
          </span>
        </div>

        {/* 4. Transmitted Packets (TX) */}
        <div className="bg-[#141618] p-2.5 rounded-[2px] border border-[#2C3238] space-y-1">
          <span className="text-[10px] text-[#5E6670] uppercase block">Kernel TX Buffer</span>
          <div className="flex items-baseline gap-2">
            <span className="text-sm font-bold text-[#E2E5E9]">
              {(stats.tx_packets || status.tx_packets).toLocaleString()}
            </span>
            <span className="text-[10px] text-[#5E6670]">pkts</span>
          </div>
          <span className="text-[10px] text-[#9098A2] block">
            {((stats.tx_bytes || status.tx_bytes) / 1024).toFixed(1)} KB injected
          </span>
        </div>
      </div>

      {/* Privilege Warning Banner when Non-Elevated */}
      {!status.is_elevated && (
        <div className="bg-[#241A1A] border border-[#C45347]/40 rounded-[2px] px-3 py-1.5 mb-3 flex items-center justify-between text-[11px]">
          <div className="flex items-center gap-2 text-[#C45347]">
            <AlertCircle className="w-3.5 h-3.5 shrink-0" />
            <span>
              Process running without Administrator privileges. Adapter creation requires elevated token.
            </span>
          </div>
          <span className="text-[10px] text-[#9098A2]">Win32: ERROR_ACCESS_DENIED (5)</span>
        </div>
      )}

      {/* Error or Success feedback */}
      {actionError && (
        <div className="bg-[#241A1A] border border-[#C45347]/40 rounded-[2px] px-3 py-1.5 mb-3 text-[11px] text-[#C45347] flex items-center gap-2">
          <AlertCircle className="w-3.5 h-3.5 shrink-0" />
          <span className="truncate">{actionError}</span>
        </div>
      )}

      {testResult && (
        <div className="bg-[#1B281F] border border-[#4EA869]/40 rounded-[2px] px-3 py-1.5 mb-3 text-[11px] text-[#4EA869] flex items-center gap-2">
          <CheckCircle2 className="w-3.5 h-3.5 shrink-0" />
          <span>{testResult}</span>
        </div>
      )}

      {/* Control Actions Toolbar */}
      <div className="flex items-center justify-between pt-1 border-t border-[#2C3238]">
        <div className="flex items-center gap-2">
          {/* Create Adapter */}
          <button
            onClick={handleCreateAdapter}
            disabled={isLoading || isAdapterReady}
            className="h-6 px-2.5 flex items-center gap-1.5 rounded-[2px] bg-[#24282E] hover:bg-[#2B3037] border border-[#2C3238] text-[11px] text-[#E2E5E9] disabled:opacity-40 transition-colors"
            title="Create 'DualFlow Tunnel' adapter via WintunCreateAdapter (requires Administrator)"
          >
            <PlusCircle className="w-3 h-3 text-[#38BDF8]" />
            <span>Create Adapter</span>
          </button>

          {/* Start Session */}
          <button
            onClick={handleStartSession}
            disabled={isLoading || isSessionRunning}
            className="h-6 px-2.5 flex items-center gap-1.5 rounded-[2px] bg-[#1E2721] hover:bg-[#25362B] border border-[#4EA869]/50 text-[11px] text-[#4EA869] disabled:opacity-40 transition-colors"
            title="Open Wintun packet processing session (4MB ring capacity)"
          >
            <Play className="w-3 h-3" fill="currentColor" />
            <span>Start Session</span>
          </button>

          {/* Stop Session */}
          <button
            onClick={handleStopSession}
            disabled={isLoading || !isSessionRunning}
            className="h-6 px-2.5 flex items-center gap-1.5 rounded-[2px] bg-[#2A1E1E] hover:bg-[#382424] border border-[#C45347]/50 text-[11px] text-[#C45347] disabled:opacity-40 transition-colors"
            title="End active packet processing session"
          >
            <Square className="w-3 h-3" fill="currentColor" />
            <span>Stop Session</span>
          </button>

          {/* Send Controlled Test Packet */}
          <button
            onClick={handleSendTestPacket}
            disabled={isLoading || !isSessionRunning}
            className="h-6 px-2.5 flex items-center gap-1.5 rounded-[2px] bg-[#24282E] hover:bg-[#2B3037] border border-[#2C3238] text-[11px] text-[#D99B32] disabled:opacity-40 transition-colors"
            title="Inject an isolated IPv4 ICMP test packet into the Wintun tunnel buffer"
          >
            <Send className="w-3 h-3" />
            <span>Send Test Packet</span>
          </button>
        </div>

        {/* Delete Adapter (Destructive with Confirmation Modal) */}
        <div>
          <button
            onClick={() => setShowDeleteModal(true)}
            disabled={isLoading || !isAdapterReady}
            className="h-6 px-2.5 flex items-center gap-1.5 rounded-[2px] bg-[#24282E] hover:bg-[#2A1E1E] border border-[#2C3238] hover:border-[#C45347]/50 text-[11px] text-[#9098A2] hover:text-[#C45347] disabled:opacity-40 transition-colors"
            title="Uninstall Wintun virtual adapter from Windows (requires confirmation)"
          >
            <Trash2 className="w-3 h-3" />
            <span>Delete Adapter...</span>
          </button>
        </div>
      </div>

      {/* Confirmation Modal for Delete Adapter */}
      {showDeleteModal && (
        <div className="absolute inset-0 bg-[#141618]/90 backdrop-blur-sm z-50 flex items-center justify-center p-4">
          <div className="bg-[#1B1E22] border border-[#C45347]/60 rounded-[2px] p-4 max-w-sm w-full space-y-3 shadow-2xl">
            <div className="flex items-center gap-2 text-[#C45347]">
              <AlertCircle className="w-4 h-4" />
              <span className="font-bold text-xs">CONFIRM ADAPTER DELETION</span>
            </div>
            <p className="text-[11px] text-[#9098A2] leading-relaxed">
              Are you sure you want to delete the Wintun virtual adapter{' '}
              <strong className="text-[#E2E5E9] font-mono">DualFlow Tunnel</strong> from the
              Windows operating system? This will unregister the device instance.
            </p>
            <div className="flex items-center justify-end gap-2 pt-2">
              <button
                onClick={() => setShowDeleteModal(false)}
                className="h-6 px-3 rounded-[2px] bg-[#24282E] hover:bg-[#2B3037] text-[11px] text-[#E2E5E9] border border-[#2C3238]"
              >
                Cancel
              </button>
              <button
                onClick={handleDeleteAdapter}
                className="h-6 px-3 rounded-[2px] bg-[#C45347] hover:bg-[#D96155] text-[11px] text-[#141618] font-bold"
              >
                Confirm Delete
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}
