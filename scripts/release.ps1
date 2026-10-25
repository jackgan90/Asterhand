#requires -Version 7.0
[CmdletBinding()]
param(
    [ValidatePattern('^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$')]
    [string]$Repository = 'jackgan90/Asterhand',
    [ValidateSet('Local', 'Draft', 'Prerelease', 'Stable')][string]$Mode = 'Draft',
    [ValidatePattern('^[A-Za-z0-9][A-Za-z0-9._-]*$')][string]$Tag,
    [string]$Platform = 'windows-amd64',
    [string]$OutputDirectory,
    [string]$ResumeDirectory,
    [string]$Go,
    [string]$Gh = 'gh',
    [string]$TargetCommit,
    [switch]$AllowDirty,
    [switch]$PrepareOnly
)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$repo = Split-Path -Parent $PSScriptRoot
. (Join-Path $PSScriptRoot 'dependency-tools.ps1')
. (Join-Path $PSScriptRoot 'release-prepare.ps1')
. (Join-Path $PSScriptRoot 'release-github.ps1')
$version = Get-ReleaseVersion $repo
if (-not $Tag) { $Tag = 'v' + $version }
$target = Get-ReleaseBuilder $repo $Platform
$hostOS = if ($IsWindows) { 'windows' } elseif ($IsMacOS) { 'darwin' } else { 'linux' }
if ($target.host_os -ne $hostOS) { throw "Release platform $Platform requires a $($target.host_os) build host." }
$dirty = @(& git -C $repo status --porcelain)
if ($LASTEXITCODE -ne 0) { throw 'Cannot read source status.' }
if ($dirty.Count -gt 0 -and (-not $AllowDirty -or $Mode -eq 'Stable')) { throw 'Commit the source first, or use -AllowDirty for an explicitly non-stable test release.' }
$commit = (& git -C $repo rev-parse HEAD).Trim()
if ($LASTEXITCODE -ne 0) { throw 'Cannot read source commit.' }
if ($Mode -eq 'Stable' -and ($version.Contains('-') -or $Tag -ne "v$version")) { throw 'Stable releases require a non-prerelease application version and its matching v<version> tag.' }
$savedToken = $env:GH_TOKEN
$savedCache = $env:GOCACHE
$savedModCache = $env:GOMODCACHE
try {
    if ($Mode -ne 'Local' -and -not $PrepareOnly) {
        Connect-ReleaseGitHub $Gh
        $remote = Invoke-ReleaseGh @('api', "repos/$Repository") | ConvertFrom-Json
        if ($remote.private) { throw 'This installer requires a public release repository for anonymous downloads.' }
        if (@(Get-ReleaseByTag $Repository $Tag | Where-Object { -not $_.draft }).Count -gt 0) { throw 'Release is already public; use a new tag.' }
        if (-not $TargetCommit) {
            $TargetCommit = if ($AllowDirty) { $remote.default_branch } else { $commit }
        }
        $resolved = Invoke-ReleaseGh @('api', "repos/$Repository/commits/$TargetCommit", '--jq', '.sha')
        $TargetCommit = $resolved.Trim()
        if ($Mode -eq 'Stable' -and $TargetCommit -ne $commit) { throw 'Stable release target must match the clean local source commit.' }
        Assert-ReleaseTagTarget $Repository $Tag $TargetCommit
    }
    $source = Get-SourceFingerprint $repo
    $tree = Get-ReleaseTreeFingerprint $repo
    if ($ResumeDirectory) {
        $workspace = $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($ResumeDirectory)
        $state = Get-Content (Join-Path $workspace 'release-state.json') -Raw | ConvertFrom-Json
        if ($state.schema -ne 1 -or $state.repository -ne $Repository -or $state.tag -ne $Tag -or $state.platform -ne $Platform -or $state.source_sha256 -ne $source -or $state.release_tree_sha256 -ne $tree -or $state.mode -ne $Mode -or ($state.target_commit -and $state.target_commit -ne $TargetCommit)) { throw 'Resume settings/source differ from the prepared release.' }
        $assets = Join-Path $workspace 'assets'
        Assert-ReleaseChecksums $assets
    } else {
        if (-not $OutputDirectory) { $OutputDirectory = Join-Path $repo ('out/release-' + [guid]::NewGuid().ToString('N')) }
        $workspace = $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($OutputDirectory)
        if (Test-Path -LiteralPath $workspace) { throw 'Choose a new output directory, or resume an already verified preparation with -ResumeDirectory.' }
        if ($workspace.StartsWith($repo.TrimEnd('\','/') + [IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase)) {
            & git -C $repo check-ignore --quiet $workspace
            if ($LASTEXITCODE -ne 0) { throw 'A release workspace inside the repository must be ignored by Git; use out/ or a directory outside the repository.' }
        }
        New-Item -ItemType Directory -Path $workspace | Out-Null
        Set-Content (Join-Path $workspace 'go.mod') 'module asterhand.local/release-output' -Encoding ascii
        $assets = Join-Path $workspace 'assets'
        if (-not $env:GOCACHE) { $env:GOCACHE = Join-Path $repo 'out/cache/go-build' }
        if (-not $env:GOMODCACHE) { $env:GOMODCACHE = Join-Path $repo 'out/cache/go-modules' }
        Write-Host 'Preparing pinned build tools and runtime dependencies.'
        & (Join-Path $repo $target.prepare) -Workspace $workspace -Go $Go
        $prepared = Get-Content (Join-Path $workspace 'prepared.json') -Raw | ConvertFrom-Json
        $builderGo = $prepared.go
        Write-Host 'Building and checking the platform bundle.'
        & (Join-Path $repo $target.builder) -Go $builderGo -NodeDistribution $prepared.node_distribution -RuntimeRoot $prepared.runtime_root -OutputDir $assets -Repository $Repository -ReleaseTag $Tag
        Write-Host 'Running source and installation regression checks.'
        & $builderGo -C $repo test ./... 2>&1 | Tee-Object -FilePath (Join-Path $workspace 'go-tests.log')
        if ($LASTEXITCODE -ne 0) { throw 'Go regression tests failed.' }
        & $builderGo -C $repo vet ./... 2>&1 | Tee-Object -FilePath (Join-Path $workspace 'go-vet.log')
        if ($LASTEXITCODE -ne 0) { throw 'Go vet failed.' }
        & $prepared.node_executable --test (Join-Path $repo 'tests/plugin-contract.test.mjs') (Join-Path $repo 'tests/web-state.test.mjs') 2>&1 | Tee-Object -FilePath (Join-Path $workspace 'node-tests.log')
        if ($LASTEXITCODE -ne 0) { throw 'Node contract tests failed.' }
        & (Join-Path $PSScriptRoot 'test-release.ps1') -OutputDirectory (Join-Path $workspace 'release-contract') | Out-Host
        & (Join-Path $PSScriptRoot 'test-installer-system-dependencies.ps1') -OutputDirectory (Join-Path $workspace 'system-dependency-contract') | Out-Host
        & (Join-Path $repo $target.verify) -PackageDirectory $assets -OutputDirectory (Join-Path $workspace 'install-check')
        if ((Get-ReleaseTreeFingerprint $repo) -ne $tree) { throw 'Source changed while the release was being prepared.' }
        Copy-Item -LiteralPath (Join-Path $repo 'packaging/release-targets.json'),(Join-Path $repo 'packaging/dependencies.json'),(Join-Path $repo 'LICENSE') -Destination $assets
        Copy-Item -LiteralPath (Join-Path $PSScriptRoot 'test-release-download.ps1') -Destination $assets
        $manifestPath = Join-Path $assets 'release-manifest.json'
        $manifest = Get-Content $manifestPath -Raw | ConvertFrom-Json -AsHashtable
        $manifest.repository = $Repository
        $manifest.tag = $Tag
        $manifest.source_commit = $commit
        $manifest.source_dirty = ($dirty.Count -gt 0)
        $manifest.release_tree_sha256 = $tree
        $manifest | ConvertTo-Json -Depth 8 | Set-Content $manifestPath -Encoding utf8NoBOM
        $catalog = Read-DependencyCatalog (Join-Path $repo 'packaging/dependencies.json')
        @{schema=1;platform=$Platform;source_sha256=$source;created_utc=[DateTime]::UtcNow.ToString('o');os=[Environment]::OSVersion.VersionString;powershell=$PSVersionTable.PSVersion.ToString();go=$catalog.go.version;node=$catalog.node.version;vc_runtime=$catalog.vc_runtime.version;go_tests='passed';go_vet='passed';node_contracts='passed';release_contracts='passed';system_dependency_contracts='passed';vendor_installer_hashes_and_signatures='passed';windows_install_regression='passed';clean_windows='not_run';tailscale_fresh_install='not_run';vc_runtime_fresh_install='not_run';model_validation='not_run'} |
            ConvertTo-Json | Set-Content (Join-Path $assets 'validation.json') -Encoding utf8NoBOM
        @"
# Asterhand $Tag

Windows x64 complete assistant bundle. Download **install.ps1** from this release and run it with Windows PowerShell 5.1 or later. The script downloads and verifies this exact release, installs the required Microsoft VC++ x64 runtime when missing or too old, and installs missing Tailscale with Windows administrator approval.

Installation ends before configuration. Start the installed start.ps1 when ready to configure Tailscale sign-in, model credentials, authorized folders and node pairing. Go, Git and npm are not required on the destination machine.

Application version: $version. Source commit: $commit. Working tree modified: $($dirty.Count -gt 0). Package source and dependency digests are in release-manifest.json. Test builds can contain uncommitted changes; production releases require the exact clean source commit in the target repository. See validation.json for automated checks and explicit validation limits.

Linux/macOS are reserved extension targets and are not included. Asterhand uses MIT; third-party license notices are included in the platform bundle. Microsoft VC++ and Tailscale installers are downloaded from their vendors rather than redistributed. These artifacts are not Authenticode-signed; verify the publishing repository and SHA256SUMS.
"@ | Set-Content (Join-Path $assets 'RELEASE-NOTES.md') -Encoding utf8NoBOM
        Write-ReleaseChecksums $assets
        @{schema=1;repository=$Repository;tag=$Tag;mode=$Mode;platform=$Platform;source_sha256=$source;release_tree_sha256=$tree;target_commit=$TargetCommit} |
            ConvertTo-Json | Set-Content (Join-Path $workspace 'release-state.json') -Encoding utf8NoBOM
    }
    if ($Mode -ne 'Local' -and -not $PrepareOnly) {
        Write-Host 'Uploading draft assets and verifying their downloaded contents.'
        $url = Publish-ReleaseAssets $Repository $Tag $Mode $assets $TargetCommit (Join-Path $workspace 'remote-verified')
        $url | Set-Content (Join-Path $workspace 'release-url.txt')
    }
    Write-Host "Release workspace: $workspace"
} finally {
    $env:GH_TOKEN = $savedToken
    $env:GOCACHE = $savedCache
    $env:GOMODCACHE = $savedModCache
}
