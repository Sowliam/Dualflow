const { app, BrowserWindow, ipcMain } = require('electron')
const path = require('path')
const fs = require('fs')
const { execFile } = require('child_process')

const appRoot = path.join(__dirname, '..')
const rendererDist = path.join(appRoot, 'dist')
const screenshotDir = path.join(appRoot, 'screenshots')

if (!fs.existsSync(screenshotDir)) {
  fs.mkdirSync(screenshotDir, { recursive: true })
}

const consoleLogs = []
const consoleErrors = []

let win = null

function getPreloadPath() {
  const candidates = [
    path.join(appRoot, 'dist-electron', 'preload.cjs'),
    path.join(appRoot, 'dist-electron', 'preload.js'),
    path.join(__dirname, 'preload.cjs'),
  ]
  for (const candidate of candidates) {
    if (fs.existsSync(candidate)) return candidate
  }
  return path.join(appRoot, 'dist-electron', 'preload.cjs')
}

function queryWindowsAdapters() {
  return new Promise((resolve, reject) => {
    const scriptPath = path.join(__dirname, 'scripts', 'get-adapters.ps1')
    execFile(
      'powershell.exe',
      ['-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', scriptPath],
      { windowsHide: true, maxBuffer: 4 * 1024 * 1024 },
      (error, stdout, stderr) => {
        if (error) {
          console.error('[Verify] PowerShell query error:', error, stderr)
          return reject(error)
        }
        try {
          const raw = stdout.trim()
          if (!raw) return resolve([])
          const parsed = JSON.parse(raw)
          resolve(Array.isArray(parsed) ? parsed : [parsed])
        } catch (err) {
          reject(err)
        }
      }
    )
  })
}

async function runVerification() {
  console.log('[VERIFY Phase 2] Starting DualFlow Live Network Discovery Verification...')

  const preloadPath = getPreloadPath()
  console.log('[VERIFY] Using Preload:', preloadPath)

  win = new BrowserWindow({
    width: 1240,
    height: 820,
    minWidth: 1024,
    minHeight: 680,
    frame: false,
    backgroundColor: '#141618',
    show: true,
    webPreferences: {
      preload: preloadPath,
      contextIsolation: true,
      nodeIntegration: false,
      sandbox: true,
    },
  })

  // Window IPC handlers
  ipcMain.on('window:minimize', () => {
    console.log('[IPC] window:minimize received')
    win?.minimize()
  })

  ipcMain.on('window:maximize', () => {
    console.log('[IPC] window:maximize received')
    if (win?.isMaximized()) {
      win.unmaximize()
    } else {
      win?.maximize()
    }
  })

  ipcMain.on('window:close', () => {
    console.log('[IPC] window:close received')
    win?.close()
  })

  ipcMain.handle('window:isMaximized', () => {
    return win?.isMaximized() ?? false
  })

  // Network IPC handlers for real Windows IP Helper API query
  let latestAdapters = []
  ipcMain.handle('dualflow:get-adapters', async () => {
    console.log('[IPC] dualflow:get-adapters invoked -> Querying Windows IP Helper API...')
    latestAdapters = await queryWindowsAdapters()
    console.log(`[IPC] Discovered ${latestAdapters.length} real adapters on Windows host:`, 
      latestAdapters.map(a => `${a.name} (${a.classification}/${a.status})`).join(', ')
    )
    return latestAdapters
  })

  ipcMain.handle('dualflow:rescan-adapters', async () => {
    console.log('[IPC] dualflow:rescan-adapters invoked -> Forcing adapter refresh')
    latestAdapters = await queryWindowsAdapters()
    return latestAdapters
  })

  win.webContents.on('console-message', (_event, level, message, line, sourceId) => {
    const entry = { level, message, line, sourceId }
    consoleLogs.push(entry)
    if (level >= 3) {
      consoleErrors.push(entry)
      console.error(`[Renderer Console Error] ${message} (${sourceId}:${line})`)
    } else {
      console.log(`[Renderer Console] [L${level}] ${message}`)
    }
  })

  win.webContents.on('did-fail-load', (_event, errorCode, errorDescription, validatedURL) => {
    consoleErrors.push({ errorCode, errorDescription, validatedURL })
    console.error(`[Renderer Fail Load] ${errorCode}: ${errorDescription} (${validatedURL})`)
  })

  const indexPath = path.join(rendererDist, 'index.html')
  console.log('[VERIFY] Loading index file:', indexPath)
  await win.loadFile(indexPath)

  // 1. Wait for real Windows adapters to arrive in the renderer DOM
  console.log('[VERIFY] Waiting for real Windows adapter data to bind to DOM...')
  await win.webContents.executeJavaScript(`
    new Promise((resolve) => {
      const startTime = Date.now();
      const interval = setInterval(() => {
        const text = document.body.innerText;
        const hasRealEthernet = text.includes('Realtek PCIe GbE Family Controller') || text.includes('192.168.1.10');
        if (hasRealEthernet || Date.now() - startTime > 15000) {
          clearInterval(interval);
          resolve(hasRealEthernet);
        }
      }, 300);
    })
  `)

  // 2. Test Window Controls
  console.log('[VERIFY] Testing Window Controls...')
  const initialMaximized = win.isMaximized()
  win.maximize()
  await new Promise((r) => setTimeout(r, 200))
  const afterMax = win.isMaximized()
  win.unmaximize()
  await new Promise((r) => setTimeout(r, 200))
  const afterUnmax = win.isMaximized()

  // 3. Inspect real adapters in Dashboard view
  const dashboardAudit = await win.webContents.executeJavaScript(`
    (() => {
      const text = document.body.innerText;
      return {
        hasRealEthernet: text.includes('Realtek PCIe GbE Family Controller') || text.includes('Ethernet'),
        hasRealIp: text.includes('192.168.1.10'),
        hasRealGateway: text.includes('192.168.1.1'),
        hasRealMac: text.includes('D4-5D-64-F2-E3-1A'),
        hasRealMetric: text.includes('10 / 1500'),
        hasAndroidStandby: text.includes('Android USB tethering device not detected') || text.includes('PATH 2'),
        excerpt: text.substring(0, 600)
      };
    })()
  `)
  console.log('[VERIFY] Dashboard DOM Audit:', dashboardAudit)

  // 4. Capture all 6 tabs in sequence
  const tabs = [
    { id: 'dashboard', name: '01-dashboard.png', label: 'Dashboard' },
    { id: 'adapters', name: '02-adapters.png', label: 'Network Adapters' },
    { id: 'rules', name: '03-rules.png', label: 'Traffic Policies' },
    { id: 'gaming', name: '04-gaming.png', label: 'Gaming Optimizer' },
    { id: 'logs', name: '05-logs.png', label: 'System Logs' },
    { id: 'settings', name: '06-settings.png', label: 'Settings' },
  ]

  for (const tab of tabs) {
    console.log(`[VERIFY] Capturing Tab: ${tab.label} (${tab.id})...`)

    await win.webContents.executeJavaScript(`
      (() => {
        const buttons = Array.from(document.querySelectorAll('aside button'));
        const target = buttons.find(b => b.textContent.includes(${JSON.stringify(tab.label)}));
        if (target) {
          target.click();
          return true;
        }
        return false;
      })()
    `)

    await new Promise((r) => setTimeout(r, 600))

    const image = await win.webContents.capturePage()
    const outPath = path.join(screenshotDir, tab.name)
    fs.writeFileSync(outPath, image.toPNG())
    console.log(`[VERIFY] Saved screenshot: ${outPath}`)
  }

  // 5. Test Live Rescan Action & Notification
  console.log('[VERIFY] Testing live network change notification trigger...')
  await win.webContents.executeJavaScript(`
    (() => {
      const rescanBtn = Array.from(document.querySelectorAll('button')).find(b => b.textContent.includes('Rescan Adapters'));
      if (rescanBtn) rescanBtn.click();
    })()
  `)
  await new Promise((r) => setTimeout(r, 2000))

  // Write Phase 2 summary report to JSON
  const report = {
    timestamp: new Date().toISOString(),
    success: consoleErrors.length === 0 && dashboardAudit.hasRealEthernet && dashboardAudit.hasRealIp,
    discoveredAdaptersCount: latestAdapters.length,
    discoveredAdapters: latestAdapters.map(a => ({
      name: a.name,
      description: a.description,
      status: a.status,
      ipv4: a.ipv4,
      gateway: a.gateway,
      metric: a.metric,
      mtu: a.mtu,
      mac: a.mac,
      mediaType: a.mediaType,
      classification: a.classification,
      classificationConfidence: a.classificationConfidence,
      classificationReasons: a.classificationReasons,
    })),
    windowControls: {
      maximizeTested: afterMax === true && afterUnmax === false,
    },
    dashboardAudit,
    consoleErrors,
    screenshots: tabs.map(t => path.join('screenshots', t.name)),
  }

  fs.writeFileSync(path.join(appRoot, 'phase2-verification-report.json'), JSON.stringify(report, null, 2))
  console.log('[VERIFY] Phase 2 Verification Report successfully saved.')

  setTimeout(() => {
    app.quit()
    process.exit(0)
  }, 1000)
}

app.whenReady().then(runVerification)
