#requires -Version 7.0
param([Parameter(Mandatory)][string]$Workspace)
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'dependency-tools.ps1')
. (Join-Path $PSScriptRoot 'release-prepare.ps1')
$catalog = Read-DependencyCatalog (Join-Path (Split-Path -Parent $PSScriptRoot) 'packaging/dependencies.json')
$null = Get-ReleaseGo $catalog $Workspace ''
