import { useState, type FC } from 'react'
import { Terminal, Trash2, Filter } from 'lucide-react'

export interface LogEntry {
  id: string
  timestamp: string
  level: 'INFO' | 'WARN' | 'ERROR' | 'DEBUG'
  subsystem: string
  message: string
}

interface LogsPanelProps {
  logs: LogEntry[]
  onClearLogs: () => void
}

export const LogsPanel: FC<LogsPanelProps> = ({ logs, onClearLogs }) => {
  const [filter, setFilter] = useState('')

  const filteredLogs = logs.filter(
    (l) =>
      l.message.toLowerCase().includes(filter.toLowerCase()) ||
      l.subsystem.toLowerCase().includes(filter.toLowerCase())
  )

  return (
    <div className="bg-[#1B1E22] border border-[#2C3238] rounded-[2px] p-3 text-xs font-mono flex flex-col h-full">
      {/* Header & Filter Controls */}
      <div className="flex items-center justify-between border-b border-[#2C3238] pb-2 mb-2">
        <div className="flex items-center gap-2">
          <Terminal className="w-4 h-4 text-[#9098A2]" strokeWidth={1.5} />
          <span className="font-semibold text-xs text-[#E2E5E9]">
            STRUCTURED SYSTEM & CORE DAEMON LOGS
          </span>
          <span className="text-[10px] text-[#5E6670]">({filteredLogs.length} events)</span>
        </div>

        <div className="flex items-center gap-2">
          <div className="flex items-center gap-1.5 px-2 py-1 bg-[#141618] border border-[#2C3238] rounded-[2px]">
            <Filter className="w-3 h-3 text-[#5E6670]" strokeWidth={1.5} />
            <input
              type="text"
              placeholder="Filter subsystem/message..."
              value={filter}
              onChange={(e) => setFilter(e.target.value)}
              className="bg-transparent border-none text-[11px] text-[#E2E5E9] focus:outline-none w-44 placeholder-[#5E6670]"
            />
          </div>

          <button
            onClick={onClearLogs}
            className="h-6 px-2 flex items-center gap-1 rounded-[2px] bg-[#24282E] hover:bg-[#2B3037] border border-[#2C3238] text-[11px] text-[#9098A2] hover:text-[#E2E5E9]"
            title="Clear in-memory log buffer"
          >
            <Trash2 className="w-3 h-3" strokeWidth={1.5} />
            <span>Clear</span>
          </button>
        </div>
      </div>

      {/* Log Stream Box */}
      <div className="flex-1 overflow-y-auto bg-[#141618] p-2 rounded-[2px] border border-[#2C3238] space-y-1 font-mono text-[11px]">
        {filteredLogs.length === 0 ? (
          <div className="text-[#5E6670] py-8 text-center">
            No events recorded matching current filter.
          </div>
        ) : (
          filteredLogs.map((log) => {
            const levelColor =
              log.level === 'ERROR'
                ? 'text-[#C45347]'
                : log.level === 'WARN'
                ? 'text-[#CFA043]'
                : log.level === 'DEBUG'
                ? 'text-[#5E6670]'
                : 'text-[#4EA869]'

            return (
              <div key={log.id} className="flex items-start gap-2 leading-relaxed">
                <span className="text-[#5E6670] shrink-0 tabular-num">{log.timestamp}</span>
                <span className={`w-12 shrink-0 font-semibold ${levelColor}`}>[{log.level}]</span>
                <span className="text-[#D99B32] shrink-0">[{log.subsystem}]</span>
                <span className="text-[#E2E5E9] break-all">{log.message}</span>
              </div>
            )
          })
        )}
      </div>
    </div>
  )
}
