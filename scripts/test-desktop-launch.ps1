#requires -Version 5.1
[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$NodeExecutable,
    [Parameter(Mandatory)][string]$LauncherExecutable,
    [Parameter(Mandatory)][string]$OutputDirectory
)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$NodeExecutable = $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($NodeExecutable)
$LauncherExecutable = $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($LauncherExecutable)
$OutputDirectory = $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($OutputDirectory)
if (Test-Path -LiteralPath $OutputDirectory) { throw 'Choose a new desktop launch test directory.' }
foreach ($path in @($NodeExecutable, $LauncherExecutable)) {
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) { throw "Missing candidate executable: $path" }
}
if ($OutputDirectory.Contains('"')) { throw 'The output directory must not contain quotes.' }
New-Item -ItemType Directory -Path $OutputDirectory | Out-Null
$unicode = [string][char]0x661f + [char]0x4f34
$program = Join-Path $OutputDirectory ($unicode + ' program')
$version = Join-Path $program 'versions\test'
$data = Join-Path $OutputDirectory ($unicode + ' data')
$logs = Join-Path $OutputDirectory 'process-logs'
New-Item -ItemType Directory -Path $version,$data,$logs -Force | Out-Null
$nodePath = Join-Path $version 'asterhand.exe'
$launcherPath = Join-Path $program 'AsterhandLauncher.exe'
Copy-Item -LiteralPath $NodeExecutable -Destination $nodePath
Copy-Item -LiteralPath $LauncherExecutable -Destination $launcherPath
Copy-Item -LiteralPath (Join-Path (Split-Path -Parent $PSScriptRoot) 'packaging\start.ps1') -Destination (Join-Path $program 'start.ps1')
@{schema=1;executable=$nodePath;data=$data;desktop_shortcut=$false} | ConvertTo-Json | Set-Content -LiteralPath (Join-Path $program 'installation.json') -Encoding UTF8
$runtimePath = Join-Path $data 'runtime.json'
$configPath = Join-Path $data 'config.json'
$checks = New-Object 'System.Collections.Generic.List[string]'
$tracked = New-Object Collections.ArrayList
$report = [ordered]@{
    status='running';created_utc=[DateTime]::UtcNow.ToString('o');windows=[Environment]::OSVersion.VersionString
    powershell=$PSVersionTable.PSVersion.ToString();node_sha256=(Get-FileHash -LiteralPath $nodePath).Hash.ToLowerInvariant()
    launcher_sha256=(Get-FileHash -LiteralPath $launcherPath).Hash.ToLowerInvariant();checks=$checks
    real_model='not_used_basic_node';two_machine_acceptance='not_run';desktop_and_startup='not_modified';browser='not_opened'
    tray='test_node_runs_real_tray_no_visual_assertion';session_shutdown='not_run';cleanup='pending'
}
$currentNode = $null
$blockedPort = $null

function Assert-Launch([bool]$Condition, [string]$Message) { if (-not $Condition) { throw $Message } }

function Register-TestProcess($Process, [string]$ExpectedExecutable) {
    $null = $Process.Handle
    $Process.Refresh()
    if ($Process.HasExited) { return }
    $live = Get-Process -Id $Process.Id -ErrorAction SilentlyContinue
    if ($null -eq $live) { return }
    $actualPath = $live.Path
    if ($actualPath -ne $ExpectedExecutable) {
        $Process.Refresh()
        if ($Process.HasExited) { return }
        throw "Refusing to track PID $($Process.Id): expected '$ExpectedExecutable', actual '$actualPath'."
    }
    $tracked.Add([pscustomobject]@{process=$Process;executable=$ExpectedExecutable;created=$Process.StartTime.ToFileTimeUtc()}) | Out-Null
}

function Start-TestProcess([string]$Executable, [string[]]$Arguments, [string]$Label) {
    $process = Start-Process -FilePath $Executable -ArgumentList $Arguments -WindowStyle Hidden -PassThru `
        -RedirectStandardOutput (Join-Path $logs ($Label + '.stdout.log')) -RedirectStandardError (Join-Path $logs ($Label + '.stderr.log'))
    Register-TestProcess $process $Executable
    return $process
}

function Wait-Launcher($Process, [bool]$Success) {
    if (-not $Process.WaitForExit(35000)) { throw 'The isolated launcher did not return within its startup deadline.' }
    $Process.Refresh()
    if ($Success -and $Process.ExitCode -ne 0) { throw 'The native launcher failed; inspect the isolated launcher.log and error.log.' }
    if (-not $Success -and $Process.ExitCode -eq 0) { throw 'The launcher accepted a node whose configured port was occupied.' }
}

function Invoke-NodeAPI($Node, [string]$Path, [string]$Method='GET', $Body=$null) {
    $arguments = @{Uri=($Node.address+$Path);Method=$Method;WebSession=$Node.session;Headers=$Node.headers;TimeoutSec=8;UseBasicParsing=$true}
    if ($null -ne $Body) {
        $arguments.ContentType = 'application/json'
        $arguments.Body = [Text.Encoding]::UTF8.GetBytes(($Body | ConvertTo-Json -Depth 20 -Compress))
    }
    return (Invoke-WebRequest @arguments).Content | ConvertFrom-Json
}

function Wait-TestNode($ExpectedProcess=$null) {
    $deadline = [DateTime]::UtcNow.AddSeconds(25)
    while ([DateTime]::UtcNow -lt $deadline) {
        if ($null -ne $ExpectedProcess -and $ExpectedProcess.HasExited) { throw 'The initialization node exited; inspect initialization.stderr.log.' }
        if (Test-Path -LiteralPath $runtimePath) {
            $runtime = Get-Content -LiteralPath $runtimePath -Raw | ConvertFrom-Json
            if ($null -ne $ExpectedProcess -and $runtime.pid -ne $ExpectedProcess.Id) { throw 'Initialization returned the wrong node process.' }
            $nodeProcess = Get-Process -Id $runtime.pid -ErrorAction Stop
            Register-TestProcess $nodeProcess $nodePath
            $node = [pscustomobject]@{process=$nodeProcess;address=('http://'+$runtime.address);session=(New-Object Microsoft.PowerShell.Commands.WebRequestSession);headers=@{Origin=('http://'+$runtime.address)}}
            $health = Invoke-NodeAPI $node '/health'
            $configuration = Get-Content -LiteralPath $configPath -Raw | ConvertFrom-Json
            Assert-Launch ($health.node_id -eq $configuration.node_id -and $health.pid -eq $nodeProcess.Id) 'Health identity does not match the test node.'
            $token = [IO.File]::ReadAllText((Join-Path $data 'web-login.token'))
            $login = Invoke-NodeAPI $node '/api/login' 'POST' @{token=$token}
            $token = $null
            $node.headers['X-Asterhand-CSRF'] = $login.csrf
            return $node
        }
        Start-Sleep -Milliseconds 100
    }
    throw 'The isolated node did not publish a healthy runtime record.'
}

function Test-PortReachable([int]$Port) {
    $client = New-Object Net.Sockets.TcpClient
    try {
        $connection = $client.ConnectAsync('127.0.0.1', $Port)
        return $connection.Wait(1000) -and $client.Connected
    } catch { return $false }
    finally { $client.Dispose() }
}

function Get-NodePorts($Node) {
    $ports = @(Get-NetTCPConnection -OwningProcess $Node.process.Id -State Listen -ErrorAction Stop | Select-Object -ExpandProperty LocalPort -Unique)
    Assert-Launch ($ports.Count -ge 2) 'Expected both local Web and peer listeners for the isolated node.'
    return $ports
}

function Assert-ServiceLock([bool]$Held) {
    $stream = $null
    $busy = $false
    try { $stream = [IO.File]::Open((Join-Path $data 'service.lock'), [IO.FileMode]::Open, [IO.FileAccess]::ReadWrite, [IO.FileShare]::None) }
    catch [IO.IOException] { $busy = $true }
    finally { if ($null -ne $stream) { $stream.Dispose() } }
    Assert-Launch ($busy -eq $Held) 'The node service lock has the wrong lifecycle state.'
}

function Count-TestNodes {
    $count = 0
    foreach ($process in @(Get-Process -Name 'asterhand' -ErrorAction SilentlyContinue)) {
        try { if ($process.Path -eq $nodePath -and -not $process.HasExited) { $count++ } } catch { continue }
    }
    return $count
}

function Stop-TestNode($Node, [int[]]$Ports) {
    if ($Node.process.HasExited) { return }
    $null = Invoke-NodeAPI $Node '/api/stop' 'POST' @{}
    if (-not $Node.process.WaitForExit(15000)) { throw 'The isolated node did not finish graceful shutdown.' }
    Assert-Launch (-not (Test-Path -LiteralPath $runtimePath)) 'Graceful shutdown left runtime.json behind.'
    Assert-ServiceLock $false
    foreach ($port in $Ports) { Assert-Launch (-not (Test-PortReachable $port)) "A stopped node listener remains open on port $port." }
}

function Assert-AppendOnlyLogs {
    foreach ($name in @('startup.log','error.log')) {
        $stream = [IO.File]::Open((Join-Path $data $name), [IO.FileMode]::Open, [IO.FileAccess]::Read, [IO.FileShare]::ReadWrite)
        $reader = New-Object IO.StreamReader($stream)
        try { $text = $reader.ReadToEnd() } finally { $reader.Dispose() }
        Assert-Launch ($text.StartsWith('desktop-launch-sentinel')) "Repeated startup truncated $name."
        Assert-Launch (-not $text.Contains('#login=')) "A login URL leaked into $name."
    }
}

function Stop-TrackedProcesses {
    $forced = 0
    foreach ($entry in $tracked) {
        $process = $entry.process
        if ($process.HasExited) { continue }
        # Both image path and process creation time must still match before a
        # failed test may terminate its own isolated process. Never kill by name.
        $live = Get-Process -Id $process.Id -ErrorAction SilentlyContinue
        if ($null -eq $live -or $live.Path -ne $entry.executable -or $live.StartTime.ToFileTimeUtc() -ne $entry.created) { continue }
        $process.Kill()
        $null = $process.WaitForExit(5000)
        $forced++
    }
    return $forced
}

try {
    $initialization = Start-TestProcess $nodePath @('start','--basic','--home',('"'+$data+'"'),'--web','127.0.0.1:0','--peer','127.0.0.1:0') 'initialization'
    $currentNode = Wait-TestNode $initialization
    $initialInfo = Invoke-NodeAPI $currentNode '/api/info'
    $report.version = $initialInfo.version
    $report.source_sha256 = $initialInfo.source_sha256
    Stop-TestNode $currentNode (Get-NodePorts $currentNode)
    $currentNode = $null
    $configuration = Get-Content -LiteralPath $configPath -Raw | ConvertFrom-Json
    $configuration.name = 'Desktop launcher isolated test'
    [IO.File]::WriteAllText($configPath, ($configuration | ConvertTo-Json -Depth 20), [Text.UTF8Encoding]::new($false))
    $expectedConfiguration = $configuration | ConvertTo-Json -Depth 20 -Compress
    $checks.Add('basic-node-initialization-and-authenticated-shutdown')

    foreach ($name in @('startup.log','error.log')) { [IO.File]::WriteAllText((Join-Path $data $name), "desktop-launch-sentinel`r`n") }
    $launchers = @()
    foreach ($index in 1..4) { $launchers += Start-TestProcess $launcherPath @('--quiet','--no-browser') ("concurrent-$index") }
    foreach ($launcher in $launchers) { Wait-Launcher $launcher $true }
    $currentNode = Wait-TestNode
    $firstNodePID = $currentNode.process.Id
    Assert-Launch ((Count-TestNodes) -eq 1) 'Concurrent launchers started more than one isolated node.'
    Assert-ServiceLock $true
    $ports = Get-NodePorts $currentNode
    $checks.Add('four-concurrent-native-launchers-one-node-and-one-service-lock')

    $status = Start-TestProcess $nodePath @('status','--home',('"'+$data+'"')) 'cli-status'
    Assert-Launch ($status.WaitForExit(8000) -and $status.ExitCode -eq 0) 'CLI status no longer recognizes the native-launched node.'
    Assert-Launch (([IO.File]::ReadAllText((Join-Path $logs 'cli-status.stdout.log'))).Trim() -eq 'running') 'CLI status returned an unexpected value.'
    & (Join-Path $program 'start.ps1') -NoBrowser
    $runtime = Get-Content -LiteralPath $runtimePath -Raw | ConvertFrom-Json
    Assert-Launch ($runtime.pid -eq $firstNodePID -and (Count-TestNodes) -eq 1) 'Script startup did not reuse the native node.'
    Assert-AppendOnlyLogs
    $checks.Add('cli-status-script-reuse-and-append-only-secret-free-logs')

    Stop-TestNode $currentNode $ports
    $currentNode = $null
    Assert-Launch ((Count-TestNodes) -eq 0) 'Node process remains after Web shutdown.'
    $checks.Add('web-stop-removes-runtime-and-releases-process-lock-and-listeners')

    $restart = Start-TestProcess $launcherPath @('--quiet','--no-browser') 'restart'
    Wait-Launcher $restart $true
    $currentNode = Wait-TestNode
    $info = Invoke-NodeAPI $currentNode '/api/info'
    $actualConfiguration = (Get-Content -LiteralPath $configPath -Raw | ConvertFrom-Json) | ConvertTo-Json -Depth 20 -Compress
    Assert-Launch ($info.node_id -eq $initialInfo.node_id -and $actualConfiguration -eq $expectedConfiguration) 'Restart changed node identity or configuration.'
    Assert-AppendOnlyLogs
    Stop-TestNode $currentNode (Get-NodePorts $currentNode)
    $currentNode = $null
    $checks.Add('native-restart-preserves-identity-configuration-and-logs')

    $blockedPort = New-Object Net.Sockets.TcpListener([Net.IPAddress]::Loopback, 0)
    $blockedPort.Start()
    $occupied = $blockedPort.LocalEndpoint.Port
    $configuration.web_listen = '127.0.0.1:' + $occupied
    [IO.File]::WriteAllText($configPath, ($configuration | ConvertTo-Json -Depth 20), [Text.UTF8Encoding]::new($false))
    foreach ($index in 1..2) {
        $failed = Start-TestProcess $launcherPath @('--quiet','--no-browser') ("occupied-port-$index")
        Wait-Launcher $failed $false
        Assert-Launch ((Count-TestNodes) -eq 0) 'Failed startup left an extra node process.'
    }
    Assert-ServiceLock $false
    Assert-Launch (-not (Test-Path -LiteralPath $runtimePath)) 'Failed startup published a runtime record.'
    Assert-Launch (Test-PortReachable $occupied) 'Failure handling interfered with the process owning the occupied port.'
    Assert-Launch (Test-Path -LiteralPath (Join-Path $data 'launcher.log')) 'Failed GUI startup produced no diagnostic log.'
    Assert-AppendOnlyLogs
    $blockedPort.Stop()
    $blockedPort = $null
    $checks.Add('occupied-port-fails-with-diagnostics-and-no-extra-node')

    $configuration.web_listen = '127.0.0.1:0'
    [IO.File]::WriteAllText($configPath, ($configuration | ConvertTo-Json -Depth 20), [Text.UTF8Encoding]::new($false))
    $recovery = Start-TestProcess $launcherPath @('--quiet','--no-browser') 'after-failure'
    Wait-Launcher $recovery $true
    $currentNode = Wait-TestNode
    $info = Invoke-NodeAPI $currentNode '/api/info'
    Assert-Launch ($info.node_id -eq $initialInfo.node_id -and (Count-TestNodes) -eq 1) 'Recovery after a failed launch changed identity or duplicated the node.'
    Assert-AppendOnlyLogs
    Stop-TestNode $currentNode (Get-NodePorts $currentNode)
    $currentNode = $null
    $checks.Add('retry-after-port-recovery-reuses-identity-and-cleans-up')
    $report.status = 'passed'
} catch {
    $report.status = 'failed'
    $report.error = $_.Exception.Message
    throw
} finally {
    if ($null -ne $blockedPort) { $blockedPort.Stop() }
    if ($null -ne $currentNode -and -not $currentNode.process.HasExited) {
        try { $null = Invoke-NodeAPI $currentNode '/api/stop' 'POST' @{}; $null = $currentNode.process.WaitForExit(10000) } catch {}
    }
    foreach ($orphan in @(Get-Process -Name 'asterhand' -ErrorAction SilentlyContinue)) {
        if ($orphan.Path -eq $nodePath) { Register-TestProcess $orphan $nodePath }
    }
    $forced = Stop-TrackedProcesses
    $report.cleanup = if ($forced) { 'terminated-owned-test-processes-after-failure' } else { 'graceful-no-processes-left' }
    $report.forced_test_processes = $forced
    $report | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath (Join-Path $OutputDirectory 'desktop-launch.json') -Encoding UTF8
}
Write-Host "Native desktop lifecycle passed ($($checks.Count) checks)."
