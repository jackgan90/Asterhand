#requires -Version 5.1
[CmdletBinding()]
param([Parameter(Mandatory)][string]$OutputDirectory)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$OutputDirectory = $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($OutputDirectory)
if (Test-Path -LiteralPath $OutputDirectory) { throw 'Choose a new desktop installer test directory.' }
New-Item -ItemType Directory -Path $OutputDirectory | Out-Null
$repo = Split-Path -Parent $PSScriptRoot
$tokens = $null
$parseErrors = $null
$ast = [Management.Automation.Language.Parser]::ParseFile((Join-Path $repo 'install.ps1'), [ref]$tokens, [ref]$parseErrors)
if ($parseErrors.Count) { throw 'Installer syntax is invalid.' }
$functionNames = @('Initialize-InstallNative','Get-InstallShortcut','Test-OwnedShortcut','Select-DesktopShortcut','Set-InstallShortcut',
    'Update-InstallShortcuts','Get-LaunchMutexName','Assert-NoPendingLaunch','Enter-InstallActivation','Exit-InstallActivation',
    'Publish-InstallFile','Write-InstallRecord','Assert-SeparateDirectories')
foreach ($name in $functionNames) {
    $definitions = @($ast.FindAll({param($node) $node -is [Management.Automation.Language.FunctionDefinitionAst] -and $node.Name -eq $name}, $true))
    if ($definitions.Count -ne 1) { throw "Missing installer function: $name" }
    . ([scriptblock]::Create($definitions[0].Extent.Text))
}
$checks = New-Object 'System.Collections.Generic.List[string]'
$report = [ordered]@{status='running';powershell=$PSVersionTable.PSVersion.ToString();checks=$checks;desktop='isolated-known-folder-substitute';system_changes='none'}
$unicode = [string][char]0x661f + [char]0x4f34
$program = Join-Path $OutputDirectory ($unicode + ' program')
$data = Join-Path $OutputDirectory ($unicode + ' data')
$desktop = Join-Path $OutputDirectory ('Redirected Cloud\' + $unicode + ' Desktop')
$startup = Join-Path $OutputDirectory 'Current user startup'
New-Item -ItemType Directory -Path $program,$data,$desktop,$startup -Force | Out-Null
$launcher = Join-Path $program 'AsterhandLauncher.exe'
[IO.File]::WriteAllText($launcher, 'shortcut target fixture')
$desktopLink = Join-Path $desktop 'Asterhand.lnk'
$startupLink = Join-Path $startup 'Asterhand.lnk'
$script:interactive = $false
$script:answer = ''
function Get-InstallFolder([string]$Kind) { if ($Kind -eq 'Desktop') { return $desktop }; return $startup }
function Test-InstallerInteractive { return $script:interactive }
function Read-Host($Prompt) { return $script:answer }
function Assert([bool]$Condition, [string]$Message) { if (-not $Condition) { throw $Message } }
function Expect-Failure([scriptblock]$Action, [string]$Pattern) {
    try { & $Action | Out-Null } catch { if ($_.Exception.Message -match $Pattern) { return }; throw }
    throw "Expected failure: $Pattern"
}

try {
    Assert (-not (Select-DesktopShortcut 'Auto' $true $false $false)) 'Noninteractive first install must skip.'
    Assert (Select-DesktopShortcut 'Create' $true $false $false) 'Explicit create ignored.'
    Assert (-not (Select-DesktopShortcut 'Skip' $true $true $true)) 'Explicit skip ignored.'
    Assert (Select-DesktopShortcut 'Auto' $true $true $true) 'Upgrade lost existing shortcut.'
    Assert (-not (Select-DesktopShortcut 'Auto' $true $true $false)) 'Upgrade resurrected deleted shortcut.'
    Assert (Select-DesktopShortcut 'Keep' $true $false $true) 'Keep lost actual shortcut.'
    Assert (-not (Select-DesktopShortcut 'Keep' $true $false $false)) 'Keep created absent shortcut.'
    $script:interactive = $true
    Assert (Select-DesktopShortcut 'Auto' $true $false $false) 'Interactive default must create.'
    $script:answer = 'n'
    Assert (-not (Select-DesktopShortcut 'Auto' $true $false $false)) 'Interactive decline ignored.'
    $script:answer = 'wrong'
    Expect-Failure { Select-DesktopShortcut 'Auto' $true $false $false } 'Choose Y or N'
    $script:interactive = $false
    Assert (-not (Select-DesktopShortcut 'Auto' $false $false $false)) 'Legacy package must keep script startup.'
    Expect-Failure { Select-DesktopShortcut 'Create' $false $false $false } 'older package'
    $checks.Add('choice-first-upgrade-manual-removal-interactive-and-legacy')

    Assert-SeparateDirectories $program $data
    Expect-Failure { Assert-SeparateDirectories $program (Join-Path $program 'data') } 'must not contain'
    Expect-Failure { Assert-SeparateDirectories (Join-Path $data 'program') $data } 'must not contain'
    Expect-Failure { Assert-SeparateDirectories $program ($program.ToUpperInvariant()+'\') } 'must not contain'
    $checks.Add('program-data-overlap-and-case-alias-rejected')

    Update-InstallShortcuts $program $true $true $false
    $link = Get-InstallShortcut $desktopLink
    Assert ($link.TargetPath -eq $launcher -and $link.Arguments -eq '') 'Desktop must target the native launcher without arguments.'
    Assert ($link.IconLocation -eq ($launcher+',0') -and $link.WorkingDirectory -eq $program) 'Desktop icon or working directory is wrong.'
    Assert (-not (Test-Path -LiteralPath $startupLink)) 'Desktop choice enabled login startup.'
    Assert (Test-OwnedShortcut $desktopLink $program) 'Owned Unicode shortcut was not recognized.'
    $checks.Add('redirected-unicode-desktop-stable-target-and-icon')

    $oldStartup = Get-InstallShortcut $startupLink
    $oldStartup.TargetPath = Join-Path $env:SystemRoot 'System32\WindowsPowerShell\v1.0\powershell.exe'
    $oldStartup.Arguments = '-NoProfile -WindowStyle Hidden -File "' + (Join-Path $program 'start.ps1') + '" -NoBrowser'
    $oldStartup.Save()
    Assert (Test-OwnedShortcut $startupLink $program) 'Legacy startup not recognized.'
    Update-InstallShortcuts $program $true $true $false
    $link = Get-InstallShortcut $startupLink
    Assert ($link.TargetPath -eq $launcher -and $link.Arguments -eq '--no-browser') 'Upgrade did not migrate owned login startup.'
    Update-InstallShortcuts $program $false $true $false
    Assert (-not (Test-Path -LiteralPath $desktopLink)) 'Skip did not remove owned desktop shortcut.'
    Assert (Test-Path -LiteralPath $startupLink) 'Desktop skip disabled independent login startup.'
    $checks.Add('legacy-startup-migration-and-independent-desktop-skip')

    $foreignProgram = Join-Path $OutputDirectory 'other installation'
    New-Item -ItemType Directory -Path $foreignProgram | Out-Null
    $foreign = Get-InstallShortcut $desktopLink
    $foreign.TargetPath = Join-Path $foreignProgram 'AsterhandLauncher.exe'
    $foreign.Save()
    $foreignDigest = (Get-FileHash -LiteralPath $desktopLink).Hash
    Update-InstallShortcuts $program $false $true $false
    Assert ((Get-FileHash -LiteralPath $desktopLink).Hash -eq $foreignDigest) 'Skip changed an unrelated shortcut.'
    Expect-Failure { Update-InstallShortcuts $program $true $true $false } 'another installation'
    Assert ((Get-FileHash -LiteralPath $desktopLink).Hash -eq $foreignDigest) 'Create overwrote an unrelated shortcut.'
    $checks.Add('foreign-shortcuts-preserved-and-conflicts-rejected')

    $guard = Enter-InstallActivation @($data, ($data.ToUpperInvariant()+'\'))
    try {
        Assert ($guard.files.Count -eq 1 -and $guard.mutexes.Count -eq 1) 'Canonical aliases acquired multiple service locks.'
        Expect-Failure { [IO.File]::Open((Join-Path $data 'service.lock'), [IO.FileMode]::Open, [IO.FileAccess]::ReadWrite, [IO.FileShare]::None) } 'another process'
    } finally { Exit-InstallActivation $guard }
    Assert (@(Get-ChildItem -LiteralPath $data -Force).Count -eq 0) 'Installation initialized or polluted fresh node data.'
    $checks.Add('activation-lock-case-alias-and-fresh-data-preserved')

    $junction = Join-Path $OutputDirectory 'data junction'
    New-Item -ItemType Junction -Path $junction -Target $data | Out-Null
    Assert ((Get-LaunchMutexName $junction) -eq (Get-LaunchMutexName $data)) 'Junction alias uses a different launch mutex.'
    $guard = Enter-InstallActivation @($junction, $data)
    try { Assert ($guard.files.Count -eq 1) 'Junction alias acquired multiple service locks.' }
    finally { Exit-InstallActivation $guard }
    $checks.Add('junction-alias-shares-canonical-launch-mutex')

    $lockPath = Join-Path $data 'service.lock'
    $service = [IO.File]::Open($lockPath, [IO.FileMode]::CreateNew, [IO.FileAccess]::ReadWrite, [IO.FileShare]::None)
    try { Expect-Failure { Enter-InstallActivation @($data) } 'Stop the node' }
    finally { $service.Dispose() }
    $guard = Enter-InstallActivation @($data)
    Exit-InstallActivation $guard
    Assert (Test-Path -LiteralPath $lockPath) 'Existing service lock was deleted.'
    $checks.Add('running-node-rejected-and-failed-activation-releases-mutex')

    $process = [Diagnostics.Process]::GetCurrentProcess()
    $pending = @{pid=$process.Id;created=$process.StartTime.ToFileTimeUtc();executable=$process.MainModule.FileName}
    $pendingPath = Join-Path $data 'launch-state.json'
    $pending | ConvertTo-Json | Set-Content -LiteralPath $pendingPath -Encoding UTF8
    Expect-Failure { Enter-InstallActivation @($data) } 'still running or starting'
    $pending.created++
    $pending | ConvertTo-Json | Set-Content -LiteralPath $pendingPath -Encoding UTF8
    $guard = Enter-InstallActivation @($data)
    Exit-InstallActivation $guard
    '{"pid":1}' | Set-Content -LiteralPath $pendingPath -Encoding UTF8
    Expect-Failure { Enter-InstallActivation @($data) } 'Cannot safely activate'
    Remove-Item -LiteralPath $pendingPath
    $checks.Add('pending-launch-process-identity-stale-pid-and-invalid-record')

    $recordPath = Join-Path $program 'installation.json'
    $record = @{schema=1;version='1.0.0';executable=(Join-Path $program 'versions\first\asterhand.exe');data=$data;desktop_shortcut=$true}
    Write-InstallRecord $record $recordPath
    $record.executable = Join-Path $program 'versions\second\asterhand.exe'
    $record.version = '1.0.1'
    Write-InstallRecord $record $recordPath
    $read = Get-Content -LiteralPath $recordPath -Raw | ConvertFrom-Json
    Assert ($read.schema -eq 1 -and $read.version -eq '1.0.1' -and $read.data -eq $data -and $read.desktop_shortcut) 'Atomic installation record failed.'
    $source = Join-Path $OutputDirectory 'launcher-candidate'
    [IO.File]::WriteAllText($source, 'replacement fixture')
    Publish-InstallFile $source $launcher
    Assert ([IO.File]::ReadAllText($launcher) -eq 'replacement fixture') 'Launcher replacement failed.'
    Assert (@(Get-ChildItem -LiteralPath $program -Filter '*.tmp').Count -eq 0) 'Activation left temporary files.'
    $checks.Add('atomic-launcher-and-record-replacement-preserves-data')

    $launcherInvocation = [pscustomobject]@{ExitCode=0;Arguments=@()}
    function Start-Process($FilePath, $ArgumentList, $WindowStyle, [switch]$Wait, [switch]$PassThru) {
        Assert ($FilePath -eq $launcher -and -not $Wait -and $PassThru -and $WindowStyle -eq 'Hidden') 'Script must launch without waiting for the node process tree.'
        $launcherInvocation.Arguments = @($ArgumentList)
        $result = [pscustomobject]@{ExitCode=$launcherInvocation.ExitCode}
        $result | Add-Member ScriptMethod WaitForExit { param($Timeout) return $Timeout -eq 45000 }
        $result | Add-Member ScriptMethod Refresh {}
        $result | Add-Member ScriptMethod Dispose {}
        return $result
    }
    $start = Join-Path $program 'start.ps1'
    Copy-Item -LiteralPath (Join-Path $repo 'packaging\start.ps1') -Destination $start
    & $start -NoBrowser
    Assert ('--quiet' -in $launcherInvocation.Arguments -and '--no-browser' -in $launcherInvocation.Arguments) 'Script lost NoBrowser or quiet mode.'
    & $start
    Assert ('--quiet' -in $launcherInvocation.Arguments -and '--no-browser' -notin $launcherInvocation.Arguments) 'Normal startup must open the browser.'
    $launcherInvocation.ExitCode = 1
    Expect-Failure { & $start } 'launcher.log and error.log'
    $checks.Add('start-script-forwarding-wait-and-error-reporting')
    $report.status = 'passed'
} catch {
    $report.status = 'failed'
    $report.error = $_.Exception.Message
    throw
} finally {
    $report | ConvertTo-Json -Depth 6 | Set-Content -LiteralPath (Join-Path $OutputDirectory 'desktop-installer.json') -Encoding UTF8
}
Write-Host "Desktop installer regression passed ($($checks.Count) checks)."
