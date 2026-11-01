import os from 'node:os'
import { exec } from 'node:child_process'
import { promisify } from 'node:util'
import type { NetworkAdapter } from '../../src/types'

const execAsync = promisify(exec)

export interface NetworkChangeWatcher {
  stop: () => void
}

export class NetworkDiscoveryService {
  private cachedAdapters: NetworkAdapter[] = []
  private isScanning = false
  private pollTimer: NodeJS.Timeout | null = null

  public async getAdapters(): Promise<NetworkAdapter[]> {
    if (this.cachedAdapters.length > 0 && this.isScanning) {
      return this.cachedAdapters
    }

    this.isScanning = true
    try {
      const adapters = await this.discoverWindowsAdapters()
      this.cachedAdapters = adapters
      return adapters
    } finally {
      this.isScanning = false
    }
  }

  public startLiveWatcher(
    onUpdate: (adapters: NetworkAdapter[]) => void,
    intervalMs = 2000
  ): NetworkChangeWatcher {
    // Immediate initial dispatch
    this.getAdapters().then(onUpdate).catch(console.error)

    let previousFingerprint = ''

    const poll = async () => {
      try {
        const adapters = await this.discoverWindowsAdapters()
        const fingerprint = JSON.stringify(
          adapters.map((a) => ({
            id: a.id,
            status: a.status,
            ipv4: a.ipv4,
            gateway: a.gateway,
            metric: a.metric,
          }))
        )

        if (fingerprint !== previousFingerprint) {
          previousFingerprint = fingerprint
          this.cachedAdapters = adapters
          onUpdate(adapters)
        }
      } catch (err) {
        console.error('[NetworkDiscovery] Watcher scan error:', err)
      }
    }

    this.pollTimer = setInterval(poll, intervalMs)

    return {
      stop: () => {
        if (this.pollTimer) {
          clearInterval(this.pollTimer)
          this.pollTimer = null
        }
      },
    }
  }

  private async discoverWindowsAdapters(): Promise<NetworkAdapter[]> {
    const osIfaces = os.networkInterfaces()

    let getmacStdout = ''
    let netshAddrStdout = ''
    let netshDnsStdout = ''
    let netshSubStdout = ''
    let netRegStdout = ''
    let classRegStdout = ''

    try {
      const [
        getmacRes,
        netshAddrRes,
        netshDnsRes,
        netshSubRes,
        netRegRes,
        classRegRes,
      ] = await Promise.all([
        execAsync('getmac /fo csv /v', { windowsHide: true }),
        execAsync('netsh interface ipv4 show addresses', { windowsHide: true }),
        execAsync('netsh interface ipv4 show dnsservers', { windowsHide: true }),
        execAsync('netsh interface ipv4 show subinterfaces', { windowsHide: true }),
        execAsync(
          'reg query HKLM\\SYSTEM\\CurrentControlSet\\Control\\Network\\{4D36E972-E325-11CE-BFC1-08002BE10318} /s /v Name',
          { windowsHide: true }
        ),
        execAsync(
          'reg query HKLM\\SYSTEM\\CurrentControlSet\\Control\\Class\\{4d36e972-e325-11ce-bfc1-08002be10318} /s',
          { windowsHide: true, maxBuffer: 10 * 1024 * 1024 }
        ),
      ])

      getmacStdout = getmacRes.stdout
      netshAddrStdout = netshAddrRes.stdout
      netshDnsStdout = netshDnsRes.stdout
      netshSubStdout = netshSubRes.stdout
      netRegStdout = netRegRes.stdout
      classRegStdout = classRegRes.stdout
    } catch (err) {
      console.warn('[NetworkDiscovery] System command warning:', err)
    }

    // 1. Build Registry Connection Name to GUID mapping
    const nameToGuidMap = new Map<string, string>()
    if (netRegStdout) {
      const blocks = netRegStdout.split(/HKEY_LOCAL_MACHINE\\/i)
      for (const block of blocks) {
        const guidMatch = block.match(/\{([0-9A-Fa-f-]{36})\}\\Connection/i)
        const nameMatch = block.match(/Name\s+REG_SZ\s+(.*)/i)
        if (guidMatch && nameMatch) {
          const guid = guidMatch[1].toLowerCase()
          const connName = nameMatch[1].trim().toLowerCase()
          nameToGuidMap.set(connName, `{${guid}}`)
        }
      }
    }

    // 2. Build Registry GUID to PnP ID, Driver, and Manufacturer mapping
    const guidToPnpMap = new Map<
      string,
      { desc: string; pnpId: string; manufacturer: string }
    >()
    if (classRegStdout) {
      const blocks = classRegStdout.split(/HKEY_LOCAL_MACHINE\\/i)
      for (const block of blocks) {
        const descMatch = block.match(/DriverDesc\s+REG_SZ\s+(.*)/i)
        const pnpMatch = block.match(/MatchingDeviceId\s+REG_SZ\s+(.*)/i)
        const mfgMatch = block.match(/ProviderName\s+REG_SZ\s+(.*)/i)
        const netCfgMatch = block.match(/NetCfgInstanceId\s+REG_SZ\s+(.*)/i)

        if (netCfgMatch) {
          const guid = netCfgMatch[1].trim().toLowerCase()
          guidToPnpMap.set(guid, {
            desc: descMatch ? descMatch[1].trim() : '',
            pnpId: pnpMatch ? pnpMatch[1].trim() : '',
            manufacturer: mfgMatch ? mfgMatch[1].trim() : '',
          })
        }
      }
    }

    // 3. Parse Subinterfaces for real MTU and MediaSenseState
    const subinterfaceMap = new Map<string, { mtu: number; mediaSenseState: number }>()
    if (netshSubStdout) {
      const lines = netshSubStdout.trim().split(/\r?\n/)
      let isTable = false
      for (const line of lines) {
        if (line.includes('---')) {
          isTable = true
          continue
        }
        if (!isTable) continue

        const match = line.trim().match(/^(\d+)\s+(\d+)\s+(\d+)\s+(\d+)\s+(.+)$/)
        if (match) {
          const mtu = parseInt(match[1], 10)
          const mediaSenseState = parseInt(match[2], 10)
          const ifName = match[5].trim().toLowerCase()
          subinterfaceMap.set(ifName, { mtu, mediaSenseState })
        }
      }
    }

    // 4. Parse getmac CSV
    const macMap = new Map<
      string,
      { name: string; description: string; mac: string; isDisconnected: boolean }
    >()

    if (getmacStdout) {
      const lines = getmacStdout
        .trim()
        .split(/\r?\n/)
        .map((l) => l.trim())
        .filter(Boolean)

      for (let i = 1; i < lines.length; i++) {
        const line = lines[i]
        const parts = line.slice(1, -1).split('","')

        if (parts.length >= 4) {
          const [connName, adapterDesc, physicalAddr, transport] = parts
          macMap.set(connName.toLowerCase(), {
            name: connName,
            description: adapterDesc,
            mac:
              physicalAddr && physicalAddr !== 'N/A' && !physicalAddr.includes('?')
                ? physicalAddr
                : '—',
            isDisconnected:
              transport.toLowerCase().includes('disconnected') ||
              transport.toLowerCase().includes('not present'),
          })
        }
      }
    }

    // 5. Parse netsh addresses
    const netshMap = new Map<
      string,
      { ipv4: string | null; netmask: string | null; gateway: string | null; metric: number }
    >()

    if (netshAddrStdout) {
      const blocks = netshAddrStdout.split(/Configuration for interface /i).filter((b) => b.trim())
      for (const block of blocks) {
        const lines = block.split(/\r?\n/).map((l) => l.trim())
        if (lines.length === 0) continue

        const header = lines[0]
        const nameMatch = header.match(/^"?([^"\r\n]+)"?/)
        if (!nameMatch) continue

        const ifaceName = nameMatch[1].trim()
        let ipv4: string | null = null
        let netmask: string | null = null
        let gateway: string | null = null
        let metric = 0

        for (const line of lines) {
          if (line.startsWith('IP Address:')) {
            ipv4 = line.replace('IP Address:', '').trim()
          } else if (line.includes('Subnet Prefix:')) {
            const maskMatch = line.match(/mask\s+([0-9.]+)/i)
            if (maskMatch) {
              netmask = maskMatch[1].trim()
            }
          } else if (line.startsWith('Default Gateway:')) {
            gateway = line.replace('Default Gateway:', '').trim()
          } else if (line.startsWith('InterfaceMetric:')) {
            metric = parseInt(line.replace('InterfaceMetric:', '').trim(), 10) || 0
          }
        }

        netshMap.set(ifaceName.toLowerCase(), { ipv4, netmask, gateway, metric })
      }
    }

    // 6. Parse netsh DNS servers
    const dnsMap = new Map<string, string[]>()
    if (netshDnsStdout) {
      const blocks = netshDnsStdout.split(/Configuration for interface /i).filter((b) => b.trim())
      for (const block of blocks) {
        const lines = block.split(/\r?\n/).map((l) => l.trim())
        if (lines.length === 0) continue

        const header = lines[0]
        const nameMatch = header.match(/^"?([^"\r\n]+)"?/)
        if (!nameMatch) continue

        const ifaceName = nameMatch[1].trim().toLowerCase()
        const servers: string[] = []

        for (const line of lines) {
          const match =
            line.match(/DNS servers configured through DHCP:\s+([0-9.]+)/i) ||
            line.match(/Statically Configured DNS Servers:\s+([0-9.]+)/i) ||
            line.match(/^\s*([0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3})$/)

          if (match && match[1] && !servers.includes(match[1])) {
            servers.push(match[1])
          }
        }

        dnsMap.set(ifaceName, servers)
      }
    }

    // 7. Combine all interfaces
    const adapterKeys = new Set<string>([
      ...macMap.keys(),
      ...netshMap.keys(),
      ...Object.keys(osIfaces).map((k) => k.toLowerCase()),
    ])

    const results: NetworkAdapter[] = []

    for (const key of adapterKeys) {
      // Exclude loopback and pseudo miniports
      if (
        key.includes('loopback') ||
        key.includes('pseudo') ||
        key.includes('teredo') ||
        key.includes('6to4') ||
        key.includes('isatap')
      ) {
        continue
      }

      const macInfo = macMap.get(key) || {
        name: key,
        description: key,
        mac: '—',
        isDisconnected: false,
      }
      const netshInfo = netshMap.get(key) || {
        ipv4: null,
        netmask: null,
        gateway: null,
        metric: 0,
      }
      const dns = dnsMap.get(key) || []
      const subInfo = subinterfaceMap.get(key) || { mtu: 1500, mediaSenseState: 1 }

      // Correlate with Registry PnP and Manufacturer
      const guid = nameToGuidMap.get(key) || ''
      const hwInfo = guidToPnpMap.get(guid.toLowerCase()) || {
        desc: '',
        pnpId: '',
        manufacturer: '',
      }

      // Cross-check with OS network interfaces
      let osIpv4: string | null = null
      let osNetmask: string | null = null
      let osMac: string | null = null

      const osMatch = Object.entries(osIfaces).find(
        ([name]) => name.toLowerCase() === key
      )

      if (osMatch && osMatch[1]) {
        const v4 = osMatch[1].find((i) => i.family === 'IPv4' || (i as any).family === 4)
        if (v4) {
          osIpv4 = v4.address
          osNetmask = v4.netmask
          osMac = v4.mac.toUpperCase().replace(/:/g, '-')
        }
      }

      const finalIpv4 = netshInfo.ipv4 || osIpv4
      const finalNetmask = netshInfo.netmask || osNetmask
      const finalMac =
        macInfo.mac !== '—' ? macInfo.mac : osMac && osMac !== '00-00-00-00-00-00' ? osMac : '—'

      // An interface is connected if MediaSenseState is 1 and has an IP assigned
      const isConnected =
        !macInfo.isDisconnected && subInfo.mediaSenseState === 1 && Boolean(finalIpv4)

      const desc = hwInfo.desc || macInfo.description
      const pnpId = hwInfo.pnpId
      const mfg = hwInfo.manufacturer

      let classification: NetworkAdapter['classification'] = 'unassigned'
      let mediaType: NetworkAdapter['mediaType'] = 'other'
      let confidence: 'high' | 'medium' | 'low' = 'low'
      const reasons: string[] = []

      // Multi-factor Classification Engine
      const isUsbBus = pnpId.toUpperCase().startsWith('USB\\')
      const isPciBus = pnpId.toUpperCase().startsWith('PCI\\')
      const isRootBus = pnpId.toUpperCase().startsWith('ROOT\\')

      const isAndroidDesc = /Remote NDIS|RNDIS|NCM|Android|SAMSUNG Mobile/i.test(desc)
      const isAndroidPnp = /Class_ef&SubClass_04&Prot_01|MS_COMP_RNDIS|VID_18D1|VID_04E8/i.test(pnpId)
      const isAndroidSubnet =
        finalIpv4?.startsWith('192.168.42.') ||
        finalIpv4?.startsWith('192.168.43.') ||
        finalIpv4?.startsWith('192.168.44.') ||
        netshInfo.gateway?.startsWith('192.168.42.') ||
        netshInfo.gateway?.startsWith('192.168.43.') ||
        netshInfo.gateway?.startsWith('192.168.44.')

      if (isAndroidDesc || isAndroidPnp) {
        // High confidence Android USB Tethering (RNDIS/NCM)
        classification = 'android'
        mediaType = /NCM/i.test(desc) ? 'ncm_android' : 'rndis_android'
        confidence = isUsbBus || isAndroidPnp ? 'high' : 'medium'
        if (pnpId) reasons.push(`PnP Hardware ID: ${pnpId}`)
        if (mfg) reasons.push(`Device Manufacturer: ${mfg}`)
        reasons.push(`Driver: ${desc}`)
        reasons.push(
          isUsbBus
            ? 'USB bus confirmed via Windows PnP subsystem'
            : 'USB Remote NDIS / Android tethering characteristics confirmed'
        )
      } else if (isAndroidSubnet && !isUsbBus && !isPciBus) {
        // IP matches 192.168.42.x but missing USB/RNDIS hardware proof -> Classify as unknown
        classification = 'unknown'
        confidence = 'low'
        reasons.push(
          `IP subnet ${finalIpv4} matches common tether range, but lacks verified USB/RNDIS hardware identifier.`
        )
      } else if (/TAP-Windows|OpenVPN|WireGuard|WireSock|HotspotShield/i.test(desc) || isRootBus) {
        classification = 'vpn'
        mediaType = 'vpn'
        confidence = 'high'
        if (pnpId) reasons.push(`PnP ID: ${pnpId}`)
        reasons.push(`VPN virtual adapter: ${desc}`)
      } else if (
        /Hyper-V|vEthernet|VirtualBox|VMware/i.test(desc) ||
        macInfo.name.toLowerCase().includes('vethernet')
      ) {
        classification = 'virtual'
        mediaType = 'virtual'
        confidence = 'high'
        reasons.push(`Virtual hypervisor switch: ${desc}`)
      } else if (/Wi-Fi|Wireless|802\.11/i.test(desc)) {
        mediaType = 'wifi'
        if (netshInfo.gateway && isConnected) {
          classification = 'modem'
          confidence = 'high'
          reasons.push(`Active Wi-Fi gateway: ${netshInfo.gateway}`)
        } else {
          reasons.push('Wi-Fi radio interface')
        }
      } else if (/PCIe|Gigabit|GbE|Ethernet|Family Controller|Realtek|Intel/i.test(desc) || isPciBus) {
        mediaType = 'ethernet'
        if (netshInfo.gateway && isConnected) {
          classification = 'modem'
          confidence = 'high'
          if (pnpId) reasons.push(`PnP Hardware ID: ${pnpId}`)
          if (mfg) reasons.push(`Manufacturer: ${mfg}`)
          reasons.push(
            `Physical PCI/PCIe Ethernet NIC with active gateway ${netshInfo.gateway} (Home Modem/Router)`
          )
        } else {
          if (pnpId) reasons.push(`PnP Hardware ID: ${pnpId}`)
          reasons.push(
            `Physical Ethernet NIC (Link status: ${isConnected ? 'Up' : 'Disconnected'})`
          )
        }
      }

      results.push({
        id: `adapter-${macInfo.name.replace(/[^a-zA-Z0-9]/g, '-').toLowerCase()}`,
        name: macInfo.name,
        description: desc,
        manufacturer: mfg || undefined,
        pnpId: pnpId || undefined,
        status: isConnected ? 'connected' : 'disconnected',
        mac: finalMac,
        mediaType,
        classification,
        classificationConfidence: confidence,
        classificationReasons: reasons,
        ipv4: finalIpv4,
        netmask: finalNetmask,
        gateway: netshInfo.gateway,
        dns,
        metric: netshInfo.metric,
        mtu: subInfo.mtu,
      })
    }

    return results
  }
}
