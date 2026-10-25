#requires -Version 5.1
[CmdletBinding()]
param([switch]$NoBrowser)
$ErrorActionPreference = 'Stop'
$installation = Get-Content -LiteralPath (Join-Path $PSScriptRoot 'installation.json') -Raw | ConvertFrom-Json
$launcher = Join-Path $PSScriptRoot 'AsterhandLauncher.exe'
if (-not (Test-Path -LiteralPath $launcher -PathType Leaf)) { throw 'Asterhand launcher is missing. Reinstall this package to repair the startup entry.' }
$arguments = @('--quiet')
if ($NoBrowser) { $arguments += '--no-browser' }
$process = Start-Process -FilePath $launcher -ArgumentList $arguments -WindowStyle Hidden -Wait -PassThru
if ($process.ExitCode -ne 0) {
    throw ('Asterhand could not start. Inspect launcher.log and error.log in ' + [string]$installation.data + '. An existing node may still be starting; do not force-start another instance.')
}
Write-Host 'Asterhand is running. Open or stop it from the system tray or the local Web UI.'
