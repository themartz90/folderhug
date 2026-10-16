[CmdletBinding()]
param([switch]$SkipBuild)

$ErrorActionPreference = 'Stop'
$previousAddress = [Environment]::GetEnvironmentVariable('FOLDERHUG_ADDR', 'Process')
$previousPublicURL = [Environment]::GetEnvironmentVariable('FOLDERHUG_PUBLIC_URL', 'Process')
Push-Location (Split-Path $PSScriptRoot -Parent)
try {
    $listeners = [Net.NetworkInformation.IPGlobalProperties]::GetIPGlobalProperties().GetActiveTcpListeners()
    if ($listeners | Where-Object Port -eq 8080) {
        throw 'Port 8080 is already in use. Stop the existing local server before starting another one.'
    }
    if (-not $SkipBuild) {
        npm --prefix web run build
        if ($LASTEXITCODE -ne 0) { throw 'Frontend build failed' }
    }
    $env:FOLDERHUG_ADDR = '127.0.0.1:8080'
    $env:FOLDERHUG_PUBLIC_URL = 'http://localhost:8080'
    go run ./cmd/folderhug dev
    if ($LASTEXITCODE -ne 0) { throw 'Local development server stopped with an error' }
} finally {
    [Environment]::SetEnvironmentVariable('FOLDERHUG_ADDR', $previousAddress, 'Process')
    [Environment]::SetEnvironmentVariable('FOLDERHUG_PUBLIC_URL', $previousPublicURL, 'Process')
    Pop-Location
}
