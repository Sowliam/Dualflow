// DualFlow Domain & IPC Type Definitions

export interface WindowControlAPI {
  minimize: () => void
  maximize: () => void
  close: () => void
  isMaximized: () => Promise<boolean>
  onStateChange: (callback: (state: { isMaximized: boolean }) => void) => () => void
}

export interface CoreStatusInfo {
  provider: 'go' | 'ts'
  isRunning: boolean
  pid: number | null
  version: string
  uptimeMs: number
  path1State: string
  path2State: string
}

export interface NetworkAPI {
  getAdapters: () => Promise<NetworkAdapter[]>
  rescanAdapters: () => Promise<NetworkAdapter[]>
  getPaths?: () => Promise<any>
  assignPath?: (pathId: string, adapterId: string | null) => Promise<any>
  onAdaptersUpdated: (callback: (adapters: NetworkAdapter[]) => void) => () => void
  onTelemetryUpdated: (callback: (data: { modem: PathTelemetryUpdate; android: PathTelemetryUpdate }) => void) => () => void
}

export interface WintunStatus {
  driver_status: 'NOT INSTALLED' | 'AVAILABLE' | 'ERROR'
  driver_version: string
  driver_path: string
  is_elevated: boolean
  adapter_exists: boolean
  adapter_name: string
  adapter_status: 'ADAPTER NOT CREATED' | 'ADAPTER READY' | 'ERROR'
  session_status: 'SESSION STOPPED' | 'SESSION RUNNING' | 'ERROR'
  last_error?: string
  rx_packets: number
  tx_packets: number
  rx_bytes: number
  tx_bytes: number
  uptime_ms: number
}

export interface WintunStats {
  rx_packets: number
  tx_packets: number
  rx_bytes: number
  tx_bytes: number
  dropped_packets: number
  last_rx_timestamp?: number
  last_tx_timestamp?: number
  last_error?: string
}

export interface WintunAPI {
  getStatus: () => Promise<WintunStatus>
  getStats: () => Promise<WintunStats>
  createAdapter: () => Promise<WintunStatus>
  openAdapter: () => Promise<WintunStatus>
  startSession: (capacity?: number) => Promise<WintunStatus>
  stopSession: () => Promise<WintunStatus>
  closeAdapter: () => Promise<WintunStatus>
  deleteAdapter: (confirm: boolean) => Promise<WintunStatus>
  sendTestPacket: (payloadSize?: number) => Promise<any>
  onStatsUpdated?: (callback: (stats: WintunStats) => void) => () => void
  onStatusUpdated?: (callback: (status: WintunStatus) => void) => () => void
}

export interface PacketStats {
  validated_packets: number
  rejected_packets: number
  queued_packets: number
  processed_bytes: number
  total_drops: number
  drops_invalid_packet: number
  drops_queue_full: number
  drops_transport_unavailable: number
  drops_transport_write_failed: number
  drops_wintun_send_failed: number
  drops_session_closed: number
  last_drop_reason?: string
  last_drop_time?: number
}

export interface TransportStats {
  state: 'DISCONNECTED' | 'CONNECTING' | 'CONNECTED' | 'ERROR'
  is_test: boolean
  tx_packets: number
  rx_packets: number
  tx_bytes: number
  rx_bytes: number
  dropped_packets: number
  last_error?: string
}

export interface PacketStatus {
  is_running: boolean
  is_test_mode: boolean
  queue_cap: number
  queue_depth: number
  stats: PacketStats
  transport: TransportStats
  timestamp: number
}

export interface PacketAPI {
  getStatus: () => Promise<PacketStatus>
  getStats: () => Promise<{ pipeline: PacketStats; transport: TransportStats }>
  setTestMode: (enabled: boolean) => Promise<PacketStatus>
  injectTestPacket: (options?: { ipVersion?: number; payloadSize?: number; corrupt?: boolean }) => Promise<any>
  resetStats: () => Promise<{ reset: boolean }>
  onStatsUpdated?: (callback: (data: { pipeline: PacketStats; transport: TransportStats }) => void) => () => void
}

export interface DualFlowAPI {
  window: WindowControlAPI
  network: NetworkAPI
  wintun?: WintunAPI
  packet?: PacketAPI
  multipath?: MultipathAPI
  system: {
    platform: string
    version: string
    getCoreStatus?: () => Promise<CoreStatusInfo>
  }
}

export interface MultipathPathSnapshot {
  id: string
  name: string
  iface_index: number
  iface_name: string
  description: string
  local_ip: string
  gateway: string
  state: 'DISCOVERED' | 'AVAILABLE' | 'DEGRADED' | 'UNAVAILABLE' | 'DISCONNECTED' | 'ERROR'
  weight: number
  rtt_ms: number
  rtt_min_ms: number
  rtt_max_ms: number
  rtt_jitter_ms: number
  packet_loss: number
  tx_packets: number
  rx_packets: number
  tx_bytes: number
  rx_bytes: number
  drops: number
  last_probe_ms: number
  is_eligible: boolean
}

export interface MultipathStats {
  total_logical_packets: number
  path_tx_packets: Record<string, number>
  path_rx_packets: Record<string, number>
  successful_sends: number
  failed_sends: number
  duplicates_dropped: number
  reordered_delivered: number
  late_packets_dropped: number
  timed_out_packets: number
  total_drops: number
}

export interface MultipathStatus {
  state: 'STOPPED' | 'STARTING' | 'RUNNING' | 'DEGRADED' | 'ERROR'
  scheduler_mode: 'round_robin' | 'weighted_rr' | 'lowest_latency' | 'redundant'
  paths: MultipathPathSnapshot[]
  paths_count: number
  stats: MultipathStats
  reorder_queue_depth: number
  reorder_reordered: number
  reorder_dropped_late: number
  reorder_timed_out: number
  is_test_mode: boolean
}

export interface MultipathAPI {
  getStatus: () => Promise<MultipathStatus>
  setMode: (mode: string) => Promise<MultipathStatus>
  setWeights: (weights: Record<string, number>) => Promise<MultipathStatus>
  onStatsUpdated?: (callback: (status: MultipathStatus) => void) => () => void
}

declare global {
  interface Window {
    dualflowAPI?: DualFlowAPI
  }
}

export type AdapterClassification = 'modem' | 'android' | 'vpn' | 'virtual' | 'unassigned' | 'unknown'
export type AdapterMediaType = 'ethernet' | 'wifi' | 'rndis_android' | 'ncm_android' | 'vpn' | 'virtual' | 'other'

export interface NetworkAdapter {
  id: string
  name: string
  description: string
  manufacturer?: string
  pnpId?: string
  mediaType: AdapterMediaType
  classification: AdapterClassification
  classificationConfidence?: 'high' | 'medium' | 'low'
  classificationReasons?: string[]
  mac: string
  ipv4: string | null
  netmask: string | null
  gateway: string | null
  dns: string[]
  metric: number
  mtu: number
  status: 'connected' | 'disconnected' | 'probing'
}

export interface PathTelemetryUpdate {
  pathId: 'modem' | 'android'
  rxBps: number
  txBps: number
  rttMs: number | null
  rttMinMs: number | null
  rttMaxMs: number | null
  jitterMs: number | null
  lossPercent: number | null
  probeTarget: string | null
  lastProbeTimestamp: string | null
  telemetryPoint?: TelemetryPoint
}

export interface TelemetryPoint {
  timestamp: number
  rttMs: number | null
  rxMbps: number
  txMbps: number
  loss: number
}

export interface PathTelemetry {
  pathId: 'modem' | 'android'
  pathName: string
  adapter: NetworkAdapter | null
  sourceIp: string | null
  rxBps: number
  txBps: number
  rttMs: number | null
  rttMinMs: number | null
  rttMaxMs: number | null
  jitterMs: number | null
  lossPercent: number | null
  probeTarget: string | null
  lastProbeTimestamp: string | null
  history: TelemetryPoint[]
}

export type OperationMode = 'off' | 'load_balancing' | 'bonding' | 'failover'
export type ActiveProfile = 'gaming' | 'download' | 'balanced'

export interface BondingCoreStatus {
  isRunning: boolean
  pid: number | null
  tunInterface: string | null
  isTunUp: boolean
  mode: OperationMode
  profile: ActiveProfile
  serverAddress: string | null
  serverPort: number | null
  isServerReachable: boolean
  uptimeSeconds: number
  lastError: string | null
}

export interface GamingRule {
  id: string
  name: string
  processName: string
  protocol: 'UDP' | 'TCP' | 'ANY'
  portRange: string
  strategy: 'best_single_path' | 'redundancy_duplicate' | 'pin_modem' | 'pin_android'
  targetPath: 'modem' | 'android' | 'auto'
  active: boolean
}
