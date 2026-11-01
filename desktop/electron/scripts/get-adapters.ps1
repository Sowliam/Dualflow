# DualFlow Windows Network Adapter Discovery Script
# Uses Windows IP Helper / NetAdapter cmdlets to retrieve real adapter telemetry

[CmdletBinding()]
param()

$ErrorActionPreference = 'SilentlyContinue'

[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
$OutputEncoding = [System.Text.Encoding]::UTF8

# 1. Enumerate adapters (filter internal miniports, transition tunnels, and kernel pseudo devices)
$adapters = Get-NetAdapter -IncludeHidden | Where-Object {
    $_.InterfaceDescription -notmatch 'QoS|WFP|Filter|LightWeight|Pacer|WAN Miniport|Kernel Debug|Teredo|IP-HTTPS|6to4|Virtual Switch Extension' -and
    $_.Name -notmatch 'Teredo|6to4|IP-HTTPS|isatap'
}

$ipConfigs = Get-NetIPConfiguration
$ipInterfaces = Get-NetIPInterface -AddressFamily IPv4

$outputList = @()

foreach ($a in $adapters) {
    $cfg = $ipConfigs | Where-Object { $_.InterfaceIndex -eq $a.InterfaceIndex }
    $ipInt = $ipInterfaces | Where-Object { $_.InterfaceIndex -eq $a.InterfaceIndex }

    $ipv4 = $null
    $netmask = $null
    if ($cfg -and $cfg.IPv4Address) {
        $ipv4 = $cfg.IPv4Address[0].IPAddress
        $prefix = $cfg.IPv4Address[0].PrefixLength
        
        # Calculate dotted decimal subnet mask from CIDR prefix
        $maskBytes = @(0,0,0,0)
        for ($i = 0; $i -lt $prefix; $i++) {
            $byteIdx = [math]::Floor($i / 8)
            $maskBytes[$byteIdx] = $maskBytes[$byteIdx] -bor (1 -shl (7 - ($i % 8)))
        }
        $netmask = ($maskBytes -join '.')
    }

    $gateway = $null
    if ($cfg -and $cfg.IPv4DefaultGateway) {
        $gateway = $cfg.IPv4DefaultGateway[0].NextHop
    }

    $dnsServers = @()
    if ($cfg -and $cfg.DNSServer) {
        $dnsServers = @($cfg.DNSServer.ServerAddresses)
    }

    $metric = 0
    $mtu = 1500
    if ($ipInt) {
        $metric = [int]$ipInt[0].InterfaceMetric
        $mtu = [int]$ipInt[0].NlMtu
    }

    # Technical Classification Logic based on real device data (PnP, Driver, Gateway, Media)
    $desc = if ($a.InterfaceDescription) { $a.InterfaceDescription } else { '' }
    $pnp = if ($a.PnPDeviceID) { $a.PnPDeviceID } else { '' }
    $media = if ($a.MediaType) { $a.MediaType } else { '' }
    $physMedia = if ($a.PhysicalMediaType) { $a.PhysicalMediaType } else { '' }

    $classification = 'unassigned'
    $confidence = 'low'
    $reasons = @()
    $detectedMedia = 'other'

    # Check for Android USB Tethering (RNDIS / NCM / USB tether)
    $isAndroidDesc = $desc -match 'Remote NDIS|RNDIS|NCM|Android|SAMSUNG Mobile USB|Google USB'
    $isUsbPnp = $pnp -match '^USB\\|Class_E0|Class_02'
    $isAndroidSubnet = $gateway -match '^192\.168\.(42|43|44)\.' -or $ipv4 -match '^192\.168\.(42|43|44)\.'

    # Check for VPN / Virtual
    $isVpn = $desc -match 'TAP-Windows|OpenVPN|WireGuard|WireSock|HotspotShield|Proton|NordVPN|AnyConnect|Tailscale|ZeroTier'
    $isVirtual = $pnp -match '^ROOT\\NET' -or $desc -match 'Hyper-V|Virtual|VMware|VirtualBox|vEthernet|Loopback|Wintun'

    # Check for Wi-Fi
    $isWifi = $physMedia -match 'Native 802\.11|Wireless' -or $media -match 'Native 802\.11' -or $desc -match 'Wi-Fi|Wireless|802\.11|WLAN'

    if ($isAndroidDesc -or ($isUsbPnp -and $isAndroidSubnet)) {
        $classification = 'android'
        $detectedMedia = if ($desc -match 'NCM') { 'ncm_android' } else { 'rndis_android' }
        $confidence = if ($isAndroidDesc -and $isUsbPnp) { 'high' } else { 'medium' }
        if ($isAndroidDesc) { $reasons += "Driver: $desc" }
        if ($isUsbPnp) { $reasons += "PnP bus: USB" }
        if ($isAndroidSubnet) { $reasons += "Subnet matches Android tether range: $gateway" }
    }
    elseif ($isVpn) {
        $classification = 'vpn'
        $detectedMedia = 'vpn'
        $confidence = 'high'
        $reasons += "VPN adapter pattern matched: $desc"
    }
    elseif ($isVirtual) {
        $classification = 'virtual'
        $detectedMedia = 'virtual'
        $confidence = 'high'
        $reasons += "Virtual network bus / Hyper-V pattern: $desc"
    }
    elseif ($isWifi) {
        $detectedMedia = 'wifi'
        if ($gateway -and $a.Status -eq 'Up') {
            $classification = 'modem'
            $confidence = 'high'
            $reasons += "Active Wi-Fi connection with default gateway $gateway"
        } else {
            $classification = 'unassigned'
            $confidence = 'medium'
            $reasons += "Wi-Fi adapter, gateway: $gateway"
        }
    }
    elseif ($pnp -match '^PCI\\' -or $desc -match 'PCIe|Gigabit|GbE|Ethernet|Family Controller|Intel\(R\)|Realtek') {
        $detectedMedia = 'ethernet'
        if ($gateway -and $a.Status -eq 'Up') {
            $classification = 'modem'
            $confidence = 'high'
            $reasons += "Physical PCI/PCIe Ethernet NIC with active gateway $gateway (Home Modem/Router)"
        } else {
            $classification = 'unassigned'
            $confidence = 'medium'
            $reasons += "Physical Ethernet NIC, link status: $($a.Status)"
        }
    }

    $statusNormalized = switch ($a.Status) {
        'Up' { 'connected' }
        'Disconnected' { 'disconnected' }
        'Disabled' { 'disconnected' }
        'Not Present' { 'disconnected' }
        Default { 'disconnected' }
    }

    $cleanMac = '-'
    if ($a.MacAddress -and ($a.MacAddress -match '^[0-9A-Fa-f]{2}([:-][0-9A-Fa-f]{2}){5}$')) {
        $cleanMac = $a.MacAddress
    }

    $item = [PSCustomObject]@{
        id = if ($a.InterfaceGuid) { [string]$a.InterfaceGuid } else { [guid]::NewGuid().ToString() }
        name = [string]$a.Name
        description = [string]$desc
        interfaceIndex = [int]$a.InterfaceIndex
        status = $statusNormalized
        mac = $cleanMac
        mediaType = $detectedMedia
        classification = $classification
        classificationConfidence = $confidence
        classificationReasons = $reasons
        linkSpeed = if ($a.LinkSpeed) { [string]$a.LinkSpeed } else { '-' }
        pnpId = [string]$pnp
        ipv4 = if ($ipv4) { [string]$ipv4 } else { $null }
        netmask = if ($netmask) { [string]$netmask } else { $null }
        gateway = if ($gateway) { [string]$gateway } else { $null }
        dns = $dnsServers
        metric = $metric
        mtu = $mtu
    }

    $outputList += $item
}

$outputList | ConvertTo-Json -Depth 4 -Compress
