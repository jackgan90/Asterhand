#requires -Version 7.0
[CmdletBinding()]
param(
    [string]$CatalogPath,
    [Parameter(Mandatory)][string]$OutputDirectory,
    [string]$NodeArchive,
    [switch]$RefreshLock
)
$ErrorActionPreference = 'Stop'
$repo = Split-Path -Parent $PSScriptRoot
. (Join-Path $PSScriptRoot 'dependency-tools.ps1')
if (-not $CatalogPath) { $CatalogPath = Join-Path $repo 'packaging\dependencies.json' }
$catalog = Read-DependencyCatalog $CatalogPath
$OutputDirectory = [IO.Path]::GetFullPath($OutputDirectory)
if (Test-Path -LiteralPath $OutputDirectory) { throw 'Choose a new empty output directory; existing runtime directories are never overwritten.' }
New-Item -ItemType Directory -Path $OutputDirectory | Out-Null
if (-not $NodeArchive) {
    $NodeArchive = Join-Path $OutputDirectory 'node.zip'
    Invoke-WebRequest -Uri $catalog.node.windows_amd64_url -OutFile $NodeArchive
}
if ((Get-FileHash -LiteralPath $NodeArchive -Algorithm SHA256).Hash.ToLowerInvariant() -ne $catalog.node.windows_amd64_sha256) {
    throw 'Node archive checksum mismatch.'
}
$nodeRoot = Join-Path $OutputDirectory 'node-distribution'
Expand-Archive -LiteralPath $NodeArchive -DestinationPath $nodeRoot
$nodeDirectory = Join-Path $nodeRoot ('node-v' + $catalog.node.version + '-win-x64')
$node = Join-Path $nodeDirectory 'node.exe'
if ((& $node --version) -ne ('v' + $catalog.node.version) -or $LASTEXITCODE -ne 0) { throw 'Node archive version mismatch.' }
$runtimeRoot = Join-Path $OutputDirectory 'runtime'
New-Item -ItemType Directory -Path $runtimeRoot | Out-Null
$dependencies = Get-RuntimeDependencies $catalog
@{name='runtime';private=$true;dependencies=$dependencies} | ConvertTo-Json -Depth 5 | Set-Content (Join-Path $runtimeRoot 'package.json') -Encoding utf8NoBOM
$npm = Join-Path $nodeDirectory 'node_modules\npm\bin\npm-cli.js'
$savedPath = $env:PATH
try {
    $env:PATH = $nodeDirectory + [IO.Path]::PathSeparator + $savedPath
    if ($RefreshLock) {
        & $node $npm install --prefix $runtimeRoot --package-lock-only --ignore-scripts --no-audit --no-fund --registry=https://registry.npmjs.org
        if ($LASTEXITCODE -ne 0) { throw 'Lock refresh failed.' }
        $lock = Get-Content (Join-Path $runtimeRoot 'package-lock.json') -Raw | ConvertFrom-Json -AsHashtable
        foreach ($component in $catalog.components) {
            $locked = $lock.packages['node_modules/' + $component.package]
            $component | Add-Member source $locked.resolved -Force
            $component | Add-Member integrity $locked.integrity -Force
        }
    } else {
        Copy-Item (Join-Path $repo 'packaging\runtime-lock.json') (Join-Path $runtimeRoot 'package-lock.json')
    }
    $null = Assert-DependencyLock $catalog (Join-Path $runtimeRoot 'package-lock.json')
    & $node $npm ci --prefix $runtimeRoot --omit=dev --ignore-scripts --no-audit --no-fund --registry=https://registry.npmjs.org
    if ($LASTEXITCODE -ne 0) { throw 'Locked runtime install failed. Evidence retained in the output directory.' }
} finally { $env:PATH = $savedPath }
$catalog | ConvertTo-Json -Depth 10 | Set-Content (Join-Path $OutputDirectory 'dependencies.json') -Encoding utf8NoBOM
Copy-Item (Join-Path $runtimeRoot 'package-lock.json') (Join-Path $OutputDirectory 'runtime-lock.json')
@{node_distribution=$nodeDirectory;runtime_root=$runtimeRoot;catalog=(Join-Path $OutputDirectory 'dependencies.json');refresh_lock=[bool]$RefreshLock;validation='not_run'} |
    ConvertTo-Json | Set-Content (Join-Path $OutputDirectory 'prepared.json') -Encoding utf8NoBOM
Write-Host "Prepared pinned dependencies in $OutputDirectory. Review the candidate catalog/lock, then run verify-dependencies.ps1. No source or active installation was changed."
