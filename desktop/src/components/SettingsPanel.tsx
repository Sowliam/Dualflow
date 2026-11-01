import type { FC } from 'react'
import { Settings, HardDrive, Key, Save } from 'lucide-react'

export const SettingsPanel: FC = () => {
  return (
    <div className="bg-[#1B1E22] border border-[#2C3238] rounded-[2px] p-3 text-xs font-mono max-w-3xl space-y-4">
      <div className="flex items-center justify-between border-b border-[#2C3238] pb-2">
        <div className="flex items-center gap-2">
          <Settings className="w-4 h-4 text-[#D99B32]" strokeWidth={1.5} />
          <span className="font-semibold text-xs text-[#E2E5E9]">
            SYSTEM CONFIGURATION & TUNNEL PARAMETERS
          </span>
        </div>

        <button className="h-6 px-3 flex items-center gap-1.5 rounded-[2px] bg-[#24282E] hover:bg-[#2B3037] border border-[#2C3238] text-[11px] text-[#E2E5E9] transition-colors">
          <Save className="w-3 h-3 text-[#4EA869]" strokeWidth={1.5} />
          <span>Save Configuration</span>
        </button>
      </div>

      {/* Network Core & Windows Service */}
      <div className="space-y-3">
        <h4 className="text-[11px] text-[#D99B32] uppercase tracking-wider flex items-center gap-1.5">
          <HardDrive className="w-3.5 h-3.5" strokeWidth={1.5} />
          Core Process & Windows Service
        </h4>

        <div className="grid grid-cols-2 gap-3">
          <div>
            <label className="text-[10px] text-[#5E6670] uppercase block mb-1">
              Virtual NIC (Wintun Adapter Name)
            </label>
            <input
              type="text"
              defaultValue="wintun0"
              className="w-full bg-[#141618] border border-[#2C3238] rounded-[2px] px-2.5 py-1 text-xs text-[#E2E5E9] font-mono focus:border-[#3D444D] focus:outline-none"
            />
          </div>

          <div>
            <label className="text-[10px] text-[#5E6670] uppercase block mb-1">
              Probe Interval (ms)
            </label>
            <input
              type="number"
              defaultValue="1000"
              className="w-full bg-[#141618] border border-[#2C3238] rounded-[2px] px-2.5 py-1 text-xs text-[#E2E5E9] font-mono focus:border-[#3D444D] focus:outline-none"
            />
          </div>
        </div>

        <div className="space-y-2 pt-1 text-[11px] text-[#9098A2]">
          <label className="flex items-center gap-2 cursor-pointer">
            <input
              type="checkbox"
              defaultChecked
              className="accent-[#D99B32] rounded-[1px] bg-[#141618]"
            />
            <span>Automatic Route Table Rollback on application exit / crash</span>
          </label>

          <label className="flex items-center gap-2 cursor-pointer">
            <input
              type="checkbox"
              defaultChecked={false}
              className="accent-[#D99B32] rounded-[1px] bg-[#141618]"
            />
            <span>Start DualFlow Core automatically as a background Windows Service</span>
          </label>
        </div>
      </div>

      {/* Remote Aggregation Server (Phase 5+) */}
      <div className="space-y-3 pt-3 border-t border-[#2C3238]">
        <h4 className="text-[11px] text-[#D99B32] uppercase tracking-wider flex items-center gap-1.5">
          <Key className="w-3.5 h-3.5" strokeWidth={1.5} />
          Remote Bonding Relay (VPS Aggregation Endpoint)
        </h4>

        <div className="grid grid-cols-2 gap-3">
          <div>
            <label className="text-[10px] text-[#5E6670] uppercase block mb-1">
              Relay Server IP / Hostname
            </label>
            <input
              type="text"
              placeholder="e.g. 198.51.100.24 or relay.dualflow.net"
              className="w-full bg-[#141618] border border-[#2C3238] rounded-[2px] px-2.5 py-1 text-xs text-[#E2E5E9] font-mono focus:border-[#3D444D] focus:outline-none placeholder-[#5E6670]"
            />
          </div>

          <div>
            <label className="text-[10px] text-[#5E6670] uppercase block mb-1">
              Tunnel UDP Port
            </label>
            <input
              type="number"
              defaultValue="51820"
              className="w-full bg-[#141618] border border-[#2C3238] rounded-[2px] px-2.5 py-1 text-xs text-[#E2E5E9] font-mono focus:border-[#3D444D] focus:outline-none"
            />
          </div>
        </div>

        <div>
          <label className="text-[10px] text-[#5E6670] uppercase block mb-1">
            Noise Tunnel Public Key / Token
          </label>
          <input
            type="password"
            placeholder="Base64 public authentication key..."
            className="w-full bg-[#141618] border border-[#2C3238] rounded-[2px] px-2.5 py-1 text-xs text-[#E2E5E9] font-mono focus:border-[#3D444D] focus:outline-none placeholder-[#5E6670]"
          />
        </div>
      </div>
    </div>
  )
}
