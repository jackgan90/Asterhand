# Local preparation helpers. Every output lives below a fresh release workspace.
function Get-ReleaseVersion([string]$RepositoryRoot) {
    $source = Get-Content (Join-Path $RepositoryRoot 'internal/config/config.go') -Raw
    if ($source -notmatch 'const Version = "([^"]+)"') { throw 'Cannot resolve application version.' }
    return $Matches[1]
}

function Get-ReleaseBuilder([string]$RepositoryRoot, [string]$Platform) {
    $registry = Get-Content (Join-Path $RepositoryRoot 'packaging/release-targets.json') -Raw | ConvertFrom-Json
    if ($registry.schema -ne 1) { throw 'Unsupported release target registry.' }
    $targets = @($registry.targets | Where-Object id -eq $Platform)
    if ($targets.Count -ne 1 -or -not $targets[0].enabled) { throw "Release platform is not enabled: $Platform" }
    foreach ($field in @('prepare', 'builder', 'verify', 'installer')) {
        $relative = $targets[0].$field
        if (-not $relative -or $relative -match '(^[/\\]|\.\.|:)' -or -not (Test-Path -LiteralPath (Join-Path $RepositoryRoot $relative) -PathType Leaf)) {
            throw "Invalid $field for release platform $Platform"
        }
    }
    return $targets[0]
}

function Get-ReleaseTreeFingerprint([string]$RepositoryRoot) {
    $paths = @(& git -C $RepositoryRoot -c core.quotepath=false ls-files --cached --others --exclude-standard)
    if ($LASTEXITCODE -ne 0) { throw 'Cannot enumerate source files.' }
    $hash = [Security.Cryptography.IncrementalHash]::CreateHash([Security.Cryptography.HashAlgorithmName]::SHA256)
    try {
        foreach ($path in $paths | Sort-Object -Unique) {
            $file = Join-Path $RepositoryRoot $path
            if (Test-Path -LiteralPath $file -PathType Leaf) {
                $hash.AppendData([Text.Encoding]::UTF8.GetBytes($path + [char]0))
                $bytes = [IO.File]::ReadAllBytes($file)
                $hash.AppendData([BitConverter]::GetBytes([long]$bytes.Length))
                $hash.AppendData($bytes)
            }
        }
        return [Convert]::ToHexString($hash.GetHashAndReset()).ToLowerInvariant()
    } finally { $hash.Dispose() }
}

function Get-ReleaseGo($Catalog, [string]$Workspace, [string]$Existing) {
    if ($Existing) { return (Get-Command $Existing -ErrorAction Stop).Source }
    $archive = Join-Path $Workspace 'go.zip'
    Invoke-WebRequest -Uri $Catalog.go.windows_amd64_url -OutFile $archive
    if ((Get-FileHash -LiteralPath $archive).Hash.ToLowerInvariant() -ne $Catalog.go.windows_amd64_sha256) { throw 'Go toolchain checksum mismatch.' }
    Expand-Archive -LiteralPath $archive -DestinationPath (Join-Path $Workspace 'toolchain')
    return (Join-Path $Workspace 'toolchain/go/bin/go.exe')
}

function Write-ReleaseChecksums([string]$Directory) {
    Get-ChildItem -LiteralPath $Directory -File | Where-Object Name -ne 'SHA256SUMS' | Sort-Object Name | ForEach-Object {
        (Get-FileHash -LiteralPath $_.FullName).Hash.ToLowerInvariant() + '  ' + $_.Name
    } | Set-Content -LiteralPath (Join-Path $Directory 'SHA256SUMS') -Encoding ascii
}

function Assert-ReleaseChecksums([string]$Directory) {
    $expected = @('SHA256SUMS')
    foreach ($line in Get-Content -LiteralPath (Join-Path $Directory 'SHA256SUMS')) {
        if ($line -notmatch '^([a-f0-9]{64})  ([A-Za-z0-9._-]+)$') { throw 'Malformed release checksum list.' }
        $hash, $name = $Matches[1], $Matches[2]
        if ($name -in $expected) { throw 'Duplicate release checksum entry.' }
        $expected += $name
        if ((Get-FileHash -LiteralPath (Join-Path $Directory $name)).Hash.ToLowerInvariant() -ne $hash) { throw "Release content changed: $name" }
    }
    if (@(Get-ChildItem -LiteralPath $Directory -File).Count -ne $expected.Count) { throw 'Unexpected file in release directory.' }
}
