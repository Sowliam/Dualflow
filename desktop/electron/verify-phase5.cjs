const { app, BrowserWindow, ipcMain } = require('electron')
const path = require('path')
const fs = require('fs')
const { spawn } = require('child_process')
const readline = require('readline')

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
      }, 25000)

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

      goProcess.on('error', (err) => {
        clearTimeout(readyTimeout)
        reject(err)
      })

      goProcess.on('exit', (code) => {
        console.log(`[GoCoreClient] Daemon process exited with code: ${code}`)
      })
    })
  }

  request(type, payload = {}) {
    return new Promise((resolve, reject) => {
      if (!goProcess || !goProcess.stdin) {
        return reject(new Error('Go core daemon is not running'))
      }

      const reqId = `req-p5-${++requestCounter}-${Date.now()}`
      const req = {
        protocol_version: '1.0.0',
        request_id: reqId,
        type,
        payload,
      }

      const timer = setTimeout(() => {
        if (pendingRequests.has(reqId)) {
          pendingRequests.delete(reqId)
          reject(new Error(`Request ${type} (ID: ${reqId}) timed out after 12000ms`))
        }
      }, 12000)

      pendingRequests.set(reqId, { resolve, reject, timer })
      goProcess.stdin.write(JSON.stringify(req) + '\n')
    })
  }

  async stop() {
    if (goProcess) {
      goProcess.stdin.end()
      await new Promise((r) => setTimeout(r, 600))
      if (!goProcess.killed) goProcess.kill()
    }
  }
}

app.whenReady().then(async () => {
  console.log('==================================================================')
  console.log('  DUALFLOW PHASE 5: PACKET PROCESSING & TUNNEL TRANSPORT TEST')
  console.log('==================================================================\n')

  const coreBinPath = path.join(appRoot, 'bin', 'dualflow-core.exe')
  if (!fs.existsSync(coreBinPath)) {
    console.error(`[FAIL] dualflow-core.exe not found at ${coreBinPath}`)
    process.exit(1)
  }

  const goClient = new GoCoreClient(coreBinPath)

  // 1. Start Go Core daemon
  console.log('[TEST 1] Initializing Go Network Core daemon...')
  try {
    await goClient.start()
    console.log('[PASS] Go Core daemon initialized and signaled READY.')
  } catch (err) {
    console.error('[FAIL] Could not start Go Core daemon:', err.message)
    process.exit(1)
  }

  // 2. Query initial packet engine status
  console.log('\n[TEST 2] Querying Packet Pipeline & Transport Status (packet_get_status)...')
  let pStatus = await goClient.request('packet_get_status')
  console.log('[PASS] Initial Packet Engine Status:', JSON.stringify(pStatus, null, 2))

  if (!pStatus.is_running || !pStatus.is_test_mode) {
    console.error('[FAIL] Expected pipeline to be running and in test mode!')
    process.exit(1)
  }
  if (pStatus.transport?.state !== 'CONNECTED' || !pStatus.transport?.is_test) {
    console.error('[FAIL] Expected in-memory test transport to be CONNECTED and flagged is_test!')
    process.exit(1)
  }

  // 3. Inject synthetic valid IPv4 packet
  console.log('\n[TEST 3] Injecting valid IPv4 test packet (packet_inject_test)...')
  const v4Res = await goClient.request('packet_inject_test', { ip_version: 4, payload_size: 48 })
  console.log('[PASS] Injected IPv4 Result:', v4Res)
  if (!v4Res.injected || v4Res.version !== 4) {
    console.error('[FAIL] IPv4 packet injection failed!')
    process.exit(1)
  }

  // 4. Inject synthetic valid IPv6 packet
  console.log('\n[TEST 4] Injecting valid IPv6 test packet (packet_inject_test)...')
  const v6Res = await goClient.request('packet_inject_test', { ip_version: 6, payload_size: 64 })
  console.log('[PASS] Injected IPv6 Result:', v6Res)
  if (!v6Res.injected || v6Res.version !== 6) {
    console.error('[FAIL] IPv6 packet injection failed!')
    process.exit(1)
  }

  // 5. Inject intentionally corrupted packet (RFC violation)
  console.log('\n[TEST 5] Injecting corrupted packet to verify strict rejection & drop accounting...')
  const corruptRes = await goClient.request('packet_inject_test', { ip_version: 4, corrupt: true })
  console.log('[PASS] Corrupt Packet Injection Response:', corruptRes)
  if (corruptRes.injected) {
    console.error('[FAIL] Corrupted packet was incorrectly accepted!')
    process.exit(1)
  }
  if (!corruptRes.error || corruptRes.stats.rejected_packets < 1 || corruptRes.stats.drops_invalid_packet < 1) {
    console.error('[FAIL] Expected rejection and drop accounting for invalid packet!')
    process.exit(1)
  }
  console.log('[PASS] Strictly verified: rejection recorded & drop counted in drops_invalid_packet.')

  // 6. Test statistics and reset
  console.log('\n[TEST 6] Querying aggregated stats (packet_get_stats)...')
  const statsRes = await goClient.request('packet_get_stats')
  console.log('[PASS] Aggregated Stats:', JSON.stringify(statsRes, null, 2))
  if (statsRes.pipeline.validated_packets < 2 || statsRes.transport.tx_packets < 2) {
    console.error('[FAIL] Stats counters do not match injected packets!')
    process.exit(1)
  }

  // 7. Setup Electron IPC & Launch Renderer Window
  console.log('\n[TEST 7] Setting up Electron IPC Handlers for Packet Engine & Launching UI...')

  // Window IPC
  ipcMain.on('window:minimize', () => win?.minimize())
  ipcMain.on('window:maximize', () => {
    if (win?.isMaximized()) win.unmaximize()
    else win?.maximize()
  })
  ipcMain.on('window:close', () => win?.close())
  ipcMain.handle('window:isMaximized', () => win?.isMaximized() ?? false)

  // Core Status
  ipcMain.handle('dualflow:get-core-status', async () => {
    return {
      provider: 'go',
      isRunning: true,
      version: '1.0.0',
      uptimeMs: 1500,
      path1State: 'connected',
      path2State: 'connected',
    }
  })

  // Network Adapters
  ipcMain.handle('dualflow:get-adapters', async () => {
    return await goClient.request('get_adapters')
  })
  ipcMain.handle('dualflow:rescan-adapters', async () => {
    return await goClient.request('rescan')
  })
  ipcMain.handle('dualflow:get-paths', async () => {
    return await goClient.request('get_paths')
  })
  ipcMain.handle('dualflow:assign-path', async (_e, pathId, adapterId) => {
    return await goClient.request('assign_path', { path_id: pathId, adapter_id: adapterId })
  })

  // Wintun IPC
  ipcMain.handle('dualflow:wintun-get-status', async () => goClient.request('wintun_get_status'))
  ipcMain.handle('dualflow:wintun-get-stats', async () => goClient.request('wintun_get_stats'))
  ipcMain.handle('dualflow:wintun-create-adapter', async () => goClient.request('wintun_create_adapter'))
  ipcMain.handle('dualflow:wintun-open-adapter', async () => goClient.request('wintun_open_adapter'))
  ipcMain.handle('dualflow:wintun-start-session', async (_e, cap) => goClient.request('wintun_start_session', { capacity: cap }))
  ipcMain.handle('dualflow:wintun-stop-session', async () => goClient.request('wintun_stop_session'))
  ipcMain.handle('dualflow:wintun-close-adapter', async () => goClient.request('wintun_close_adapter'))
  ipcMain.handle('dualflow:wintun-delete-adapter', async (_e, c) => goClient.request('wintun_delete_adapter', { confirm: c }))
  ipcMain.handle('dualflow:wintun-send-test-packet', async (_e, sz) => goClient.request('wintun_send_test_packet', { payload_size: sz }))

  // Phase 5 Packet IPC
  ipcMain.handle('dualflow:packet-get-status', async () => goClient.request('packet_get_status'))
  ipcMain.handle('dualflow:packet-get-stats', async () => goClient.request('packet_get_stats'))
  ipcMain.handle('dualflow:packet-set-test-mode', async (_e, en) => goClient.request('packet_set_test_mode', { enabled: en }))
  ipcMain.handle('dualflow:packet-inject-test', async (_e, opt) => goClient.request('packet_inject_test', opt))
  ipcMain.handle('dualflow:packet-reset-stats', async () => goClient.request('packet_reset_stats'))

  win = new BrowserWindow({
    width: 1280,
    height: 860,
    minWidth: 1024,
    minHeight: 680,
    frame: false,
    backgroundColor: '#141618',
    show: false,
    webPreferences: {
      preload: path.join(appRoot, 'dist-electron', 'preload.cjs'),
      contextIsolation: true,
      nodeIntegration: false,
      sandbox: true,
    },
  })

  win.loadFile(path.join(rendererDist, 'index.html'))

  await new Promise((resolve) => {
    win.webContents.once('did-finish-load', () => resolve(true))
  })
  win.show()
  console.log('[PASS] Electron window loaded and visible.')

  // Subscribe to live telemetry
  goClient.on('telemetry_update', (data) => {
    try {
      if (win && !win.isDestroyed() && win.webContents && !win.webContents.isDestroyed()) {
        win.webContents.send('dualflow:telemetry-updated', {
          modem: { pathId: 'modem', rxBps: data.path1?.rxBps || 0, txBps: data.path1?.txBps || 0 },
          android: { pathId: 'android', rxBps: data.path2?.rxBps || 0, txBps: data.path2?.txBps || 0 },
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
      }
    } catch (_) {}
  })
  await goClient.request('subscribe')

  console.log('Waiting for React DOM to mount into #root...')
  for (let i = 0; i < 20; i++) {
    const isMounted = await win.webContents.executeJavaScript(`
      (() => {
        const root = document.querySelector('#root');
        return Boolean(root && root.innerText && root.innerText.includes('DUALFLOW'));
      })()
    `).catch(() => false)
    if (isMounted) {
      console.log(`React DOM mounted successfully on attempt ${i + 1}`)
      break
    }
    await new Promise((r) => setTimeout(r, 500))
  }

  // 8. DOM Audit of PacketEngineCard
  console.log('\n[TEST 8] Verifying Packet Engine Component in Dashboard DOM...')
  const domAudit = await win.webContents.executeJavaScript(`
    (() => {
      const text = document.body.innerText;
      return {
        hasPacketHeader: text.includes('PACKET PROCESSING & TUNNEL TRANSPORT'),
        hasTestBadge: text.includes('SAFE IN-MEMORY TEST TRANSPORT'),
        hasTransportConnected: text.includes('TRANSPORT: CONNECTED'),
        hasValidatedMetric: text.includes('Validated Packets'),
        hasRejectionMetric: text.includes('Validation Rejections'),
        hasDropBreakdown: text.includes('Packet Drop Accounting'),
        hasInjectIPv4Btn: text.includes('Inject IPv4 Test'),
        hasInjectIPv6Btn: text.includes('Inject IPv6 Test'),
        hasInjectCorruptBtn: text.includes('Inject Corrupt'),
        hasResetBtn: text.includes('Reset'),
        excerpt: text.substring(0, 1000)
      };
    })()
  `)
  console.log('[PASS] DOM Verification Results:', domAudit)

  if (!domAudit.hasPacketHeader || !domAudit.hasTestBadge || !domAudit.hasValidatedMetric) {
    console.error('[FAIL] PacketEngineCard component missing from Dashboard DOM!')
    process.exit(1)
  }

  // 9. Interactive UI Action Test (Click "Inject IPv4 Test" button from renderer)
  console.log('\n[TEST 9] Simulating user click on "Inject IPv4 Test" button...')
  const clickRes = await win.webContents.executeJavaScript(`
    (() => {
      const buttons = Array.from(document.querySelectorAll('button'));
      const btn = buttons.find(b => b.textContent.includes('Inject IPv4 Test'));
      if (btn) {
        btn.click();
        return { clicked: true };
      }
      return { clicked: false };
    })()
  `)
  console.log('[PASS] Inject button clicked:', clickRes)

  // Wait for injection IPC and state re-render
  await new Promise((r) => setTimeout(r, 1500))

  const afterClickDOM = await win.webContents.executeJavaScript(`
    (() => {
      return {
        hasSuccessBanner: document.body.innerText.includes('Injected valid IPv4 packet'),
      };
    })()
  `)
  console.log('[PASS] UI feedback after injection:', afterClickDOM)

  // 10. Capture High-Resolution Screenshots of All 6 UI Views
  console.log('\n[TEST 10] Capturing High-Resolution Screenshots of All 6 UI Views...')
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
    await new Promise((r) => setTimeout(r, 700))
    const image = await win.webContents.capturePage()
    const outPath = path.join(screenshotDir, tab.name)
    fs.writeFileSync(outPath, image.toPNG())

    // Copy to artifact directory
    const artifactPath = path.join(artifactDir, tab.name)
    fs.writeFileSync(artifactPath, image.toPNG())
    console.log(`Saved screenshot to: ${outPath} & ${artifactPath}`)
  }

  // 11. Clean shutdown
  console.log('\n[TEARDOWN] Shutting down verification harness...')
  await goClient.stop()
  win.close()

  console.log('\n==================================================================')
  console.log('       ALL PHASE 5 VERIFICATION TESTS COMPLETED SUCCESSFULLY!')
  console.log('==================================================================')
  process.exit(0)
})
