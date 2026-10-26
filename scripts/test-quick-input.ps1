#requires -Version 7.0
[CmdletBinding()]
param([string]$Go='go', [Parameter(Mandatory)][string]$OutputDirectory, [switch]$Desktop, [ValidateRange(0,100)][int]$Cycles=0)
$ErrorActionPreference = 'Stop'
$repo = Split-Path -Parent $PSScriptRoot
New-Item -ItemType Directory -Path $OutputDirectory -Force | Out-Null
$OutputDirectory = [IO.Path]::GetFullPath($OutputDirectory)
$savedDesktop=$env:ASTERHAND_DESKTOP_TEST; $savedCycles=$env:ASTERHAND_SURFACE_CYCLES; $savedReport=$env:ASTERHAND_SURFACE_REPORT
try {
    if ($Cycles -gt 0 -and -not $Desktop) { throw 'Cycles requires an explicitly enabled interactive desktop.' }
    $env:ASTERHAND_DESKTOP_TEST = if ($Desktop) {'1'} else {'0'}
    $env:ASTERHAND_SURFACE_CYCLES = $Cycles
    $env:ASTERHAND_SURFACE_REPORT = Join-Path $OutputDirectory 'surface-cost.json'
    & $Go -C $repo test ./internal/quickinput ./internal/desktop -count=1 -json | Tee-Object -FilePath (Join-Path $OutputDirectory 'go-desktop.jsonl')
    if ($LASTEXITCODE -ne 0) { throw 'Quick input Go tests failed.' }
    & node --test (Join-Path $repo 'tests/quick-input.test.mjs') (Join-Path $repo 'tests/quick-input-bridge.test.mjs') (Join-Path $repo 'tests/desktop-settings.test.mjs')
    if ($LASTEXITCODE -ne 0) { throw 'Quick input text tests failed.' }
    & (Join-Path $PSScriptRoot 'test-installer-webview2.ps1') -OutputDirectory (Join-Path $OutputDirectory 'installer')
} finally {
    $env:ASTERHAND_DESKTOP_TEST=$savedDesktop; $env:ASTERHAND_SURFACE_CYCLES=$savedCycles; $env:ASTERHAND_SURFACE_REPORT=$savedReport
}
