#requires -Version 7.0
[CmdletBinding()]
param(
    [string]$InstallDir = (Join-Path $env:LOCALAPPDATA 'Programs\Asterhand'),
    [string]$DataDir,
    [string]$PreparedPath,
    [string]$Go,
    [ValidateSet('Auto','Create','Skip','Keep')][string]$DesktopShortcut = 'Auto',
    [switch]$SkipTailscale
)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
if (-not $IsWindows) { throw 'Local installation requires a Windows build host.' }
$repo = Split-Path -Parent $PSScriptRoot
. (Join-Path $PSScriptRoot 'dependency-tools.ps1')
. (Join-Path $PSScriptRoot 'release-prepare.ps1')
$workspace = Join-Path $repo 'out\local-install'
New-Item -ItemType Directory -Path $workspace -Force | Out-Null
$guard = $null
$savedCache = $env:GOCACHE
$savedModCache = $env:GOMODCACHE

function Get-LocalGo($Catalog) {
    if ($Go) { return (Get-Command $Go -ErrorAction Stop).Source }
    $available = Get-Command go -ErrorAction SilentlyContinue
    if ($available) {
        $version = & $available.Source version
        if ($LASTEXITCODE -eq 0 -and $version -match ('go' + [regex]::Escape($Catalog.go.version) + '\s')) {
            return $available.Source
        }
    }
    # Use a fresh workspace so interrupted preparation can be retried safely.
    $toolchain = Join-Path $workspace ('go-' + [guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Path $toolchain | Out-Null
    $executable = Get-ReleaseGo $Catalog $toolchain ''
    @{executable=$executable;version=$Catalog.go.version} | ConvertTo-Json |
        Set-Content -LiteralPath (Join-Path $workspace 'go.json') -Encoding utf8NoBOM
    return $executable
}

function Get-LocalDependencies([string]$BuilderGo) {
    $inputs = @('packaging\dependencies.json','packaging\runtime-lock.json') | ForEach-Object {
        (Get-FileHash -LiteralPath (Join-Path $repo $_)).Hash
    }
    $fingerprint = $inputs -join ':'
    $cachePath = Join-Path $workspace 'prepared.json'
    $prepared = $null
    if ($PreparedPath) {
        $prepared = Get-Content -LiteralPath $PreparedPath -Raw | ConvertFrom-Json
    } elseif (Test-Path -LiteralPath $cachePath) {
        $cached = Get-Content -LiteralPath $cachePath -Raw | ConvertFrom-Json
        if ($cached.inputs -eq $fingerprint) { $prepared = $cached }
    }
    if ($null -eq $prepared) {
        $directory = Join-Path $workspace ('dependencies-' + [guid]::NewGuid().ToString('N'))
        & (Join-Path $PSScriptRoot 'prepare-dependencies.ps1') -OutputDirectory $directory | Out-Host
        $prepared = Get-Content -LiteralPath (Join-Path $directory 'prepared.json') -Raw | ConvertFrom-Json
    }
    $null = Assert-PreparedDependencies $repo $prepared.node_distribution $prepared.runtime_root $BuilderGo
    @{inputs=$fingerprint;node_distribution=$prepared.node_distribution;runtime_root=$prepared.runtime_root} |
        ConvertTo-Json | Set-Content -LiteralPath $cachePath -Encoding utf8NoBOM
    return $prepared
}

function Find-LocalPackage([string]$Source) {
    $recordPath = Join-Path $repo 'out\latest-build.json'
    if (-not (Test-Path -LiteralPath $recordPath)) { return $null }
    try {
        $record = Get-Content -LiteralPath $recordPath -Raw | ConvertFrom-Json
        if ($record.schema -ne 1 -or $record.source_sha256 -ne $Source) { return $null }
        $manifest = Get-Content -LiteralPath $record.manifest -Raw | ConvertFrom-Json
        if ($manifest.schema -ne 1 -or $manifest.source_sha256 -ne $Source) { return $null }
        $packages = @($manifest.packages | Where-Object { $_.os -eq 'windows' -and $_.arch -eq 'amd64' })
        if ($packages.Count -ne 1 -or $packages[0].file -notmatch '^asterhand-[A-Za-z0-9.-]+-windows-amd64\.zip$') { return $null }
        $path = Join-Path (Split-Path -Parent $record.manifest) $packages[0].file
        if ((Get-Item -LiteralPath $path).Length -ne $packages[0].size -or
            (Get-FileHash -LiteralPath $path).Hash -ne $packages[0].sha256) { return $null }
        return Split-Path -Parent $record.manifest
    } catch {
        Write-Host 'The recorded build is unavailable or invalid; rebuilding current source.'
        return $null
    }
}

try {
    try {
        $guard = [IO.File]::Open((Join-Path $workspace '.run.lock'), [IO.FileMode]::OpenOrCreate, [IO.FileAccess]::ReadWrite, [IO.FileShare]::None)
    } catch { throw 'Another local build/install is running. Wait for it to finish.' }
    if (-not $env:GOCACHE) { $env:GOCACHE = Join-Path $repo 'out\cache\go-build' }
    if (-not $env:GOMODCACHE) { $env:GOMODCACHE = Join-Path $repo 'out\cache\go-modules' }
    $source = Get-SourceFingerprint $repo
    $packageDirectory = Find-LocalPackage $source
    if ($packageDirectory) {
        Write-Host "Current source already has a verified package; skipping the build: $packageDirectory"
    } else {
        $catalog = Read-DependencyCatalog (Join-Path $repo 'packaging\dependencies.json')
        $goRecord = Join-Path $workspace 'go.json'
        if (-not $Go -and (Test-Path -LiteralPath $goRecord)) {
            $cachedGo = Get-Content -LiteralPath $goRecord -Raw | ConvertFrom-Json
            if ($cachedGo.version -eq $catalog.go.version -and (Test-Path -LiteralPath $cachedGo.executable)) { $Go = $cachedGo.executable }
        }
        $builderGo = Get-LocalGo $catalog
        $prepared = Get-LocalDependencies $builderGo
        $run = Join-Path $workspace ('runs\' + (Get-Date -Format 'yyyyMMdd-HHmmss') + '-' + [guid]::NewGuid().ToString('N'))
        $packageDirectory = Join-Path $run 'package'
        Write-Host 'Building the current source with the standard package builder. The installed node stays online during this step.'
        & (Join-Path $PSScriptRoot 'build.ps1') -Go $builderGo -NodeDistribution $prepared.node_distribution `
            -RuntimeRoot $prepared.runtime_root -OutputDir $packageDirectory | Out-Host
    }
    $manifest = Get-Content -LiteralPath (Join-Path $packageDirectory 'release-manifest.json') -Raw | ConvertFrom-Json
    if ($manifest.source_sha256 -ne (Get-SourceFingerprint $repo)) { throw 'Source changed before installation; retry with the current source.' }
    $package = @($manifest.packages | Where-Object { $_.os -eq 'windows' -and $_.arch -eq 'amd64' })
    if ($package.Count -ne 1) { throw 'The build did not produce exactly one Windows x64 package.' }
    $parameters = @{
        Repository=[string]$manifest.repository
        PackagePath=(Join-Path $packageDirectory $package[0].file)
        InstallDir=$InstallDir
        DesktopShortcut=$DesktopShortcut
        StopRunning=$true
        StartAfterInstall=$true
        SkipTailscale=[bool]$SkipTailscale
    }
    if ($PSBoundParameters.ContainsKey('DataDir')) { $parameters.DataDir = $DataDir }
    Write-Host "Installing through the standard installer. Package: $packageDirectory"
    & (Join-Path $repo 'install.ps1') @parameters
    Write-Host 'Local installation complete. Asterhand is running.'
} finally {
    if ($null -ne $guard) { $guard.Dispose() }
    $env:GOCACHE = $savedCache
    $env:GOMODCACHE = $savedModCache
}
