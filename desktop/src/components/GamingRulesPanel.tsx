import type { FC } from 'react'
import { ShieldCheck, Crosshair, Plus } from 'lucide-react'
import type { GamingRule } from '../types'

interface GamingRulesPanelProps {
  rules: GamingRule[]
  onToggleRule: (id: string) => void
}

export const GamingRulesPanel: FC<GamingRulesPanelProps> = ({ rules, onToggleRule }) => {
  return (
    <div className="bg-[#1B1E22] border border-[#2C3238] rounded-[2px] p-3 text-xs font-mono">
      <div className="flex items-center justify-between border-b border-[#2C3238] pb-2 mb-3">
        <div className="flex items-center gap-2">
          <Crosshair className="w-4 h-4 text-[#4EA869]" strokeWidth={1.5} />
          <span className="font-semibold text-xs text-[#E2E5E9] tracking-wide">
            GAMING TRAFFIC OPTIMIZATION & UDP REDUNDANCY RULES
          </span>
        </div>

        <button className="h-6 px-2 flex items-center gap-1.5 rounded-[2px] bg-[#24282E] hover:bg-[#2B3037] border border-[#2C3238] text-[11px] text-[#9098A2] hover:text-[#E2E5E9] transition-colors">
          <Plus className="w-3 h-3 text-[#D99B32]" strokeWidth={1.5} />
          <span>Add Custom Process Rule</span>
        </button>
      </div>

      <div className="overflow-x-auto">
        <table className="w-full text-left border-collapse">
          <thead>
            <tr className="border-b border-[#2C3238] text-[10px] text-[#5E6670] uppercase">
              <th className="py-1.5 px-2">Profile Name</th>
              <th className="py-1.5 px-2">Process Image</th>
              <th className="py-1.5 px-2">Protocol / Ports</th>
              <th className="py-1.5 px-2">Routing Strategy</th>
              <th className="py-1.5 px-2">Assigned Target</th>
              <th className="py-1.5 px-2 text-right">Rule State</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-[#2C3238]/50">
            {rules.map((rule) => (
              <tr key={rule.id} className="hover:bg-[#24282E]/40 transition-colors">
                <td className="py-2 px-2 text-[#E2E5E9] font-medium flex items-center gap-1.5">
                  <ShieldCheck className="w-3.5 h-3.5 text-[#4EA869]" strokeWidth={1.5} />
                  <span>{rule.name}</span>
                </td>
                <td className="py-2 px-2 text-[#9098A2]">{rule.processName}</td>
                <td className="py-2 px-2 text-[#9098A2]">
                  {rule.protocol} : {rule.portRange}
                </td>
                <td className="py-2 px-2">
                  <span className="text-[#D99B32]">{rule.strategy.replace(/_/g, ' ')}</span>
                </td>
                <td className="py-2 px-2">
                  <span
                    className="px-1.5 py-0.5 rounded-[2px] text-[10px] font-mono border"
                    style={{
                      borderColor:
                        rule.targetPath === 'modem'
                          ? '#4F8FBF40'
                          : rule.targetPath === 'android'
                          ? '#B97A4A40'
                          : '#2C3238',
                      color:
                        rule.targetPath === 'modem'
                          ? '#4F8FBF'
                          : rule.targetPath === 'android'
                          ? '#B97A4A'
                          : '#9098A2',
                    }}
                  >
                    {rule.targetPath.toUpperCase()}
                  </span>
                </td>
                <td className="py-2 px-2 text-right">
                  <button
                    onClick={() => onToggleRule(rule.id)}
                    className={`px-2 py-0.5 rounded-[2px] text-[10px] font-mono border transition-colors ${
                      rule.active
                        ? 'bg-[#1E2721] border-[#4EA869]/50 text-[#4EA869]'
                        : 'bg-[#141618] border-[#2C3238] text-[#5E6670]'
                    }`}
                  >
                    {rule.active ? 'ACTIVE' : 'MUTED'}
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  )
}
