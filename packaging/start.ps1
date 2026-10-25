#requires -Version 5.1
[CmdletBinding()]
param([switch]$NoBrowser)
$ErrorActionPreference = 'Stop'
$installation = Get-Content -LiteralPath (Join-Path $PSScriptRoot 'installation.json') -Raw | ConvertFrom-Json
$executable = [IO.Path]::GetFullPath($installation.executable)
$programPrefix = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot 'versions')).TrimEnd('\') + '\'
if (-not $executable.StartsWith($programPrefix, [StringComparison]::OrdinalIgnoreCase)) { throw 'Invalid installation executable path.' }
$dataDir = [IO.Path]::GetFullPath($installation.data)
if ($dataDir.Contains('"')) { throw 'Invalid data directory.' }
function Test-NodeRunning {
    # Windows PowerShell 5.1 turns native stderr into errors even with 2>$null.
    # A stopped node is an expected status result, not a launcher failure.
    $savedPreference = $ErrorActionPreference
    try {
        $ErrorActionPreference = 'Continue'
        & $executable status --home $dataDir 2>$null | Out-Null
        return $LASTEXITCODE -eq 0
    } finally { $ErrorActionPreference = $savedPreference }
}
if (Test-NodeRunning) {
    if (-not $NoBrowser) { & $executable open --home $dataDir }
    return
}
$arguments = @('start', '--home', ('"' + $dataDir + '"'))
if (-not $NoBrowser) { $arguments += '--open' }
$process = Start-Process -FilePath $executable -ArgumentList $arguments -WindowStyle Hidden -RedirectStandardOutput (Join-Path $dataDir 'startup.log') -RedirectStandardError (Join-Path $dataDir 'error.log') -PassThru
$deadline = [DateTime]::UtcNow.AddSeconds(20)
while ([DateTime]::UtcNow -lt $deadline) {
    if ($process.HasExited) { throw ('Asterhand failed to start. See ' + (Join-Path $dataDir 'error.log')) }
    if (Test-NodeRunning) { Write-Host 'Asterhand is running. Stop it from Node settings in the local Web UI.'; return }
    Start-Sleep -Milliseconds 250
}
throw 'Startup health check timed out. Inspect the node error.log before retrying.'
