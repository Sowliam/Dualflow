# ==============================================================================
# DualFlow Phase 7 — Zero-Cost Local Hardware & Engine Validation
# ==============================================================================
param(
    [int]$Duration = 5,
    [int]$Streams = 4
)

$ErrorActionPreference = "Stop"

Write-Host "================================================================================" -ForegroundColor Cyan
Write-Host "      DUALFLOW LOCAL MULTI-PATH AGGREGATION & PROTOCOL BENCHMARK                " -ForegroundColor Cyan
Write-Host "================================================================================" -ForegroundColor Cyan

$coreDir = Join-Path $PSScriptRoot "..\desktop\network-core"
$relayExe = Join-Path $coreDir "dualflow-relay.exe"
$benchExe = Join-Path $coreDir "dualflow-bench.exe"

$staticPriv = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
$pskHex = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

# 1. Derive Relay Static Public Key
$pubKey = (& $relayExe -static-key $staticPriv -show-pubkey).Trim()
Write-Host "[1/4] Derived Relay Static Public Key: $pubKey" -ForegroundColor Green

# 2. Start Local Echo Server
Write-Host "[2/4] Starting Local TCP Echo Target (127.0.0.1:8888)..." -ForegroundColor Yellow
$echoProc = Start-Process -FilePath $benchExe -ArgumentList @("-mode", "echo", "-listen", "127.0.0.1:8888") -PassThru -WindowStyle Hidden

# 3. Start Local DualFlow Relay Server
Write-Host "[3/4] Starting Local Noise_IKpsk2 Relay Server (127.0.0.1:51820)..." -ForegroundColor Yellow
$relayProc = Start-Process -FilePath $relayExe -ArgumentList @("-listen", "127.0.0.1:51820", "-static-key", $staticPriv, "-psk", $pskHex) -PassThru -WindowStyle Hidden

Start-Sleep -Milliseconds 800

try {
    # 4. Run Multi-Stream Aggregation Benchmark
    Write-Host "[4/4] Executing $Streams concurrent multi-path streams for $Duration seconds..." -ForegroundColor Cyan
    $benchArgs = @(
        "-mode", "client",
        "-relay", "127.0.0.1:51820",
        "-psk", $pskHex,
        "-static-pub", $pubKey,
        "-target", "127.0.0.1:8888",
        "-duration", $Duration,
        "-streams", $Streams
    )

    & $benchExe @benchArgs
}
finally {
    Write-Host "`n[CLEANUP] Stopping background test daemons..." -ForegroundColor Gray
    if ($echoProc -and -not $echoProc.HasExited) { Stop-Process -Id $echoProc.Id -Force -ErrorAction SilentlyContinue }
    if ($relayProc -and -not $relayProc.HasExited) { Stop-Process -Id $relayProc.Id -Force -ErrorAction SilentlyContinue }
    Write-Host "[DONE] Local validation completed." -ForegroundColor Green
}
