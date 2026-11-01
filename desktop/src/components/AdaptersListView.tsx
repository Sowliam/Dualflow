import type { FC } from 'react'
import { Network, CheckCircle2, XCircle } from 'lucide-react'
import type { NetworkAdapter } from '../types'

interface AdaptersListViewProps {
  adapters: NetworkAdapter[]
  onAssignAdapter: (adapterId: string, role: 'modem' | 'android' | 'unassigned') => void
}

export const AdaptersListView: FC<AdaptersListViewProps> = ({
  adapters,
  onAssignAdapter,
}) => {
  return (
    <div className="bg-[#1B1E22] border border-[#2C3238] rounded-[2px] p-3 text-xs font-mono">
      <div className="flex items-center justify-between border-b border-[#2C3238] pb-2 mb-3">
        <div className="flex items-center gap-2">
          <Network className="w-4 h-4 text-[#D99B32]" strokeWidth={1.5} />
          <span className="font-semibold text-xs text-[#E2E5E9]">
            DETECTED WINDOWS NETWORK ADAPTERS (IP HELPER API)
          </span>
          <span className="text-[10px] text-[#5E6670]">({adapters.length} discovered)</span>
        </div>
      </div>

      <div className="space-y-2">
        {adapters.length === 0 ? (
          <div className="text-center py-10 text-[#5E6670]">
            No physical or virtual network adapters discovered. Run rescan to query IP Helper API.
          </div>
        ) : (
          adapters.map((adapter) => {
            const isConnected = adapter.status === 'connected'
            const isAssignedModem = adapter.classification === 'modem'
            const isAssignedAndroid = adapter.classification === 'android'

            return (
              <div
                key={adapter.id}
                className="bg-[#141618] border border-[#2C3238] rounded-[2px] p-3 hover:border-[#3D444D] transition-colors"
              >
                <div className="flex items-start justify-between">
                  <div className="space-y-1">
                    <div className="flex items-center gap-2">
                      {isConnected ? (
                        <CheckCircle2 className="w-4 h-4 text-[#4EA869]" strokeWidth={1.5} />
                      ) : (
                        <XCircle className="w-4 h-4 text-[#5E6670]" strokeWidth={1.5} />
                      )}
                      <span className="font-semibold text-sm text-[#E2E5E9]">
                        {adapter.name}
                      </span>
                      <span className="text-[10px] text-[#9098A2] px-1.5 py-0.5 rounded-[2px] bg-[#1B1E22] border border-[#2C3238] uppercase">
                        {adapter.mediaType}
                      </span>
                      {isAssignedModem && (
                        <span className="text-[10px] text-[#4F8FBF] px-1.5 py-0.5 rounded-[2px] bg-[#4F8FBF]/10 border border-[#4F8FBF]/30 font-semibold">
                          ASSIGNED: PATH 1 (MODEM)
                        </span>
                      )}
                      {isAssignedAndroid && (
                        <span className="text-[10px] text-[#B97A4A] px-1.5 py-0.5 rounded-[2px] bg-[#B97A4A]/10 border border-[#B97A4A]/30 font-semibold">
                          ASSIGNED: PATH 2 (ANDROID)
                        </span>
                      )}
                      {adapter.classification === 'vpn' && (
                        <span className="text-[10px] text-[#9098A2] px-1.5 py-0.5 rounded-[2px] bg-[#1B1E22] border border-[#2C3238]">
                          VPN / TUNNEL
                        </span>
                      )}
                      {adapter.classification === 'virtual' && (
                        <span className="text-[10px] text-[#5E6670] px-1.5 py-0.5 rounded-[2px] bg-[#141618] border border-[#2C3238]">
                          VIRTUAL NIC
                        </span>
                      )}
                      {adapter.classification === 'unknown' && (
                        <span className="text-[10px] text-[#D9A045] px-1.5 py-0.5 rounded-[2px] bg-[#D9A045]/10 border border-[#D9A045]/30">
                          UNKNOWN / UNVERIFIED
                        </span>
                      )}
                      {adapter.classificationConfidence && (
                        <span className="text-[9px] text-[#5E6670] font-mono">
                          confidence: {adapter.classificationConfidence}
                        </span>
                      )}
                    </div>
                    <p className="text-[11px] text-[#9098A2] pl-6">{adapter.description}</p>
                    {adapter.classificationReasons && adapter.classificationReasons.length > 0 && (
                      <div className="pl-6 text-[10px] text-[#5E6670] font-mono">
                        Classification evidence: {adapter.classificationReasons.join(' | ')}
                      </div>
                    )}
                  </div>

                  {/* Path Assignment Controls */}
                  <div className="flex items-center gap-1.5">
                    <button
                      onClick={() => onAssignAdapter(adapter.id, 'modem')}
                      className={`h-6 px-2 text-[10px] font-mono rounded-[2px] border transition-colors ${
                        isAssignedModem
                          ? 'bg-[#4F8FBF]/20 border-[#4F8FBF] text-[#4F8FBF]'
                          : 'bg-[#1B1E22] border-[#2C3238] text-[#9098A2] hover:text-[#E2E5E9]'
                      }`}
                    >
                      Set as Path 1
                    </button>
                    <button
                      onClick={() => onAssignAdapter(adapter.id, 'android')}
                      className={`h-6 px-2 text-[10px] font-mono rounded-[2px] border transition-colors ${
                        isAssignedAndroid
                          ? 'bg-[#B97A4A]/20 border-[#B97A4A] text-[#B97A4A]'
                          : 'bg-[#1B1E22] border-[#2C3238] text-[#9098A2] hover:text-[#E2E5E9]'
                      }`}
                    >
                      Set as Path 2
                    </button>
                  </div>
                </div>

                {/* Adapter Technical Metadata Table */}
                <div className="mt-2.5 pt-2 border-t border-[#2C3238]/60 grid grid-cols-5 gap-2 text-[11px] font-mono">
                  <div>
                    <span className="text-[10px] text-[#5E6670] uppercase block">IPv4 Address</span>
                    <span className="text-[#E2E5E9] tabular-num">{adapter.ipv4 ?? '—'}</span>
                  </div>
                  <div>
                    <span className="text-[10px] text-[#5E6670] uppercase block">Subnet Mask</span>
                    <span className="text-[#9098A2] tabular-num">{adapter.netmask ?? '—'}</span>
                  </div>
                  <div>
                    <span className="text-[10px] text-[#5E6670] uppercase block">Gateway</span>
                    <span className="text-[#E2E5E9] tabular-num">{adapter.gateway ?? '—'}</span>
                  </div>
                  <div>
                    <span className="text-[10px] text-[#5E6670] uppercase block">Metric / MTU</span>
                    <span className="text-[#9098A2] tabular-num">
                      {adapter.metric} / {adapter.mtu}
                    </span>
                  </div>
                  <div>
                    <span className="text-[10px] text-[#5E6670] uppercase block">Hardware MAC</span>
                    <span className="text-[#9098A2] tabular-num">
                      {adapter.mac && adapter.mac !== '-' && !adapter.mac.includes('?') ? adapter.mac : '—'}
                    </span>
                  </div>
                </div>
              </div>
            )
          })
        )}
      </div>
    </div>
  )
}
