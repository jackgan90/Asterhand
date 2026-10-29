#requires -Version 7.0
[CmdletBinding()]
param([Parameter(Mandatory)][string]$OutputDirectory)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$repo = Split-Path -Parent $PSScriptRoot
$OutputDirectory = $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($OutputDirectory)
if (Test-Path -LiteralPath $OutputDirectory) { throw 'Choose a new local entry test directory.' }
$fixture = Join-Path $OutputDirectory 'source fixture'
New-Item -ItemType Directory -Path (Join-Path $fixture 'scripts'),(Join-Path $fixture 'packaging') -Force | Out-Null
Copy-Item -LiteralPath (Join-Path $repo 'scripts\install-local.ps1') -Destination (Join-Path $fixture 'scripts')
Copy-Item -LiteralPath (Join-Path $repo 'scripts\local-build-cache.ps1') -Destination (Join-Path $fixture 'scripts')
$checks = [Collections.Generic.List[string]]::new()
function Assert([bool]$Condition, [string]$Message) { if (-not $Condition) { throw $Message } }
function Expect-Failure([scriptblock]$Action, [string]$Pattern) {
    try { & $Action | Out-Null } catch { if ($_.Exception.Message -match $Pattern) { return }; throw }
    throw "Expected failure: $Pattern"
}
function Write-Fixture([string]$Path, [string]$Content) { Set-Content -LiteralPath (Join-Path $fixture $Path) -Value $Content -Encoding utf8 }
Write-Fixture 'packaging\dependencies.json' '{"go":{"version":"1.27.3"}}'
Write-Fixture 'packaging\runtime-lock.json' '{}'
Write-Fixture 'current-source.txt' 'first source'
Write-Fixture 'compiler.ps1' 'param($Command); if ($Command -ne ''version'') { throw ''Unexpected compiler invocation'' }; ''go version go1.27.3 windows/amd64''; $global:LASTEXITCODE=0'
Write-Fixture 'scripts\release-prepare.ps1' '# No toolchain download is needed with an explicit compiler.'
Write-Fixture 'scripts\build-progress.ps1' @'
function Invoke-BuildProcess($Label,$Executable,$Arguments) {
    $index = [Array]::IndexOf($Arguments, '-File')
    $parameters = @{}
    for ($i=$index+2; $i -lt $Arguments.Count; $i+=2) { $parameters[$Arguments[$i].TrimStart('-')]=$Arguments[$i+1] }
    & $Arguments[$index+1] @parameters
}
'@
Write-Fixture 'scripts\local-runtime-cache.ps1' @'
function Get-LocalPreparedDependencies($Repository,$Workspace,$BuilderGo,$PreparedPath,[switch]$Fresh) {
    $key = (Get-FileHash (Join-Path $Repository 'packaging/runtime-lock.json')).Hash
    $record = Join-Path $Workspace 'prepared.json'
    if (-not $Fresh -and (Test-Path $record)) {
        $cached = Get-Content $record -Raw | ConvertFrom-Json
        if ($cached.key -eq $key) { return $cached }
    }
    $directory = Join-Path $Workspace ([guid]::NewGuid().ToString('N'))
    & (Join-Path $Repository 'scripts/prepare-dependencies.ps1') -OutputDirectory $directory
    $prepared = @{key=$key;node_distribution=$directory;runtime_root=$directory}
    Write-LocalRecord $prepared $record
    return $prepared
}
function Get-LocalInstalledRuntime($Repository,$Workspace,$Program,$BuilderGo,$PreparedPath,[switch]$Repair) {
    $prepared = Get-LocalPreparedDependencies $Repository $Workspace $BuilderGo $PreparedPath -Fresh:$Repair
    return @{directory=$prepared.runtime_root;key=$prepared.key;reused=$true}
}
'@
Write-Fixture 'scripts\dependency-tools.ps1' @'
function Read-DependencyCatalog($Path) { Get-Content -LiteralPath $Path -Raw | ConvertFrom-Json }
function Get-SourceFingerprint($Repository) {
    return ((Get-FileHash (Join-Path $Repository 'current-source.txt')).Hash + (Get-FileHash (Join-Path $Repository 'packaging\runtime-lock.json')).Hash)
}
function Assert-PreparedDependencies($Repository,$NodeDistribution,$RuntimeRoot,$Go) {
    if (-not (Test-Path -LiteralPath $NodeDistribution) -or -not (Test-Path -LiteralPath $RuntimeRoot)) { throw 'Missing prepared dependencies.' }
}
'@
Write-Fixture 'scripts\prepare-dependencies.ps1' @'
param($OutputDirectory)
New-Item -ItemType Directory -Path $OutputDirectory | Out-Null
@{node_distribution=$OutputDirectory;runtime_root=$OutputDirectory} | ConvertTo-Json | Set-Content (Join-Path $OutputDirectory 'prepared.json')
Add-Content (Join-Path (Split-Path -Parent $PSScriptRoot) 'prepare-calls.txt') 'prepared'
'@
Write-Fixture 'scripts\build.ps1' @'
param($Go,$NodeDistribution,$RuntimeRoot,$OutputDir)
$root = Split-Path -Parent $PSScriptRoot
if (Test-Path (Join-Path $root 'fail-build')) { throw 'Synthetic build failure.' }
New-Item -ItemType Directory -Path $OutputDir -Force | Out-Null
$package = Join-Path $OutputDir 'asterhand-1.0.0-windows-amd64.zip'
Copy-Item -LiteralPath (Join-Path $root 'current-source.txt') -Destination $package
$source = Get-SourceFingerprint $root
$manifestPath = Join-Path $OutputDir 'release-manifest.json'
@{schema=1;repository='example/local-build';source_sha256=$source;packages=@(@{os='windows';arch='amd64';file=(Split-Path -Leaf $package);sha256=(Get-FileHash $package).Hash;size=(Get-Item $package).Length})} | ConvertTo-Json -Depth 5 | Set-Content $manifestPath
@{schema=1;source_sha256=$source;manifest=$manifestPath} | ConvertTo-Json | Set-Content (Join-Path $root 'out\latest-build.json')
Add-Content (Join-Path $root 'build-calls.txt') 'built'
'@
Write-Fixture 'install.ps1' @'
param($Repository,$PackagePath,$InstallDir,$DataDir,$DesktopShortcut,[switch]$StopRunning,[switch]$StartAfterInstall,[switch]$SkipTailscale,
    [switch]$InitializeLocalCache,$LocalBuildPath,$LocalRuntimePath,[scriptblock]$BeforeLocalActivation,$ExpectedSourceDigest)
if ($InitializeLocalCache) { return $InstallDir }
if ($LocalBuildPath) {
    if (Test-Path (Join-Path $PSScriptRoot 'change-before-activation')) { Set-Content (Join-Path $PSScriptRoot 'current-source.txt') 'changed inside installer' }
    & $BeforeLocalActivation
    $content=(Get-Content -LiteralPath (Join-Path $LocalBuildPath 'source.txt') -Raw).Trim()
} else { $content=(Get-Content -LiteralPath $PackagePath -Raw).Trim() }
@{repository=$Repository;package=$PackagePath;application=$LocalBuildPath;content=$content;install=$InstallDir;data=$DataDir;data_explicit=$PSBoundParameters.ContainsKey('DataDir');desktop=$DesktopShortcut;stop=[bool]$StopRunning;start=[bool]$StartAfterInstall;skip=[bool]$SkipTailscale} | ConvertTo-Json | Set-Content (Join-Path $PSScriptRoot 'invocation.json')
Add-Content (Join-Path $PSScriptRoot 'install-calls.txt') 'installed'
'@
Write-Fixture 'scripts\build-application.ps1' @'
param($Go,$Node,$OutputDirectory,$SourceDigest)
$root=Split-Path -Parent $PSScriptRoot
New-Item -ItemType Directory -Path $OutputDirectory -Force | Out-Null
Copy-Item (Join-Path $root 'current-source.txt') (Join-Path $OutputDirectory 'source.txt')
@{version='1.0.0';source_sha256=$SourceDigest} | ConvertTo-Json | Set-Content (Join-Path $OutputDirectory 'input-host.json')
Add-Content (Join-Path $root 'application-calls.txt') 'built'
'@
$entry = Join-Path $fixture 'scripts\install-local.ps1'
function Invoke-FixtureEntry { & $entry -Mode Full @args }
$compiler = Join-Path $fixture 'compiler.ps1'
$target = Join-Path $OutputDirectory 'program space'
$report = [ordered]@{status='running';powershell=$PSVersionTable.PSVersion.ToString();checks=$checks;boundaries='fixture-builder-installer-and-network'}
try {
    Invoke-FixtureEntry -Go $compiler -InstallDir $target -SkipTailscale
    $first = Get-Content (Join-Path $fixture 'invocation.json') -Raw | ConvertFrom-Json
    Assert ($first.content -eq 'first source' -and $first.stop -and $first.start -and $first.skip -and $first.repository -eq 'example/local-build') 'First local installation lost build content, repository or installer options.'
    Assert (-not $first.data_explicit -and $first.desktop -eq 'Auto' -and $first.install -eq $target) 'Default local entry must preserve installer directory and desktop policy.'
    Assert (@(Get-Content (Join-Path $fixture 'prepare-calls.txt')).Count -eq 1) 'First entry did not prepare dependencies exactly once.'
    $checks.Add('first-entry-builds-and-delegates-default-directory-and-desktop-policy')

    Write-Fixture 'current-source.txt' 'changed source'
    Invoke-FixtureEntry -Go $compiler -InstallDir $target -DataDir (Join-Path $OutputDirectory 'custom data') -DesktopShortcut Keep
    $second = Get-Content (Join-Path $fixture 'invocation.json') -Raw | ConvertFrom-Json
    Assert ($second.content -eq 'changed source' -and $second.package -ne $first.package) 'Repeated entry reused a stale package.'
    Assert ($second.data_explicit -and $second.desktop -eq 'Keep') 'Explicit options were lost.'
    Assert (@(Get-Content (Join-Path $fixture 'prepare-calls.txt')).Count -eq 1) 'Repeated entry did not reuse prepared dependencies.'
    $checks.Add('repeat-rebuilds-current-source-reuses-dependencies-and-forwards-options')

    Invoke-FixtureEntry -Go $compiler -InstallDir $target
    $reused = Get-Content (Join-Path $fixture 'invocation.json') -Raw | ConvertFrom-Json
    Assert ($reused.package -eq $second.package -and @(Get-Content (Join-Path $fixture 'build-calls.txt')).Count -eq 2) 'Unchanged source did not reuse the verified package.'
    $checks.Add('unchanged-source-reuses-existing-standard-build')
    Set-Content -LiteralPath $reused.package -Value 'damaged package'
    Invoke-FixtureEntry -Go $compiler -InstallDir $target
    Assert (@(Get-Content (Join-Path $fixture 'build-calls.txt')).Count -eq 3) 'Corrupted package did not trigger a rebuild.'
    $checks.Add('corrupted-package-is-rebuilt')

    Write-Fixture 'packaging\runtime-lock.json' '{"changed":true}'
    Invoke-FixtureEntry -Go $compiler -InstallDir $target
    Assert (@(Get-Content (Join-Path $fixture 'prepare-calls.txt')).Count -eq 2) 'Changed dependency lock did not trigger preparation.'
    $checks.Add('changed-lock-invalidates-dependency-cache')
    Write-Fixture 'fail-build' 'fail'
    Write-Fixture 'current-source.txt' 'source requiring a failed rebuild'
    Expect-Failure { Invoke-FixtureEntry -Go $compiler -InstallDir $target } 'Synthetic build failure'
    Assert (@(Get-Content (Join-Path $fixture 'install-calls.txt')).Count -eq 5) 'Failed build invoked installation.'
    $checks.Add('build-failure-does-not-install-or-stop-node')

    & $entry -Go $compiler -InstallDir $target -SkipTailscale
    $fast = Get-Content (Join-Path $fixture 'invocation.json') -Raw | ConvertFrom-Json
    Assert ($fast.application -and $fast.content -eq 'source requiring a failed rebuild') 'Default Fast entry did not activate the current directory candidate.'
    & $entry -Go $compiler -InstallDir $target -SkipTailscale
    Assert (@(Get-Content (Join-Path $fixture 'application-calls.txt')).Count -eq 1) 'Fast repeat did not reuse the application.'
    $beforeCalls = @(Get-Content (Join-Path $fixture 'install-calls.txt')).Count
    Write-Fixture 'change-before-activation' 'change'
    Expect-Failure { & $entry -Go $compiler -InstallDir $target } 'Source changed before activation'
    Assert (@(Get-Content (Join-Path $fixture 'install-calls.txt')).Count -eq $beforeCalls) 'Source change inside the installer reached activation.'
    $checks.Add('default-fast-build-reuse-and-closed-source-guard-before-activation')

    & (Join-Path $PSScriptRoot 'test-installer-bootstrap.ps1') -OutputDirectory (Join-Path $OutputDirectory 'bootstrap')
    $checks.Add('readme-and-guide-one-line-bootstrap-and-parameters')
    $report.status = 'passed'
} catch { $report.status='failed'; $report.error=$_.Exception.Message; throw }
finally { $report | ConvertTo-Json -Depth 6 | Set-Content -LiteralPath (Join-Path $OutputDirectory 'local-entry.json') -Encoding utf8 }
Write-Host "Local entry regression passed: $($checks.Count) checks."
