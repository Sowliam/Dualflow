import { app, BrowserWindow, ipcMain } from 'electron'
import path from 'node:path'
import fs from 'node:fs'
import { fileURLToPath } from 'node:url'

const __dirname = path.dirname(fileURLToPath(import.meta.url))
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
    path.join(appRoot, 'dist-electron', 'preload.js'),
    path.join(appRoot, 'dist-electron', 'preload.mjs'),
    path.join(__dirname, 'preload.js'),
  ]
  for (const candidate of candidates) {
    if (fs.existsSync(candidate)) return candidate
  }
  return path.join(appRoot, 'dist-electron', 'preload.js')
}

async function runVerification() {
  console.log('[VERIFY] Starting DualFlow Electron Phase 1 Verification...')

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

  // Wait 1.5s for initial layout & fonts to settle
  await new Promise((r) => setTimeout(r, 1500))

  // 1. Verify window IPC & titlebar button actions
  console.log('[VERIFY] Testing Window Controls...')
  const initialMaximized = win.isMaximized()
  console.log('[VERIFY] Initial isMaximized:', initialMaximized)

  win.maximize()
  await new Promise((r) => setTimeout(r, 400))
  const afterMax = win.isMaximized()
  console.log('[VERIFY] After maximize isMaximized:', afterMax)

  win.unmaximize()
  await new Promise((r) => setTimeout(r, 400))
  const afterUnmax = win.isMaximized()
  console.log('[VERIFY] After unmaximize isMaximized:', afterUnmax)

  // 2. Test Tabs & Capture Screenshots
  const tabs = [
    { id: 'dashboard', name: '01-dashboard.png', label: 'Dashboard' },
    { id: 'adapters', name: '02-adapters.png', label: 'Network Adapters' },
    { id: 'rules', name: '03-rules.png', label: 'Traffic Policies' },
    { id: 'gaming', name: '04-gaming.png', label: 'Gaming Optimizer' },
    { id: 'logs', name: '05-logs.png', label: 'System Logs' },
    { id: 'settings', name: '06-settings.png', label: 'Settings' },
  ]

  for (const tab of tabs) {
    console.log(`[VERIFY] Switching to tab: ${tab.label} (${tab.id})...`)

    // Click navigation button for this tab
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

    // Wait for transition & canvas repaint
    await new Promise((r) => setTimeout(r, 500))

    // Capture screenshot
    const image = await win.webContents.capturePage()
    const outPath = path.join(screenshotDir, tab.name)
    fs.writeFileSync(outPath, image.toPNG())
    console.log(`[VERIFY] Saved screenshot: ${outPath} (${image.getSize().width}x${image.getSize().height})`)
  }

  // 3. Inspect DOM for fake data
  console.log('[VERIFY] Auditing DOM for fake data or marketing copy...')
  const domAudit = await win.webContents.executeJavaScript(`
    (() => {
      const text = document.body.innerText;
      const hasHype = /supercharge|seamless|blazing|unleash|magic|10x faster/i.test(text);
      const hasEmojis = /[\u{1F300}-\u{1F6FF}]/u.test(text);
      const hasDualFlowAPI = typeof window.dualflowAPI !== 'undefined';
      return {
        hasHype,
        hasEmojis,
        hasDualFlowAPI,
        textSample: text.substring(0, 300)
      };
    })()
  `)

  console.log('[VERIFY] DOM Audit result:', domAudit)
  console.log('[VERIFY] Total Console Errors:', consoleErrors.length)

  // Write summary report to JSON
  const report = {
    timestamp: new Date().toISOString(),
    success: consoleErrors.length === 0 && !domAudit.hasHype && !domAudit.hasEmojis && domAudit.hasDualFlowAPI,
    windowControls: {
      maximizeTested: afterMax === true && afterUnmax === false,
    },
    domAudit,
    consoleErrors,
    screenshots: tabs.map(t => path.join('screenshots', t.name)),
  }

  fs.writeFileSync(path.join(appRoot, 'verification-report.json'), JSON.stringify(report, null, 2))
  console.log('[VERIFY] Verification Report saved to verification-report.json')

  setTimeout(() => {
    app.quit()
    process.exit(0)
  }, 1000)
}

app.whenReady().then(runVerification)

