import type { FC } from 'react'
import { Play, Square, RefreshCw, Layers } from 'lucide-react'
import type { ActiveProfile, BondingCoreStatus } from '../types'

interface TopStatusBarProps {
  coreStatus: BondingCoreStatus
  onToggleCore: () => void
  onSelectProfile: (profile: ActiveProfile) => void
  onRescanAdapters: () => void
  isScanning: boolean
}

export const TopStatusBar: FC<TopStatusBarProps> = ({
  coreStatus,
  onToggleCore,
  onSelectProfile,
  onRescanAdapters,
  isScanning,
}) => {
  const profiles: { id: ActiveProfile; label: string; desc: string }[] = [
    { id: 'gaming', label: 'Gaming', desc: 'Lowest jitter, redundancy standby' },
    { id: 'balanced', label: 'Balanced', desc: 'Stability first, per-flow distribution' },
    { id: 'download', label: 'Download', desc: 'Maximum throughput via bonding tunnel' },
  ]

  return (
    <div className="h-11 px-4 bg-[#1B1E22] border-b border-[#2C3238] flex items-center justify-between select-none">
      {/* Left: Core Start/Stop Control & Status Message */}
      <div className="flex items-center gap-3">
        <button
          onClick={onToggleCore}
          className={`h-7 px-3 flex items-center gap-2 rounded-[2px] text-xs font-medium border transition-colors duration-100 ${
            coreStatus.isRunning
              ? 'bg-[#2A1E1E] border-[#C45347]/50 text-[#E2E5E9] hover:bg-[#382424]'
              : 'bg-[#1E2721] border-[#4EA869]/50 text-[#E2E5E9] hover:bg-[#25362B]'
          }`}
        >
          {coreStatus.isRunning ? (
            <>
              <Square className="w-3 h-3 text-[#C45347]" fill="currentColor" />
              <span>Stop Core Service</span>
            </>
          ) : (
            <>
              <Play className="w-3 h-3 text-[#4EA869]" fill="currentColor" />
              <span>Start Core Service</span>
            </>
          )}
        </button>

        <div className="h-4 w-[1px] bg-[#2C3238]" />

        <button
          onClick={onRescanAdapters}
          disabled={isScanning}
          className="h-7 px-2.5 flex items-center gap-1.5 rounded-[2px] bg-[#24282E] hover:bg-[#2B3037] border border-[#2C3238] text-xs text-[#9098A2] hover:text-[#E2E5E9] transition-colors duration-100 disabled:opacity-50"
          title="Rescan Windows network adapters via IP Helper API"
        >
          <RefreshCw className={`w-3.5 h-3.5 ${isScanning ? 'animate-spin text-[#D99B32]' : ''}`} strokeWidth={1.5} />
          <span>Rescan Adapters</span>
        </button>

        <span className="text-xs text-[#5E6670] font-mono pl-1">
          {isScanning ? 'Querying GetAdaptersAddresses...' : 'Interfaces enumerated'}
        </span>
      </div>

      {/* Right: Profile Segmented Control */}
      <div className="flex items-center gap-2">
        <span className="text-[11px] text-[#5E6670] font-mono uppercase tracking-wider flex items-center gap-1">
          <Layers className="w-3 h-3 text-[#9098A2]" strokeWidth={1.5} />
          Profile:
        </span>

        <div className="flex p-0.5 bg-[#141618] border border-[#2C3238] rounded-[3px]">
          {profiles.map((p) => {
            const isSelected = coreStatus.profile === p.id
            return (
              <button
                key={p.id}
                onClick={() => onSelectProfile(p.id)}
                className={`h-6 px-2.5 rounded-[2px] text-xs font-mono transition-colors duration-100 relative ${
                  isSelected
                    ? 'bg-[#24282E] text-[#D99B32] font-medium border border-[#3D444D]'
                    : 'text-[#9098A2] hover:text-[#E2E5E9] hover:bg-[#1B1E22]/60'
                }`}
                title={p.desc}
              >
                {p.label}
              </button>
            )
          })}
        </div>
      </div>
    </div>
  )
}
