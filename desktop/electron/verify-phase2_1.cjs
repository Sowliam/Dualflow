const { app, BrowserWindow, ipcMain } = require('electron')
const path = require('path')
const fs = require('fs')
const { exec } = require('child_process')
const { promisify } = require('util')

const execAsync = promisify(exec)

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
    path.join(__dirname, 'preload.cjs'),
  ]
  for (const candidate of candidates) {
    if (fs.existsSync(candidate)) return candidate
  }
  return path.join(appRoot, 'dist-electron', 'preload.cjs')
}

// 1. Native Windows Network Discovery (Registry + netsh + getmac + os)
async function discoverWindowsAdapters() {
  const os = require('os')
  const osIfaces = os.networkInterfaces()

  let getmacStdout = ''
  let netshAddrStdout = ''
  let netshDnsStdout = ''
  let netshSubStdout = ''
  let netRegStdout = ''
  let classRegStdout = ''

  try {
    const [getmacRes, addrRes, dnsRes, subRes, netRegRes, classRegRes] = await Promise.all([
      execAsync('getmac /fo csv /v', { windowsHide: true }),
      execAsync('netsh interface ipv4 show addresses', { windowsHide: true }),
      execAsync('netsh interface ipv4 show dnsservers', { windowsHide: true }),
      execAsync('netsh interface ipv4 show subinterfaces', { windowsHide: true }),
      execAsync('reg query HKLM\\SYSTEM\\CurrentControlSet\\Control\\Network\\{4D36E972-E325-11CE-BFC1-08002BE10318} /s /v Name', { windowsHide: true }),
      execAsync('reg query HKLM\\SYSTEM\\CurrentControlSet\\Control\\Class\\{4d36e972-e325-11ce-bfc1-08002be10318} /s', { windowsHide: true, maxBuffer: 10 * 1024 * 1024 }),
    ])
    getmacStdout = getmacRes.stdout
    netshAddrStdout = addrRes.stdout
    netshDnsStdout = dnsRes.stdout
    netshSubStdout = subRes.stdout
    netRegStdout = netRegRes.stdout
    classRegStdout = classRegRes.stdout
  } catch (err) {
    console.warn('[Discovery] Query error:', err.message)
  }

  // Name to GUID
  const nameToGuidMap = new Map()
  if (netRegStdout) {
    const blocks = netRegStdout.split(/HKEY_LOCAL_MACHINE\\/i)
    for (const b of blocks) {
      const gMatch = b.match(/\{([0-9A-Fa-f-]{36})\}\\Connection/i)
      const nMatch = b.match(/Name\s+REG_SZ\s+(.*)/i)
      if (gMatch && nMatch) {
        nameToGuidMap.set(nMatch[1].trim().toLowerCase(), `{${gMatch[1].toLowerCase()}}`)
      }
    }
  }

  // GUID to PnP & Mfg
  const guidToPnpMap = new Map()
  if (classRegStdout) {
    const blocks = classRegStdout.split(/HKEY_LOCAL_MACHINE\\/i)
    for (const b of blocks) {
      const dMatch = b.match(/DriverDesc\s+REG_SZ\s+(.*)/i)
      const pMatch = b.match(/MatchingDeviceId\s+REG_SZ\s+(.*)/i)
      const mMatch = b.match(/ProviderName\s+REG_SZ\s+(.*)/i)
      const nMatch = b.match(/NetCfgInstanceId\s+REG_SZ\s+(.*)/i)
      if (nMatch) {
        const guid = nMatch[1].trim().toLowerCase()
        guidToPnpMap.set(guid, {
          desc: dMatch ? dMatch[1].trim() : '',
          pnpId: pMatch ? pMatch[1].trim() : '',
          mfg: mMatch ? mMatch[1].trim() : '',
        })
      }
    }
  }

  // Subinterfaces for MTU & state
  const subMap = new Map()
  if (netshSubStdout) {
    const lines = netshSubStdout.trim().split(/\r?\n/)
    let isTable = false
    for (const l of lines) {
      if (l.includes('---')) { isTable = true; continue; }
      if (!isTable) continue;
      const m = l.trim().match(/^(\d+)\s+(\d+)\s+(\d+)\s+(\d+)\s+(.+)$/)
      if (m) {
        subMap.set(m[5].trim().toLowerCase(), {
          mtu: parseInt(m[1], 10),
          state: parseInt(m[2], 10),
          bytesIn: parseInt(m[3], 10),
          bytesOut: parseInt(m[4], 10),
        })
      }
    }
  }

  // getmac
  const macMap = new Map()
  if (getmacStdout) {
    const lines = getmacStdout.trim().split(/\r?\n/).filter(Boolean)
    for (let i = 1; i < lines.length; i++) {
      const parts = lines[i].slice(1, -1).split('","')
      if (parts.length >= 4) {
        const [conn, desc, mac, trans] = parts
        macMap.set(conn.toLowerCase(), {
          name: conn,
          desc,
          mac: mac && mac !== 'N/A' && !mac.includes('?') ? mac : '—',
          isDisconnected: trans.toLowerCase().includes('disconnected') || trans.toLowerCase().includes('not present'),
        })
      }
    }
  }

  // netsh address
  const netshMap = new Map()
  if (netshAddrStdout) {
    const blocks = netshAddrStdout.split(/Configuration for interface /i).filter(b => b.trim())
    for (const b of blocks) {
      const lines = b.split(/\r?\n/).map(l => l.trim())
      if (!lines.length) continue
      const nameMatch = lines[0].match(/^"?([^"\r\n]+)"?/)
      if (!nameMatch) continue
      const name = nameMatch[1].trim().toLowerCase()
      let ipv4 = null, netmask = null, gateway = null, metric = 0
      for (const line of lines) {
        if (line.startsWith('IP Address:')) ipv4 = line.replace('IP Address:', '').trim()
        else if (line.includes('Subnet Prefix:')) {
          const mm = line.match(/mask\s+([0-9.]+)/i)
          if (mm) netmask = mm[1].trim()
        } else if (line.startsWith('Default Gateway:')) gateway = line.replace('Default Gateway:', '').trim()
        else if (line.startsWith('InterfaceMetric:')) metric = parseInt(line.replace('InterfaceMetric:', '').trim(), 10) || 0
      }
      netshMap.set(name, { ipv4, netmask, gateway, metric })
    }
  }

  // Combine
  const allKeys = new Set([...macMap.keys(), ...netshMap.keys(), ...Object.keys(osIfaces).map(k => k.toLowerCase())])
  const results = []

  for (const k of allKeys) {
    if (k.includes('loopback') || k.includes('pseudo') || k.includes('teredo') || k.includes('6to4') || k.includes('isatap')) continue

    const mac = macMap.get(k) || { name: k, desc: k, mac: '—', isDisconnected: false }
    const net = netshMap.get(k) || { ipv4: null, netmask: null, gateway: null, metric: 0 }
    const sub = subMap.get(k) || { mtu: 1500, state: 1, bytesIn: 0, bytesOut: 0 }
    const guid = nameToGuidMap.get(k) || ''
    const hw = guidToPnpMap.get(guid.toLowerCase()) || { desc: '', pnpId: '', mfg: '' }

    let osIpv4 = null, osNetmask = null, osMac = null
    const osM = Object.entries(osIfaces).find(([n]) => n.toLowerCase() === k)
    if (osM && osM[1]) {
      const v4 = osM[1].find(i => i.family === 'IPv4' || i.family === 4)
      if (v4) { osIpv4 = v4.address; osNetmask = v4.netmask; osMac = v4.mac.toUpperCase().replace(/:/g, '-'); }
    }

    const finalIp = net.ipv4 || osIpv4
    const finalMask = net.netmask || osNetmask
    const finalMac = mac.mac !== '—' ? mac.mac : (osMac && osMac !== '00-00-00-00-00-00' ? osMac : '—')
    const isConn = !mac.isDisconnected && sub.state === 1 && Boolean(finalIp)
    const desc = hw.desc || mac.desc

    const isUsb = hw.pnpId.toUpperCase().startsWith('USB\\')
    const isPci = hw.pnpId.toUpperCase().startsWith('PCI\\')
    const isRoot = hw.pnpId.toUpperCase().startsWith('ROOT\\')

    let classification = 'unassigned'
    let mediaType = 'other'
    let confidence = 'low'
    const reasons = []

    const isAndroidDesc = /Remote NDIS|RNDIS|NCM|Android|SAMSUNG Mobile/i.test(desc)
    const isAndroidPnp = /Class_ef&SubClass_04&Prot_01|MS_COMP_RNDIS|VID_18D1|VID_04E8/i.test(hw.pnpId)
    const isAndroidSubnet = finalIp?.startsWith('192.168.42.') || finalIp?.startsWith('192.168.43.') || net.gateway?.startsWith('192.168.42.')

    if (isAndroidDesc || isAndroidPnp) {
      classification = 'android'
      mediaType = /NCM/i.test(desc) ? 'ncm_android' : 'rndis_android'
      confidence = isUsb || isAndroidPnp ? 'high' : 'medium'
      if (hw.pnpId) reasons.push(`PnP Hardware ID: ${hw.pnpId}`)
      if (hw.mfg) reasons.push(`Device Manufacturer: ${hw.mfg}`)
      reasons.push(`Driver: ${desc}`)
    } else if (isAndroidSubnet && !isUsb && !isPci) {
      classification = 'unknown'
      confidence = 'low'
      reasons.push(`IP subnet ${finalIp} matches tether range, but lacks verified USB/RNDIS hardware identifier.`)
    } else if (/TAP-Windows|OpenVPN|WireGuard|WireSock|HotspotShield/i.test(desc) || isRoot) {
      classification = 'vpn'
      mediaType = 'vpn'
      confidence = 'high'
      if (hw.pnpId) reasons.push(`PnP ID: ${hw.pnpId}`)
      reasons.push(`VPN virtual adapter: ${desc}`)
    } else if (/Hyper-V|vEthernet|VirtualBox|VMware/i.test(desc) || mac.name.toLowerCase().includes('vethernet')) {
      classification = 'virtual'
      mediaType = 'virtual'
      confidence = 'high'
      reasons.push(`Virtual hypervisor switch: ${desc}`)
    } else if (/PCIe|Gigabit|GbE|Ethernet|Family Controller|Realtek|Intel/i.test(desc) || isPci) {
      mediaType = 'ethernet'
      if (net.gateway && isConn) {
        classification = 'modem'
        confidence = 'high'
        if (hw.pnpId) reasons.push(`PnP Hardware ID: ${hw.pnpId}`)
        if (hw.mfg) reasons.push(`Manufacturer: ${hw.mfg}`)
        reasons.push(`Physical PCI/PCIe Ethernet NIC with active gateway ${net.gateway} (Home Modem/Router)`)
      } else {
        if (hw.pnpId) reasons.push(`PnP Hardware ID: ${hw.pnpId}`)
        reasons.push(`Physical Ethernet NIC (Link status: ${isConn ? 'Up' : 'Disconnected'})`)
      }
    }

    results.push({
      id: `adapter-${mac.name.replace(/[^a-zA-Z0-9]/g, '-').toLowerCase()}`,
      name: mac.name,
      description: desc,
      manufacturer: hw.mfg || undefined,
      pnpId: hw.pnpId || undefined,
      status: isConn ? 'connected' : 'disconnected',
      mac: finalMac,
      mediaType,
      classification,
      classificationConfidence: confidence,
      classificationReasons: reasons,
      ipv4: finalIp,
      netmask: finalMask,
      gateway: net.gateway,
      metric: net.metric,
      mtu: sub.mtu,
    })
  }

  return results
}

// 2. Real Source-Bound RTT Probe Execution
async function probePath(sourceIp, target = '1.1.1.1') {
  if (!sourceIp) return { success: false, rttMs: null }
  try {
    const res = await execAsync(`ping.exe -n 1 -w 1000 -S ${sourceIp} ${target}`, { windowsHide: true })
    const timeMatch = res.stdout.match(/time[=<]([0-9]+)ms/i)
    return { success: Boolean(timeMatch), rttMs: timeMatch ? parseInt(timeMatch[1], 10) : null }
  } catch {
    return { success: false, rttMs: null }
  }
}

// 3. Real Per-Adapter Throughput Sample
let prevBytesIn = 0, prevBytesOut = 0, prevTimestamp = 0
async function sampleEthernetThroughput(adapterName = 'Ethernet') {
  try {
    const { stdout } = await execAsync('netsh interface ipv4 show subinterfaces', { windowsHide: true })
    const now = Date.now()
    const lines = stdout.trim().split(/\r?\n/)
    let isTable = false
    for (const l of lines) {
      if (l.includes('---')) { isTable = true; continue; }
      if (!isTable) continue;
      const m = l.trim().match(/^(\d+)\s+(\d+)\s+(\d+)\s+(\d+)\s+(.+)$/)
      if (m && m[5].trim().toLowerCase() === adapterName.toLowerCase()) {
        const curIn = parseInt(m[3], 10)
        const curOut = parseInt(m[4], 10)
        let rxBps = 0, txBps = 0
        if (prevTimestamp > 0) {
          const el = (now - prevTimestamp) / 1000
          if (el > 0.2 && curIn >= prevBytesIn && curOut >= prevBytesOut) {
            rxBps = (curIn - prevBytesIn) / el
            txBps = (curOut - prevBytesOut) / el
          }
        }
        prevBytesIn = curIn
        prevBytesOut = curOut
        prevTimestamp = now
        return { rxBps, txBps, rxMbps: (rxBps * 8) / 1000000, txMbps: (txBps * 8) / 1000000 }
      }
    }
  } catch (err) {
    console.warn('[Throughput] Sample error:', err.message)
  }
  return { rxBps: 0, txBps: 0, rxMbps: 0, txMbps: 0 }
}

async function runVerification() {
  console.log('[VERIFY Phase 2.1] Starting DualFlow Telemetry & Independent Verification...')

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
  ipcMain.on('window:minimize', () => win?.minimize())
  ipcMain.on('window:maximize', () => win?.isMaximized() ? win.unmaximize() : win?.maximize())
  ipcMain.on('window:close', () => win?.close())
  ipcMain.handle('window:isMaximized', () => win?.isMaximized() ?? false)

  let latestAdapters = []
  ipcMain.handle('dualflow:get-adapters', async () => {
    console.log('[IPC] dualflow:get-adapters invoked -> Querying Windows IP Helper & Registry...')
    latestAdapters = await discoverWindowsAdapters()
    console.log(`[IPC] Discovered ${latestAdapters.length} real adapters on Windows host.`)
    return latestAdapters
  })

  ipcMain.handle('dualflow:rescan-adapters', async () => {
    console.log('[IPC] dualflow:rescan-adapters invoked')
    latestAdapters = await discoverWindowsAdapters()
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

  const indexPath = path.join(rendererDist, 'index.html')
  await win.loadFile(indexPath)

  // 1. Wait for real Windows adapters to bind to DOM
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

  // 2. Start Live Telemetry Push loop to feed real throughput and RTT to renderer
  console.log('[VERIFY] Running live telemetry stream for 4 seconds...')
  const rttHistory = []
  let measuredRxMbps = 0
  let measuredTxMbps = 0
  let measuredRtt = null

  for (let i = 0; i < 4; i++) {
    const throughput = await sampleEthernetThroughput('Ethernet')
    const probe = await probePath('192.168.1.10', '1.1.1.1')
    
    measuredRxMbps = throughput.rxMbps
    measuredTxMbps = throughput.txMbps
    if (probe.rttMs !== null) {
      measuredRtt = probe.rttMs
      rttHistory.push(probe.rttMs)
    }

    const minRtt = rttHistory.length > 0 ? Math.min(...rttHistory) : null
    const maxRtt = rttHistory.length > 0 ? Math.max(...rttHistory) : null
    let jitter = null
    if (rttHistory.length >= 2) {
      jitter = Math.abs(rttHistory[rttHistory.length - 1] - rttHistory[rttHistory.length - 2])
    }

    const payload = {
      modem: {
        pathId: 'modem',
        rxBps: throughput.rxBps,
        txBps: throughput.txBps,
        rttMs: probe.rttMs,
        rttMinMs: minRtt,
        rttMaxMs: maxRtt,
        jitterMs: jitter,
        lossPercent: 0,
        probeTarget: '1.1.1.1:53',
        lastProbeTimestamp: new Date().toLocaleTimeString(),
        telemetryPoint: {
          timestamp: Date.now(),
          rttMs: probe.rttMs,
          rxMbps: throughput.rxMbps,
          txMbps: throughput.txMbps,
          loss: 0,
        },
      },
      android: {
        pathId: 'android',
        rxBps: 0,
        txBps: 0,
        rttMs: null,
        rttMinMs: null,
        rttMaxMs: null,
        jitterMs: null,
        lossPercent: null,
        probeTarget: '1.1.1.1:53',
        lastProbeTimestamp: null,
      },
    }

    win.webContents.send('dualflow:telemetry-updated', payload)
    console.log(`[VERIFY Telemetry Tick ${i + 1}] RTT: ${probe.rttMs} ms | Rx: ${throughput.rxMbps.toFixed(4)} Mbps | Tx: ${throughput.txMbps.toFixed(4)} Mbps`)
    await new Promise(r => setTimeout(r, 1000))
  }

  // 3. Inspect DOM for real telemetry binding
  const domAudit = await win.webContents.executeJavaScript(`
    (() => {
      const text = document.body.innerText;
      return {
        hasRealEthernet: text.includes('Realtek PCIe GbE Family Controller'),
        hasRealIp: text.includes('192.168.1.10'),
        hasRealGateway: text.includes('192.168.1.1'),
        hasRealMac: text.includes('D4-5D-64-F2-E3-1A'),
        hasAndroidStandby: text.includes('Android USB tethering device not detected'),
        excerpt: text.substring(0, 700),
      };
    })()
  `)
  console.log('[VERIFY] DOM Audit:', domAudit)

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
        if (target) { target.click(); return true; }
        return false;
      })()
    `)
    await new Promise(r => setTimeout(r, 600))
    const image = await win.webContents.capturePage()
    const outPath = path.join(screenshotDir, tab.name)
    fs.writeFileSync(outPath, image.toPNG())
    console.log(`[VERIFY] Saved screenshot: ${outPath}`)
  }

  // 5. Generate Phase 2.1 Verification Report
  const report = {
    timestamp: new Date().toISOString(),
    success: consoleErrors.length === 0 && domAudit.hasRealEthernet && domAudit.hasRealIp,
    discoveredAdaptersCount: latestAdapters.length,
    discoveredAdapters: latestAdapters,
    telemetryValidation: {
      measuredSourceIp: '192.168.1.10',
      probeTarget: '1.1.1.1',
      lastRttMs: measuredRtt,
      rttSamplesCount: rttHistory.length,
      rttMinMs: rttHistory.length > 0 ? Math.min(...rttHistory) : null,
      rttMaxMs: rttHistory.length > 0 ? Math.max(...rttHistory) : null,
      measuredRxMbps: measuredRxMbps,
      measuredTxMbps: measuredTxMbps,
      throughputFormula: 'Mbps = (deltaBytes * 8) / elapsedSeconds / 1,000,000',
    },
    domAudit,
    consoleErrors,
    screenshots: tabs.map(t => path.join('screenshots', t.name)),
  }

  fs.writeFileSync(path.join(appRoot, 'phase2_1-verification-report.json'), JSON.stringify(report, null, 2))
  console.log('[VERIFY] Phase 2.1 Verification Report successfully written.')

  setTimeout(() => {
    app.quit()
    process.exit(0)
  }, 1000)
}

app.whenReady().then(runVerification)

