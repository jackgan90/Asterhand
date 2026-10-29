#requires -Version 7.0
[CmdletBinding()]
param(
    [string]$Go = 'go',
    [Parameter(Mandatory)][string]$NodeDistribution,
    [Parameter(Mandatory)][string]$RuntimeRoot,
    [string]$OutputDir,
    [ValidatePattern('^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$')]
    [string]$Repository = 'jackgan90/Asterhand',
    [ValidatePattern('^[A-Za-z0-9][A-Za-z0-9._-]*$')]
    [string]$ReleaseTag
)
$ErrorActionPreference = 'Stop'
$repo = Split-Path -Parent $PSScriptRoot
if (-not $OutputDir) { $OutputDir = Join-Path $repo 'dist' }
$OutputDir = [IO.Path]::GetFullPath($OutputDir)
$versionSource = Get-Content (Join-Path $repo 'internal\config\config.go') -Raw
if ($versionSource -notmatch 'const Version = "([^"]+)"') { throw 'Cannot resolve application version.' }
$version = $Matches[1]
if (-not $ReleaseTag) { $ReleaseTag = 'v' + $version }
New-Item -ItemType Directory -Path (Join-Path $repo 'out') -Force | Out-Null
$outputModule = Join-Path $repo 'out/go.mod'
if (-not (Test-Path -LiteralPath $outputModule)) {
    Set-Content -LiteralPath $outputModule -Value 'module asterhand.local/build-output' -Encoding ascii
}
New-Item -ItemType Directory -Path $OutputDir -Force | Out-Null
$stageRoot = Join-Path $repo ('out\packages-' + [guid]::NewGuid().ToString('N'))
$windows = Join-Path $stageRoot 'windows'


. (Join-Path $PSScriptRoot 'dependency-tools.ps1')
$catalog = Assert-PreparedDependencies $repo $NodeDistribution $RuntimeRoot $Go
. (Join-Path $PSScriptRoot 'build-progress.ps1')
$sourceDigest = Get-SourceFingerprint $repo
# Application assembly is identical for local directory candidates and release ZIPs.
& (Join-Path $PSScriptRoot 'build-application.ps1') -Go $Go -Node (Join-Path $NodeDistribution 'node.exe') -OutputDirectory $windows -SourceDigest $sourceDigest
[IO.Directory]::CreateDirectory((Join-Path $windows 'runtime')) | Out-Null
Copy-BuildTree $NodeDistribution (Join-Path $windows 'runtime/node')
Copy-BuildTree (Join-Path $RuntimeRoot 'node_modules') (Join-Path $windows 'runtime/node_modules')
Copy-Item -LiteralPath (Join-Path $RuntimeRoot 'package.json'),(Join-Path $RuntimeRoot 'package-lock.json') -Destination (Join-Path $windows 'runtime')
& (Join-Path $windows 'asterhand.exe') bundle-check --directory $windows
if ($LASTEXITCODE -ne 0) { throw 'Staged runtime bundle failed diagnostics.' }
$windowsName = "asterhand-$version-windows-amd64.zip"
$windowsArchive = Join-Path $OutputDir $windowsName
if (Test-Path -LiteralPath $windowsArchive) { Remove-Item -LiteralPath $windowsArchive -Force }
Invoke-BuildProcess 'Compress complete release package' (Join-Path $PSHOME 'pwsh.exe') @('-NoProfile','-File',(Join-Path $PSScriptRoot 'compress-package.ps1'),'-Directory',$windows,'-Archive',$windowsArchive)
$packages = @()
foreach ($entry in @(@{os='windows';file=$windowsName;profile='assistant'})) {
    $file = Get-Item -LiteralPath (Join-Path $OutputDir $entry.file)
    $entry.arch = 'amd64'
    $entry.sha256 = (Get-FileHash -LiteralPath $file.FullName -Algorithm SHA256).Hash.ToLowerInvariant()
    $entry.size = $file.Length
    $packages += $entry
}
$manifest = @{schema=1;version=$version;repository=$Repository;tag=$ReleaseTag;source_sha256=$sourceDigest;catalog_sha256=(Get-FileHash (Join-Path $repo 'packaging\dependencies.json')).Hash.ToLowerInvariant();created_utc=[DateTime]::UtcNow.ToString('o');packages=$packages}
$manifest | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath (Join-Path $OutputDir 'release-manifest.json') -Encoding utf8NoBOM
$installer = Get-Content (Join-Path $repo 'install.ps1') -Raw
foreach ($binding in @{Repository=$Repository}.GetEnumerator()) {
    $pattern = '(?m)^(    \[string\]\$' + [regex]::Escape($binding.Key) + " = ')[^']*(',?\r?$)"
    if ([regex]::Matches($installer, $pattern).Count -ne 1) { throw "Cannot bind installer default: $($binding.Key)" }
    $replacementValue = $binding.Value
    $installer = [regex]::Replace($installer, $pattern, { param($match) $match.Groups[1].Value + $replacementValue + $match.Groups[2].Value })
}
Set-Content -LiteralPath (Join-Path $OutputDir 'install.ps1') -Value $installer -Encoding utf8BOM
Copy-Item -LiteralPath (Join-Path $repo 'docs\installation\quickstart.md') -Destination $OutputDir -Force
Get-ChildItem -LiteralPath $OutputDir -File | Where-Object Name -ne 'SHA256SUMS' | Sort-Object Name | ForEach-Object {
    ((Get-FileHash -LiteralPath $_.FullName -Algorithm SHA256).Hash.ToLowerInvariant()) + '  ' + $_.Name
} | Set-Content -LiteralPath (Join-Path $OutputDir 'SHA256SUMS') -Encoding ascii
Write-Host "Packages built in $OutputDir"
Write-Host "Staging retained for inspection: $stageRoot"
if ((Get-SourceFingerprint $repo) -ne $sourceDigest) { throw 'Source changed during the build; rebuild before installing.' }
$buildRecord = Join-Path $repo 'out\latest-build.json'
$temporaryRecord = Join-Path $repo ('out\.latest-build-' + [guid]::NewGuid().ToString('N') + '.json')
try {
    @{schema=1;source_sha256=$sourceDigest;manifest=(Join-Path $OutputDir 'release-manifest.json')} |
        ConvertTo-Json | Set-Content -LiteralPath $temporaryRecord -Encoding utf8NoBOM
    [IO.File]::Move($temporaryRecord, $buildRecord, $true)
} finally {
    if (Test-Path -LiteralPath $temporaryRecord) { Remove-Item -LiteralPath $temporaryRecord -Force }
}
