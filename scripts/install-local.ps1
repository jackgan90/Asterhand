#requires -Version 7.0
[CmdletBinding()]
param(
    [string]$InstallDir = (Join-Path $env:LOCALAPPDATA 'Programs\Asterhand'),
    [string]$DataDir,
    [string]$PreparedPath,
    [string]$Go,
    [ValidateSet('Fast','Full')][string]$Mode = 'Fast',
    [switch]$RepairDependencies,
    [ValidateSet('Auto','Create','Skip','Keep')][string]$DesktopShortcut = 'Auto',
    [switch]$SkipTailscale
)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
if (-not $IsWindows) { throw 'Local installation requires a Windows build host.' }
if ($RepairDependencies -and $PreparedPath) { throw 'RepairDependencies cannot be combined with PreparedPath.' }
$repo = Split-Path -Parent $PSScriptRoot
foreach ($helper in @('dependency-tools.ps1','release-prepare.ps1','build-progress.ps1','local-build-cache.ps1','local-runtime-cache.ps1')) { . (Join-Path $PSScriptRoot $helper) }
$workspace = Join-Path $repo 'out/local-install'
[IO.Directory]::CreateDirectory($workspace) | Out-Null
$guard = $null
$transcribing = $false
$savedCache = $env:GOCACHE
$savedModCache = $env:GOMODCACHE
$stages = [Collections.Generic.List[object]]::new()
$report = [ordered]@{status='running';mode=$Mode;stages=$stages;runtime_reused=$false;application_reused=$false}

function Measure-LocalStage([string]$Name, [scriptblock]$Action) {
    Write-Host "[stage] $Name"
    $watch = [Diagnostics.Stopwatch]::StartNew()
    $status = 'failed'
    try { & $Action; $status = 'passed' }
    finally {
        $stages.Add(@{name=$Name;status=$status;seconds=[math]::Round($watch.Elapsed.TotalSeconds,3)})
        Write-Host ("[{0}] {1}: {2:n1}s" -f $status, $Name, $watch.Elapsed.TotalSeconds)
    }
}

function Get-LocalGo($Catalog) {
    if (-not $Go -and (Test-Path -LiteralPath (Join-Path $workspace 'go.json'))) {
        try {
            $cached = Get-Content -LiteralPath (Join-Path $workspace 'go.json') -Raw | ConvertFrom-Json
            if ($cached.version -eq $Catalog.go.version -and (Test-Path -LiteralPath $cached.executable)) { $Go = $cached.executable }
        } catch { Write-Host 'Cached Go record is unavailable; resolving the pinned compiler.' }
    }
    $available = if ($Go) { Get-Command $Go -ErrorAction Stop } else { Get-Command go -ErrorAction SilentlyContinue }
    if ($available) {
        $version = & $available.Source version
        if ($LASTEXITCODE -eq 0 -and $version -match ('go'+[regex]::Escape($Catalog.go.version)+'\s')) { return $available.Source }
        if ($Go) { throw 'Explicit Go compiler version mismatch.' }
    }
    $toolchain = Join-Path $workspace ('go-'+[guid]::NewGuid().ToString('N'))
    [IO.Directory]::CreateDirectory($toolchain) | Out-Null
    Invoke-BuildProcess 'Prepare pinned Go compiler' (Join-Path $PSHOME 'pwsh.exe') @('-NoProfile','-File',(Join-Path $PSScriptRoot 'prepare-local-go.ps1'),'-Workspace',$toolchain)
    $executable = Join-Path $toolchain 'toolchain/go/bin/go.exe'
    Write-LocalRecord @{executable=$executable;version=$Catalog.go.version} (Join-Path $workspace 'go.json')
    return $executable
}

function Find-LocalPackage([string]$Source) {
    $recordPath = Join-Path $repo 'out/latest-build.json'
    if (-not (Test-Path -LiteralPath $recordPath)) { return $null }
    try {
        $record = Get-Content -LiteralPath $recordPath -Raw | ConvertFrom-Json
        if ($record.schema -ne 1 -or $record.source_sha256 -ne $Source) { return $null }
        $manifest = Get-Content -LiteralPath $record.manifest -Raw | ConvertFrom-Json
        if ($manifest.schema -ne 1 -or $manifest.source_sha256 -ne $Source) { return $null }
        $packages = @($manifest.packages | Where-Object { $_.os -eq 'windows' -and $_.arch -eq 'amd64' })
        if ($packages.Count -ne 1 -or $packages[0].file -notmatch '^asterhand-[A-Za-z0-9.-]+-windows-amd64\.zip$') { return $null }
        $path = Join-Path (Split-Path -Parent $record.manifest) $packages[0].file
        if ((Get-Item -LiteralPath $path).Length -ne $packages[0].size -or (Get-FileHash -LiteralPath $path).Hash -ne $packages[0].sha256) { return $null }
        return Split-Path -Parent $record.manifest
    } catch { Write-Host 'Recorded full package is unavailable; rebuilding.'; return $null }
}

try {
    try { $guard = [IO.File]::Open((Join-Path $workspace '.run.lock'), [IO.FileMode]::OpenOrCreate, [IO.FileAccess]::ReadWrite, [IO.FileShare]::None) }
    catch { throw 'Another local build/install is running. Wait for it to finish.' }
    if (-not $env:GOCACHE) { $env:GOCACHE = Join-Path $repo 'out/cache/go-build' }
    if (-not $env:GOMODCACHE) { $env:GOMODCACHE = Join-Path $repo 'out/cache/go-modules' }
    $run = Join-Path $workspace ('runs/'+(Get-Date -Format 'yyyyMMdd-HHmmss')+'-'+[guid]::NewGuid().ToString('N'))
    [IO.Directory]::CreateDirectory($run) | Out-Null
    Start-Transcript -LiteralPath (Join-Path $run 'install.log') | Out-Null
    $transcribing = $true
    $total = [Diagnostics.Stopwatch]::StartNew()
    $source = Measure-LocalStage 'Check current source' { Get-SourceFingerprint $repo }
    $report.source_sha256 = $source
    $parameters = @{InstallDir=$InstallDir;DesktopShortcut=$DesktopShortcut;StopRunning=$true;StartAfterInstall=$true;SkipTailscale=[bool]$SkipTailscale;ExpectedSourceDigest=$source}
    if ($PSBoundParameters.ContainsKey('DataDir')) { $parameters.DataDir = $DataDir }
    if ($Mode -eq 'Fast') {
        $catalog = Read-DependencyCatalog (Join-Path $repo 'packaging/dependencies.json')
        $builderGo = Measure-LocalStage 'Resolve pinned compiler' { Get-LocalGo $catalog }
        $initialize = @{InstallDir=$InstallDir;InitializeLocalCache=$true}
        if ($PSBoundParameters.ContainsKey('DataDir')) { $initialize.DataDir = $DataDir }
        $program = & (Join-Path $repo 'install.ps1') @initialize
        $runtime = Measure-LocalStage 'Resolve installed runtime' {
            Get-LocalInstalledRuntime $repo $workspace $program $builderGo $PreparedPath -Repair:$RepairDependencies
        }
        $report.runtime_reused = $runtime.reused
        $application = Find-LocalApplication $workspace $source $runtime.key
        if ($application) { $report.application_reused=$true; Write-Host 'Application cache hit: skipping compilation.' }
        else {
            $application = Join-Path $run 'application'
            Measure-LocalStage 'Build current application' {
                Invoke-BuildProcess 'Application assembly' (Join-Path $PSHOME 'pwsh.exe') @('-NoProfile','-File',(Join-Path $PSScriptRoot 'build-application.ps1'),'-Go',$builderGo,'-Node',(Join-Path $runtime.directory 'node/node.exe'),'-OutputDirectory',$application,'-SourceDigest',$source)
                New-LocalApplicationReceipt $application $source $runtime.key
                Save-LocalApplication $workspace $application
            }
        }
        $parameters.LocalBuildPath = $application
        $parameters.LocalRuntimePath = $runtime.directory
        $sourceFingerprint = ${function:Get-SourceFingerprint}
        $parameters.BeforeLocalActivation = {
            if ((& $sourceFingerprint $repo) -ne $source) { throw 'Source changed before activation; retry with current source.' }
        }.GetNewClosure()
    } else {
        $packageDirectory = if (-not $RepairDependencies -and -not $PreparedPath) { Find-LocalPackage $source } else { $null }
        if ($packageDirectory) { $report.application_reused=$true; Write-Host 'Full package cache hit: skipping build.' }
        else {
            $catalog = Read-DependencyCatalog (Join-Path $repo 'packaging/dependencies.json')
            $builderGo = Measure-LocalStage 'Resolve pinned compiler' { Get-LocalGo $catalog }
            $prepared = Measure-LocalStage 'Prepare full package dependencies' { Get-LocalPreparedDependencies $repo $workspace $builderGo $PreparedPath -Fresh:$RepairDependencies }
            $packageDirectory = Join-Path $run 'package'
            Measure-LocalStage 'Build full release package' {
                Invoke-BuildProcess 'Complete package build' (Join-Path $PSHOME 'pwsh.exe') @('-NoProfile','-File',(Join-Path $PSScriptRoot 'build.ps1'),'-Go',$builderGo,'-NodeDistribution',$prepared.node_distribution,'-RuntimeRoot',$prepared.runtime_root,'-OutputDir',$packageDirectory)
            }
        }
        $manifest = Get-Content -LiteralPath (Join-Path $packageDirectory 'release-manifest.json') -Raw | ConvertFrom-Json
        if ($manifest.source_sha256 -ne $source) { throw 'Full package source mismatch.' }
        $package = @($manifest.packages | Where-Object { $_.os -eq 'windows' -and $_.arch -eq 'amd64' })
        if ($package.Count -ne 1) { throw 'Expected exactly one Windows x64 package.' }
        $parameters.Repository = [string]$manifest.repository
        $parameters.PackagePath = Join-Path $packageDirectory $package[0].file
    }
    if ((Get-SourceFingerprint $repo) -ne $source) { throw 'Source changed before installation; retry with current source.' }
    Measure-LocalStage 'Verify, activate and start' { & (Join-Path $repo 'install.ps1') @parameters }
    if ((Get-SourceFingerprint $repo) -ne $source) { throw 'Source changed during installation; run again before acceptance.' }
    $report.status = 'passed'
    Write-Host ("Local installation complete, current source verified. Total: {0:n1}s. Log: {1}" -f $total.Elapsed.TotalSeconds,(Join-Path $run 'install.log'))
} catch { $report.status='failed'; $report.error=$_.Exception.Message; throw }
finally {
    try {
        if ($transcribing) {
            $report.total_seconds = [math]::Round($total.Elapsed.TotalSeconds,3)
            try { Write-LocalRecord $report (Join-Path $run 'timings.json') }
            finally { Stop-Transcript | Out-Null }
        }
    } finally {
        if ($null -ne $guard) { $guard.Dispose() }
        $env:GOCACHE = $savedCache
        $env:GOMODCACHE = $savedModCache
    }
}
