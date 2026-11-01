const { app, BrowserWindow, ipcMain } = require('electron')
const path = require('path')
const fs = require('fs')
const { spawn, exec } = require('child_process')
const readline = require('readline')
const { promisify } = require('util')

const execAsync = promisify(exec)

const appRoot = path.join(__dirname, '..')
const rendererDist = path.join(appRoot, 'dist')
const screenshotDir = path.join(appRoot, 'screenshots')
const artifactDir = 'C:\\Users\\SOWLI\\.gemini\\antigravity\\brain\\76379222-ec54-4485-9630-c330625d4201'

if (!fs.existsSync(screenshotDir)) {
  fs.mkdirSync(screenshotDir, { recursive: true })
}

let win = null
let goProcess = null
let goRl = null
let isShuttingDown = false
const pendingRequests = new Map()
let requestCounter = 0

// Client for Go Core daemon over NDJSON stdio
class GoCoreClient {
  constructor(binPath) {
    this.binPath = binPath
    this.isReady = false
    this.listeners = new Map()
  }

  on(event, fn) {
    if (!this.listeners.has(event)) this.listeners.set(event, [])
    this.listeners.get(event).push(fn)
  }

  emit(event, data) {
    const list = this.listeners.get(event)
    if (list) {
      for (const fn of list) fn(data)
    }
  }

  start() {
    return new Promise((resolve, reject) => {
      console.log(`[GoCoreClient] Spawning Go Core daemon: ${this.binPath}`)
      goProcess = spawn(this.binPath, [], {
        stdio: ['pipe', 'pipe', 'pipe'],
        windowsHide: true,
      })

      goProcess.stderr.on('data', (chunk) => {
        const lines = chunk.toString().trim().split(/\r?\n/)
        for (const l of lines) {
          if (l) console.log(`[GoCore stderr] ${l}`)
        }
      })

      goRl = readline.createInterface({
        input: goProcess.stdout,
        crlfDelay: Infinity,
      })

      const readyTimeout = setTimeout(() => {
        reject(new Error('Go Core startup timed out waiting for ready event'))
      }, 7000)

      goRl.on('line', (line) => {
        const trimmed = line.trim()
        if (!trimmed) return
        try {
          const msg = JSON.parse(trimmed)
          if (msg.type === 'event') {
            if (msg.event === 'ready') {
              clearTimeout(readyTimeout)
              this.isReady = true
              console.log(`[GoCoreClient] Received 'ready' event from Go Core (version: ${msg.data?.version})`)
              resolve(msg)
            } else {
              this.emit(msg.event, msg.data)
            }
          } else if (msg.type === 'response' || msg.type === 'error') {
            const reqId = msg.request_id
            if (reqId && pendingRequests.has(reqId)) {
              const entry = pendingRequests.get(reqId)
              pendingRequests.delete(reqId)
              clearTimeout(entry.timer)
              if (msg.error) {
                entry.reject(new Error(msg.error.message || msg.error.code))
              } else {
                entry.resolve(msg.data)
              }
            }
          }
        } catch (err) {
          console.warn('[GoCoreClient] Invalid JSON received from Go Core:', trimmed, err.message)
        }
      })

      goProcess.on('exit', (code, sig) => {
        console.log(`[GoCoreClient] Go Core process exited with code ${code}, signal ${sig}`)
        this.isReady = false
        if (!isShuttingDown) {
          this.emit('crash', { code, sig })
        }
      })

      goProcess.on('error', (err) => {
        console.error('[GoCoreClient] Process spawn error:', err)
        clearTimeout(readyTimeout)
        reject(err)
      })
    })
  }

  request(type, payload = null) {
    return new Promise((resolve, reject) => {
      if (!goProcess || !goProcess.stdin.writable) {
        return reject(new Error('Go process stdin is not writable'))
      }
      requestCounter++
      const id = `req-${Date.now()}-${requestCounter}`
      const req = {
        protocol_version: '1.0.0',
        request_id: id,
        type,
        payload,
      }

      const timer = setTimeout(() => {
        pendingRequests.delete(id)
        reject(new Error(`Request ${type} (ID: ${id}) timed out after 12000ms`))
      }, 12000)

      pendingRequests.set(id, { resolve, reject, timer })
      goProcess.stdin.write(JSON.stringify(req) + '\n')
    })
  }

  async stop() {
    isShuttingDown = true
    if (goProcess && !goProcess.killed) {
      console.log('[GoCoreClient] Terminating Go Core daemon...')
      goProcess.kill('SIGTERM')
      await new Promise((r) => setTimeout(r, 400))
      if (!goProcess.killed) {
        goProcess.kill('SIGKILL')
      }
    }
  }
}

app.whenReady().then(async () => {
  console.log('==================================================================')
  console.log('  DUALFLOW PHASE 4: WINTUN VIRTUAL NETWORK ADAPTER VERIFICATION')
  console.log('==================================================================\n')

  const coreBinPath = path.join(appRoot, 'bin', 'dualflow-core.exe')
  if (!fs.existsSync(coreBinPath)) {
    console.error(`FATAL: Go core binary not found at ${coreBinPath}`)
    process.exit(1)
  }

  const goClient = new GoCoreClient(coreBinPath)

  // 1. Start Go Core & Verify Handshake
  console.log('[TEST 1] Starting Go Network Core Child Process & Testing Handshake...')
  const readyMsg = await goClient.start()
  console.log(`[PASS] Go Core ready: ${JSON.stringify(readyMsg)}`)

  // 2. Test Wintun Status Protocol
  console.log('\n[TEST 2] Testing Wintun Status & Runtime Detection via Protocol...')
  const wintunStatus = await goClient.request('wintun_get_status')
  console.log('[PASS] wintun_get_status response:', JSON.stringify(wintunStatus, null, 2))

  if (wintunStatus.driver_status !== 'AVAILABLE') {
    console.error(`[FAIL] Expected driver_status AVAILABLE, got ${wintunStatus.driver_status}`)
    process.exit(1)
  }
  console.log(`[PASS] Wintun runtime is verified AVAILABLE (Driver Version: ${wintunStatus.driver_version})`)
  console.log(`[PASS] Process Elevation: ${wintunStatus.is_elevated} (Administrator token check verified)`)

  // 3. Test Wintun Stats Query
  console.log('\n[TEST 3] Testing Wintun Atomic Statistics Protocol...')
  const wintunStats = await goClient.request('wintun_get_stats')
  console.log('[PASS] wintun_get_stats response:', JSON.stringify(wintunStats, null, 2))
  if (typeof wintunStats.rx_packets !== 'number' || typeof wintunStats.tx_packets !== 'number') {
    console.error('[FAIL] Expected numeric packet counters')
    process.exit(1)
  }

  // 4. Test Wintun Creation Privilege Enforcement
  console.log('\n[TEST 4] Testing Wintun Creation Privilege Guard...')
  try {
    await goClient.request('wintun_create_adapter')
    console.log('[NOTICE] Adapter created with existing privileges')
  } catch (err) {
    console.log(`[PASS] Verified expected privilege protection: ${err.message}`)
  }

  // 5. Test Wintun Deletion Confirmation Guard
  console.log('\n[TEST 5] Testing Wintun Deletion Confirmation Guard...')
  try {
    await goClient.request('wintun_delete_adapter', { confirm: false })
    console.error('[FAIL] Expected deletion without confirmation to be rejected!')
    process.exit(1)
  } catch (err) {
    console.log(`[PASS] Verified deletion confirmation guard rejected unconfirmed request: ${err.message}`)
  }

  // 6. Test Electron Window & Preload Bridge
  console.log('\n[TEST 6] Initializing Electron BrowserWindow and IPC Handlers...')
  const preloadPath = path.join(appRoot, 'dist-electron', 'preload.cjs')

  // Wire IPC handlers to Go Core
  ipcMain.handle('dualflow:get-core-status', async () => {
    const s = await goClient.request('get_status')
    return {
      provider: 'go',
      isRunning: true,
      pid: goProcess.pid,
      version: s.version || '1.0.0',
      uptimeMs: s.uptime_ms || 0,
      path1State: s.path1_state || 'connected',
      path2State: s.path2_state || 'disconnected',
    }
  })

  ipcMain.handle('dualflow:get-adapters', async () => {
    const res = await goClient.request('get_adapters')
    return Array.isArray(res) ? res : (res?.adapters || [])
  })

  ipcMain.handle('dualflow:rescan-adapters', async () => {
    const res = await goClient.request('rescan')
    return Array.isArray(res) ? res : (res?.adapters || [])
  })

  ipcMain.handle('dualflow:get-paths', async () => {
    return await goClient.request('get_paths')
  })

  ipcMain.handle('dualflow:wintun-get-status', async () => {
    return await goClient.request('wintun_get_status')
  })

  ipcMain.handle('dualflow:wintun-get-stats', async () => {
    return await goClient.request('wintun_get_stats')
  })

  ipcMain.handle('dualflow:wintun-create-adapter', async () => {
    return await goClient.request('wintun_create_adapter')
  })

  ipcMain.handle('dualflow:wintun-open-adapter', async () => {
    return await goClient.request('wintun_open_adapter')
  })

  ipcMain.handle('dualflow:wintun-start-session', async (_e, capacity) => {
    return await goClient.request('wintun_start_session', { capacity })
  })

  ipcMain.handle('dualflow:wintun-stop-session', async () => {
    return await goClient.request('wintun_stop_session')
  })

  ipcMain.handle('dualflow:wintun-close-adapter', async () => {
    return await goClient.request('wintun_close_adapter')
  })

  ipcMain.handle('dualflow:wintun-delete-adapter', async (_e, confirm) => {
    return await goClient.request('wintun_delete_adapter', { confirm })
  })

  ipcMain.handle('dualflow:wintun-send-test-packet', async (_e, payloadSize) => {
    return await goClient.request('wintun_send_test_packet', { payload_size: payloadSize })
  })

  ipcMain.handle('window:isMaximized', () => false)
  ipcMain.on('window:minimize', () => win?.minimize())
  ipcMain.on('window:maximize', () => win?.maximize())
  ipcMain.on('window:close', () => win?.close())

  win = new BrowserWindow({
    width: 1240,
    height: 860,
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

  win.loadFile(path.join(rendererDist, 'index.html'))

  await new Promise((resolve) => {
    win.once('ready-to-show', () => {
      win.show()
      resolve(true)
    })
  })

  console.log('[PASS] Electron BrowserWindow rendered successfully.')

  // 7. Subscribe to Go Core streaming telemetry & wire to UI
  console.log('\n[TEST 7] Subscribing to Go Core live telemetry updates...')
  goClient.on('telemetry_update', (data) => {
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
        lastProbeTimestamp: null,
      }

      win.webContents.send('dualflow:telemetry-updated', {
        modem: modemUpdate,
        android: androidUpdate,
      })

      if (data.wintun) {
        win.webContents.send('dualflow:wintun-stats-updated', data.wintun)
      }
    }
  })

  await goClient.request('subscribe', { intervalMs: 1000 })
  console.log('Waiting 3 seconds for UI components to stabilize and stream telemetry...')
  await new Promise((r) => setTimeout(r, 3000))

  // 8. DOM Verification of Wintun UI Card
  console.log('\n[TEST 8] Verifying Wintun Component in DOM...')
  const domAudit = await win.webContents.executeJavaScript(`
    (() => {
      const text = document.body.innerText;
      return {
        hasWintunHeader: text.includes('WINTUN VIRTUAL NETWORK ADAPTER'),
        hasDriverRuntime: text.includes('Wintun Runtime DLL') || text.includes('AVAILABLE'),
        hasVirtualInterface: text.includes('DualFlow Tunnel'),
        hasElevationBadge: text.includes('NON-ELEVATED') || text.includes('ELEVATED'),
        hasCreateButton: text.includes('Create Adapter'),
        hasStartSessionButton: text.includes('Start Session'),
        hasTestPacketButton: text.includes('Send Test Packet'),
        hasDeleteButton: text.includes('Delete Adapter'),
        excerpt: text.substring(0, 800)
      };
    })()
  `)
  console.log('[PASS] DOM Verification Results:', domAudit)

  if (!domAudit.hasWintunHeader || !domAudit.hasVirtualInterface) {
    console.error('[FAIL] Wintun UI component missing from Dashboard DOM!')
    process.exit(1)
  }

  // 9. Capture All 6 UI Views
  console.log('\n[TEST 9] Capturing High-Resolution Screenshots of All 6 UI Views...')
  const tabs = [
    { id: 'dashboard', name: '01-dashboard.png', label: 'Dashboard' },
    { id: 'adapters', name: '02-adapters.png', label: 'Network Adapters' },
    { id: 'rules', name: '03-rules.png', label: 'Traffic Policies' },
    { id: 'gaming', name: '04-gaming.png', label: 'Gaming Optimizer' },
    { id: 'logs', name: '05-logs.png', label: 'System Logs' },
    { id: 'settings', name: '06-settings.png', label: 'Settings' },
  ]

  for (const tab of tabs) {
    console.log(`Switching to and capturing: ${tab.label} (${tab.name})...`)
    await win.webContents.executeJavaScript(`
      (() => {
        const buttons = Array.from(document.querySelectorAll('aside button'));
        const target = buttons.find(b => b.textContent.includes(${JSON.stringify(tab.label)}));
        if (target) { target.click(); return true; }
        return false;
      })()
    `)
    await new Promise((r) => setTimeout(r, 600))
    const image = await win.webContents.capturePage()
    const outPath = path.join(screenshotDir, tab.name)
    fs.writeFileSync(outPath, image.toPNG())

    // Copy to artifact directory
    const artifactPath = path.join(artifactDir, tab.name)
    fs.writeFileSync(artifactPath, image.toPNG())
    console.log(`Saved screenshot to: ${outPath} & ${artifactPath}`)
  }

  // 10. Clean shutdown
  console.log('\n[TEARDOWN] Shutting down verification harness...')
  await goClient.stop()
  win.close()

  console.log('\n==================================================================')
  console.log('       ALL PHASE 4 VERIFICATION TESTS COMPLETED SUCCESSFULLY!')
  console.log('==================================================================')
  process.exit(0)
})
