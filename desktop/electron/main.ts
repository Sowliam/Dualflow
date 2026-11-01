import { app, BrowserWindow, ipcMain } from 'electron'
import path from 'node:path'
import fs from 'node:fs'
import { fileURLToPath } from 'node:url'
import { NetworkDiscoveryService } from './services/networkDiscovery'
import { TelemetryMonitorService } from './services/telemetryMonitor'
import { GoCoreBridge } from './services/goCoreBridge'
import type { NetworkAdapter } from '../src/types'

const __dirname = path.dirname(fileURLToPath(import.meta.url))

process.env.APP_ROOT = path.join(__dirname, '..')

export const VITE_DEV_SERVER_URL = process.env['VITE_DEV_SERVER_URL']
export const MAIN_DIST = path.join(process.env.APP_ROOT, 'dist-electron')
export const RENDERER_DIST = path.join(process.env.APP_ROOT, 'dist')

process.env.VITE_PUBLIC = VITE_DEV_SERVER_URL ? path.join(process.env.APP_ROOT, 'public') : RENDERER_DIST

let win: BrowserWindow | null = null

// Core Provider Configuration: 'go' (default) with 'ts' (fallback)
const DESIRED_PROVIDER = process.env.DUALFLOW_CORE_PROVIDER || 'go'
let activeProvider: 'go' | 'ts' = 'go'

const goCore = new GoCoreBridge()
const tsDiscovery = new NetworkDiscoveryService()
const tsTelemetry = new TelemetryMonitorService()

let cachedAdapters: NetworkAdapter[] = []
let tsTelemetryTimer: NodeJS.Timeout | null = null

function getPreloadPath(): string {
  const candidates = [
    path.join(__dirname, 'preload.cjs'),
    path.join(__dirname, 'preload.js'),
    path.join(__dirname, 'preload.mjs'),
  ]
  for (const candidate of candidates) {
    if (fs.existsSync(candidate)) return candidate
  }
  return path.join(__dirname, 'preload.cjs')
}

async function initializeCoreEngine() {
  if (DESIRED_PROVIDER === 'go') {
    try {
      console.log('[DualFlow Electron] Initializing Go Network Core Daemon...')
      await goCore.start()
      activeProvider = 'go'
      console.log('[DualFlow Electron] Go Network Core Daemon operational (Active Provider: GO).')

      // Run Parity Audit on boot
      try {
        const [goAdapters, tsAdapters] = await Promise.all([
          goCore.getAdapters(),
          tsDiscovery.getAdapters(),
        ])
        console.log(`[BOOT PARITY AUDIT] Go Core: ${goAdapters.length} adapters | TypeScript Engine: ${tsAdapters.length} adapters`)
      } catch (pErr) {
        console.warn('[BOOT PARITY AUDIT] Non-fatal parity check warning:', pErr)
      }
    } catch (err: any) {
      console.warn(`[PROVIDER FAILOVER] Go Core startup failed: ${err.message}. Falling back to TypeScript networking stack.`)
      activeProvider = 'ts'
    }
  } else {
    console.log('[DualFlow Electron] Active Provider explicitly set to TypeScript.')
    activeProvider = 'ts'
  }
}

async function createWindow() {
  const preloadPath = getPreloadPath()
  console.log('[DualFlow Electron] Preload path resolved:', preloadPath)

  win = new BrowserWindow({
    width: 1240,
    height: 820,
    minWidth: 1024,
    minHeight: 680,
    frame: false,
    backgroundColor: '#141618',
    show: false,
    webPreferences: {
      preload: preloadPath,
      contextIsolation: true,
      nodeIntegration: false,
      sandbox: true,
    },
  })

  win.on('ready-to-show', () => {
    win?.show()
  })

  // Window control IPC
  ipcMain.on('window:minimize', () => win?.minimize())
  ipcMain.on('window:maximize', () => {
    if (win?.isMaximized()) win.unmaximize()
    else win?.maximize()
  })
  ipcMain.on('window:close', () => win?.close())
  ipcMain.handle('window:isMaximized', () => win?.isMaximized() ?? false)

  win.on('maximize', () => {
    win?.webContents.send('window:state-changed', { isMaximized: true })
  })
  win.on('unmaximize', () => {
    win?.webContents.send('window:state-changed', { isMaximized: false })
  })

  // Core Status IPC
  ipcMain.handle('dualflow:get-core-status', async () => {
    if (activeProvider === 'go' && goCore.getIsReady()) {
      const status: any = await goCore.getStatus().catch(() => null)
      return {
        provider: 'go',
        isRunning: true,
        version: status?.version || '1.0.0',
        uptimeMs: status?.uptimeMs || status?.uptime_ms || 0,
        path1State: status?.path1State || status?.path1_state || 'unknown',
        path2State: status?.path2State || status?.path2_state || 'unknown',
      }
    }
    return {
      provider: 'ts',
      isRunning: true,
      version: '0.1.0-alpha',
      uptimeMs: 0,
      path1State: 'connected',
      path2State: 'disconnected',
    }
  })

  // Network Discovery IPC
  ipcMain.handle('dualflow:get-adapters', async () => {
    if (activeProvider === 'go' && goCore.getIsReady()) {
      try {
        cachedAdapters = await goCore.getAdapters()
        return cachedAdapters
      } catch (err) {
        console.warn('[IPC] Go get_adapters error, falling back to TS:', err)
      }
    }
    cachedAdapters = await tsDiscovery.getAdapters()
    return cachedAdapters
  })

  ipcMain.handle('dualflow:rescan-adapters', async () => {
    if (activeProvider === 'go' && goCore.getIsReady()) {
      try {
        const res = await goCore.rescan()
        cachedAdapters = res.adapters
        return cachedAdapters
      } catch (err) {
        console.warn('[IPC] Go rescan error, falling back to TS:', err)
      }
    }
    cachedAdapters = await tsDiscovery.getAdapters()
    return cachedAdapters
  })

  // Wintun Virtual Adapter IPC Handlers
  ipcMain.handle('dualflow:wintun-get-status', async () => {
    if (activeProvider === 'go' && goCore.getIsReady()) {
      try {
        return await goCore.wintunGetStatus()
      } catch (err: any) {
        return {
          driver_status: 'ERROR',
          driver_version: 'UNKNOWN',
          driver_path: '',
          is_elevated: false,
          adapter_exists: false,
          adapter_name: 'DualFlow Tunnel',
          adapter_status: 'ERROR',
          session_status: 'ERROR',
          last_error: err.message,
          rx_packets: 0,
          tx_packets: 0,
          rx_bytes: 0,
          tx_bytes: 0,
          uptime_ms: 0,
        }
      }
    }
    return {
      driver_status: 'AVAILABLE',
      driver_version: '0.14.1 (DLL Staged)',
      driver_path: 'bin/wintun.dll',
      is_elevated: false,
      adapter_exists: false,
      adapter_name: 'DualFlow Tunnel',
      adapter_status: 'ADAPTER NOT CREATED',
      session_status: 'SESSION STOPPED',
      rx_packets: 0,
      tx_packets: 0,
      rx_bytes: 0,
      tx_bytes: 0,
      uptime_ms: 0,
    }
  })

  ipcMain.handle('dualflow:wintun-get-stats', async () => {
    if (activeProvider === 'go' && goCore.getIsReady()) {
      try {
        return await goCore.wintunGetStats()
      } catch (err: any) {
        return { rx_packets: 0, tx_packets: 0, rx_bytes: 0, tx_bytes: 0, dropped_packets: 0, last_error: err.message }
      }
    }
    return { rx_packets: 0, tx_packets: 0, rx_bytes: 0, tx_bytes: 0, dropped_packets: 0 }
  })

  ipcMain.handle('dualflow:wintun-create-adapter', async () => {
    if (activeProvider === 'go' && goCore.getIsReady()) {
      return await goCore.wintunCreateAdapter()
    }
    throw new Error('Go Core is not running')
  })

  ipcMain.handle('dualflow:wintun-open-adapter', async () => {
    if (activeProvider === 'go' && goCore.getIsReady()) {
      return await goCore.wintunOpenAdapter()
    }
    throw new Error('Go Core is not running')
  })

  ipcMain.handle('dualflow:wintun-start-session', async (_event, capacity?: number) => {
    if (activeProvider === 'go' && goCore.getIsReady()) {
      return await goCore.wintunStartSession(capacity)
    }
    throw new Error('Go Core is not running')
  })

  ipcMain.handle('dualflow:wintun-stop-session', async () => {
    if (activeProvider === 'go' && goCore.getIsReady()) {
      return await goCore.wintunStopSession()
    }
    throw new Error('Go Core is not running')
  })

  ipcMain.handle('dualflow:wintun-close-adapter', async () => {
    if (activeProvider === 'go' && goCore.getIsReady()) {
      return await goCore.wintunCloseAdapter()
    }
    throw new Error('Go Core is not running')
  })

  ipcMain.handle('dualflow:wintun-delete-adapter', async (_event, confirm: boolean) => {
    if (activeProvider === 'go' && goCore.getIsReady()) {
      return await goCore.wintunDeleteAdapter(confirm)
    }
    throw new Error('Go Core is not running')
  })

  ipcMain.handle('dualflow:wintun-send-test-packet', async (_event, payloadSize?: number) => {
    if (activeProvider === 'go' && goCore.getIsReady()) {
      return await goCore.wintunSendTestPacket(payloadSize)
    }
    throw new Error('Go Core is not running')
  })

  // Phase 5: Packet Engine & Transport IPC
  ipcMain.handle('dualflow:packet-get-status', async () => {
    if (activeProvider === 'go' && goCore.getIsReady()) {
      return await goCore.packetGetStatus()
    }
    throw new Error('Go Core is not running')
  })

  ipcMain.handle('dualflow:packet-get-stats', async () => {
    if (activeProvider === 'go' && goCore.getIsReady()) {
      return await goCore.packetGetStats()
    }
    throw new Error('Go Core is not running')
  })

  ipcMain.handle('dualflow:packet-set-test-mode', async (_event, enabled: boolean) => {
    if (activeProvider === 'go' && goCore.getIsReady()) {
      return await goCore.packetSetTestMode(enabled)
    }
    throw new Error('Go Core is not running')
  })

  ipcMain.handle('dualflow:packet-inject-test', async (_event, options?: { ipVersion?: number; payloadSize?: number; corrupt?: boolean }) => {
    if (activeProvider === 'go' && goCore.getIsReady()) {
      return await goCore.packetInjectTest(options)
    }
    throw new Error('Go Core is not running')
  })

  ipcMain.handle('dualflow:packet-reset-stats', async () => {
    if (activeProvider === 'go' && goCore.getIsReady()) {
      return await goCore.packetResetStats()
    }
    throw new Error('Go Core is not running')
  })

  // Phase 6: Multi-Path Bonding Engine IPC Handlers
  ipcMain.handle('dualflow:multipath-get-status', async () => {
    if (activeProvider === 'go' && goCore.getIsReady()) {
      return await goCore.multipathGetStatus()
    }
    throw new Error('Go Core is not running')
  })

  ipcMain.handle('dualflow:multipath-set-mode', async (_event, mode: string) => {
    if (activeProvider === 'go' && goCore.getIsReady()) {
      return await goCore.multipathSetMode(mode)
    }
    throw new Error('Go Core is not running')
  })

  ipcMain.handle('dualflow:multipath-set-weights', async (_event, weights: Record<string, number>) => {
    if (activeProvider === 'go' && goCore.getIsReady()) {
      return await goCore.multipathSetWeights(weights)
    }
    throw new Error('Go Core is not running')
  })

  // Wire Go Core push events or fallback to TS watcher
  if (activeProvider === 'go' && goCore.getIsReady()) {
    goCore.on('telemetry_update', (data) => {
      if (win && !win.isDestroyed()) {
        const modemUpdate = {
          pathId: 'modem',
          rxBps: data.path1?.rxBps || 0,
          txBps: data.path1?.txBps || 0,
          rttMs: data.path1?.rttMs ?? null,
          rttMinMs: data.path1?.rttMinMs ?? null,
          rttMaxMs: data.path1?.rttMaxMs ?? null,
          jitterMs: data.path1?.jitterMs ?? null,
          lossPercent: data.path1?.lossPercent ?? null,
          probeTarget: '1.1.1.1:53',
          lastProbeTimestamp: new Date().toLocaleTimeString(),
          telemetryPoint: {
            timestamp: Date.now(),
            rttMs: data.path1?.rttMs ?? null,
            rxMbps: data.path1?.rxMbps || 0,
            txMbps: data.path1?.txMbps || 0,
            loss: data.path1?.lossPercent || 0,
          },
        }

        const androidUpdate = {
          pathId: 'android',
          rxBps: data.path2?.rxBps || 0,
          txBps: data.path2?.txBps || 0,
          rttMs: data.path2?.rttMs ?? null,
          rttMinMs: data.path2?.rttMinMs ?? null,
          rttMaxMs: data.path2?.rttMaxMs ?? null,
          jitterMs: data.path2?.jitterMs ?? null,
          lossPercent: data.path2?.lossPercent ?? null,
          probeTarget: '1.1.1.1:53',
          lastProbeTimestamp: new Date().toLocaleTimeString(),
          telemetryPoint: {
            timestamp: Date.now(),
            rttMs: data.path2?.rttMs ?? null,
            rxMbps: data.path2?.rxMbps || 0,
            txMbps: data.path2?.txMbps || 0,
            loss: data.path2?.lossPercent || 0,
          },
        }

        win.webContents.send('dualflow:telemetry-updated', {
          modem: modemUpdate,
          android: androidUpdate,
        })

        if (data.wintun) {
          win.webContents.send('dualflow:wintun-stats-updated', data.wintun)
        }

        if (data.packet || data.transport) {
          win.webContents.send('dualflow:packet-stats-updated', {
            pipeline: data.packet,
            transport: data.transport,
          })
        }

        if (data.multipath) {
          win.webContents.send('dualflow:multipath-stats-updated', data.multipath)
        }
      }
    })

    goCore.on('adapters_update', (adapters) => {
      cachedAdapters = adapters
      if (win && !win.isDestroyed()) {
        win.webContents.send('dualflow:adapters-updated', adapters)
      }
    })

    // Subscribe to periodic core telemetry stream
    goCore.subscribe().catch(console.warn)
  } else {
    // TypeScript Fallback Polling Loop
    const watcher = tsDiscovery.startLiveWatcher((adapters) => {
      cachedAdapters = adapters
      if (win && !win.isDestroyed()) {
        win.webContents.send('dualflow:adapters-updated', adapters)
      }
    })

    const pollTsTelemetry = async () => {
      try {
        const primary =
          cachedAdapters.find(
            (a) => (a.classification === 'modem' || a.mediaType === 'ethernet') && a.status === 'connected'
          ) ?? null
        const android =
          cachedAdapters.find((a) => a.classification === 'android' && a.status === 'connected') ?? null

        const throughputMap = await tsTelemetry.sampleInterfaceThroughput()
        const [modemUpdate, androidUpdate] = await Promise.all([
          tsTelemetry.probePath('modem', primary?.ipv4 ?? null, '1.1.1.1'),
          tsTelemetry.probePath('android', android?.ipv4 ?? null, '1.1.1.1'),
        ])

        const primaryStats = primary ? throughputMap.get(primary.name.toLowerCase()) : null
        const androidStats = android ? throughputMap.get(android.name.toLowerCase()) : null

        modemUpdate.rxBps = primaryStats?.rxBps ?? 0
        modemUpdate.txBps = primaryStats?.txBps ?? 0
        if (modemUpdate.telemetryPoint) {
          modemUpdate.telemetryPoint.rxMbps = primaryStats?.rxMbps ?? 0
          modemUpdate.telemetryPoint.txMbps = primaryStats?.txMbps ?? 0
        }

        androidUpdate.rxBps = androidStats?.rxBps ?? 0
        androidUpdate.txBps = androidStats?.txBps ?? 0
        if (androidUpdate.telemetryPoint) {
          androidUpdate.telemetryPoint.rxMbps = androidStats?.rxMbps ?? 0
          androidUpdate.telemetryPoint.txMbps = androidStats?.txMbps ?? 0
        }

        if (win && !win.isDestroyed()) {
          win.webContents.send('dualflow:telemetry-updated', {
            modem: modemUpdate,
            android: androidUpdate,
          })
        }
      } catch (err) {
        console.warn('[Telemetry TS Fallback] Error:', err)
      }
    }

    tsTelemetryTimer = setInterval(pollTsTelemetry, 1500)

    win.on('closed', () => {
      watcher.stop()
      if (tsTelemetryTimer) {
        clearInterval(tsTelemetryTimer)
        tsTelemetryTimer = null
      }
    })
  }

  win.on('closed', () => {
    goCore.stop().catch(console.error)
    win = null
  })

  if (VITE_DEV_SERVER_URL) {
    win.loadURL(VITE_DEV_SERVER_URL)
  } else {
    win.loadFile(path.join(RENDERER_DIST, 'index.html'))
  }
}

app.on('window-all-closed', () => {
  if (process.platform !== 'darwin') {
    goCore.stop().finally(() => {
      app.quit()
      win = null
    })
  }
})

app.on('activate', () => {
  if (BrowserWindow.getAllWindows().length === 0) {
    createWindow()
  }
})

app.whenReady().then(async () => {
  await initializeCoreEngine()
  await createWindow()
})
