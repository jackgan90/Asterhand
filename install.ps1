#requires -Version 5.1
[CmdletBinding()]
param(
    [ValidatePattern('^[0-9]+\.[0-9]+\.[0-9]+(?:-[A-Za-z0-9.]+)?$')]
    [string]$Version = '0.1.0-alpha.1',
    [string]$PackagePath,
    [string]$ManifestPath,
    [string]$InstallDir = (Join-Path $env:LOCALAPPDATA 'Programs\Asterhand'),
    [string]$DataDir = (Join-Path $env:APPDATA 'Asterhand'),
    [switch]$SkipTailscale,
    [switch]$NoStart,
    [switch]$LoginAtStartup
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
if (-not [Environment]::Is64BitOperatingSystem -or $env:PROCESSOR_ARCHITECTURE -eq 'ARM64') {
    throw 'This release supports Windows x64. Windows ARM64 is not yet validated.'
}
if ([Environment]::OSVersion.Version.Major -lt 10) { throw 'Windows 10 or later is required.' }

function Resolve-FileSystemPath([string]$Path) {
    $provider = $null
    $drive = $null
    $resolved = $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($Path, [ref]$provider, [ref]$drive)
    if ($provider.Name -ne 'FileSystem') { throw 'Installation paths must use the FileSystem provider.' }
    return $resolved
}

function Resolve-AppDirectory([string]$Path) {
    $resolved = Resolve-FileSystemPath $Path
    $volumeRoot = [IO.Path]::GetPathRoot($resolved)
    if ($resolved.TrimEnd('\') -eq $volumeRoot.TrimEnd('\') -or
        $resolved.TrimEnd('\') -eq $env:USERPROFILE.TrimEnd('\')) {
        throw 'Use a dedicated application directory, not a drive root or user home.'
    }
    if ($resolved.Contains('"') -or $resolved.Contains("`r") -or $resolved.Contains("`n")) {
        throw 'Invalid application directory.'
    }
    return $resolved
}

function Assert-ChildPath([string]$Path, [string]$Parent) {
    $resolved = [IO.Path]::GetFullPath($Path)
    $prefix = [IO.Path]::GetFullPath($Parent).TrimEnd('\') + '\'
    if (-not $resolved.StartsWith($prefix, [StringComparison]::OrdinalIgnoreCase)) {
        throw "Path is outside the intended application directory: $resolved"
    }
    return $resolved
}

function Get-VerifiedFile([string]$Url, [string]$Destination, [string]$SHA256) {
    if (Test-Path -LiteralPath $Destination) {
        if ((Get-FileHash -LiteralPath $Destination -Algorithm SHA256).Hash -eq $SHA256) { return }
    }
    $partial = $Destination + '.partial'
    Invoke-WebRequest -Uri $Url -OutFile $partial -UseBasicParsing
    if ((Get-FileHash -LiteralPath $partial -Algorithm SHA256).Hash -ne $SHA256) {
        Remove-Item -LiteralPath $partial -Force
        throw "Checksum verification failed: $Url"
    }
    Move-Item -LiteralPath $partial -Destination $Destination -Force
}

function Expand-SafePackage([string]$Archive, [string]$Destination) {
    Add-Type -AssemblyName System.IO.Compression.FileSystem
    $zip = [IO.Compression.ZipFile]::OpenRead($Archive)
    try {
        foreach ($entry in $zip.Entries) {
            if ($entry.FullName.Contains(':') -or [IO.Path]::IsPathRooted($entry.FullName)) {
                throw 'Unsafe path in release archive.'
            }
            $target = Assert-ChildPath (Join-Path $Destination $entry.FullName) $Destination
            $longPath = if ($target.StartsWith('\\')) { '\\?\UNC\' + $target.Substring(2) } else { '\\?\' + $target }
            if ($entry.FullName.EndsWith('/')) {
                [IO.Directory]::CreateDirectory($longPath) | Out-Null
                continue
            }
            [IO.Directory]::CreateDirectory([IO.Path]::GetDirectoryName($longPath)) | Out-Null
            $inputStream = $entry.Open()
            try {
                $outputStream = [IO.File]::Open($longPath, [IO.FileMode]::CreateNew, [IO.FileAccess]::Write, [IO.FileShare]::None)
                try { $inputStream.CopyTo($outputStream); $outputStream.Flush() }
                finally { $outputStream.Dispose() }
            } finally { $inputStream.Dispose() }
        }
    } finally { $zip.Dispose() }
}

function Initialize-Tailscale([string]$Cache, $Dependency) {
    $tailscale = Join-Path $env:ProgramFiles 'Tailscale\tailscale.exe'
    if (-not (Test-Path -LiteralPath $tailscale)) {
        Write-Host 'Cross-NAT access uses Tailscale. The official installer needs Windows administrator approval.'
        Write-Host 'It installs a system network service. Asterhand will not change your router or enable unattended mode.'
        $choice = Read-Host "Install Tailscale $($Dependency.version) now? [Y/n]"
        if ($choice -match '^[nN]') { throw 'Tailscale installation deferred. Run this installer again when ready, or use -SkipTailscale for an existing LAN.' }
        $msi = Join-Path $Cache 'tailscale-setup-1.104.2-amd64.msi'
        Get-VerifiedFile $Dependency.windows_amd64_url $msi $Dependency.windows_amd64_sha256
        $signature = Get-AuthenticodeSignature -LiteralPath $msi
        if ($signature.Status -ne 'Valid' -or $signature.SignerCertificate.Subject -notmatch 'Tailscale') {
            throw 'The Tailscale installer signature is not valid for Tailscale.'
        }
        $installer = Start-Process msiexec.exe -ArgumentList @('/i', ('"' + $msi + '"'), '/passive', '/norestart') -Verb RunAs -WindowStyle Hidden -Wait -PassThru
        if ($installer.ExitCode -notin @(0, 3010)) { throw "Tailscale install failed: $($installer.ExitCode)" }
        if ($installer.ExitCode -eq 3010) { Write-Warning 'Windows requested a restart to finish Tailscale installation.' }
    } else { Write-Host 'Reusing the installed Tailscale client; it will not be upgraded automatically.' }
    $savedPreference = $ErrorActionPreference
    try {
        $ErrorActionPreference = 'Continue'
        $statusText = & $tailscale status --json 2>$null
    } finally { $ErrorActionPreference = $savedPreference }
    $status = $null
    try { $status = $statusText | ConvertFrom-Json } catch { }
    if ($null -eq $status -or $status.BackendState -ne 'Running') {
        Write-Host 'Sign in to the same Tailscale account / Tailnet on A and B. Complete authentication in your browser.'
        & $tailscale up --timeout=5m
        if ($LASTEXITCODE -ne 0) { throw 'Tailscale login is incomplete. Open Tailscale, sign in, then rerun install.ps1.' }
    }
}

if ($PackagePath) { $PackagePath = Resolve-FileSystemPath $PackagePath }
if ($ManifestPath) { $ManifestPath = Resolve-FileSystemPath $ManifestPath }
$InstallDir = Resolve-AppDirectory $InstallDir
$DataDir = Resolve-AppDirectory $DataDir
if ($InstallDir -eq $DataDir) { throw 'Program and data directories must be separate.' }
if (Test-Path -LiteralPath $InstallDir) {
    $existing = @(Get-ChildItem -LiteralPath $InstallDir -Force)
    if ($existing.Count -gt 0 -and -not (Test-Path -LiteralPath (Join-Path $InstallDir '.asterhand-install'))) {
        throw 'Installation directory is nonempty and is not owned by Asterhand.'
    }
}
New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
Set-Content -LiteralPath (Join-Path $InstallDir '.asterhand-install') -Value 'Asterhand installer schema 1' -Encoding ASCII
$cache = Join-Path $InstallDir 'downloads'
New-Item -ItemType Directory -Path $cache -Force | Out-Null
$releaseBase = "https://github.com/jackgan90/Asterhand/releases/download/v$Version"
if (-not $ManifestPath) {
    if ($PackagePath) { $ManifestPath = Join-Path (Split-Path -Parent $PackagePath) 'release-manifest.json' }
    else {
        $ManifestPath = Join-Path $cache "manifest-$Version.json"
        Invoke-WebRequest -Uri "$releaseBase/release-manifest.json" -OutFile $ManifestPath -UseBasicParsing
    }
}
$manifest = Get-Content -LiteralPath $ManifestPath -Raw | ConvertFrom-Json
if ($manifest.schema -ne 1 -or $manifest.version -ne $Version) { throw 'Release manifest version mismatch.' }
$package = @($manifest.packages | Where-Object { $_.os -eq 'windows' -and $_.arch -eq 'amd64' })
if ($package.Count -ne 1 -or $package[0].sha256 -notmatch '^[a-fA-F0-9]{64}$') { throw 'Missing or invalid Windows package manifest.' }
$package = $package[0]
if ($package.file -notmatch '^asterhand-[A-Za-z0-9.-]+-windows-amd64\.zip$') { throw 'Invalid release package filename.' }
if (-not $PackagePath) {
    $PackagePath = Join-Path $cache $package.file
    Get-VerifiedFile "$releaseBase/$($package.file)" $PackagePath $package.sha256
}
if ((Get-FileHash -LiteralPath $PackagePath -Algorithm SHA256).Hash -ne $package.sha256) { throw 'Release package checksum mismatch.' }
if ((Get-Item -LiteralPath $PackagePath).Length -ne $package.size) { throw 'Release package size mismatch.' }

$versionName = $Version + '-' + $package.sha256.Substring(0, 12)
$versions = Join-Path $InstallDir 'versions'
New-Item -ItemType Directory -Path $versions -Force | Out-Null
$versionDir = Assert-ChildPath (Join-Path $versions $versionName) $versions
$staging = Assert-ChildPath (Join-Path $InstallDir ('.staging-' + [guid]::NewGuid().ToString('N'))) $InstallDir
try {
    if (-not (Test-Path -LiteralPath $versionDir)) {
        Write-Host 'Extracting the verified runtime bundle. This may take several minutes.'
        Expand-SafePackage $PackagePath $staging
        $candidate = Join-Path $staging 'asterhand.exe'
        if ((& $candidate version) -ne $Version -or $LASTEXITCODE -ne 0) { throw 'Asterhand version check failed.' }
        & $candidate bundle-check --directory $staging
        if ($LASTEXITCODE -ne 0) { throw 'Bundled dependency check failed.' }
        Move-Item -LiteralPath $staging -Destination $versionDir
    }
        $candidate = Join-Path $versionDir 'asterhand.exe'
    & $candidate bundle-check --directory $versionDir
    if ($LASTEXITCODE -ne 0) { throw 'Installed bundle health check failed.' }
    $dependencies = Get-Content -LiteralPath (Join-Path $versionDir 'packaging\dependencies.json') -Raw | ConvertFrom-Json
    if (-not $SkipTailscale) { Initialize-Tailscale $cache $dependencies.tailscale }
    New-Item -ItemType Directory -Path $DataDir -Force | Out-Null
    $launch = @{ schema = 1; version = $Version; executable = (Join-Path $versionDir 'asterhand.exe'); data = $DataDir; package_sha256 = $package.sha256 }
    $launch | ConvertTo-Json | Set-Content -LiteralPath (Join-Path $InstallDir 'installation.json') -Encoding UTF8
    Copy-Item -LiteralPath (Join-Path $versionDir 'packaging\start.ps1') -Destination (Join-Path $InstallDir 'start.ps1') -Force
    Copy-Item -LiteralPath (Join-Path $versionDir 'packaging\configure-firewall.ps1') -Destination (Join-Path $InstallDir 'configure-firewall.ps1') -Force
    if ($LoginAtStartup) {
        $startup = [Environment]::GetFolderPath('Startup')
        $shell = New-Object -ComObject WScript.Shell
        $shortcut = $shell.CreateShortcut((Join-Path $startup 'Asterhand.lnk'))
        $shortcut.TargetPath = (Join-Path $env:SystemRoot 'System32\WindowsPowerShell\v1.0\powershell.exe')
        $shortcut.Arguments = '-NoProfile -WindowStyle Hidden -File "' + (Join-Path $InstallDir 'start.ps1') + '" -NoBrowser'
        $shortcut.WindowStyle = 7
        $shortcut.Save()
        Write-Host 'Enabled startup after this Windows user logs in.'
    }
    Write-Host "Installed Asterhand $Version. Data and credentials are preserved in $DataDir."
    Write-Host "Startup entry: $InstallDir\start.ps1"
    if (-not $SkipTailscale) {
        Write-Host 'If peer connectivity is blocked, configure the narrow Tailscale-only firewall rule:'
        Write-Host "  & '$InstallDir\configure-firewall.ps1'"
    }
    if (-not $NoStart) { & (Join-Path $InstallDir 'start.ps1') }
} finally {
    if (Test-Path -LiteralPath $staging) {
        $safeStaging = Assert-ChildPath $staging $InstallDir
        if ((Split-Path -Leaf $safeStaging) -notlike '.staging-*') { throw 'Refusing unexpected cleanup target.' }
        Remove-Item -LiteralPath $safeStaging -Recurse -Force
    }
}
