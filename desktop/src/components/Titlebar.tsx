import { useEffect, useState, type FC } from 'react'
import { Minus, Square, Copy, X, Radio, Server } from 'lucide-react'
import type { BondingCoreStatus } from '../types'

interface TitlebarProps {
  coreStatus: BondingCoreStatus
}

export const Titlebar: FC<TitlebarProps> = ({ coreStatus }) => {
  const [isMaximized, setIsMaximized] = useState(false)

  useEffect(() => {
    if (window.dualflowAPI) {
      window.dualflowAPI.window.isMaximized().then(setIsMaximized)
      const cleanup = window.dualflowAPI.window.onStateChange((state) => {
        setIsMaximized(state.isMaximized)
      })
      return cleanup
    }
  }, [])

  const handleMinimize = () => window.dualflowAPI?.window.minimize()
  const handleMaximize = () => window.dualflowAPI?.window.maximize()
  const handleClose = () => window.dualflowAPI?.window.close()

  return (
    <header className="h-8 bg-[#141618] border-b border-[#2C3238] flex items-center justify-between select-none titlebar-drag-region z-50">
      {/* App Identity & Core Telemetry Pills */}
      <div className="flex items-center gap-3 px-3 titlebar-no-drag">
        {/* Custom Hardware-Inspired DualFlow Glyph */}
        <div className="flex items-center gap-1.5">
          <div className="flex flex-col gap-0.5">
            <div className="w-3.5 h-[2px] bg-[#4F8FBF] rounded-[1px]" />
            <div className="w-3.5 h-[2px] bg-[#B97A4A] rounded-[1px]" />
          </div>
          <span className="font-semibold text-xs text-[#E2E5E9] tracking-wider font-mono">
            DUALFLOW
          </span>
          <span className="text-[10px] text-[#5E6670] font-mono">v0.1.0-alpha</span>
        </div>

        <div className="h-3 w-[1px] bg-[#2C3238]" />

        {/* Core Daemon Status Pill */}
        <div className="flex items-center gap-1.5 px-2 py-0.5 rounded-[2px] bg-[#1B1E22] border border-[#2C3238] text-[10px] font-mono">
          <span
            className={`w-1.5 h-1.5 rounded-[1px] ${
              coreStatus.isRunning ? 'bg-[#4EA869]' : 'bg-[#5A626A]'
            }`}
          />
          <span className="text-[#9098A2]">CORE:</span>
          <span className={coreStatus.isRunning ? 'text-[#E2E5E9]' : 'text-[#5E6670]'}>
            {coreStatus.isRunning
              ? `RUNNING (PID ${coreStatus.pid ?? '—'})`
              : 'IDLE'}
          </span>
        </div>

        {/* Wintun Virtual Adapter Pill */}
        <div className="flex items-center gap-1.5 px-2 py-0.5 rounded-[2px] bg-[#1B1E22] border border-[#2C3238] text-[10px] font-mono">
          <Server className="w-3 h-3 text-[#5E6670]" strokeWidth={1.5} />
          <span className="text-[#9098A2]">TUN:</span>
          <span className={coreStatus.isTunUp ? 'text-[#4EA869]' : 'text-[#5E6670]'}>
            {coreStatus.isTunUp
              ? `${coreStatus.tunInterface ?? 'wintun0'} UP`
              : 'UNBOUND'}
          </span>
        </div>

        {/* Active Profile Pill */}
        <div className="flex items-center gap-1.5 px-2 py-0.5 rounded-[2px] bg-[#1B1E22] border border-[#2C3238] text-[10px] font-mono uppercase">
          <Radio className="w-3 h-3 text-[#D99B32]" strokeWidth={1.5} />
          <span className="text-[#D99B32]">{coreStatus.profile}</span>
        </div>
      </div>

      {/* Windows Standard Window Controls */}
      <div className="flex items-center h-full titlebar-no-drag">
        <button
          onClick={handleMinimize}
          className="h-full px-3.5 flex items-center justify-center text-[#9098A2] hover:text-[#E2E5E9] hover:bg-[#24282E] transition-colors duration-100"
          title="Minimize"
          aria-label="Minimize"
        >
          <Minus className="w-3.5 h-3.5" strokeWidth={1.5} />
        </button>

        <button
          onClick={handleMaximize}
          className="h-full px-3.5 flex items-center justify-center text-[#9098A2] hover:text-[#E2E5E9] hover:bg-[#24282E] transition-colors duration-100"
          title={isMaximized ? 'Restore' : 'Maximize'}
          aria-label={isMaximized ? 'Restore' : 'Maximize'}
        >
          {isMaximized ? (
            <Copy className="w-3 h-3" strokeWidth={1.5} />
          ) : (
            <Square className="w-3 h-3" strokeWidth={1.5} />
          )}
        </button>

        <button
          onClick={handleClose}
          className="h-full px-4 flex items-center justify-center text-[#9098A2] hover:text-white hover:bg-[#C42B1C] transition-colors duration-100"
          title="Close"
          aria-label="Close"
        >
          <X className="w-3.5 h-3.5" strokeWidth={1.5} />
        </button>
      </div>
    </header>
  )
}
