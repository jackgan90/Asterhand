#requires -Version 7.0
[CmdletBinding()]
param([Parameter(Mandatory)][string]$ApplicationDirectory, [Parameter(Mandatory)][string]$PreparedPath,
    [Parameter(Mandatory)][string]$Go, [Parameter(Mandatory)][string]$OutputDirectory,
    [string]$PackageDirectory)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$repo = Split-Path -Parent $PSScriptRoot
foreach ($helper in @('dependency-tools.ps1','build-progress.ps1','local-build-cache.ps1','local-runtime-cache.ps1')) { . (Join-Path $PSScriptRoot $helper) }
$OutputDirectory = $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($OutputDirectory)
$ApplicationDirectory = $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($ApplicationDirectory)
$PreparedPath = $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($PreparedPath)
if (Test-Path -LiteralPath $OutputDirectory) { throw 'Choose a new isolated candidate test directory.' }
[IO.Directory]::CreateDirectory($OutputDirectory) | Out-Null
$program = Join-Path $OutputDirectory 'program space'
$data = Join-Path $OutputDirectory 'data space'
$cache = Join-Path $OutputDirectory 'cache'
[IO.Directory]::CreateDirectory($cache) | Out-Null
$checks = [Collections.Generic.List[string]]::new()
$timings = [Collections.Generic.List[object]]::new()
$report = @{status='running';powershell=$PSVersionTable.PSVersion.ToString();checks=$checks;timings=$timings;model='not_run';system_install='disabled';desktop='isolated'}
$node = $null
function Assert([bool]$Condition,[string]$Message) { if (-not $Condition) { throw $Message } }
function Expect-Failure([scriptblock]$Action,[string]$Pattern) {
    try { & $Action | Out-Null } catch { if ($_.Exception.Message -match $Pattern) { return }; throw }
    throw "Expected failure: $Pattern"
}
function Invoke-TestAPI($Node,[string]$Path,[string]$Method='GET',$Body=$null) {
    $parameters = @{Uri=($Node.address+$Path);Method=$Method;WebSession=$Node.session;Headers=$Node.headers;TimeoutSec=10}
    if ($null -ne $Body) { $parameters.ContentType='application/json'; $parameters.Body=[Text.Encoding]::UTF8.GetBytes(($Body | ConvertTo-Json -Compress)) }
    return (Invoke-WebRequest @parameters).Content | ConvertFrom-Json
}
function Wait-TestNode {
    $deadline = [DateTime]::UtcNow.AddSeconds(30)
    while ([DateTime]::UtcNow -lt $deadline) {
        try {
            $runtime = Get-Content -LiteralPath (Join-Path $data 'runtime.json') -Raw | ConvertFrom-Json
            $result = @{process=(Get-Process -Id $runtime.pid);address=('http://'+$runtime.address);session=[Microsoft.PowerShell.Commands.WebRequestSession]::new();headers=@{Origin=('http://'+$runtime.address)}}
            $login = Invoke-TestAPI $result '/api/login' 'POST' @{token=[IO.File]::ReadAllText((Join-Path $data 'web-login.token'))}
            $result.headers['X-Asterhand-CSRF']=$login.csrf
            return $result
        } catch { Start-Sleep -Milliseconds 200 }
    }
    throw 'Isolated node did not become healthy.'
}
function Start-Process($FilePath,$ArgumentList,$WindowStyle,[switch]$Wait,[switch]$PassThru) {
    Assert (-not $Wait) 'Launcher must not wait for the whole node process tree.'
    return Microsoft.PowerShell.Management\Start-Process -FilePath $FilePath -ArgumentList (@($ArgumentList)+'--no-browser') -WindowStyle Hidden -PassThru
}
function Timed([string]$Name,[scriptblock]$Action) {
    $watch = [Diagnostics.Stopwatch]::StartNew()
    & $Action
    $timings.Add(@{name=$Name;seconds=[math]::Round($watch.Elapsed.TotalSeconds,3)})
}

# Preserve the installer except Known Folders and refusal to install system-wide
# prerequisites in a regression test. Real native-module loading remains enabled.
$tokens=$null; $errors=$null
$installerPath = Join-Path $repo 'install.ps1'
$ast = [Management.Automation.Language.Parser]::ParseFile($installerPath,[ref]$tokens,[ref]$errors)
$source = [IO.File]::ReadAllText($installerPath)
$replacements = @{
    'Get-InstallFolder'=('function Get-InstallFolder($Kind) { $path=Join-Path '''+$OutputDirectory.Replace("'","''")+''' $Kind; [IO.Directory]::CreateDirectory($path) | Out-Null; return $path }')
    'Initialize-VCRuntime'='function Initialize-VCRuntime($Cache,$Dependency) { if ([version](Get-InstalledVCRuntime) -lt [version]$Dependency.version) { throw ''Test requires an already installed VC++ Runtime.'' } }'
    'Initialize-WebView2'='function Initialize-WebView2($Cache,$Dependency) { if ([version](Get-InstalledWebView2) -lt [version]$Dependency.minimum_runtime_version) { throw ''Test requires an already installed WebView2 Runtime.'' } }'
}
foreach ($name in $replacements.Keys) {
    $definition = $ast.Find({param($n) $n -is [Management.Automation.Language.FunctionDefinitionAst] -and $n.Name -eq $name},$true)
    $source = $source.Replace($definition.Extent.Text,$replacements[$name])
}
$installer = Join-Path $OutputDirectory 'install.ps1'
[IO.File]::WriteAllText($installer,$source,[Text.UTF8Encoding]::new($true))
try {
    $null = & $installer -InitializeLocalCache -InstallDir $program -DataDir $data
    $runtime = Get-LocalInstalledRuntime $repo $cache $program $Go $PreparedPath
    $application = Get-Content -LiteralPath (Join-Path $ApplicationDirectory 'local-build.json') -Raw | ConvertFrom-Json
    $parameters = @{LocalBuildPath=$ApplicationDirectory;LocalRuntimePath=$runtime.directory;InstallDir=$program;DataDir=$data;DesktopShortcut='Create';SkipTailscale=$true;BeforeLocalActivation={}}
    Timed 'fresh-directory-activation' { & $installer @parameters }
    $record = Get-Content -LiteralPath (Join-Path $program 'installation.json') -Raw | ConvertFrom-Json
    Assert (-not (Test-Path (Join-Path $data 'config.json'))) 'Installer initialized node data.'
    Assert (Test-Path (Join-Path $OutputDirectory 'Desktop/Asterhand.lnk')) 'Isolated desktop shortcut missing.'
    $initial = Microsoft.PowerShell.Management\Start-Process -FilePath $record.executable -ArgumentList @('setup','--basic','--home',('"'+$data+'"'),'--web','127.0.0.1:0','--peer','127.0.0.1:0') -WindowStyle Hidden -PassThru -RedirectStandardOutput (Join-Path $OutputDirectory 'node.log') -RedirectStandardError (Join-Path $OutputDirectory 'node-error.log')
    $node = Wait-TestNode
    Assert ($node.process.Id -eq $initial.Id) 'Unexpected initial node.'
    $before = Invoke-TestAPI $node '/api/info'
    Assert ($before.source_sha256 -eq $application.source_sha256) 'Initial source mismatch.'
    $tokenHash = (Get-FileHash -LiteralPath (Join-Path $data 'web-login.token')).Hash
    [IO.File]::WriteAllText((Join-Path $data 'sentinel.txt'),'preserved')
    $checks.Add('real-directory-install-junction-native-load-basic-node-source-and-desktop')

    $rejected = $parameters.Clone()
    $rejected.BeforeLocalActivation = { throw 'Synthetic source changed before activation.' }
    Expect-Failure { & $installer @rejected -StopRunning } 'source changed'
    Assert (-not $node.process.HasExited) 'Source guard stopped old node.'
    $badApp = Join-Path $OutputDirectory 'damaged-app'
    Copy-BuildTree $ApplicationDirectory $badApp
    $rejected = $parameters.Clone()
    $rejected.LocalBuildPath = $badApp
    $receiptPath = Join-Path $badApp 'local-build.json'
    $originalReceipt = [IO.File]::ReadAllText($receiptPath)
    foreach ($field in @('source_sha256','version')) {
        $receipt = $originalReceipt | ConvertFrom-Json
        $receipt.$field = if ($field -eq 'source_sha256') { '1'*64 } else { '999.0.0' }
        Write-LocalRecord $receipt $receiptPath
        Expect-Failure { & $installer @rejected -StopRunning } 'identity does not match'
        Assert (-not $node.process.HasExited) 'Mismatched application identity stopped old node.'
    }
    [IO.File]::WriteAllText($receiptPath,$originalReceipt)
    [IO.File]::AppendAllText((Join-Path $badApp 'asterhand.exe'),'damage')
    Expect-Failure { & $installer @rejected -StopRunning } 'checksum mismatch'
    Assert (-not $node.process.HasExited) 'Bad candidate stopped old node.'
    $checks.Add('source-guard-and-corrupt-application-preserve-online-node')

    $old = $node.process
    $warm = Get-LocalInstalledRuntime $repo $cache $program $Go ''
    Assert ($warm.reused -and $warm.directory -eq $runtime.directory) 'Warm repeat failed runtime reuse.'
    $parameters.Remove('DataDir')
    $parameters.DesktopShortcut = 'Auto'
    Timed 'warm-online-reactivation' { & $installer @parameters -StopRunning -StartAfterInstall }
    $node = Wait-TestNode
    Assert ($old.HasExited -and $node.process.Id -ne $old.Id) 'Online update did not restart.'
    $after = Invoke-TestAPI $node '/api/info'
    Assert ($after.node_id -eq $before.node_id -and $after.source_sha256 -eq $application.source_sha256) 'Identity/source changed unexpectedly.'
    Assert ((Get-FileHash -LiteralPath (Join-Path $data 'web-login.token')).Hash -eq $tokenHash) 'Login token changed.'
    Assert ([IO.File]::ReadAllText((Join-Path $data 'sentinel.txt')) -eq 'preserved') 'Data changed.'
    $checks.Add('warm-zero-runtime-copy-online-restart-data-token-identity-and-source')

    # Explicit verified import exercises a real new generation without network.
    # The boundary suite separately proves repair bypasses prepared sources.
    $replacement = Get-LocalInstalledRuntime $repo $cache $program $Go $PreparedPath
    $oldVersion = (Get-Content -LiteralPath (Join-Path $program 'installation.json') -Raw | ConvertFrom-Json).executable
    $parameters.LocalRuntimePath = $replacement.directory
    & $installer @parameters -StopRunning -StartAfterInstall
    $node = Wait-TestNode
    $newVersion = (Get-Content -LiteralPath (Join-Path $program 'installation.json') -Raw | ConvertFrom-Json).executable
    Assert ($newVersion -ne $oldVersion) 'Runtime replacement reused old installed version.'
    Assert ((Get-Item -LiteralPath (Join-Path (Split-Path -Parent $oldVersion) 'runtime')).Target -eq $runtime.directory) 'Old runtime link was changed.'
    $checks.Add('same-source-new-generation-changes-version-without-rewriting-old-link')

    if ($PackageDirectory) {
        $manifest = Get-Content -LiteralPath (Join-Path $PackageDirectory 'release-manifest.json') -Raw | ConvertFrom-Json
        $package = $manifest.packages | Where-Object { $_.os -eq 'windows' -and $_.arch -eq 'amd64' }
        $full = @{Repository=$manifest.repository;PackagePath=(Join-Path $PackageDirectory $package.file);InstallDir=$program;DesktopShortcut='Auto';SkipTailscale=$true;StopRunning=$true;StartAfterInstall=$true;ExpectedSourceDigest=$manifest.source_sha256}
        & $installer @full
        $node = Wait-TestNode
        Assert ((Invoke-TestAPI $node '/api/info').node_id -eq $before.node_id) 'Fast to Full lost identity.'
        & $installer @parameters -StopRunning -StartAfterInstall
        $node = Wait-TestNode
        Assert ((Invoke-TestAPI $node '/api/info').node_id -eq $before.node_id) 'Full to Fast lost identity.'
        $checks.Add('updated-full-package-to-fast-interoperation-preserves-node')
    } else { $report.full_interoperation = 'skipped-package-not-provided' }
    $report.status='passed'
} catch { $report.status='failed'; $report.error=$_.Exception.Message; throw }
finally {
    if ($node -and -not $node.process.HasExited) {
        try { $null = Invoke-TestAPI $node '/api/stop' 'POST' @{}; if (-not $node.process.WaitForExit(15000)) { throw 'Test node failed normal shutdown.' } }
        catch { $report.cleanup_error=$_.Exception.Message; $report.status='failed' }
    }
    Write-LocalRecord $report (Join-Path $OutputDirectory 'local-candidate.json')
}
if ($report.status -ne 'passed') { throw 'Local candidate validation failed; inspect its report.' }
Write-Host "Local candidate lifecycle passed: $($checks.Count) groups."
