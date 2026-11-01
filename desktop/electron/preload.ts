import { ipcRenderer, contextBridge } from 'electron'
import type { DualFlowAPI, NetworkAdapter } from '../src/types'

const dualflowAPI: DualFlowAPI = {
  window: {
    minimize: () => ipcRenderer.send('window:minimize'),
    maximize: () => ipcRenderer.send('window:maximize'),
    close: () => ipcRenderer.send('window:close'),
    isMaximized: () => ipcRenderer.invoke('window:isMaximized'),
    onStateChange: (callback: (state: { isMaximized: boolean }) => void) => {
      const listener = (_event: Electron.IpcRendererEvent, state: { isMaximized: boolean }) => callback(state)
      ipcRenderer.on('window:state-changed', listener)
      return () => {
        ipcRenderer.removeListener('window:state-changed', listener)
      }
    },
  },
  network: {
    getAdapters: () => ipcRenderer.invoke('dualflow:get-adapters'),
    rescanAdapters: () => ipcRenderer.invoke('dualflow:rescan-adapters'),
    getPaths: () => ipcRenderer.invoke('dualflow:get-paths'),
    assignPath: (pathId: string, adapterId: string | null) =>
      ipcRenderer.invoke('dualflow:assign-path', pathId, adapterId),
    onAdaptersUpdated: (callback: (adapters: NetworkAdapter[]) => void) => {
      const listener = (_event: Electron.IpcRendererEvent, adapters: NetworkAdapter[]) => callback(adapters)
      ipcRenderer.on('dualflow:adapters-updated', listener)
      return () => {
        ipcRenderer.removeListener('dualflow:adapters-updated', listener)
      }
    },
    onTelemetryUpdated: (callback: (data: { modem: any; android: any }) => void) => {
      const listener = (_event: Electron.IpcRendererEvent, data: { modem: any; android: any }) => callback(data)
      ipcRenderer.on('dualflow:telemetry-updated', listener)
      return () => {
        ipcRenderer.removeListener('dualflow:telemetry-updated', listener)
      }
    },
  },
  wintun: {
    getStatus: () => ipcRenderer.invoke('dualflow:wintun-get-status'),
    getStats: () => ipcRenderer.invoke('dualflow:wintun-get-stats'),
    createAdapter: () => ipcRenderer.invoke('dualflow:wintun-create-adapter'),
    openAdapter: () => ipcRenderer.invoke('dualflow:wintun-open-adapter'),
    startSession: (capacity?: number) => ipcRenderer.invoke('dualflow:wintun-start-session', capacity),
    stopSession: () => ipcRenderer.invoke('dualflow:wintun-stop-session'),
    closeAdapter: () => ipcRenderer.invoke('dualflow:wintun-close-adapter'),
    deleteAdapter: (confirm: boolean) => ipcRenderer.invoke('dualflow:wintun-delete-adapter', confirm),
    sendTestPacket: (payloadSize?: number) => ipcRenderer.invoke('dualflow:wintun-send-test-packet', payloadSize),
    onStatsUpdated: (callback: (stats: any) => void) => {
      const listener = (_event: Electron.IpcRendererEvent, stats: any) => callback(stats)
      ipcRenderer.on('dualflow:wintun-stats-updated', listener)
      return () => {
        ipcRenderer.removeListener('dualflow:wintun-stats-updated', listener)
      }
    },
    onStatusUpdated: (callback: (status: any) => void) => {
      const listener = (_event: Electron.IpcRendererEvent, status: any) => callback(status)
      ipcRenderer.on('dualflow:wintun-status-updated', listener)
      return () => {
        ipcRenderer.removeListener('dualflow:wintun-status-updated', listener)
      }
    },
  },
  packet: {
    getStatus: () => ipcRenderer.invoke('dualflow:packet-get-status'),
    getStats: () => ipcRenderer.invoke('dualflow:packet-get-stats'),
    setTestMode: (enabled: boolean) => ipcRenderer.invoke('dualflow:packet-set-test-mode', enabled),
    injectTestPacket: (options?: { ipVersion?: number; payloadSize?: number; corrupt?: boolean }) =>
      ipcRenderer.invoke('dualflow:packet-inject-test', options),
    resetStats: () => ipcRenderer.invoke('dualflow:packet-reset-stats'),
    onStatsUpdated: (callback: (data: any) => void) => {
      const listener = (_event: Electron.IpcRendererEvent, data: any) => callback(data)
      ipcRenderer.on('dualflow:packet-stats-updated', listener)
      return () => {
        ipcRenderer.removeListener('dualflow:packet-stats-updated', listener)
      }
    },
  },
  multipath: {
    getStatus: () => ipcRenderer.invoke('dualflow:multipath-get-status'),
    setMode: (mode: string) => ipcRenderer.invoke('dualflow:multipath-set-mode', mode),
    setWeights: (weights: Record<string, number>) =>
      ipcRenderer.invoke('dualflow:multipath-set-weights', weights),
    onStatsUpdated: (callback: (data: any) => void) => {
      const listener = (_event: Electron.IpcRendererEvent, data: any) => callback(data)
      ipcRenderer.on('dualflow:multipath-stats-updated', listener)
      return () => {
        ipcRenderer.removeListener('dualflow:multipath-stats-updated', listener)
      }
    },
  },
  system: {
    platform: process.platform,
    version: '0.1.0-alpha',
    getCoreStatus: () => ipcRenderer.invoke('dualflow:get-core-status'),
  },
}

contextBridge.exposeInMainWorld('dualflowAPI', dualflowAPI)
