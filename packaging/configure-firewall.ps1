#requires -Version 5.1
[CmdletBinding()]
param([switch]$Apply)
$ErrorActionPreference = 'Stop'
$installation = Get-Content -LiteralPath (Join-Path $PSScriptRoot 'installation.json') -Raw | ConvertFrom-Json
$executable = [IO.Path]::GetFullPath($installation.executable)
$prefix = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot 'versions')).TrimEnd('\') + '\'
if (-not $executable.StartsWith($prefix, [StringComparison]::OrdinalIgnoreCase)) { throw 'Invalid executable path.' }
if (-not $Apply) {
    Write-Host 'This adds one Windows firewall rule: inbound TCP 17433 to Asterhand, only from Tailscale address ranges.'
    $choice = Read-Host 'Apply this rule with administrator approval? [y/N]'
    if ($choice -notmatch '^[yY]') { return }
    $windowsPowerShell = Join-Path $env:WINDIR 'System32\WindowsPowerShell\v1.0\powershell.exe'
    Start-Process -FilePath $windowsPowerShell -ArgumentList @('-NoProfile','-File',('"' + $PSCommandPath + '"'),'-Apply') -Verb RunAs -WindowStyle Hidden -Wait
    return
}
$ruleName = 'Asterhand-Tailnet-TCP-17433'
$existing = Get-NetFirewallRule -Name $ruleName -ErrorAction SilentlyContinue
if ($existing) { $existing | Remove-NetFirewallRule }
New-NetFirewallRule -Name $ruleName -DisplayName 'Asterhand from Tailscale devices' -Direction Inbound -Action Allow -Protocol TCP -LocalPort 17433 -Program $executable -RemoteAddress '100.64.0.0/10','fd7a:115c:a1e0::/48' -Profile Any | Out-Null
Write-Host 'The Tailscale-only Asterhand firewall rule is active. Tailnet ACLs must also allow this connection.'
