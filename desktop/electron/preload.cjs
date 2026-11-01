const { ipcRenderer, contextBridge } = require('electron')

const dualflowAPI = {
  window: {
    minimize: () => ipcRenderer.send('window:minimize'),
    maximize: () => ipcRenderer.send('window:maximize'),
    close: () => ipcRenderer.send('window:close'),
    isMaximized: () => ipcRenderer.invoke('window:isMaximized'),
    onStateChange: (callback) => {
      const listener = (_event, state) => callback(state)
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
    assignPath: (pathId, adapterId) => ipcRenderer.invoke('dualflow:assign-path', pathId, adapterId),
    onAdaptersUpdated: (callback) => {
      const listener = (_event, adapters) => callback(adapters)
      ipcRenderer.on('dualflow:adapters-updated', listener)
      return () => {
        ipcRenderer.removeListener('dualflow:adapters-updated', listener)
      }
    },
    onTelemetryUpdated: (callback) => {
      const listener = (_event, data) => callback(data)
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
    startSession: (capacity) => ipcRenderer.invoke('dualflow:wintun-start-session', capacity),
    stopSession: () => ipcRenderer.invoke('dualflow:wintun-stop-session'),
    closeAdapter: () => ipcRenderer.invoke('dualflow:wintun-close-adapter'),
    deleteAdapter: (confirm) => ipcRenderer.invoke('dualflow:wintun-delete-adapter', confirm),
    sendTestPacket: (payloadSize) => ipcRenderer.invoke('dualflow:wintun-send-test-packet', payloadSize),
    onStatsUpdated: (callback) => {
      const listener = (_event, stats) => callback(stats)
      ipcRenderer.on('dualflow:wintun-stats-updated', listener)
      return () => {
        ipcRenderer.removeListener('dualflow:wintun-stats-updated', listener)
      }
    },
    onStatusUpdated: (callback) => {
      const listener = (_event, status) => callback(status)
      ipcRenderer.on('dualflow:wintun-status-updated', listener)
      return () => {
        ipcRenderer.removeListener('dualflow:wintun-status-updated', listener)
      }
    },
  },
  packet: {
    getStatus: () => ipcRenderer.invoke('dualflow:packet-get-status'),
    getStats: () => ipcRenderer.invoke('dualflow:packet-get-stats'),
    setTestMode: (enabled) => ipcRenderer.invoke('dualflow:packet-set-test-mode', enabled),
    injectTestPacket: (options) => ipcRenderer.invoke('dualflow:packet-inject-test', options),
    resetStats: () => ipcRenderer.invoke('dualflow:packet-reset-stats'),
    onStatsUpdated: (callback) => {
      const listener = (_event, data) => callback(data)
      ipcRenderer.on('dualflow:packet-stats-updated', listener)
      return () => {
        ipcRenderer.removeListener('dualflow:packet-stats-updated', listener)
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
