#requires -Version 7.0
[CmdletBinding()]
param(
    [string]$Go = 'go',
    [Parameter(Mandatory)][string]$NodeDistribution,
    [Parameter(Mandatory)][string]$RuntimeRoot,
    [Parameter(Mandatory)][string]$OutputDirectory,
    [string]$LiveHome,
    [string]$LiveAddress,
    [string]$PackageDirectory,
    [string]$PreviousPackageDirectory
)
$ErrorActionPreference = 'Stop'
$repo = Split-Path -Parent $PSScriptRoot
. (Join-Path $PSScriptRoot 'dependency-tools.ps1')
New-Item -ItemType Directory -Path $OutputDirectory -Force | Out-Null
$OutputDirectory = [IO.Path]::GetFullPath($OutputDirectory)
$report = [ordered]@{
    schema=1; created_utc=[DateTime]::UtcNow.ToString('o'); platform=[Environment]::OSVersion.VersionString
    catalog_sha256=(Get-FileHash (Join-Path $repo 'packaging\dependencies.json')).Hash.ToLowerInvariant()
    lock_sha256=(Get-FileHash (Join-Path $repo 'packaging\runtime-lock.json')).Hash.ToLowerInvariant()
    source_sha256=(Get-SourceFingerprint $repo)
    checks=[ordered]@{}; upgrade_ready=$false; user_acceptance='pending'; published=$false
}
function Invoke-Check([string]$Name,[scriptblock]$Action) {
    Write-Host "Verifying $Name..."
    try { & $Action; $report.checks[$Name] = @{status='passed'} }
    catch { $report.checks[$Name] = @{status='failed';reason=$_.Exception.Message}; throw }
}
try {
    Invoke-Check 'dependency_catalog' { $null = Assert-PreparedDependencies $repo $NodeDistribution $RuntimeRoot $Go }
    Invoke-Check 'dependency_guards' { & (Join-Path $PSScriptRoot 'test-dependency-tools.ps1') }
    Invoke-Check 'go_tests' {
        & $Go -C $repo test -count=1 -json ./... | Set-Content (Join-Path $OutputDirectory 'go-tests.jsonl') -Encoding utf8NoBOM
        if ($LASTEXITCODE -ne 0) { throw 'Go tests failed; see go-tests.jsonl.' }
    }
    Invoke-Check 'go_vet' {
        & $Go -C $repo vet ./... 2>&1 | Set-Content (Join-Path $OutputDirectory 'go-vet.txt') -Encoding utf8NoBOM
        if ($LASTEXITCODE -ne 0) { throw 'Go vet failed; see go-vet.txt.' }
    }
    Invoke-Check 'javascript_contracts' {
        $node = Join-Path $NodeDistribution 'node.exe'
        & $node --check (Join-Path $repo 'web\app.js')
        if ($LASTEXITCODE -ne 0) { throw 'Web JavaScript syntax failed.' }
        $uiTests = Get-ChildItem -LiteralPath (Join-Path $repo 'tests') -Filter '*.test.mjs' -File | Sort-Object Name
        & $node --test $uiTests.FullName
        if ($LASTEXITCODE -ne 0) { throw 'Tool plugin contract failed.' }
    }
    if ($LiveHome -and $LiveAddress) {
        Invoke-Check 'real_model' { & (Join-Path $PSScriptRoot 'verify-live.ps1') -NodeHome $LiveHome -Address $LiveAddress -ExpectedSourceDigest $report.source_sha256 -OutputPath (Join-Path $OutputDirectory 'real-model.json') }
    } else { $report.checks['real_model'] = @{status='skipped';reason='Supply LiveHome and LiveAddress for an explicitly configured local node; this uses model quota.'} }
    if ($PackageDirectory) {
        $manifest = Get-Content (Join-Path $PackageDirectory 'release-manifest.json') -Raw | ConvertFrom-Json
        if ($manifest.source_sha256 -ne $report.source_sha256 -or $manifest.catalog_sha256 -ne $report.catalog_sha256) { throw 'Candidate package manifest differs from the source/catalog being verified.' }
        $report.candidate = @{version=$manifest.version;windows_sha256=($manifest.packages | Where-Object os -eq 'windows').sha256}
        Invoke-Check 'windows_install' { & (Join-Path $PSScriptRoot 'test-install.ps1') -PackageDirectory $PackageDirectory -PreviousPackageDirectory $PreviousPackageDirectory -OutputDirectory (Join-Path $OutputDirectory 'installation') }
        $report.checks['windows_upgrade'] = if ($PreviousPackageDirectory) { @{status='passed'} } else { @{status='skipped';reason='Supply PreviousPackageDirectory to verify data preservation across candidates.'} }
    } else { $report.checks['windows_install'] = @{status='skipped';reason='Build a candidate first, then supply PackageDirectory.'} }
    $report.upgrade_ready = @($report.checks.Values | Where-Object { $_.status -ne 'passed' }).Count -eq 0
} finally {
    $report | ConvertTo-Json -Depth 12 | Set-Content (Join-Path $OutputDirectory 'verification.json') -Encoding utf8NoBOM
}
Write-Host "Verification recorded in $OutputDirectory; upgrade_ready=$($report.upgrade_ready). Two-machine acceptance and publishing remain separate."
