#requires -Version 7.0
[CmdletBinding()]
param([string]$Go = 'go', [Parameter(Mandatory)][string]$Node,
    [Parameter(Mandatory)][string]$OutputDirectory, [Parameter(Mandatory)][string]$SourceDigest)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$repo = Split-Path -Parent $PSScriptRoot
. (Join-Path $PSScriptRoot 'dependency-tools.ps1')
. (Join-Path $PSScriptRoot 'build-progress.ps1')
$catalog = Read-DependencyCatalog (Join-Path $repo 'packaging/dependencies.json')
if ((& $Go version) -notmatch ('go' + [regex]::Escape($catalog.go.version) + '\s') -or $LASTEXITCODE -ne 0) { throw 'Go compiler version mismatch.' }
if ($SourceDigest -ne (Get-SourceFingerprint $repo)) { throw 'Source changed before application build.' }
$OutputDirectory = $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($OutputDirectory)
if (Test-Path -LiteralPath $OutputDirectory) { throw 'Application output must be a new directory.' }
[IO.Directory]::CreateDirectory($OutputDirectory) | Out-Null
$versionSource = Get-Content -LiteralPath (Join-Path $repo 'internal/config/config.go') -Raw
if ($versionSource -notmatch 'const Version = "([^"]+)"') { throw 'Cannot resolve application version.' }
$version = $Matches[1]
$savedEnvironment = @{}
$buildEnvironment = @{GOOS='windows';GOARCH='amd64';CGO_ENABLED='0';GOFLAGS='-mod=readonly';GOWORK='off';GOEXPERIMENT='';GOAMD64='v1';GOTOOLCHAIN='local'}

function Copy-ApplicationLicenses {
    $root = Join-Path $OutputDirectory 'licenses/go-modules'
    [IO.Directory]::CreateDirectory($root) | Out-Null
    $modules = & $Go -C $repo list -m -f '{{.Path}}|{{.Version}}|{{.Dir}}' all
    if ($LASTEXITCODE -ne 0) { throw 'Cannot inspect Go module licenses.' }
    foreach ($line in $modules) {
        $parts = $line -split '\|',3
        if (-not $parts[1] -or -not $parts[2]) { continue }
        $destination = Join-Path $root (($parts[0] -replace '[/\\]','_') + '@' + $parts[1])
        [IO.Directory]::CreateDirectory($destination) | Out-Null
        $notices = @(Get-ChildItem -LiteralPath $parts[2] -File | Where-Object { $_.Name -match '^(LICENSE|COPYING|NOTICE)' })
        if ($notices.Count -eq 0) { throw "Missing license notice for $($parts[0])" }
        $notices | Copy-Item -Destination $destination
        if ($parts[0] -eq 'github.com/wailsapp/go-webview2') {
            Copy-Item -LiteralPath (Join-Path $parts[2] 'webviewloader/LICENSE') -Destination (Join-Path $destination 'webviewloader-LICENSE')
        }
    }
    $goRoot = & $Go env GOROOT
    if ($LASTEXITCODE -ne 0) { throw 'Cannot resolve Go license.' }
    Copy-Item -LiteralPath (Join-Path $goRoot 'LICENSE') -Destination (Join-Path $root 'Go-LICENSE')
    $mathRoot = Join-Path $OutputDirectory 'licenses/katex'
    [IO.Directory]::CreateDirectory($mathRoot) | Out-Null
    Copy-Item -LiteralPath (Join-Path $repo 'internal/quickinput/assets/vendor/katex/LICENSE'),(Join-Path $repo 'internal/quickinput/assets/vendor/katex/manifest.json') -Destination $mathRoot
}

try {
    foreach ($entry in $buildEnvironment.GetEnumerator()) {
        $savedEnvironment[$entry.Key] = [Environment]::GetEnvironmentVariable($entry.Key)
        [Environment]::SetEnvironmentVariable($entry.Key, $entry.Value)
    }
    Invoke-BuildProcess 'Verify frontend assets' $Node @((Join-Path $PSScriptRoot 'verify-frontend-assets.mjs'))
    foreach ($target in @(
        @{name='asterhand';file='asterhand.exe';gui=$false},
        @{name='asterhand-launcher';file='AsterhandLauncher.exe';gui=$true},
        @{name='asterhand-input-host';file='AsterhandInputHost.exe';gui=$true})) {
        $flags = "-s -w -X github.com/jackgan90/Asterhand/internal/config.SourceDigest=$SourceDigest"
        if ($target.gui) { $flags += ' -H=windowsgui' }
        $binary = Join-Path $OutputDirectory $target.file
        Invoke-BuildProcess ("Build " + $target.file) $Go @('-C',$repo,'build','-trimpath','-ldflags',$flags,'-o',$binary,('./cmd/'+$target.name))
        & (Join-Path $PSScriptRoot 'set-executable-icon.ps1') -Executable $binary -Icon (Join-Path $repo 'internal/desktop/assets/application.ico')
    }
    @{schema=1;version=$version;source_sha256=$SourceDigest;sha256=(Get-FileHash -LiteralPath (Join-Path $OutputDirectory 'AsterhandInputHost.exe')).Hash.ToLowerInvariant()} |
        ConvertTo-Json | Set-Content -LiteralPath (Join-Path $OutputDirectory 'input-host.json') -Encoding utf8NoBOM
    Copy-ApplicationLicenses
    Copy-Item -LiteralPath (Join-Path $repo 'LICENSE') -Destination $OutputDirectory
    Copy-Item -LiteralPath (Join-Path $repo 'docs/installation/quickstart.md') -Destination (Join-Path $OutputDirectory 'INSTALL.md')
    Copy-BuildTree (Join-Path $repo 'plugins') (Join-Path $OutputDirectory 'plugins')
    [IO.Directory]::CreateDirectory((Join-Path $OutputDirectory 'packaging')) | Out-Null
    foreach ($name in @('dependencies.json','runtime-lock.json','start.ps1','configure-firewall.ps1')) {
        Copy-Item -LiteralPath (Join-Path $repo ('packaging/'+$name)) -Destination (Join-Path $OutputDirectory 'packaging')
    }
    if ($SourceDigest -ne (Get-SourceFingerprint $repo)) { throw 'Source changed during application build.' }
} finally {
    foreach ($entry in $savedEnvironment.GetEnumerator()) { [Environment]::SetEnvironmentVariable($entry.Key, $entry.Value) }
}
