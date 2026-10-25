#requires -Version 5.1
[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$PackageDirectory,
    [Parameter(Mandatory)][string]$OutputDirectory
)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$repo = Split-Path -Parent $PSScriptRoot
$PackageDirectory = $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($PackageDirectory)
$OutputDirectory = $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($OutputDirectory)
if (Test-Path -LiteralPath $OutputDirectory) { throw 'Choose a new lifecycle test directory.' }
New-Item -ItemType Directory -Path $OutputDirectory | Out-Null
$program = Join-Path $OutputDirectory 'program space'
$data = Join-Path $OutputDirectory 'custom data'
$desktop = Join-Path $OutputDirectory 'desktop'
$startup = Join-Path $OutputDirectory 'startup'
New-Item -ItemType Directory -Path $desktop,$startup | Out-Null
$fixture = Join-Path $OutputDirectory 'install.ps1'
$tokens = $null; $parseErrors = $null
$ast = [Management.Automation.Language.Parser]::ParseFile((Join-Path $repo 'install.ps1'), [ref]$tokens, [ref]$parseErrors)
if ($parseErrors.Count) { throw 'Installer syntax is invalid.' }
$folder = $ast.Find({param($node) $node -is [Management.Automation.Language.FunctionDefinitionAst] -and $node.Name -eq 'Get-InstallFolder'}, $true)
$replacement = 'function Get-InstallFolder([string]$Kind) { if ($Kind -eq ''Desktop'') { return ''' + $desktop.Replace("'", "''") + ''' }; return ''' + $startup.Replace("'", "''") + ''' }'
# Only Known Folders are substituted; the complete installer is otherwise unchanged.
$source = [IO.File]::ReadAllText((Join-Path $repo 'install.ps1')).Replace($folder.Extent.Text, $replacement)
[IO.File]::WriteAllText($fixture, $source, (New-Object Text.UTF8Encoding($true)))
$manifest = Get-Content -LiteralPath (Join-Path $PackageDirectory 'release-manifest.json') -Raw | ConvertFrom-Json
$package = $manifest.packages | Where-Object { $_.os -eq 'windows' -and $_.arch -eq 'amd64' }
$parameters = @{PackagePath=(Join-Path $PackageDirectory $package.file);InstallDir=$program;SkipTailscale=$true}
$checks = New-Object 'System.Collections.Generic.List[string]'
$report = [ordered]@{status='running';powershell=$PSVersionTable.PSVersion.ToString();checks=$checks;model='not_run';system_dependencies='existing-vc-runtime-tailscale-skipped'}
$lifecycleTestState = @{suppressStart=$true;startCalls=0}
$node = $null
$initial = $null

function Assert([bool]$Condition, [string]$Message) { if (-not $Condition) { throw $Message } }
function Expect-Failure([scriptblock]$Action, [string]$Pattern) {
    try { & $Action | Out-Null } catch { if ($_.Exception.Message -match $Pattern) { return }; throw }
    throw "Expected failure: $Pattern"
}
function Read-Installation { return Get-Content -LiteralPath (Join-Path $program 'installation.json') -Raw | ConvertFrom-Json }
function Start-Process($FilePath, $ArgumentList, $WindowStyle, [switch]$Wait, [switch]$PassThru) {
    $lifecycleTestState.startCalls++
    $record = Read-Installation
    # Startup must happen after activation releases both installation and service locks.
    foreach ($path in @((Join-Path $program '.activation.lock'), (Join-Path $record.data 'service.lock'))) {
        $stream = [IO.File]::Open($path, [IO.FileMode]::OpenOrCreate, [IO.FileAccess]::ReadWrite, [IO.FileShare]::None)
        $stream.Dispose()
    }
    Assert (-not $Wait) 'Startup must not wait for the long-lived node process tree.'
    if ($lifecycleTestState.suppressStart) {
        $result = [pscustomobject]@{ExitCode=0}
        $result | Add-Member ScriptMethod WaitForExit { param($Timeout) return $true }
        $result | Add-Member ScriptMethod Refresh {}
        $result | Add-Member ScriptMethod Dispose {}
        return $result
    }
    $process = Microsoft.PowerShell.Management\Start-Process -FilePath $FilePath -ArgumentList (@($ArgumentList) + '--no-browser') -WindowStyle Hidden -PassThru
    return $process
}
function Invoke-NodeAPI($Node, [string]$Path, [string]$Method='GET', $Body=$null) {
    $args = @{Uri=($Node.address+$Path);Method=$Method;WebSession=$Node.session;Headers=$Node.headers;TimeoutSec=10;UseBasicParsing=$true}
    if ($null -ne $Body) { $args.ContentType='application/json'; $args.Body=[Text.Encoding]::UTF8.GetBytes(($Body | ConvertTo-Json -Compress)) }
    return (Invoke-WebRequest @args).Content | ConvertFrom-Json
}
function Read-Node {
    $runtime = Get-Content -LiteralPath (Join-Path $data 'runtime.json') -Raw | ConvertFrom-Json
    $result = @{process=(Get-Process -Id $runtime.pid);address=('http://'+$runtime.address);session=(New-Object Microsoft.PowerShell.Commands.WebRequestSession);headers=@{Origin=('http://'+$runtime.address)}}
    $token = [IO.File]::ReadAllText((Join-Path $data 'web-login.token'))
    $login = Invoke-NodeAPI $result '/api/login' 'POST' @{token=$token}
    $result.headers['X-Asterhand-CSRF'] = $login.csrf
    return $result
}
function Wait-Node {
    $deadline = [DateTime]::UtcNow.AddSeconds(25)
    $failure = 'No runtime record.'
    while ([DateTime]::UtcNow -lt $deadline) {
        try { return Read-Node } catch { $failure=$_.Exception.Message; Start-Sleep -Milliseconds 200 }
    }
    throw "The isolated test node did not become reachable: $failure"
}

try {
    & $fixture @parameters -DataDir $data -DesktopShortcut Create -StopRunning -StartAfterInstall
    Assert ($lifecycleTestState.startCalls -eq 1) 'First installation did not dispatch startup.'
    Assert (-not (Test-Path -LiteralPath (Join-Path $data 'config.json'))) 'Installation initialized node data before startup.'
    Assert (Test-Path -LiteralPath (Join-Path $desktop 'Asterhand.lnk')) 'First explicit desktop choice failed.'
    $checks.Add('fresh-install-explicit-desktop-and-start-after-lock-release')

    $record = Read-Installation
    $initial = Microsoft.PowerShell.Management\Start-Process -FilePath $record.executable -ArgumentList @('setup','--basic','--home',('"'+$data+'"'),'--web','127.0.0.1:0','--peer','127.0.0.1:0') -WindowStyle Hidden -PassThru -RedirectStandardOutput (Join-Path $OutputDirectory 'node.log') -RedirectStandardError (Join-Path $OutputDirectory 'node-error.log')
    $node = Wait-Node
    Assert ($node.process.Id -eq $initial.Id) 'Unexpected node process.'
    $identity = (Invoke-NodeAPI $node '/api/info').node_id
    $tokenHash = (Get-FileHash -LiteralPath (Join-Path $data 'web-login.token')).Hash
    [IO.File]::WriteAllText((Join-Path $data 'sentinel.txt'), 'preserved')
    $lifecycleTestState.suppressStart = $false

    $bad = Join-Path $OutputDirectory 'bad.zip'
    [IO.File]::WriteAllText($bad, 'invalid package')
    $badParameters = $parameters.Clone()
    $badParameters.PackagePath = $bad
    $badParameters.ManifestPath = Join-Path $PackageDirectory 'release-manifest.json'
    Expect-Failure { & $fixture @badParameters -StopRunning -StartAfterInstall } 'checksum mismatch'
    Assert (-not $node.process.HasExited) 'Bad package stopped the installed node.'
    Expect-Failure { & $fixture @parameters } 'running|Stop the node'
    Assert (-not $node.process.HasExited) 'Default installation stopped the node.'
    $checks.Add('invalid-package-and-default-install-preserve-online-node')

    $runtimePath = Join-Path $data 'runtime.json'
    $runtimeBytes = [IO.File]::ReadAllBytes($runtimePath)
    try {
        $runtime = Get-Content -LiteralPath $runtimePath -Raw | ConvertFrom-Json
        $runtime.address = '192.0.2.1:80'
        $runtime | ConvertTo-Json | Set-Content -LiteralPath $runtimePath -Encoding UTF8
        Expect-Failure { & $fixture @parameters -StopRunning } 'loopback'
        $runtime.pid = $PID
        $runtime | ConvertTo-Json | Set-Content -LiteralPath $runtimePath -Encoding UTF8
        Expect-Failure { & $fixture @parameters -StopRunning } 'outside|not owned'
    } finally { [IO.File]::WriteAllBytes($runtimePath, $runtimeBytes) }
    Assert (-not $node.process.HasExited) 'Identity rejection stopped the target node.'
    $checks.Add('non-loopback-and-unrelated-process-rejected')

    $oldProcess = $node.process
    & $fixture @parameters -StopRunning -StartAfterInstall
    $node = Wait-Node
    Assert ($oldProcess.HasExited -and $node.process.Id -ne $oldProcess.Id) 'Online installation did not stop and restart the node.'
    Assert ((Read-Installation).data -eq $data) 'Overwrite installation lost the custom data directory.'
    Assert ((Invoke-NodeAPI $node '/api/info').node_id -eq $identity) 'Overwrite installation lost node identity.'
    Assert ((Get-FileHash -LiteralPath (Join-Path $data 'web-login.token')).Hash -eq $tokenHash) 'Overwrite installation changed the login token.'
    Assert ([IO.File]::ReadAllText((Join-Path $data 'sentinel.txt')) -eq 'preserved') 'Overwrite installation changed user data.'
    Assert (Test-Path -LiteralPath (Join-Path $desktop 'Asterhand.lnk')) 'Auto removed an existing desktop shortcut.'
    $checks.Add('online-stop-restart-custom-home-identity-token-data-and-desktop-preserved')

    Remove-Item -LiteralPath (Join-Path $desktop 'Asterhand.lnk')
    $null = Invoke-NodeAPI $node '/api/stop' 'POST' @{}
    Assert ($node.process.WaitForExit(15000)) 'Isolated node did not stop.'
    & $fixture @parameters -StopRunning -StartAfterInstall
    $node = Wait-Node
    Assert (-not (Test-Path -LiteralPath (Join-Path $desktop 'Asterhand.lnk'))) 'Auto recreated a manually deleted shortcut.'
    $checks.Add('offline-repeat-restarts-and-preserves-absent-desktop')
    Expect-Failure { & $fixture @parameters -StartAfterInstall -NoStart } 'cannot be combined'
    $checks.Add('conflicting-start-options-rejected')
    $report.status = 'passed'
} catch { $report.status='failed'; $report.error=$_.Exception.Message; throw }
finally {
    if ($node -and -not $node.process.HasExited) {
        try { $null = Invoke-NodeAPI $node '/api/stop' 'POST' @{}; $null = $node.process.WaitForExit(15000) } catch {}
    }
    # A test-owned initialization process may be healthy but unreachable in a
    # restricted environment. It has no model or user work and must not leak.
    if ($initial -and -not $initial.HasExited) { $initial.Kill(); $null = $initial.WaitForExit(5000) }
    $report | ConvertTo-Json -Depth 6 | Set-Content -LiteralPath (Join-Path $OutputDirectory 'lifecycle.json') -Encoding UTF8
}
Write-Host "Installer lifecycle passed: $($checks.Count) checks."
