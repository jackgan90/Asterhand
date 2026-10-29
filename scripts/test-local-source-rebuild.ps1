#requires -Version 7.0
[CmdletBinding()]
param([Parameter(Mandatory)][string]$LifecycleDirectory, [Parameter(Mandatory)][string]$Go,
    [Parameter(Mandatory)][string]$OutputDirectory)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$repo = Split-Path -Parent $PSScriptRoot
. (Join-Path $PSScriptRoot 'build-progress.ps1')
. (Join-Path $PSScriptRoot 'local-build-cache.ps1')
$LifecycleDirectory = $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($LifecycleDirectory)
$OutputDirectory = $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($OutputDirectory)
$evidence = Get-Content -LiteralPath (Join-Path $LifecycleDirectory 'local-candidate.json') -Raw | ConvertFrom-Json
if ($evidence.status -ne 'passed' -or $evidence.system_install -ne 'disabled') { throw 'A passed isolated local-candidate test is required.' }
if (Test-Path -LiteralPath $OutputDirectory) { throw 'Choose a new source-rebuild output directory.' }
$program = Join-Path $LifecycleDirectory 'program space'
$data = Join-Path $LifecycleDirectory 'data space'
$record = Get-Content -LiteralPath (Join-Path $program 'installation.json') -Raw | ConvertFrom-Json
if ($record.data -ne $data) { throw 'Lifecycle test data directory does not match.' }
$snapshot = Join-Path $OutputDirectory 'source'
[IO.Directory]::CreateDirectory($snapshot) | Out-Null
foreach ($directory in @('cmd','internal','web','plugins','packaging','scripts','tests')) { Copy-BuildTree (Join-Path $repo $directory) (Join-Path $snapshot $directory) }
foreach ($file in @('go.mod','go.sum','install.sh','install-local.cmd','LICENSE')) { Copy-Item -LiteralPath (Join-Path $repo $file) -Destination $snapshot }
# Preserve test-only Known Folders and refusal of system installers from the
# lifecycle fixture. Application sources and build scripts are actual inputs.
Copy-Item -LiteralPath (Join-Path $LifecycleDirectory 'install.ps1') -Destination $snapshot
[IO.Directory]::CreateDirectory((Join-Path $snapshot 'docs/installation')) | Out-Null
Copy-Item -LiteralPath (Join-Path $repo 'docs/installation/quickstart.md') -Destination (Join-Path $snapshot 'docs/installation')
$savedCache=$env:GOCACHE; $savedModules=$env:GOMODCACHE
$env:GOCACHE=Join-Path $repo 'out/cache/go-build'
$env:GOMODCACHE=Join-Path $repo 'out/cache/go-modules'
$runs = [Collections.Generic.List[object]]::new()
$report = @{status='running';runs=$runs;boundaries='isolated-source-copy-and-basic-node; no-live-install-or-model'}
function Start-Process($FilePath,$ArgumentList,$WindowStyle,[switch]$Wait,[switch]$PassThru) {
    if ($Wait) { throw 'Launcher unexpectedly waited for the node tree.' }
    return Microsoft.PowerShell.Management\Start-Process -FilePath $FilePath -ArgumentList (@($ArgumentList)+'--no-browser') -WindowStyle Hidden -PassThru
}
function Run-Entry([string]$Name) {
    & (Join-Path $snapshot 'scripts/install-local.ps1') -Go $Go -InstallDir $program -DataDir $data -DesktopShortcut Skip -SkipTailscale
    $latest = Get-ChildItem -LiteralPath (Join-Path $snapshot 'out/local-install/runs') -Directory | Sort-Object Name | Select-Object -Last 1
    $timing = Get-Content -LiteralPath (Join-Path $latest.FullName 'timings.json') -Raw | ConvertFrom-Json
    if ($timing.status -ne 'passed' -or -not $timing.runtime_reused) { throw 'Fast entry did not verify success with runtime reuse.' }
    $runs.Add(@{name=$Name;result=$timing})
    return $timing
}
try {
    $first = Run-Entry 'initial-application-build'
    [IO.File]::AppendAllText((Join-Path $snapshot 'internal/prompt/default-chat.txt'), "`nIsolated source freshness acceptance fixture.`n")
    $changed = Run-Entry 'txt-source-change'
    if ($changed.application_reused -or $changed.source_sha256 -eq $first.source_sha256) { throw 'Changed embedded prompt reused stale application.' }
    $repeat = Run-Entry 'unchanged-source-repeat'
    if (-not $repeat.application_reused -or $repeat.source_sha256 -ne $changed.source_sha256) { throw 'Unchanged source did not reuse application.' }
    $report.status='passed'
} catch { $report.status='failed'; $report.error=$_.Exception.Message; throw }
finally {
    # Use the production identity/loopback checks before accessing test credentials.
    try {
        $tokens=$null; $errors=$null
        $ast=[Management.Automation.Language.Parser]::ParseFile((Join-Path $LifecycleDirectory 'install.ps1'),[ref]$tokens,[ref]$errors)
        foreach ($name in @('Initialize-InstallNative','Assert-ChildPath','Invoke-InstallNodeAPI','Get-InstallNodeContext')) {
            $definition=$ast.Find({param($n) $n -is [Management.Automation.Language.FunctionDefinitionAst] -and $n.Name -eq $name},$true)
            . ([scriptblock]::Create($definition.Extent.Text))
        }
        $context = Get-InstallNodeContext $data $program
        if ($context) {
            try {
                $null=Invoke-InstallNodeAPI $context.origin '/api/stop' 'POST' $context.cookies @{} $context.csrf
                if (-not $context.process.WaitForExit(15000)) { throw 'Test node failed normal shutdown.' }
            } finally { $context.process.Dispose() }
        }
    } catch { $report.cleanup_error=$_.Exception.Message; $report.status='failed' }
    $env:GOCACHE=$savedCache; $env:GOMODCACHE=$savedModules
    Write-LocalRecord $report (Join-Path $OutputDirectory 'source-rebuild.json')
}
if ($report.status -ne 'passed') { throw 'Source-rebuild validation failed; inspect its report.' }
Write-Host 'Actual Fast entry: changed embedded prompt and warm repeat passed.'
