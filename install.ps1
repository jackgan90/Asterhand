#requires -Version 5.1
[CmdletBinding()]
param(
    [ValidatePattern('^v?[0-9]+\.[0-9]+\.[0-9]+(?:-[A-Za-z0-9.-]+)?$')]
    [string]$Version,
    [ValidatePattern('^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$')]
    [string]$Repository = 'jackgan90/Asterhand',
    [ValidatePattern('^[A-Za-z0-9][A-Za-z0-9._-]*$')]
    [string]$ReleaseTag,
    [string]$PackagePath,
    [string]$ManifestPath,
    [string]$LocalBuildPath,
    [string]$LocalRuntimePath,
    [switch]$InitializeLocalCache,
    [scriptblock]$BeforeLocalActivation,
    [ValidatePattern('^[a-f0-9]{64}$')][string]$ExpectedSourceDigest,
    [string]$InstallDir = (Join-Path $env:LOCALAPPDATA 'Programs\Asterhand'),
    [string]$DataDir = (Join-Path $env:APPDATA 'Asterhand'),
    [switch]$SkipTailscale,
    [switch]$NoStart,
    [switch]$StopRunning,
    [switch]$StartAfterInstall,
    [switch]$LoginAtStartup,
    [ValidateSet('Auto','Create','Skip','Keep')]
    [string]$DesktopShortcut = 'Auto'
)

$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
Set-StrictMode -Version Latest
if ($NoStart -and $StartAfterInstall) { throw 'NoStart and StartAfterInstall cannot be combined.' }
if ($LocalBuildPath -or $InitializeLocalCache) {
    foreach ($name in @('Version','Repository','ReleaseTag','PackagePath','ManifestPath')) {
        if ($PSBoundParameters.ContainsKey($name)) { throw 'Local directory mode cannot select a release or ZIP.' }
    }
} elseif ($LocalRuntimePath -or $BeforeLocalActivation) { throw 'Local runtime and source guard require a local directory candidate.' }
if ($LocalBuildPath -and (-not $LocalRuntimePath -or $InitializeLocalCache)) { throw 'Choose local initialization or a candidate with its runtime.' }
if ($InitializeLocalCache -and ($StopRunning -or $StartAfterInstall -or $BeforeLocalActivation -or $LocalRuntimePath)) { throw 'Local cache initialization cannot activate a node.' }
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

function Get-InstallRelease([string]$Repository, [string]$Version, [string]$ReleaseTag,
    [string]$PackagePath, [string]$ManifestPath, [string]$Cache) {
    $requestedVersion = $Version -replace '^v', ''
    if (-not $ManifestPath -and $PackagePath) {
        $ManifestPath = Join-Path (Split-Path -Parent $PackagePath) 'release-manifest.json'
    }
    if (-not $ManifestPath) {
        if ($requestedVersion -and -not $ReleaseTag) { $ReleaseTag = 'v' + $requestedVersion }
        if (-not $ReleaseTag) {
            try {
                $latest = Invoke-RestMethod -Uri "https://api.github.com/repos/$Repository/releases/latest" `
                    -Headers @{Accept='application/vnd.github+json';'User-Agent'='Asterhand-Installer'} -TimeoutSec 30
            } catch {
                throw "Cannot resolve the latest stable release for $Repository. Check network/API availability and that a stable release exists. Use -Version or -ReleaseTag for a published prerelease. $($_.Exception.Message)"
            }
            if ($latest.draft -ne $false -or $latest.prerelease -ne $false) {
                throw 'Latest must identify a published stable release.'
            }
            $ReleaseTag = [string]$latest.tag_name
        }
        if ($ReleaseTag -notmatch '^[A-Za-z0-9][A-Za-z0-9._-]*$') { throw 'Invalid release tag.' }
        $ManifestPath = Join-Path $Cache "manifest-$ReleaseTag.json"
        Invoke-WebRequest -Uri "https://github.com/$Repository/releases/download/$ReleaseTag/release-manifest.json" `
            -OutFile $ManifestPath -UseBasicParsing
    }
    $manifest = Get-Content -LiteralPath $ManifestPath -Raw | ConvertFrom-Json
    if ($manifest.schema -ne 1 -or $manifest.version -notmatch '^[0-9]+\.[0-9]+\.[0-9]+(?:-[A-Za-z0-9.-]+)?$') {
        throw 'Unsupported release manifest schema or version.'
    }
    if ($requestedVersion -and $manifest.version -ne $requestedVersion) { throw 'Release manifest version mismatch.' }
    $manifestTag = $manifest.PSObject.Properties['tag']
    $manifestRepository = $manifest.PSObject.Properties['repository']
    if (-not $ReleaseTag) {
        $ReleaseTag = if ($manifestTag) { [string]$manifestTag.Value } else { 'v' + $manifest.version }
    }
    if ($ReleaseTag -notmatch '^[A-Za-z0-9][A-Za-z0-9._-]*$') { throw 'Invalid release tag.' }
    if ($manifestTag -and $manifestTag.Value -ne $ReleaseTag) { throw 'Release manifest tag mismatch.' }
    if ($manifestRepository -and $manifestRepository.Value -ne $Repository) { throw 'Release manifest repository mismatch.' }
    return [pscustomobject]@{
        version = [string]$manifest.version
        tag = $ReleaseTag
        base = "https://github.com/$Repository/releases/download/$ReleaseTag"
        manifest = $manifest
    }
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

function Assert-SeparateDirectories([string]$Program, [string]$Data) {
    $programPath = [IO.Path]::GetFullPath($Program).TrimEnd('\')
    $dataPath = [IO.Path]::GetFullPath($Data).TrimEnd('\')
    if ($programPath -eq $dataPath -or
        $programPath.StartsWith($dataPath + '\', [StringComparison]::OrdinalIgnoreCase) -or
        $dataPath.StartsWith($programPath + '\', [StringComparison]::OrdinalIgnoreCase)) {
        throw 'Program and data directories must be separate and must not contain one another.'
    }
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
    $watch = [Diagnostics.Stopwatch]::StartNew()
    $processed = 0
    $nextProgress = 5
    try {
        foreach ($entry in $zip.Entries) {
            $processed++
            if ($watch.Elapsed.TotalSeconds -ge $nextProgress) {
                Write-Host ("Extracting: {0}/{1} entries, {2:n1}s elapsed" -f $processed, $zip.Entries.Count, $watch.Elapsed.TotalSeconds)
                $nextProgress = $watch.Elapsed.TotalSeconds + 5
            }
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
        $msi = Join-Path $Cache ("tailscale-setup-$($Dependency.version)-amd64.msi")
        Get-VerifiedFile $Dependency.windows_amd64_url $msi $Dependency.windows_amd64_sha256
        $signature = Get-AuthenticodeSignature -LiteralPath $msi
        if ($signature.Status -ne 'Valid' -or $signature.SignerCertificate.Subject -notmatch 'Tailscale') {
            throw 'The Tailscale installer signature is not valid for Tailscale.'
        }
        $installer = Start-Process msiexec.exe -ArgumentList @('/i', ('"' + $msi + '"'), '/passive', '/norestart') -Verb RunAs -WindowStyle Hidden -Wait -PassThru
        if ($installer.ExitCode -notin @(0, 3010)) { throw "Tailscale install failed: $($installer.ExitCode)" }
        if ($installer.ExitCode -eq 3010) { Write-Warning 'Windows requested a restart to finish Tailscale installation.' }
    } else { Write-Host 'Reusing the installed Tailscale client; it will not be upgraded automatically.' }
    if (-not (Test-Path -LiteralPath $tailscale)) { throw 'Tailscale installation did not produce the expected client. Restart Windows if requested, then rerun install.ps1.' }
    & $tailscale version
    if ($LASTEXITCODE -ne 0) { throw 'Installed Tailscale client failed its version check.' }
}

function Get-InstalledVCRuntime {
    $newest = $null
    foreach ($view in @([Microsoft.Win32.RegistryView]::Registry64, [Microsoft.Win32.RegistryView]::Registry32)) {
        $base = [Microsoft.Win32.RegistryKey]::OpenBaseKey([Microsoft.Win32.RegistryHive]::LocalMachine, $view)
        try {
            $key = $base.OpenSubKey('SOFTWARE\Microsoft\VisualStudio\14.0\VC\Runtimes\x64')
            if ($null -eq $key) { continue }
            try {
                if ($key.GetValue('Installed') -ne 1) { continue }
                $version = $null
                if ([version]::TryParse(([string]$key.GetValue('Version')).TrimStart('v'), [ref]$version)) {
                    if ($null -eq $newest -or $version -gt $newest) { $newest = $version }
                }
            } finally { $key.Dispose() }
        } finally { $base.Dispose() }
    }
    return $newest
}

function Initialize-VCRuntime([string]$Cache, $Dependency) {
    $minimum = [version]$Dependency.version
    $installed = Get-InstalledVCRuntime
    if ($null -ne $installed -and $installed -ge $minimum) {
        Write-Host "Reusing Microsoft VC++ x64 runtime $installed."
        return
    }
    Write-Host "Native runtime components require Microsoft VC++ x64 $minimum. Windows administrator approval is required."
    $installerPath = Join-Path $Cache ("vc_redist.$minimum.x64.exe")
    Get-VerifiedFile $Dependency.windows_amd64_url $installerPath $Dependency.windows_amd64_sha256
    $signature = Get-AuthenticodeSignature -LiteralPath $installerPath
    if ($signature.Status -ne 'Valid' -or $signature.SignerCertificate.Subject -notmatch 'Microsoft Corporation') {
        throw 'The VC++ runtime installer signature is not valid for Microsoft.'
    }
    $installer = Start-Process $installerPath -ArgumentList @('/install', '/passive', '/norestart') -Verb RunAs -WindowStyle Hidden -Wait -PassThru
    if ($installer.ExitCode -notin @(0, 3010, 1638)) { throw "VC++ runtime installation failed: $($installer.ExitCode)" }
    $installed = Get-InstalledVCRuntime
    if ($null -eq $installed -or $installed -lt $minimum) { throw 'Microsoft VC++ runtime is still missing or too old. Restart Windows if requested, then rerun install.ps1.' }
    if ($installer.ExitCode -eq 3010) { Write-Warning 'Windows requested a restart to complete the VC++ runtime installation.' }
}

function Get-InstalledWebView2 {
    $newest = $null
    # Match the pure-Go loader: stable Runtime, system installation before user.
    foreach ($hive in @([Microsoft.Win32.RegistryHive]::LocalMachine, [Microsoft.Win32.RegistryHive]::CurrentUser)) {
        foreach ($view in @([Microsoft.Win32.RegistryView]::Registry32)) {
            $base = [Microsoft.Win32.RegistryKey]::OpenBaseKey($hive, $view)
            try {
                $key = $base.OpenSubKey('SOFTWARE\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}')
                if ($null -eq $key) { continue }
                try {
                    $version = $null
                    if ([version]::TryParse([string]$key.GetValue('pv'), [ref]$version)) {
                        if ($version -gt [version]'0.0.0.0') { return $version }
                    }
                } finally { $key.Dispose() }
            } finally { $base.Dispose() }
        }
    }
    return $newest
}

function Initialize-WebView2([string]$Cache, $Dependency) {
    $minimum = [version]$Dependency.minimum_runtime_version
    $installed = Get-InstalledWebView2
    if ($null -ne $installed -and $installed -ge $minimum) {
        Write-Host "Reusing shared WebView2 Runtime $installed; its update policy is unchanged."
        return
    }
    $installerPath = Join-Path $Cache ("webview2-standalone-$($Dependency.version)-x64.exe")
    Get-VerifiedFile $Dependency.windows_amd64_url $installerPath $Dependency.windows_amd64_sha256
    $signature = Get-AuthenticodeSignature -LiteralPath $installerPath
    if ($signature.Status -ne 'Valid' -or $signature.SignerCertificate.Subject -notmatch 'Microsoft Corporation') {
        throw 'The WebView2 standalone installer signature is not valid for Microsoft.'
    }
    # A standard-user invocation installs per-user. Never request elevation or
    # change the shared updater policy, and never restart the active node here.
    $installer = Start-Process $installerPath -ArgumentList @('/silent', '/install') -WindowStyle Hidden -Wait -PassThru
    if ($installer.ExitCode -ne 0) { throw "WebView2 preparation failed ($($installer.ExitCode)); the running node was retained. Complete any Windows restart separately, then retry." }
    $installed = Get-InstalledWebView2
    if ($null -eq $installed -or $installed -lt $minimum) { throw 'WebView2 is still unavailable; the running node was retained.' }
}

function Test-WindowsNativeModules([string]$Directory, $Dependencies) {
    $paths = @()
    foreach ($module in $Dependencies.windows_native_modules) {
        if ($module -notmatch '^runtime/node_modules/[A-Za-z0-9@_./-]+\.node$' -or $module -match '(^|/)\.\.(/|$)') { throw 'Invalid native module check path.' }
        $path = Assert-ChildPath (Join-Path $Directory $module) $Directory
        if (-not (Test-Path -LiteralPath $path -PathType Leaf)) { throw "Missing native runtime module: $module" }
        $paths += $path
    }
    if ($paths.Count -eq 0) { throw 'The Windows bundle must declare native component checks.' }
    $node = Join-Path $Directory 'runtime/node/node.exe'
    & $node -e 'for (const file of process.argv.slice(1)) require(file);' @paths
    if ($LASTEXITCODE -ne 0) { throw 'Native runtime module loading failed. Check the Microsoft VC++ runtime installation.' }
}

function Initialize-InstallNative {
    if ('Asterhand.InstallNative' -as [type]) { return }
    Add-Type -TypeDefinition @'
using System;
using System.ComponentModel;
using System.Runtime.InteropServices;
using System.Text;
using Microsoft.Win32.SafeHandles;
namespace Asterhand {
    public static class InstallNative {
        [DllImport("kernel32.dll", CharSet=CharSet.Unicode, SetLastError=true)]
        static extern SafeFileHandle CreateFile(string path, uint access, uint share, IntPtr security, uint creation, uint flags, IntPtr template);
        [DllImport("kernel32.dll", CharSet=CharSet.Unicode, SetLastError=true)]
        static extern uint GetFinalPathNameByHandle(SafeFileHandle handle, StringBuilder path, uint size, uint flags);
        [DllImport("kernel32.dll", CharSet=CharSet.Unicode, ExactSpelling=true, SetLastError=true)]
        static extern int LCMapStringEx(string locale, uint flags, string source, int length, StringBuilder destination, int capacity, IntPtr version, IntPtr reserved, IntPtr sort);
        [DllImport("kernel32.dll", SetLastError=true)]
        static extern SafeFileHandle OpenProcess(uint access, bool inherit, int pid);
        [DllImport("kernel32.dll", SetLastError=true)]
        static extern bool GetProcessTimes(SafeFileHandle handle, out long created, out long exited, out long kernel, out long user);
        [DllImport("kernel32.dll", SetLastError=true)]
        static extern bool GetExitCodeProcess(SafeFileHandle handle, out uint code);
        [DllImport("kernel32.dll", CharSet=CharSet.Unicode, SetLastError=true)]
        static extern bool QueryFullProcessImageName(SafeFileHandle handle, uint flags, StringBuilder path, ref uint size);
        public static string CanonicalDirectory(string path) {
            using (var handle = CreateFile(path, 0, 7, IntPtr.Zero, 3, 0x02000000, IntPtr.Zero)) {
                if (handle.IsInvalid) throw new Win32Exception(Marshal.GetLastWin32Error());
                var value = new StringBuilder(32768);
                uint count = GetFinalPathNameByHandle(handle, value, (uint)value.Capacity, 0);
                if (count == 0 || count >= value.Capacity) throw new Win32Exception(Marshal.GetLastWin32Error());
                string result = value.ToString();
                if (result.StartsWith(@"\\?\UNC\", StringComparison.OrdinalIgnoreCase)) result = @"\\" + result.Substring(8);
                else if (result.StartsWith(@"\\?\")) result = result.Substring(4);
                return result.TrimEnd('\\');
            }
        }
        public static string LaunchLockKey(string path) {
            // Match internal/launch's Windows filesystem casing, not .NET's
            // Unicode casing tables. Hash the resulting string as UTF-8.
            string source = CanonicalDirectory(path);
            const uint lowercase = 0x00000100;
            // Include the terminator for StringBuilder marshalling on .NET Framework.
            int count = LCMapStringEx("", lowercase, source, -1, null, 0, IntPtr.Zero, IntPtr.Zero, IntPtr.Zero);
            if (count == 0) throw new Win32Exception(Marshal.GetLastWin32Error());
            var result = new StringBuilder(count);
            if (LCMapStringEx("", lowercase, source, -1, result, result.Capacity, IntPtr.Zero, IntPtr.Zero, IntPtr.Zero) == 0)
                throw new Win32Exception(Marshal.GetLastWin32Error());
            return result.ToString();
        }
        public static bool LaunchStillRunning(int pid, long created, string executable) {
            using (var handle = OpenProcess(0x1000, false, pid)) {
                if (handle.IsInvalid) {
                    int error = Marshal.GetLastWin32Error();
                    if (error == 87) return false;
                    throw new Win32Exception(error);
                }
                uint code;
                long actual, exited, kernel, user;
                if (!GetExitCodeProcess(handle, out code) || !GetProcessTimes(handle, out actual, out exited, out kernel, out user))
                    throw new Win32Exception(Marshal.GetLastWin32Error());
                if (code != 259 || actual != created) return false;
                var image = new StringBuilder(32768);
                uint size = (uint)image.Capacity;
                if (!QueryFullProcessImageName(handle, 0, image, ref size)) throw new Win32Exception(Marshal.GetLastWin32Error());
                return String.Equals(image.ToString(), executable, StringComparison.OrdinalIgnoreCase);
            }
        }
    }
}
'@
}

function Get-InstallFolder([string]$Kind) {
    $folder = if ($Kind -eq 'Desktop') { 'DesktopDirectory' } else { 'Startup' }
    $path = [Environment]::GetFolderPath($folder)
    if (-not $path) { throw "The current user's $Kind folder is unavailable." }
    return $path
}

function Get-InstallShortcut([string]$Path) {
    return (New-Object -ComObject WScript.Shell).CreateShortcut($Path)
}

function Test-OwnedShortcut([string]$Path, [string]$Program) {
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) { return $false }
    $link = Get-InstallShortcut $Path
    if ($link.TargetPath -eq (Join-Path $Program 'AsterhandLauncher.exe')) { return $true }
    $powershell = Join-Path $env:SystemRoot 'System32\WindowsPowerShell\v1.0\powershell.exe'
    $legacy = '-NoProfile -WindowStyle Hidden -File "' + (Join-Path $Program 'start.ps1') + '" -NoBrowser'
    return $link.TargetPath -eq $powershell -and $link.Arguments -eq $legacy
}

function Test-InstallerInteractive {
    if ($Host.Name -ne 'ConsoleHost' -or -not [Environment]::UserInteractive) { return $false }
    if ([Environment]::GetCommandLineArgs() -match '^-NonInteractive$') { return $false }
    try { return -not ([Console]::IsInputRedirected -or [Console]::IsOutputRedirected) }
    catch { return $false }
}

function Select-DesktopShortcut([string]$Mode, [bool]$HasLauncher, [bool]$Upgrade, [bool]$Owned) {
    if (-not $HasLauncher) {
        if ($Mode -eq 'Create') { throw 'This older package has no desktop launcher. Select a newer package or use -DesktopShortcut Skip.' }
        Write-Host 'This package uses the legacy start.ps1 entry; no desktop icon will be created.'
        return $false
    }
    if ($Mode -eq 'Create') { return $true }
    if ($Mode -eq 'Skip') { return $false }
    if ($Mode -eq 'Keep' -or $Upgrade) { return $Owned }
    if (-not (Test-InstallerInteractive)) {
        Write-Host 'Non-interactive installation: desktop icon skipped. Use -DesktopShortcut Create to request one.'
        return $false
    }
    $answer = Read-Host 'Create an Asterhand desktop icon? [Y/n]'
    if ($answer -notmatch '^(?i:y|yes|n|no)?$') { throw 'Choose Y or N, or specify -DesktopShortcut Create/Skip.' }
    return $answer -notmatch '^(?i:n|no)$'
}

function Set-InstallShortcut([string]$Path, [string]$Program, [bool]$Startup, [bool]$HasLauncher) {
    if ((Test-Path -LiteralPath $Path) -and -not (Test-OwnedShortcut $Path $Program)) {
        throw "A shortcut belonging to another application or installation already exists: $Path"
    }
    New-Item -ItemType Directory -Path (Split-Path -Parent $Path) -Force | Out-Null
    $shortcut = Get-InstallShortcut $Path
    if ($HasLauncher) {
        $shortcut.TargetPath = Join-Path $Program 'AsterhandLauncher.exe'
        $shortcut.Arguments = if ($Startup) { '--no-browser' } else { '' }
        $shortcut.IconLocation = $shortcut.TargetPath + ',0'
    } else {
        $shortcut.TargetPath = Join-Path $env:SystemRoot 'System32\WindowsPowerShell\v1.0\powershell.exe'
        $shortcut.Arguments = '-NoProfile -WindowStyle Hidden -File "' + (Join-Path $Program 'start.ps1') + '" -NoBrowser'
    }
    $shortcut.WorkingDirectory = $Program
    $shortcut.Description = 'Asterhand - personal assistant across your devices'
    $shortcut.WindowStyle = 7
    $shortcut.Save()
}

function Update-InstallShortcuts([string]$Program, [bool]$DesktopEnabled, [bool]$HasLauncher, [bool]$EnableStartup) {
    $desktopPath = Join-Path (Get-InstallFolder 'Desktop') 'Asterhand.lnk'
    $startupPath = Join-Path (Get-InstallFolder 'Startup') 'Asterhand.lnk'
    $ownedStartup = Test-OwnedShortcut $startupPath $Program
    foreach ($entry in @(@{path=$desktopPath;enabled=$DesktopEnabled}, @{path=$startupPath;enabled=($EnableStartup -or $ownedStartup)})) {
        if ($entry.enabled -and (Test-Path -LiteralPath $entry.path) -and -not (Test-OwnedShortcut $entry.path $Program)) {
            throw "Asterhand will not overwrite a shortcut from another installation: $($entry.path)"
        }
    }
    if ($DesktopEnabled) { Set-InstallShortcut $desktopPath $Program $false $HasLauncher }
    elseif (Test-OwnedShortcut $desktopPath $Program) { Remove-Item -LiteralPath $desktopPath -Force }
    if ($EnableStartup -or $ownedStartup) { Set-InstallShortcut $startupPath $Program $true $HasLauncher }
    Write-Host ('Desktop icon: ' + $(if ($DesktopEnabled) { 'created/preserved' } else { 'skipped' }))
    if ($EnableStartup -or $ownedStartup) { Write-Host 'Startup after this Windows user logs in is enabled; the browser will remain closed.' }
}

function Get-LaunchMutexName([string]$Data) {
    Initialize-InstallNative
    $canonical = [Asterhand.InstallNative]::LaunchLockKey($Data)
    $sha = [Security.Cryptography.SHA256]::Create()
    try { $digest = $sha.ComputeHash([Text.Encoding]::UTF8.GetBytes($canonical)) }
    finally { $sha.Dispose() }
    return 'Local\AsterhandLaunch-' + ([BitConverter]::ToString($digest).Replace('-', '').ToLowerInvariant())
}

function Assert-NoPendingLaunch([string]$Data) {
    $path = Join-Path $Data 'launch-state.json'
    if (-not (Test-Path -LiteralPath $path)) { return }
    try {
        $record = Get-Content -LiteralPath $path -Raw | ConvertFrom-Json
        if ($record.pid -le 0 -or $record.created -le 0 -or -not [IO.Path]::IsPathRooted($record.executable)) { throw 'Invalid launch record.' }
        if ([Asterhand.InstallNative]::LaunchStillRunning($record.pid, $record.created, $record.executable)) {
            throw 'Asterhand is still running or starting.'
        }
    } catch { throw "Cannot safely activate this installation. Stop the node and inspect $path before retrying. $($_.Exception.Message)" }
}

function Invoke-InstallNodeAPI([string]$Address, [string]$Path, [string]$Method, $Cookies, $Body, [string]$CSRF) {
    $request = [Net.HttpWebRequest]::Create($Address + $Path)
    $request.Proxy = $null
    $request.AllowAutoRedirect = $false
    $request.Timeout = 10000
    $request.ReadWriteTimeout = 10000
    $request.Method = $Method
    $request.CookieContainer = $Cookies
    $request.Headers['Origin'] = $Address
    if ($CSRF) { $request.Headers['X-Asterhand-CSRF'] = $CSRF }
    if ($null -ne $Body) {
        $bytes = [Text.Encoding]::UTF8.GetBytes(($Body | ConvertTo-Json -Compress))
        $request.ContentType = 'application/json'
        $request.ContentLength = $bytes.Length
        $stream = $request.GetRequestStream()
        try { $stream.Write($bytes, 0, $bytes.Length) } finally { $stream.Dispose() }
    }
    $response = $request.GetResponse()
    try {
        if ([int]$response.StatusCode -lt 200 -or [int]$response.StatusCode -ge 300) { throw "Node API rejected $Path." }
        $reader = New-Object IO.StreamReader($response.GetResponseStream())
        try { return $reader.ReadToEnd() | ConvertFrom-Json } finally { $reader.Dispose() }
    } finally { $response.Dispose() }
}

function Stop-InstallNode([string]$Data, [string]$Program) {
    $runtimePath = Join-Path $Data 'runtime.json'
    $deadline = [DateTime]::UtcNow.AddSeconds(20)
    # A launch may not have published runtime.json yet. Preserve uncertain state.
    while (-not (Test-Path -LiteralPath $runtimePath)) {
        try { Assert-NoPendingLaunch $Data; return }
        catch {
            if ([DateTime]::UtcNow -ge $deadline) { throw }
            Start-Sleep -Milliseconds 200
        }
    }
    $context = Get-InstallNodeContext $Data $Program
    if ($null -eq $context) { return }
    try {
        Write-Host 'Stopping this installation node; local in-flight work will require reconciliation after restart.'
        $null = Invoke-InstallNodeAPI $context.origin '/api/stop' 'POST' $context.cookies @{} $context.csrf
        if (-not $context.process.WaitForExit(30000)) { throw 'The node did not stop within 30 seconds. Inspect its logs and retry.' }
    } finally { $context.process.Dispose() }
}

function Get-InstallNodeContext([string]$Data, [string]$Program) {
    $runtime = Get-Content -LiteralPath (Join-Path $Data 'runtime.json') -Raw | ConvertFrom-Json
    if ($runtime.pid -le 0) { throw 'Invalid node process ID; inspect runtime.json.' }
    $process = Get-Process -Id $runtime.pid -ErrorAction SilentlyContinue
    if ($null -eq $process) { return }
    try {
        $executable = $process.MainModule.FileName
        $created = $process.StartTime.ToFileTimeUtc()
        Initialize-InstallNative
        $versions = [Asterhand.InstallNative]::CanonicalDirectory((Join-Path $Program 'versions'))
        $parent = [Asterhand.InstallNative]::CanonicalDirectory((Split-Path -Parent $executable))
        $null = Assert-ChildPath $parent $versions
        if ((Split-Path -Leaf $executable) -ne 'asterhand.exe') { throw 'The running process is not owned by this installation.' }
        if (-not [Asterhand.InstallNative]::LaunchStillRunning($runtime.pid, $created, $executable)) { throw 'Node process identity changed.' }
        $address = [uri]('http://' + $runtime.address)
        $ip = $null
        if ($address.UserInfo -or $address.AbsolutePath -ne '/' -or $address.Query -or $address.Fragment -or
            -not [Net.IPAddress]::TryParse($address.DnsSafeHost, [ref]$ip) -or -not [Net.IPAddress]::IsLoopback($ip)) {
            throw 'The node address must be a loopback IP and port.'
        }
        $origin = $address.GetLeftPart([UriPartial]::Authority)
        $cookies = New-Object Net.CookieContainer
        $health = Invoke-InstallNodeAPI $origin '/health' 'GET' $cookies $null ''
        $config = Get-Content -LiteralPath (Join-Path $Data 'config.json') -Raw | ConvertFrom-Json
        $healthPID = $health.PSObject.Properties['pid']
        if (-not $config.node_id -or $health.node_id -ne $config.node_id -or ($healthPID -and $healthPID.Value -ne $runtime.pid)) {
            throw 'The node health identity does not match the installation.'
        }
        if (-not [Asterhand.InstallNative]::LaunchStillRunning($runtime.pid, $created, $executable)) { throw 'Node process identity changed.' }
        $token = [IO.File]::ReadAllText((Join-Path $Data 'web-login.token'))
        $login = Invoke-InstallNodeAPI $origin '/api/login' 'POST' $cookies @{token=$token} ''
        $token = $null
        return @{process=$process;origin=$origin;cookies=$cookies;csrf=$login.csrf;executable=$executable}
    } catch { $process.Dispose(); throw }
}

function Enter-InstallActivation([string[]]$DataDirectories, [string]$Program, [switch]$StopRunning) {
    $held = [pscustomobject]@{mutexes=(New-Object Collections.ArrayList);files=(New-Object Collections.ArrayList)}
    try {
        $targets = @{}
        foreach ($data in $DataDirectories) {
            New-Item -ItemType Directory -Path $data -Force | Out-Null
            $targets[(Get-LaunchMutexName $data)] = $data
        }
        foreach ($name in @($targets.Keys | Sort-Object)) {
            $mutex = New-Object Threading.Mutex($false, $name)
            $acquired = $false
            try {
                try { $acquired = $mutex.WaitOne(20000) } catch [Threading.AbandonedMutexException] { $acquired = $true }
                if (-not $acquired) { throw 'Asterhand is starting or another installation is active. Wait for it to finish, then retry.' }
                $held.mutexes.Add($mutex) | Out-Null
            } finally { if (-not $acquired) { $mutex.Dispose() } }
            $data = $targets[$name]
            if ($StopRunning) { Stop-InstallNode $data $Program }
            Assert-NoPendingLaunch $data
            $lockPath = Join-Path $data 'service.lock'
            $options = if (Test-Path -LiteralPath $lockPath) { [IO.FileOptions]::None } else { [IO.FileOptions]::DeleteOnClose }
            try { $lock = New-Object IO.FileStream($lockPath, [IO.FileMode]::OpenOrCreate, [IO.FileAccess]::ReadWrite, [IO.FileShare]::None, 1, $options) }
            catch { throw "Asterhand is running, or its data directory is not writable. Stop the node before installing: $data" }
            $held.files.Add($lock) | Out-Null
        }
        return $held
    } catch { Exit-InstallActivation $held; throw }
}

function Exit-InstallActivation($Held) {
    if ($null -eq $Held) { return }
    foreach ($file in $Held.files) { $file.Dispose() }
    foreach ($mutex in $Held.mutexes) { $mutex.ReleaseMutex(); $mutex.Dispose() }
}

function Publish-InstallFile([string]$Source, [string]$Destination) {
    $temporary = $Destination + '.' + [guid]::NewGuid().ToString('N') + '.tmp'
    try {
        Copy-Item -LiteralPath $Source -Destination $temporary
        if (Test-Path -LiteralPath $Destination) { [IO.File]::Replace($temporary, $Destination, [NullString]::Value) }
        else { [IO.File]::Move($temporary, $Destination) }
    } finally { if (Test-Path -LiteralPath $temporary) { Remove-Item -LiteralPath $temporary -Force } }
}

function Write-InstallRecord($Record, [string]$Destination) {
    $temporary = $Destination + '.' + [guid]::NewGuid().ToString('N') + '.tmp'
    try {
        $Record | ConvertTo-Json | Set-Content -LiteralPath $temporary -Encoding UTF8
        if (Test-Path -LiteralPath $Destination) { [IO.File]::Replace($temporary, $Destination, [NullString]::Value) }
        else { [IO.File]::Move($temporary, $Destination) }
    } finally { if (Test-Path -LiteralPath $temporary) { Remove-Item -LiteralPath $temporary -Force } }
}

function Resolve-InstallCanonicalPath([string]$Path) {
    Initialize-InstallNative
    $item = Get-Item -LiteralPath $Path -Force -ErrorAction SilentlyContinue
    if ($null -ne $item) { return [Asterhand.InstallNative]::CanonicalDirectory($Path) }
    $parent = Split-Path -Parent $Path
    if (-not $parent -or $parent -eq $Path) { throw 'Cannot resolve installation path.' }
    return Join-Path (Resolve-InstallCanonicalPath $parent) (Split-Path -Leaf $Path)
}

function Assert-LocalInstallFile([string]$Root, [string]$Relative) {
    if ($Relative -notmatch '^[^:\\]+$' -or $Relative -match '(^/|(^|/)\.{1,2}(/|$)|//|[. ](/|$))') { throw 'Invalid local receipt path.' }
    $path = $Root
    foreach ($part in @('') + @($Relative.Split('/'))) {
        if ($part) { $path = Join-Path $path $part }
        $item = Get-Item -LiteralPath $path -Force
        if ($item.Attributes -band [IO.FileAttributes]::ReparsePoint) { throw 'Unexpected local candidate reparse point.' }
    }
    if ($item.PSIsContainer) { throw 'Local receipt requires a regular file.' }
    return $item
}

function Assert-LocalInstallFiles([string]$Root, $Files) {
    $seen = @{}
    if (@($Files).Count -eq 0) { throw 'Empty local file receipt.' }
    foreach ($file in $Files) {
        if ($seen.ContainsKey($file.path) -or $file.sha256 -notmatch '^[a-f0-9]{64}$' -or $file.size -lt 0) { throw 'Invalid or duplicate local receipt.' }
        $seen[$file.path] = $true
        $item = Assert-LocalInstallFile $Root $file.path
        if ($item.Length -ne $file.size -or (Get-FileHash -LiteralPath $item.FullName).Hash -ne $file.sha256) { throw "Local checksum mismatch: $($file.path)" }
    }
}

function Assert-LocalApplicationInventory([string]$Root, $Files) {
    $expected = @{'local-build.json'=$true}
    foreach ($file in $Files) { $expected[$file.path] = $true }
    $pending = New-Object 'Collections.Generic.Stack[string]'
    $pending.Push($Root)
    $count = 0
    while ($pending.Count) {
        foreach ($item in Get-ChildItem -LiteralPath $pending.Pop() -Force) {
            if ($item.FullName -eq (Join-Path $Root 'runtime')) { continue }
            if ($item.Attributes -band [IO.FileAttributes]::ReparsePoint) { throw 'Unexpected application reparse point.' }
            if ($item.PSIsContainer) { $pending.Push($item.FullName); continue }
            $relative = $item.FullName.Substring($Root.TrimEnd('\').Length+1).Replace('\','/')
            if (-not $expected.ContainsKey($relative)) { throw "Unexpected application file: $relative" }
            $count++
        }
    }
    if ($count -ne $expected.Count) { throw 'Application file inventory mismatch.' }
}

function Read-LocalInstallCandidate([string]$Application, [string]$Runtime, [string]$Program) {
    $manifestFile = Assert-LocalInstallFile $Application 'local-build.json'
    $app = Get-Content -LiteralPath $manifestFile.FullName -Raw | ConvertFrom-Json
    if ($app.schema -ne 1 -or $app.kind -ne 'local-application' -or $app.source_sha256 -notmatch '^[a-f0-9]{64}$' -or
        $app.dependency_key -notmatch '^[a-f0-9]{64}$' -or $app.version -notmatch '^[0-9]+\.[0-9]+\.[0-9]+(?:-[A-Za-z0-9.-]+)?$') { throw 'Invalid local application identity.' }
    Assert-LocalInstallFiles $Application $app.files
    Assert-LocalApplicationInventory $Application $app.files
    if (Test-Path -LiteralPath (Join-Path $Application 'runtime')) { throw 'Application input must not supply a runtime directory.' }
    foreach ($required in @('asterhand.exe','AsterhandLauncher.exe','AsterhandInputHost.exe','input-host.json','packaging/dependencies.json','packaging/runtime-lock.json','packaging/start.ps1','packaging/configure-firewall.ps1')) {
        if ($required -notin @($app.files.path)) { throw "Missing local application entry: $required" }
    }
    $hostIdentity = Get-Content -LiteralPath (Join-Path $Application 'input-host.json') -Raw | ConvertFrom-Json
    if ($hostIdentity.schema -ne 1 -or $hostIdentity.version -ne $app.version -or $hostIdentity.source_sha256 -ne $app.source_sha256) {
        throw 'Local application identity does not match the input host manifest.'
    }
    $generation = Split-Path -Leaf $Runtime
    $runtimeRoot = Join-Path $Program 'local-runtimes'
    if ($generation -notmatch ('^'+$app.dependency_key+'-[a-f0-9]{32}$') -or
        $Runtime -ne (Join-Path $runtimeRoot $generation)) { throw 'Runtime generation is outside this installation.' }
    $receiptFile = Assert-LocalInstallFile $runtimeRoot ($generation+'/runtime-receipt.json')
    $runtimeReceipt = Get-Content -LiteralPath $receiptFile.FullName -Raw | ConvertFrom-Json
    if ($runtimeReceipt.schema -ne 1 -or $runtimeReceipt.key -ne $app.dependency_key -or $runtimeReceipt.generation -ne $generation -or
        $runtimeReceipt.catalog_sha256 -ne (Get-FileHash -LiteralPath (Join-Path $Application 'packaging/dependencies.json')).Hash) { throw 'Runtime receipt identity mismatch.' }
    Assert-LocalInstallFiles $Runtime $runtimeReceipt.files
    if ((Get-FileHash -LiteralPath (Join-Path $Runtime 'package-lock.json')).Hash -ne (Get-FileHash -LiteralPath (Join-Path $Application 'packaging/runtime-lock.json')).Hash) { throw 'Runtime lock mismatch.' }
    $requiredPaths = @('node/node.exe','package.json','package-lock.json')
    $catalog = Get-Content -LiteralPath (Join-Path $Application 'packaging/dependencies.json') -Raw | ConvertFrom-Json
    foreach ($component in $catalog.components) {
        $prefix = 'node_modules/' + $component.package + '/'
        $requiredPaths += $prefix + 'package.json'
        $metadataFile = Assert-LocalInstallFile $Runtime ($prefix+'package.json')
        $metadata = Get-Content -LiteralPath $metadataFile.FullName -Raw | ConvertFrom-Json
        foreach ($name in @('main','module','bin')) {
            $property = $metadata.PSObject.Properties[$name]
            if (-not $property) { continue }
            $entries = if ($property.Value -is [string]) { @($property.Value) } else { @($property.Value.PSObject.Properties.Value) }
            foreach ($entry in $entries) { $requiredPaths += $prefix + ($entry -replace '^\./','') }
        }
    }
    foreach ($module in $catalog.windows_native_modules) {
        if (-not $module.StartsWith('runtime/')) { throw 'Invalid native module path.' }
        $requiredPaths += $module.Substring(8)
    }
    foreach ($required in $requiredPaths) {
        if ($required -notin @($runtimeReceipt.files.path)) { throw 'Missing critical runtime receipt entry.' }
    }
    $appHash = (Get-FileHash -LiteralPath $manifestFile.FullName).Hash.ToLowerInvariant()
    $runtimeHash = (Get-FileHash -LiteralPath $receiptFile.FullName).Hash.ToLowerInvariant()
    $sha = [Security.Cryptography.SHA256]::Create()
    try { $id = ([BitConverter]::ToString($sha.ComputeHash([Text.Encoding]::UTF8.GetBytes($appHash+':'+$generation+':'+$runtimeHash)))).Replace('-','').ToLowerInvariant() }
    finally { $sha.Dispose() }
    return @{app=$app;id=$id;runtime=$Runtime;generation=$generation;runtime_sha256=$runtimeHash;application_sha256=$appHash}
}

function Assert-LocalVersion([string]$Directory, $Local) {
    Assert-LocalInstallFiles $Directory $Local.app.files
    Assert-LocalApplicationInventory $Directory $Local.app.files
    if ((Get-FileHash -LiteralPath (Join-Path $Directory 'local-build.json')).Hash -ne $Local.application_sha256) { throw 'Installed application receipt mismatch.' }
    $link = Get-Item -LiteralPath (Join-Path $Directory 'runtime') -Force
    if (-not ($link.Attributes -band [IO.FileAttributes]::ReparsePoint) -or $link.LinkType -ne 'Junction' -or
        [Asterhand.InstallNative]::CanonicalDirectory($link.FullName) -ne [Asterhand.InstallNative]::CanonicalDirectory($Local.runtime)) { throw 'Installed runtime junction mismatch.' }
}

if ($PackagePath) { $PackagePath = Resolve-FileSystemPath $PackagePath }
if ($ManifestPath) { $ManifestPath = Resolve-FileSystemPath $ManifestPath }
if ($LocalBuildPath) { $LocalBuildPath = Resolve-FileSystemPath $LocalBuildPath }
if ($LocalRuntimePath) { $LocalRuntimePath = Resolve-FileSystemPath $LocalRuntimePath }
$InstallDir = Resolve-AppDirectory $InstallDir
if (-not $PSBoundParameters.ContainsKey('DataDir')) {
    $previousPath = Join-Path $InstallDir 'installation.json'
    if (Test-Path -LiteralPath $previousPath) {
        $previousRecord = Get-Content -LiteralPath $previousPath -Raw | ConvertFrom-Json
        if ($previousRecord.schema -ne 1 -or -not [IO.Path]::IsPathRooted([string]$previousRecord.data)) { throw 'Invalid existing installation record.' }
        $DataDir = [string]$previousRecord.data
    }
}
$DataDir = Resolve-AppDirectory $DataDir
Assert-SeparateDirectories $InstallDir $DataDir
$InstallDir = Resolve-AppDirectory (Resolve-InstallCanonicalPath $InstallDir)
$DataDir = Resolve-AppDirectory (Resolve-InstallCanonicalPath $DataDir)
Assert-SeparateDirectories $InstallDir $DataDir
if (Test-Path -LiteralPath $InstallDir) {
    $existing = @(Get-ChildItem -LiteralPath $InstallDir -Force)
    if ($existing.Count -gt 0 -and -not (Test-Path -LiteralPath (Join-Path $InstallDir '.asterhand-install'))) {
        throw 'Installation directory is nonempty and is not owned by Asterhand.'
    }
}
New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
$activation = $null
$installationGuard = $null
$staging = $null
try {
$guardPath = Join-Path $InstallDir '.activation.lock'
try { $installationGuard = New-Object IO.FileStream($guardPath, [IO.FileMode]::OpenOrCreate, [IO.FileAccess]::ReadWrite, [IO.FileShare]::None, 1, [IO.FileOptions]::DeleteOnClose) }
catch { throw 'Another installation is preparing or activating this directory. Wait for it to finish, then retry.' }
$marker = Join-Path $InstallDir '.asterhand-install'
if (-not (Test-Path -LiteralPath $marker)) {
    if (@(Get-ChildItem -LiteralPath $InstallDir -Force | Where-Object Name -ne '.activation.lock').Count) { throw 'Installation directory became nonempty and is not owned by Asterhand.' }
    Set-Content -LiteralPath $marker -Value 'Asterhand installer schema 1' -Encoding ASCII
} elseif ((Get-Item -LiteralPath $marker -Force).Attributes -band [IO.FileAttributes]::ReparsePoint -or
    (Get-Content -LiteralPath $marker -Raw).Trim() -ne 'Asterhand installer schema 1') { throw 'Invalid installation ownership marker.' }
if ($InitializeLocalCache) { return $InstallDir }
$cache = Join-Path $InstallDir 'downloads'
New-Item -ItemType Directory -Path $cache -Force | Out-Null
if ($LocalBuildPath) {
    $local = Read-LocalInstallCandidate $LocalBuildPath $LocalRuntimePath $InstallDir
    $Version = $local.app.version
    if ($ExpectedSourceDigest -and $ExpectedSourceDigest -ne $local.app.source_sha256) { throw 'Local source identity mismatch.' }
    $ExpectedSourceDigest = $local.app.source_sha256
    $versionName = 'local-' + $local.id
    Write-Host "Installing local application $Version ($($local.app.source_sha256.Substring(0,12)))."
} else {
$release = Get-InstallRelease $Repository $Version $ReleaseTag $PackagePath $ManifestPath $cache
$Version = $release.version
$ReleaseTag = $release.tag
$releaseBase = $release.base
$manifest = $release.manifest
Write-Host "Installing Asterhand $Version from $Repository ($ReleaseTag)."
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
}
$versions = Join-Path $InstallDir 'versions'
if ((Test-Path -LiteralPath $versions) -and ((Get-Item -LiteralPath $versions -Force).Attributes -band [IO.FileAttributes]::ReparsePoint)) { throw 'Versions directory must not be a reparse point.' }
New-Item -ItemType Directory -Path $versions -Force | Out-Null
$versionDir = Assert-ChildPath (Join-Path $versions $versionName) $versions
$staging = Assert-ChildPath (Join-Path $InstallDir ('.staging-' + [guid]::NewGuid().ToString('N'))) $InstallDir
    if (-not (Test-Path -LiteralPath $versionDir)) {
        if ($LocalBuildPath) {
            New-Item -ItemType Directory -Path $staging | Out-Null
            foreach ($file in $local.app.files) {
                $target = Assert-ChildPath (Join-Path $staging $file.path) $staging
                [IO.Directory]::CreateDirectory((Split-Path -Parent $target)) | Out-Null
                Copy-Item -LiteralPath (Join-Path $LocalBuildPath $file.path) -Destination $target
            }
            Copy-Item -LiteralPath (Join-Path $LocalBuildPath 'local-build.json') -Destination $staging
            New-Item -ItemType Junction -Path (Join-Path $staging 'runtime') -Target $local.runtime | Out-Null
            Assert-LocalVersion $staging $local
        } else {
        Write-Host 'Extracting the verified runtime bundle. This may take several minutes.'
        Expand-SafePackage $PackagePath $staging
        }
        $candidate = Join-Path $staging 'asterhand.exe'
        if ((& $candidate version) -ne $Version -or $LASTEXITCODE -ne 0) { throw 'Asterhand version check failed.' }
        & $candidate bundle-check --directory $staging
        if ($LASTEXITCODE -ne 0) { throw 'Bundled dependency check failed.' }
        Move-Item -LiteralPath $staging -Destination $versionDir
    }
    if ($LocalBuildPath) { Assert-LocalVersion $versionDir $local }
    $candidate = Join-Path $versionDir 'asterhand.exe'
    if ((& $candidate version) -ne $Version -or $LASTEXITCODE -ne 0) { throw 'Installed Asterhand version check failed.' }
    & $candidate bundle-check --directory $versionDir
    if ($LASTEXITCODE -ne 0) { throw 'Installed bundle health check failed.' }
    $dependencies = Get-Content -LiteralPath (Join-Path $versionDir 'packaging\dependencies.json') -Raw | ConvertFrom-Json
    Initialize-VCRuntime $cache $dependencies.vc_runtime
    if ($dependencies.PSObject.Properties['webview2']) { Initialize-WebView2 $cache $dependencies.webview2 }
    Test-WindowsNativeModules $versionDir $dependencies
    if (-not $SkipTailscale) { Initialize-Tailscale $cache $dependencies.tailscale }
    $recordPath = Join-Path $InstallDir 'installation.json'
    $previous = if (Test-Path -LiteralPath $recordPath) { Get-Content -LiteralPath $recordPath -Raw | ConvertFrom-Json } else { $null }
    $directories = @($DataDir)
    if ($null -ne $previous) { $directories += Resolve-AppDirectory ([string]$previous.data) }
    if ($BeforeLocalActivation) { $null = & $BeforeLocalActivation }
    Write-Host 'Candidate verified; activating the installation.'
    $activation = Enter-InstallActivation $directories $InstallDir -StopRunning:$StopRunning
    Assert-SeparateDirectories ([Asterhand.InstallNative]::CanonicalDirectory($InstallDir)) ([Asterhand.InstallNative]::CanonicalDirectory($DataDir))
    $hasLauncher = Test-Path -LiteralPath (Join-Path $versionDir 'AsterhandLauncher.exe') -PathType Leaf
    $desktopPath = Join-Path (Get-InstallFolder 'Desktop') 'Asterhand.lnk'
    $desktopEnabled = Select-DesktopShortcut $DesktopShortcut $hasLauncher ($null -ne $previous) (Test-OwnedShortcut $desktopPath $InstallDir)
    if ($hasLauncher) { Publish-InstallFile (Join-Path $versionDir 'AsterhandLauncher.exe') (Join-Path $InstallDir 'AsterhandLauncher.exe') }
    Publish-InstallFile (Join-Path $versionDir 'packaging\start.ps1') (Join-Path $InstallDir 'start.ps1')
    Publish-InstallFile (Join-Path $versionDir 'packaging\configure-firewall.ps1') (Join-Path $InstallDir 'configure-firewall.ps1')
    Update-InstallShortcuts $InstallDir $desktopEnabled $hasLauncher ([bool]$LoginAtStartup)
    $launch = @{ schema = 1; version = $Version; executable = (Join-Path $versionDir 'asterhand.exe'); data = $DataDir; desktop_shortcut = $desktopEnabled }
    if ($LocalBuildPath) {
        $launch.local_build_sha256 = $local.application_sha256
        $launch.runtime_generation = $local.generation
        $launch.source_sha256 = $local.app.source_sha256
    } else { $launch.package_sha256 = $package.sha256 }
    Write-InstallRecord $launch $recordPath
    Write-Host "Installed Asterhand $Version. Data and credentials are preserved in $DataDir."
    Write-Host "Startup entry: $InstallDir\start.ps1"
    if (-not $SkipTailscale) {
        Write-Host 'If peer connectivity is blocked, configure the narrow Tailscale-only firewall rule:'
        Write-Host "  & '$InstallDir\configure-firewall.ps1'"
    }
    Write-Host 'Installation complete. Use the desktop icon or start.ps1 when you are ready to configure this node.'
    Write-Host 'Tailscale sign-in, model credentials, workspace authorization and pairing are separate setup steps.'
} finally {
    Exit-InstallActivation $activation
    if ($null -ne $installationGuard) { $installationGuard.Dispose() }
    if ($staging -and (Test-Path -LiteralPath $staging)) {
        $safeStaging = Assert-ChildPath $staging $InstallDir
        if ((Split-Path -Leaf $safeStaging) -notlike '.staging-*') { throw 'Refusing unexpected cleanup target.' }
        $runtimeLink = Get-Item -LiteralPath (Join-Path $safeStaging 'runtime') -Force -ErrorAction SilentlyContinue
        if ($null -ne $runtimeLink -and ($runtimeLink.Attributes -band [IO.FileAttributes]::ReparsePoint)) { [IO.Directory]::Delete($runtimeLink.FullName) }
        Remove-Item -LiteralPath $safeStaging -Recurse -Force
    }
}
if ($StartAfterInstall) {
    Write-Host 'Starting the installed node and waiting for its health check.'
    & (Join-Path $InstallDir 'start.ps1')
    if ($ExpectedSourceDigest) {
        $context = Get-InstallNodeContext $DataDir $InstallDir
        if ($null -eq $context) { throw 'Installed node exited before source verification.' }
        try {
            if ($context.executable -ne (Join-Path $versionDir 'asterhand.exe')) { throw 'Startup reused a different installation version.' }
            $info = Invoke-InstallNodeAPI $context.origin '/api/info' 'GET' $context.cookies $null ''
            if ($info.source_sha256 -ne $ExpectedSourceDigest) { throw 'Running node source digest does not match the intended build.' }
            Write-Host "Verified running source: $ExpectedSourceDigest"
        } finally { $context.process.Dispose() }
    }
}
