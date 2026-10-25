#requires -Version 5.1
[CmdletBinding()]
param([Parameter(Mandatory)][string]$OutputDirectory)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$OutputDirectory = $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($OutputDirectory)
if (Test-Path -LiteralPath $OutputDirectory) { throw 'Choose a new release-selection test directory.' }
New-Item -ItemType Directory -Path $OutputDirectory | Out-Null
$installer = Join-Path (Split-Path -Parent $PSScriptRoot) 'install.ps1'
$tokens = $null
$parseErrors = $null
$ast = [Management.Automation.Language.Parser]::ParseFile($installer, [ref]$tokens, [ref]$parseErrors)
if ($parseErrors.Count) { throw 'Installer syntax is invalid.' }
$definition = @($ast.FindAll({ param($node) $node -is [Management.Automation.Language.FunctionDefinitionAst] -and $node.Name -eq 'Get-InstallRelease' }, $true))
if ($definition.Count -ne 1) { throw 'Missing release selection function.' }
. ([scriptblock]::Create($definition[0].Extent.Text))
$checks = New-Object 'System.Collections.Generic.List[string]'
$report = [ordered]@{status='running';powershell=$PSVersionTable.PSVersion.ToString();checks=$checks;network='mocked'}

function Reset-Fixture {
    $script:apiCalls = 0
    $script:downloads = New-Object 'System.Collections.Generic.List[string]'
    $script:apiError = ''
    $script:latest = [pscustomobject]@{tag_name='v2.3.4';draft=$false;prerelease=$false}
    $script:manifest = @{schema=1;version='2.3.4';repository='example/project';tag='v2.3.4';packages=@()}
}
function Invoke-RestMethod([string]$Uri, $Headers, [int]$TimeoutSec) {
    if ($Uri -ne 'https://api.github.com/repos/example/project/releases/latest') { throw 'Unexpected API URL.' }
    if ($Headers.Accept -ne 'application/vnd.github+json' -or $TimeoutSec -le 0) { throw 'Invalid API options.' }
    $script:apiCalls++
    if ($script:apiError) { throw $script:apiError }
    return $script:latest
}
function Invoke-WebRequest([string]$Uri, [string]$OutFile, [switch]$UseBasicParsing) {
    $script:downloads.Add($Uri)
    $script:manifest | ConvertTo-Json -Depth 6 | Set-Content -LiteralPath $OutFile -Encoding UTF8
    # Simulate a new latest release appearing after selection; downloads must retain the first tag.
    $script:latest.tag_name = 'v9.9.9'
}
function Select-Release([string]$Version='', [string]$Tag='', [string]$Package='', [string]$Manifest='') {
    Get-InstallRelease 'example/project' $Version $Tag $Package $Manifest $OutputDirectory
}
function Expect-Failure([scriptblock]$Action, [string]$Pattern) {
    try { & $Action | Out-Null } catch { if ($_.Exception.Message -match $Pattern) { return }; throw }
    throw "Expected rejection: $Pattern"
}
function Assert-NoLatest {
    if ($script:apiCalls -ne 0) { throw 'Explicit or local selection unexpectedly queried Latest.' }
}
try {
    Reset-Fixture
    $release = Select-Release
    if ($script:apiCalls -ne 1 -or $script:downloads.Count -ne 1 -or
        $script:downloads[0] -ne 'https://github.com/example/project/releases/download/v2.3.4/release-manifest.json' -or
        $release.base -ne 'https://github.com/example/project/releases/download/v2.3.4' -or $release.version -ne '2.3.4') {
        throw 'Latest selection did not freeze a single tag.'
    }
    $checks.Add('latest-resolved-once-and-downloads-pinned')

    foreach ($version in @('2.3.4','v2.3.4')) {
        Reset-Fixture
        $release = Select-Release -Version $version
        Assert-NoLatest
        if ($release.tag -ne 'v2.3.4' -or $release.version -ne '2.3.4') { throw 'Explicit version normalization failed.' }
    }
    $checks.Add('explicit-version-with-optional-v')

    Reset-Fixture
    $script:manifest.version = '3.0.0-alpha.1'
    $script:manifest.tag = 'v3.0.0-alpha.1'
    $release = Select-Release -Version '3.0.0-alpha.1'
    Assert-NoLatest
    if ($release.version -ne '3.0.0-alpha.1') { throw 'Explicit prerelease version failed.' }
    $checks.Add('explicit-prerelease')

    Reset-Fixture
    $script:manifest.tag = 'install-test.7'
    $release = Select-Release -Tag 'install-test.7'
    Assert-NoLatest
    if ($release.version -ne '2.3.4' -or $release.tag -ne 'install-test.7') { throw 'Custom tag failed.' }
    Select-Release -Version '2.3.4' -Tag 'install-test.7' | Out-Null
    Expect-Failure { Select-Release -Version '1.0.0' -Tag 'install-test.7' } 'version mismatch'
    $checks.Add('custom-tag-with-optional-version-assertion')

    Reset-Fixture
    $localManifest = Join-Path $OutputDirectory 'release-manifest.json'
    $script:manifest.tag = 'install-test.7'
    $script:manifest | ConvertTo-Json | Set-Content -LiteralPath $localManifest -Encoding UTF8
    $release = Select-Release -Version '2.3.4' -Package (Join-Path $OutputDirectory 'package.zip')
    Assert-NoLatest
    if ($script:downloads.Count -ne 0 -or $release.tag -ne 'install-test.7') { throw 'Local package must derive coordinates from its manifest.' }
    $release = Select-Release -Manifest $localManifest
    if ($release.base -ne 'https://github.com/example/project/releases/download/install-test.7') { throw 'Manifest-only package URL is wrong.' }
    Assert-NoLatest
    Expect-Failure { Select-Release -Version '1.0.0' -Manifest $localManifest } 'version mismatch'
    $checks.Add('local-package-and-manifest-only-without-latest')

    Reset-Fixture
    $script:manifest.Remove('tag')
    $script:manifest.Remove('repository')
    $script:manifest | ConvertTo-Json | Set-Content -LiteralPath $localManifest -Encoding UTF8
    $release = Select-Release -Manifest $localManifest
    if ($release.tag -ne 'v2.3.4') { throw 'Legacy schema-1 manifest compatibility failed.' }
    Assert-NoLatest
    $checks.Add('legacy-manifest-coordinate-inference')

    foreach ($failure in @('404 Not Found','403 API rate limit','Network unavailable')) {
        Reset-Fixture
        $script:apiError = $failure
        Expect-Failure { Select-Release } 'Cannot resolve the latest stable release'
        if ($script:downloads.Count) { throw 'Failed lookup must not download a fallback.' }
    }
    $checks.Add('missing-stable-api-limit-and-network-fail-without-fallback')

    foreach ($flag in @('draft','prerelease')) {
        Reset-Fixture
        $script:latest.$flag = $true
        Expect-Failure { Select-Release } 'published stable release'
    }
    $checks.Add('latest-rejects-draft-and-prerelease')

    Reset-Fixture
    $script:latest.tag_name = '../invalid'
    Expect-Failure { Select-Release } 'Invalid release tag'
    if ($script:downloads.Count) { throw 'Invalid tag must fail before download.' }
    $checks.Add('invalid-remote-tag-rejected')

    foreach ($field in @('schema','version','tag','repository')) {
        Reset-Fixture
        $script:manifest[$field] = switch ($field) { 'schema' {2}; 'version' {'../unsafe'}; 'tag' {'v8.0.0'}; 'repository' {'other/project'} }
        Expect-Failure { Select-Release } 'manifest'
    }
    $checks.Add('manifest-schema-version-and-coordinates-validated')
    $report.status = 'passed'
} catch {
    $report.status = 'failed'
    $report.error = $_.Exception.Message
    throw
} finally {
    $report | ConvertTo-Json -Depth 5 | Set-Content -LiteralPath (Join-Path $OutputDirectory 'release-selection.json') -Encoding UTF8
}
Write-Host "Installer release selection passed: $($checks.Count) checks."
