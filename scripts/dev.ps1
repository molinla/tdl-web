# Build and run tdl-web (API + frontend).
#
# Examples:
#   .\scripts\dev.ps1 -BuildOnly
#   .\scripts\dev.ps1
#   .\scripts\dev.ps1 -JsonFile .\result.json -DownloadDir "F:\telegram" -Takeout -Continue -SkipSame -RangeType id -Range 100,500
#   .\scripts\dev.ps1 -Dev

param(
    [switch]$BuildOnly,
    [switch]$Dev,
    [string]$JsonFile = ".\result.json",
    [string]$DownloadDir = ".\downloads",
    [string]$ApiAddr = "127.0.0.1:8080",
    [string]$UiAddr = "127.0.0.1:5173",
    [switch]$Takeout,
    [switch]$Continue,
    [switch]$SkipSame,
    [string]$RangeType = "",
    [string]$Range = "",
    [int]$Limit = 0
)

$ErrorActionPreference = "Stop"

$Root = Split-Path -Parent $PSScriptRoot
$Exe = Join-Path $Root "outputs\tdl-web.exe"
$WebDir = Join-Path $Root "web"

function Refresh-Path {
    $env:Path = [Environment]::GetEnvironmentVariable("Path", "Machine") + ";" +
        [Environment]::GetEnvironmentVariable("Path", "User")
}

function Require-Command([string]$Name) {
    if (-not (Get-Command $Name -ErrorAction SilentlyContinue)) {
        throw "Command not found: $Name"
    }
}

function Build-Project {
    Refresh-Path
    Require-Command go
    Require-Command npm

    $outputs = Join-Path $Root "outputs"
    New-Item -ItemType Directory -Force -Path $outputs | Out-Null

    Write-Host "==> go build" -ForegroundColor Cyan
    Push-Location $Root
    try {
        go build -o $Exe .
    } finally {
        Pop-Location
    }

    Write-Host "==> npm install (web)" -ForegroundColor Cyan
    Push-Location $WebDir
    try {
        if (-not (Test-Path "node_modules")) {
            npm install
        }
        if ($Dev) {
            Write-Host "==> skip frontend production build (using dev server)" -ForegroundColor Yellow
        } else {
            Write-Host "==> npm run build (web)" -ForegroundColor Cyan
            npm run build
        }
    } finally {
        Pop-Location
    }

    Write-Host "Build OK: $Exe" -ForegroundColor Green
    if (-not $Dev) {
        Write-Host "Frontend dist: $(Join-Path $WebDir 'dist')" -ForegroundColor Green
    }
}

function Build-ApiArgs {
    $args = @(
        "web",
        "-f", (Resolve-Path -LiteralPath $JsonFile).Path,
        "-d", $DownloadDir,
        "--addr", $ApiAddr
    )
    if ($Takeout) { $args += "--takeout" }
    if ($Continue) { $args += "--continue" }
    if ($SkipSame) { $args += "--skip-same" }
    if ($RangeType) {
        $args += "--type", $RangeType
        if ($Range) {
            $args += "-i", $Range
        }
    }
    if ($Limit -gt 0) {
        $args += "-l", "$Limit"
    }
    return $args
}

function Start-DevStack {
    if (-not (Test-Path $Exe)) {
        throw "Binary not found: $Exe (run with -BuildOnly first, or run without -BuildOnly to build automatically)"
    }
    if (-not (Test-Path $JsonFile)) {
        throw "JSON file not found: $JsonFile"
    }
    if (-not $Dev -and -not (Test-Path (Join-Path $WebDir "dist\index.html"))) {
        throw "Frontend dist not found. Re-run without -Dev, or run -BuildOnly first."
    }

    $apiArgs = Build-ApiArgs
    Write-Host "==> API: $Exe $($apiArgs -join ' ')" -ForegroundColor Cyan

    $api = Start-Process `
        -FilePath $Exe `
        -ArgumentList $apiArgs `
        -WorkingDirectory $Root `
        -PassThru `
        -NoNewWindow

    $uiCmd = if ($Dev) { "npm run dev -- --host $($UiAddr.Split(':')[0]) --port $($UiAddr.Split(':')[1])" }
    else { "npm run preview -- --host $($UiAddr.Split(':')[0]) --port $($UiAddr.Split(':')[1])" }

    Write-Host "==> UI:  $uiCmd" -ForegroundColor Cyan
    Write-Host "Open http://$($UiAddr.Replace('127.0.0.1','localhost'))/" -ForegroundColor Green
    Write-Host "API http://$($ApiAddr.Replace('127.0.0.1','localhost'))/" -ForegroundColor Green
    Write-Host "Press Ctrl+C to stop both." -ForegroundColor Yellow

    Push-Location $WebDir
    try {
        Invoke-Expression $uiCmd
    } finally {
        Pop-Location
        if ($api -and -not $api.HasExited) {
            Stop-Process -Id $api.Id -Force -ErrorAction SilentlyContinue
        }
    }
}

Push-Location $Root
try {
    Build-Project
    if ($BuildOnly) {
        return
    }
    Start-DevStack
} finally {
    Pop-Location
}
