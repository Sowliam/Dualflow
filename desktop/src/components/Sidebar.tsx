import type { FC } from 'react'
import {
  Activity,
  Network,
  SlidersHorizontal,
  Gamepad2,
  Terminal,
  Settings,
  ShieldCheck,
  Cpu,
} from 'lucide-react'

export type NavTab = 'dashboard' | 'adapters' | 'rules' | 'gaming' | 'logs' | 'settings'

interface SidebarProps {
  currentTab: NavTab
  onSelectTab: (tab: NavTab) => void
}

export const Sidebar: FC<SidebarProps> = ({ currentTab, onSelectTab }) => {
  const navItems: { id: NavTab; label: string; icon: FC<{ className?: string; strokeWidth?: number }> }[] = [
    { id: 'dashboard', label: 'Dashboard', icon: Activity },
    { id: 'adapters', label: 'Network Adapters', icon: Network },
    { id: 'rules', label: 'Traffic Policies', icon: SlidersHorizontal },
    { id: 'gaming', label: 'Gaming Optimizer', icon: Gamepad2 },
    { id: 'logs', label: 'System Logs', icon: Terminal },
    { id: 'settings', label: 'Settings', icon: Settings },
  ]

  return (
    <aside className="w-56 bg-[#16181A] border-r border-[#2C3238] flex flex-col justify-between select-none">
      {/* Navigation List */}
      <div className="p-2 space-y-1">
        <div className="px-2.5 py-1 text-[11px] font-mono text-[#5E6670] uppercase tracking-wider">
          Navigation
        </div>

        {navItems.map((item) => {
          const Icon = item.icon
          const isActive = currentTab === item.id
          return (
            <button
              key={item.id}
              onClick={() => onSelectTab(item.id)}
              className={`w-full h-8 px-2.5 flex items-center gap-2.5 rounded-[2px] text-xs font-medium transition-colors duration-100 relative ${
                isActive
                  ? 'bg-[#24282E] text-[#E2E5E9]'
                  : 'text-[#9098A2] hover:text-[#E2E5E9] hover:bg-[#1B1E22]'
              }`}
            >
              {isActive && (
                <div className="absolute left-0 top-1 bottom-1 w-[2px] bg-[#D99B32] rounded-r-[1px]" />
              )}
              <Icon className="w-4 h-4 text-[#9098A2]" strokeWidth={1.5} />
              <span>{item.label}</span>
            </button>
          )
        })}
      </div>

      {/* Hardware & System Environment Diagnostics */}
      <div className="p-3 border-t border-[#2C3238] space-y-2 text-[11px] font-mono text-[#5E6670]">
        <div className="flex items-center justify-between">
          <span className="flex items-center gap-1.5 text-[#9098A2]">
            <Cpu className="w-3.5 h-3.5" strokeWidth={1.5} />
            <span>Target OS</span>
          </span>
          <span className="text-[#E2E5E9]">Windows x64</span>
        </div>

        <div className="flex items-center justify-between">
          <span className="flex items-center gap-1.5 text-[#9098A2]">
            <ShieldCheck className="w-3.5 h-3.5 text-[#4EA869]" strokeWidth={1.5} />
            <span>IPC Pipe</span>
          </span>
          <span className="text-[#4EA869]">Connected</span>
        </div>

        <div className="pt-1 text-[10px] text-[#5E6670] border-t border-[#2C3238]/60 flex justify-between">
          <span>Route Guardian</span>
          <span className="text-[#9098A2]">Armed (Auto-restore)</span>
        </div>
      </div>
    </aside>
  )
}
