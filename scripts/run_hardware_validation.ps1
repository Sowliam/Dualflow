# ==============================================================================
# DualFlow Physical Hardware Validation Automated Runner
# ==============================================================================
param(
    [Parameter(Mandatory=$false)]
    [string]$RelayHost,

    [Parameter(Mandatory=$false)]
    [int]$RelayPort = 51820,

    [Parameter(Mandatory=$false)]
    [string]$PSK = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",

    [Parameter(Mandatory=$false)]
    [string]$StaticPubKey,

    [Parameter(Mandatory=$false)]
    [int]$Duration = 10,

    [Parameter(Mandatory=$false)]
    [int]$Streams = 4
)

$ErrorActionPreference = "Stop"

Write-Host "================================================================================" -ForegroundColor Cyan
Write-Host "         DUALFLOW PHYSICAL HARDWARE VALIDATION PRE-FLIGHT CHECK                 " -ForegroundColor Cyan
Write-Host "================================================================================" -ForegroundColor Cyan

# 1. Inspect Physical Network Adapters on Windows
Write-Host "`n[STEP 1] Inspecting Windows Network Adapters..." -ForegroundColor Yellow
$adapters = Get-NetAdapter | Where-Object { $_.Status -eq "Up" -and $_.Virtual -ne $true }

if ($adapters.Count -lt 2) {
    Write-Host "[WARNING] Less than 2 active physical network adapters found!" -ForegroundColor Red
    Write-Host "Found $($adapters.Count) physical adapter(s). To achieve true physical hardware aggregation,"
    Write-Host "connect at least two independent internet connections (e.g. Wi-Fi + Mobile Hotspot or Ethernet + Wi-Fi)."
} else {
    Write-Host "[PASS] Detected $($adapters.Count) active physical network adapters:" -ForegroundColor Green
}

foreach ($a in $adapters) {
    $ip = (Get-NetIPAddress -InterfaceIndex $a.InterfaceIndex -AddressFamily IPv4 -ErrorAction SilentlyContinue).IPAddress
    Write-Host "  - Interface $($a.InterfaceIndex): $($a.Name) | Speed: $($a.LinkSpeed) | IPv4: $ip" -ForegroundColor Gray
}

# 2. Check Relay Target Configuration
Write-Host "`n[STEP 2] Relay Configuration..." -ForegroundColor Yellow
if (-not $RelayHost) {
    $RelayHost = $env:DUALFLOW_TEST_REMOTE_RELAY
}
if (-not $RelayHost) {
    Write-Host "Enter Remote Relay IP/Hostname (e.g. 192.168.1.100 or vps.example.com):" -ForegroundColor White
    $RelayHost = Read-Host
}
if (-not $RelayHost) {
    Write-Host "[FATAL] Remote Relay host is required for hardware validation." -ForegroundColor Red
    exit 1
}

$relayEndpoint = "$($RelayHost):$($RelayPort)"
Write-Host "Target Physical Relay: $relayEndpoint" -ForegroundColor Green

if (-not $StaticPubKey) {
    $StaticPubKey = $env:DUALFLOW_RELAY_STATIC_PUB
}
if (-not $StaticPubKey) {
    Write-Host "Enter Relay Server Static Public Key (64-char hex) derived from relay:" -ForegroundColor White
    $StaticPubKey = Read-Host
}
if (-not $StaticPubKey) {
    Write-Host "[FATAL] Server static public key is required for Noise_IKpsk2 authentication." -ForegroundColor Red
    exit 1
}

# 3. Locate Bench Binary
$benchExe = Join-Path $PSScriptRoot "..\desktop\network-core\dualflow-bench.exe"
if (-not (Test-Path $benchExe)) {
    Write-Host "[INFO] Building dualflow-bench.exe..." -ForegroundColor Yellow
    Push-Location (Join-Path $PSScriptRoot "..\desktop\network-core")
    & go build -o dualflow-bench.exe ./cmd/dualflow-bench
    Pop-Location
}

# 4. Execute Benchmark
Write-Host "`n[STEP 3] Executing Live Multipath Benchmark..." -ForegroundColor Yellow
$benchArgs = @(
    "-mode", "client",
    "-relay", $relayEndpoint,
    "-psk", $PSK,
    "-static-pub", $StaticPubKey,
    "-duration", $Duration,
    "-streams", $Streams
)

& $benchExe @benchArgs

Write-Host "`n[STEP 4] Hardware Validation completed." -ForegroundColor Cyan
