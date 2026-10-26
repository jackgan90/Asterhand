#requires -Version 7.0
[CmdletBinding()]
param([string]$Go='go', [Parameter(Mandatory)][string]$OutputDirectory, [string]$SourceDigest='development')
$ErrorActionPreference = 'Stop'
$repo = Split-Path -Parent $PSScriptRoot
New-Item -ItemType Directory -Path $OutputDirectory -Force | Out-Null
$OutputDirectory = [IO.Path]::GetFullPath($OutputDirectory)
$versionSource = Get-Content (Join-Path $repo 'internal/config/config.go') -Raw
if ($versionSource -notmatch 'const Version = "([^"]+)"') { throw 'Cannot resolve version.' }
$version = $Matches[1]
if ($SourceDigest -ne 'development' -and $SourceDigest -notmatch '^[0-9a-f]{64}$') { throw 'Invalid source digest.' }
$savedOS=$env:GOOS; $savedArch=$env:GOARCH; $savedCGO=$env:CGO_ENABLED
try {
    $env:GOOS='windows'; $env:GOARCH='amd64'; $env:CGO_ENABLED='0'
    $hostPath = Join-Path $OutputDirectory 'AsterhandInputHost.exe'
    & $Go -C $repo build -trimpath -ldflags "-H=windowsgui -X github.com/jackgan90/Asterhand/internal/config.SourceDigest=$SourceDigest" -o $hostPath ./cmd/asterhand-input-host
    if ($LASTEXITCODE -ne 0) { throw 'Input host build failed.' }
    & (Join-Path $PSScriptRoot 'set-executable-icon.ps1') -Executable $hostPath -Icon (Join-Path $repo 'internal/desktop/assets/application.ico')
    @{schema=1;version=$version;source_sha256=$SourceDigest;sha256=(Get-FileHash -LiteralPath $hostPath).Hash.ToLowerInvariant()} | ConvertTo-Json | Set-Content -LiteralPath (Join-Path $OutputDirectory 'input-host.json') -Encoding utf8NoBOM
} finally {
    $env:GOOS=$savedOS; $env:GOARCH=$savedArch; $env:CGO_ENABLED=$savedCGO
}
