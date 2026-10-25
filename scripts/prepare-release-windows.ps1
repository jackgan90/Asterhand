#requires -Version 7.0
[CmdletBinding()]
param([Parameter(Mandatory)][string]$Workspace, [string]$Go)
$ErrorActionPreference = 'Stop'
if (-not $IsWindows) { throw 'Windows dependency preparation requires a Windows host.' }
$repo = Split-Path -Parent $PSScriptRoot
. (Join-Path $PSScriptRoot 'dependency-tools.ps1')
. (Join-Path $PSScriptRoot 'release-prepare.ps1')
$catalog = Read-DependencyCatalog (Join-Path $repo 'packaging/dependencies.json')
$builderGo = Get-ReleaseGo $catalog $Workspace $Go
foreach ($name in @('vc_runtime', 'tailscale')) {
    $dependency = $catalog.$name
    $extension = if ($name -eq 'tailscale') { '.msi' } else { '.exe' }
    $file = Join-Path $Workspace ($name + $extension)
    Write-Host "Verifying the pinned vendor installer: $name $($dependency.version)"
    Invoke-WebRequest -Uri $dependency.windows_amd64_url -OutFile $file
    if ((Get-FileHash -LiteralPath $file).Hash.ToLowerInvariant() -ne $dependency.windows_amd64_sha256) { throw "Vendor installer checksum mismatch: $name" }
    $signature = Get-AuthenticodeSignature -LiteralPath $file
    $signer = if ($name -eq 'tailscale') { 'Tailscale' } else { 'Microsoft Corporation' }
    if ($signature.Status -ne 'Valid' -or $signature.SignerCertificate.Subject -notmatch $signer) { throw "Vendor installer signature mismatch: $name" }
}
& (Join-Path $PSScriptRoot 'prepare-dependencies.ps1') -OutputDirectory (Join-Path $Workspace 'dependencies')
$prepared = Get-Content (Join-Path $Workspace 'dependencies/prepared.json') -Raw | ConvertFrom-Json -AsHashtable
$prepared.go = $builderGo
$prepared.node_executable = Join-Path $prepared.node_distribution 'node.exe'
$prepared | ConvertTo-Json | Set-Content (Join-Path $Workspace 'prepared.json') -Encoding utf8NoBOM
