#requires -Version 7.0
[CmdletBinding()]
param(
    [string]$Go = 'go',
    [Parameter(Mandatory)][string]$NodeDistribution,
    [Parameter(Mandatory)][string]$RuntimeRoot,
    [string]$OutputDir
)
$ErrorActionPreference = 'Stop'
$repo = Split-Path -Parent $PSScriptRoot
if (-not $OutputDir) { $OutputDir = Join-Path $repo 'dist' }
$OutputDir = [IO.Path]::GetFullPath($OutputDir)
$versionSource = Get-Content (Join-Path $repo 'internal\config\config.go') -Raw
if ($versionSource -notmatch 'const Version = "([^"]+)"') { throw 'Cannot resolve application version.' }
$version = $Matches[1]
New-Item -ItemType Directory -Path $OutputDir -Force | Out-Null
$stageRoot = Join-Path $repo ('out\packages-' + [guid]::NewGuid().ToString('N'))
$windows = Join-Path $stageRoot 'windows'
$linux = Join-Path $stageRoot 'linux'
New-Item -ItemType Directory -Path $windows,$linux -Force | Out-Null

function Build-Binary([string]$Platform, [string]$Destination) {
    $savedGOOS = $env:GOOS
    $savedGOARCH = $env:GOARCH
    $savedCGO = $env:CGO_ENABLED
    try {
        $env:GOOS = $Platform
        $env:GOARCH = 'amd64'
        $env:CGO_ENABLED = '0'
        & $Go -C $repo build -trimpath -ldflags "-s -w -X github.com/jackgan90/Asterhand/internal/config.SourceDigest=$sourceDigest" -o $Destination ./cmd/asterhand
        if ($LASTEXITCODE -ne 0) { throw "$Platform build failed" }
    } finally {
        $env:GOOS = $savedGOOS
        $env:GOARCH = $savedGOARCH
        $env:CGO_ENABLED = $savedCGO
    }
}

function Copy-GoLicenses([string]$Stage) {
    $licenseRoot = Join-Path $Stage 'licenses\go-modules'
    New-Item -ItemType Directory -Path $licenseRoot -Force | Out-Null
    $modules = & $Go -C $repo list -m -f '{{.Path}}|{{.Version}}|{{.Dir}}' all
    if ($LASTEXITCODE -ne 0) { throw 'Cannot inspect Go module licenses.' }
    foreach ($line in $modules) {
        $parts = $line -split '\|',3
        if (-not $parts[1] -or -not $parts[2]) { continue }
        $destination = Join-Path $licenseRoot (($parts[0] -replace '[/\\]','_') + '@' + $parts[1])
        New-Item -ItemType Directory -Path $destination -Force | Out-Null
        $notices = @(Get-ChildItem -LiteralPath $parts[2] -File | Where-Object { $_.Name -match '^(LICENSE|COPYING|NOTICE)' })
        if ($notices.Count -eq 0) { throw "Missing license notice for $($parts[0])" }
        $notices | Copy-Item -Destination $destination
    }
    $goRoot = & $Go env GOROOT
    Copy-Item -LiteralPath (Join-Path $goRoot 'LICENSE') -Destination (Join-Path $licenseRoot 'Go-LICENSE')
}

. (Join-Path $PSScriptRoot 'dependency-tools.ps1')
$catalog = Assert-PreparedDependencies $repo $NodeDistribution $RuntimeRoot $Go
$sourceDigest = Get-SourceFingerprint $repo
Build-Binary 'windows' (Join-Path $windows 'asterhand.exe')
Build-Binary 'linux' (Join-Path $linux 'asterhand')
New-Item -ItemType Directory -Path (Join-Path $windows 'runtime') -Force | Out-Null
Copy-Item -LiteralPath $NodeDistribution -Destination (Join-Path $windows 'runtime\node') -Recurse -Force
Copy-Item -LiteralPath (Join-Path $RuntimeRoot 'node_modules') -Destination (Join-Path $windows 'runtime\node_modules') -Recurse -Force
Copy-Item -LiteralPath (Join-Path $RuntimeRoot 'package.json'),(Join-Path $RuntimeRoot 'package-lock.json') -Destination (Join-Path $windows 'runtime')
foreach ($stage in @($windows,$linux)) {
    Copy-GoLicenses $stage
    Copy-Item -LiteralPath (Join-Path $repo 'LICENSE') -Destination $stage
    New-Item -ItemType Directory -Path (Join-Path $stage 'packaging') -Force | Out-Null
    Copy-Item -LiteralPath (Join-Path $repo 'packaging\dependencies.json') -Destination (Join-Path $stage 'packaging')
    Copy-Item -LiteralPath (Join-Path $repo 'docs\installation\quickstart.md') -Destination (Join-Path $stage 'INSTALL.md')
}
Copy-Item -LiteralPath (Join-Path $repo 'plugins') -Destination (Join-Path $windows 'plugins') -Recurse -Force
Copy-Item -LiteralPath (Join-Path $repo 'packaging\start.ps1'),(Join-Path $repo 'packaging\configure-firewall.ps1'),(Join-Path $repo 'packaging\runtime-lock.json') -Destination (Join-Path $windows 'packaging')
& (Join-Path $windows 'asterhand.exe') bundle-check --directory $windows
if ($LASTEXITCODE -ne 0) { throw 'Staged runtime bundle failed diagnostics.' }
$windowsName = "asterhand-$version-windows-amd64.zip"
$linuxName = "asterhand-$version-linux-amd64-base.tar.gz"
$windowsArchive = Join-Path $OutputDir $windowsName
if (Test-Path -LiteralPath $windowsArchive) { Remove-Item -LiteralPath $windowsArchive -Force }
[IO.Compression.ZipFile]::CreateFromDirectory($windows, $windowsArchive, [IO.Compression.CompressionLevel]::Optimal, $false)
& tar -czf (Join-Path $OutputDir $linuxName) -C $linux .
if ($LASTEXITCODE -ne 0) { throw 'Linux archive creation failed.' }
$packages = @()
foreach ($entry in @(@{os='windows';file=$windowsName;profile='assistant'},@{os='linux';file=$linuxName;profile='base';evidence='cross_compiled_not_runtime_validated'})) {
    $file = Get-Item -LiteralPath (Join-Path $OutputDir $entry.file)
    $entry.arch = 'amd64'
    $entry.sha256 = (Get-FileHash -LiteralPath $file.FullName -Algorithm SHA256).Hash.ToLowerInvariant()
    $entry.size = $file.Length
    $packages += $entry
}
$manifest = @{schema=1;version=$version;source_sha256=$sourceDigest;catalog_sha256=(Get-FileHash (Join-Path $repo 'packaging\dependencies.json')).Hash.ToLowerInvariant();created_utc=[DateTime]::UtcNow.ToString('o');packages=$packages}
$manifest | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath (Join-Path $OutputDir 'release-manifest.json') -Encoding utf8NoBOM
Copy-Item -LiteralPath (Join-Path $repo 'install.ps1'),(Join-Path $repo 'install.sh'),(Join-Path $repo 'docs\installation\quickstart.md') -Destination $OutputDir -Force
Get-ChildItem -LiteralPath $OutputDir -File | Where-Object Name -ne 'SHA256SUMS' | Sort-Object Name | ForEach-Object {
    ((Get-FileHash -LiteralPath $_.FullName -Algorithm SHA256).Hash.ToLowerInvariant()) + '  ' + $_.Name
} | Set-Content -LiteralPath (Join-Path $OutputDir 'SHA256SUMS') -Encoding ascii
Write-Host "Packages built in $OutputDir"
Write-Host "Staging retained for inspection: $stageRoot"
