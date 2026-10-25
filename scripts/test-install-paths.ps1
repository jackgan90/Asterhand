#requires -Version 5.1
[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$Installer,
    [Parameter(Mandatory)][string]$PackageDirectory,
    [Parameter(Mandatory)][string]$OutputDirectory,
    [string]$BaselineInstaller
)
$ErrorActionPreference = 'Stop'
$Installer = $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($Installer)
$PackageDirectory = $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($PackageDirectory)
$OutputDirectory = $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($OutputDirectory)
if ($BaselineInstaller) { $BaselineInstaller = $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($BaselineInstaller) }
if (Test-Path -LiteralPath $OutputDirectory) { throw 'Use a new test output directory.' }
$originalLocation = Get-Location
$originalProcessDirectory = [Environment]::CurrentDirectory
$report = [ordered]@{status='running';powershell=$PSVersionTable.PSVersion.ToString();checks=@();network='skipped';real_model='skipped'}
$packageLocation = Join-Path $OutputDirectory 'package space'
$processLocation = Join-Path $OutputDirectory 'process location'
New-Item -ItemType Directory -Path $packageLocation,$processLocation -Force | Out-Null
$manifest = Get-Content -LiteralPath (Join-Path $PackageDirectory 'release-manifest.json') -Raw | ConvertFrom-Json
$repository = if ($manifest.PSObject.Properties['repository']) { $manifest.repository } else { 'jackgan90/Asterhand' }
$package = $manifest.packages | Where-Object { $_.os -eq 'windows' -and $_.arch -eq 'amd64' }
Copy-Item -LiteralPath (Join-Path $PackageDirectory $package.file),(Join-Path $PackageDirectory 'release-manifest.json') -Destination $packageLocation
# Build Unicode names without relying on Windows PowerShell's BOM-less script decoding.
$unicode = [string][char]0x8282 + [char]0x70b9
$relativeProgram = '.\' + $unicode + ' program'
$relativeData = '.\' + $unicode + ' data'
$program = Join-Path $packageLocation ($unicode + ' program')
$data = Join-Path $packageLocation ($unicode + ' data')
$sentinel = Join-Path $data 'preserved.txt'

function Assert-Installation([string]$Case) {
    $installed = Get-Content -LiteralPath (Join-Path $program 'installation.json') -Raw | ConvertFrom-Json
    if ($installed.data -ne $data -or -not (Test-Path -LiteralPath $installed.executable)) { throw "$Case resolved the wrong installation paths." }
    if (-not $installed.executable.StartsWith($program + '\', [StringComparison]::OrdinalIgnoreCase)) { throw "$Case installed outside the expected program directory." }
    if ($installed.package_sha256 -ne $package.sha256) { throw "$Case used the wrong package." }
    if ([Environment]::CurrentDirectory -ne $processLocation) { throw "$Case changed the process working directory." }
    $report.checks += @{name=$Case;status='passed'}
}

try {
    Set-Location -LiteralPath $packageLocation
    [Environment]::CurrentDirectory = $processLocation
    if ($BaselineInstaller) {
        $failure = $null
        try {
            & $BaselineInstaller -Repository $repository -PackagePath ('.\' + $package.file) -InstallDir $relativeProgram -DataDir $relativeData -SkipTailscale -NoStart
        } catch { $failure = $_.Exception.Message }
        if (-not $failure -or -not $failure.Contains((Join-Path $processLocation 'release-manifest.json'))) { throw 'The original manifest-path defect did not reproduce.' }
        $report.baseline = @{status='failed_as_expected';error=$failure}
    }
    & $Installer -Repository $repository -PackagePath ('.\' + $package.file) -InstallDir $relativeProgram -DataDir $relativeData -SkipTailscale -NoStart
    Assert-Installation 'relative-package-auto-manifest'
    [IO.File]::WriteAllText($sentinel, 'preserve-user-data')
    & $Installer -Repository $repository -PackagePath ('.\' + $package.file) -ManifestPath '.\release-manifest.json' -InstallDir $relativeProgram -DataDir $relativeData -SkipTailscale -NoStart
    Assert-Installation 'relative-explicit-manifest-repeat'
    Set-Location -LiteralPath $OutputDirectory
    & $Installer -Repository $repository -PackagePath (Join-Path $packageLocation $package.file) -InstallDir $program -DataDir $data -SkipTailscale -NoStart
    Assert-Installation 'absolute-package-from-other-location'
    if ([IO.File]::ReadAllText($sentinel) -ne 'preserve-user-data') { throw 'Repeated installation changed existing data.' }
    $report.checks += @{name='existing-data-preserved';status='passed'}
    $failure = $null
    try {
        & $Installer -PackagePath 'HKCU:\Software' -InstallDir $program -DataDir $data -SkipTailscale -NoStart
    } catch { $failure = $_.Exception.Message }
    if (-not $failure -or -not $failure.Contains('FileSystem provider')) { throw 'Non-file provider path was not rejected.' }
    $report.checks += @{name='non-filesystem-provider-rejected';status='passed'}
    $report.package_sha256 = $package.sha256
    $report.installer_sha256 = (Get-FileHash -LiteralPath $Installer -Algorithm SHA256).Hash.ToLowerInvariant()
    $report.status = 'passed'
} catch {
    $report.status = 'failed'
    $report.error = $_.Exception.Message
    throw
} finally {
    [Environment]::CurrentDirectory = $originalProcessDirectory
    Set-Location -LiteralPath $originalLocation.Path
    $report | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath (Join-Path $OutputDirectory 'verification.json') -Encoding UTF8
}
Write-Host 'Installer path regression passed.'
