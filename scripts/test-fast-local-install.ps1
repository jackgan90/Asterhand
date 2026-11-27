#requires -Version 7.0
[CmdletBinding()]
param([Parameter(Mandatory)][string]$OutputDirectory)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$repo = Split-Path -Parent $PSScriptRoot
$OutputDirectory = $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($OutputDirectory)
if (Test-Path -LiteralPath $OutputDirectory) { throw 'Choose a new fast-install test directory.' }
[IO.Directory]::CreateDirectory($OutputDirectory) | Out-Null
foreach ($helper in @('dependency-tools.ps1','build-progress.ps1','local-build-cache.ps1','local-runtime-cache.ps1')) { . (Join-Path $PSScriptRoot $helper) }
$checks = [Collections.Generic.List[string]]::new()
$report = @{status='running';powershell=$PSVersionTable.PSVersion.ToString();checks=$checks;boundaries='real-fingerprints-receipts-files-and-junctions; synthetic-runtime-preparation; no-node-or-system-install'}
function Assert([bool]$Condition,[string]$Message) { if (-not $Condition) { throw $Message } }
function Expect-Failure([scriptblock]$Action,[string]$Pattern) {
    try { & $Action | Out-Null } catch { if ($_.Exception.Message -match $Pattern) { return }; throw }
    throw "Expected failure: $Pattern"
}
function Write-Text([string]$Path,[string]$Text='fixture') {
    [IO.Directory]::CreateDirectory((Split-Path -Parent $Path)) | Out-Null
    [IO.File]::WriteAllText($Path,$Text)
}
$tokens=$null; $parseErrors=$null
$ast = [Management.Automation.Language.Parser]::ParseFile((Join-Path $repo 'install.ps1'),[ref]$tokens,[ref]$parseErrors)
Assert ($parseErrors.Count -eq 0) 'Installer syntax failed.'
foreach ($name in @('Initialize-InstallNative','Assert-ChildPath','Publish-InstallDirectory','Assert-LocalInstallFile','Assert-LocalInstallFiles','Assert-LocalApplicationInventory','Read-LocalInstallCandidate','Assert-LocalVersion')) {
    $definition = $ast.Find({param($node) $node -is [Management.Automation.Language.FunctionDefinitionAst] -and $node.Name -eq $name},$true)
    . ([scriptblock]::Create($definition.Extent.Text))
}
try {
    $staging = Join-Path $OutputDirectory 'publish/staging'
    $published = Join-Path $OutputDirectory 'publish/version'
    $sharedRuntime = Join-Path $OutputDirectory 'publish/shared-runtime'
    $sharedReceipt = Join-Path $sharedRuntime 'runtime-receipt.json'
    $heldFile = Join-Path $staging 'application.exe'
    Write-Text $sharedReceipt 'shared runtime fixture'
    Write-Text $heldFile
    New-Item -ItemType Junction -Path (Join-Path $staging 'runtime') -Target $sharedRuntime | Out-Null
    $handle = [IO.File]::Open($heldFile, 'Open', 'Read', 'Read')
    try {
        $renameFailure = $null
        try { Publish-InstallDirectory $staging $published }
        catch { $renameFailure = $_.Exception.GetBaseException() }
        Assert ($null -ne $renameFailure -and $renameFailure.HResult -in @(0x80070005, 0x80070020, 0x80070021)) 'Publishing with an open descendant must report the original rename error.'
        Assert (-not (Test-Path -LiteralPath $published)) 'Failed rename left a partially published version.'
        Assert (([IO.File]::ReadAllText($sharedReceipt)) -eq 'shared runtime fixture') 'Failed publish modified the shared runtime.'
    } finally { $handle.Dispose() }
    Publish-InstallDirectory $staging $published
    $publishedRuntime = Get-Item -LiteralPath (Join-Path $published 'runtime') -Force
    Assert ($publishedRuntime.LinkType -eq 'Junction' -and $publishedRuntime.Target -eq $sharedRuntime) 'Publishing after release did not preserve the runtime junction.'
    Assert (([IO.File]::ReadAllText($sharedReceipt)) -eq 'shared runtime fixture') 'Publishing moved shared runtime files.'
    $checks.Add('contended-publish-preserves-shared-runtime-and-junction')

    $sourceRoot = Join-Path $OutputDirectory 'source'
    foreach ($directory in @('cmd','internal','web','plugins','packaging','scripts','tests')) { [IO.Directory]::CreateDirectory((Join-Path $sourceRoot $directory)) | Out-Null }
    foreach ($path in @('go.mod','go.sum','install.ps1','install.sh','install-local.cmd','LICENSE','docs/installation/quickstart.md','internal/prompt/default-chat.txt','web/app.js','cmd/main.go','plugins/tool.js')) { Write-Text (Join-Path $sourceRoot $path) }
    $baseline = Get-SourceFingerprint $sourceRoot
    foreach ($relative in @('internal/prompt/default-chat.txt','web/app.js','cmd/main.go','plugins/tool.js')) {
        $path = Join-Path $sourceRoot $relative
        Write-Text $path 'modified'
        Assert ((Get-SourceFingerprint $sourceRoot) -ne $baseline) "Fingerprint missed $relative"
        Write-Text $path
    }
    $added = Join-Path $sourceRoot 'internal/new-resource.data'
    Write-Text $added
    $withAdded = Get-SourceFingerprint $sourceRoot
    Assert ($withAdded -ne $baseline) 'Untracked input was ignored.'
    Rename-Item -LiteralPath $added -NewName 'renamed-resource.data'
    Assert ((Get-SourceFingerprint $sourceRoot) -ne $withAdded) 'Rename was ignored.'
    Remove-Item -LiteralPath (Join-Path $sourceRoot 'internal/renamed-resource.data')
    Assert ((Get-SourceFingerprint $sourceRoot) -eq $baseline) 'Deleted input was retained.'
    foreach ($relative in @('root.go','support/value.go','support/prompt.txt')) {
        $path = Join-Path $sourceRoot $relative
        Write-Text $path 'before'
        $before = Get-SourceFingerprint $sourceRoot
        Write-Text $path 'after'
        Assert ((Get-SourceFingerprint $sourceRoot) -ne $before) "New first-party input was ignored: $relative"
        Remove-Item -LiteralPath $path
        Assert ((Get-SourceFingerprint $sourceRoot) -eq $baseline) "Deleted first-party input was retained: $relative"
    }
    Write-Text (Join-Path $sourceRoot 'web/node_modules/generated.js')
    Write-Text (Join-Path $sourceRoot 'out/build/application.exe')
    Write-Text (Join-Path $sourceRoot '.git/index')
    foreach ($agentDir in @('.codex','.agents','.gemini','.claude','.cursor')) {
        Write-Text (Join-Path $sourceRoot "$agentDir/skills/example.md")
    }
    $agentLink = Join-Path $sourceRoot '.gemini/linked-skills'
    New-Item -ItemType Junction -Path $agentLink -Target (Join-Path $sourceRoot '.codex/skills') | Out-Null
    Assert ((Get-SourceFingerprint $sourceRoot) -eq $baseline) 'Generated dependencies or excluded agent directories entered source fingerprint.'
    $sourceLink = Join-Path $sourceRoot 'internal/reparse-input'
    New-Item -ItemType Junction -Path $sourceLink -Target (Join-Path $sourceRoot '.codex/skills') | Out-Null
    try { Expect-Failure { Get-SourceFingerprint $sourceRoot } 'reparse point' }
    finally { [IO.Directory]::Delete($sourceLink) }
    Write-Text (Join-Path $sourceRoot 'go.mod') 'replace example.test/module => ../external'
    Expect-Failure { Get-SourceFingerprint $sourceRoot } 'Local Go replacements'
    $checks.Add('real-input-fingerprint-txt-web-go-plugin-untracked-rename-delete-and-external-replace')

    $program = Join-Path $OutputDirectory 'program space'
    $data = Join-Path $OutputDirectory 'data space'
    $initialized = & (Join-Path $repo 'install.ps1') -InitializeLocalCache -InstallDir $program -DataDir $data
    Assert ($initialized -eq $program -and -not (Test-Path $data)) 'Initialization changed node data or returned wrong path.'
    $foreign = Join-Path $OutputDirectory 'foreign'
    Write-Text (Join-Path $foreign 'user-file')
    Expect-Failure { & (Join-Path $repo 'install.ps1') -InitializeLocalCache -InstallDir $foreign -DataDir $data } 'not owned'
    $alias = Join-Path $OutputDirectory 'program alias'
    New-Item -ItemType Junction -Path $alias -Target $program | Out-Null
    Expect-Failure { & (Join-Path $repo 'install.ps1') -InitializeLocalCache -InstallDir $program -DataDir (Join-Path $alias 'data') } 'separate|contain'
    $lock = [IO.File]::Open((Join-Path $program '.activation.lock'),[IO.FileMode]::OpenOrCreate,[IO.FileAccess]::ReadWrite,[IO.FileShare]::None)
    try { Expect-Failure { & (Join-Path $repo 'install.ps1') -InitializeLocalCache -InstallDir $program -DataDir $data } 'Another installation' }
    finally { $lock.Dispose() }
    $checks.Add('first-root-owned-before-cache-foreign-alias-overlap-and-cross-entry-lock-rejected')

    $catalog = Read-DependencyCatalog (Join-Path $repo 'packaging/dependencies.json')
    $preparedNode = Join-Path $OutputDirectory 'prepared/node'
    $preparedRuntime = Join-Path $OutputDirectory 'prepared/runtime'
    Write-Text (Join-Path $preparedNode 'node.exe')
    Write-Text (Join-Path $preparedRuntime 'package.json') '{}'
    Copy-Item -LiteralPath (Join-Path $repo 'packaging/runtime-lock.json') -Destination (Join-Path $preparedRuntime 'package-lock.json')
    foreach ($component in $catalog.components) {
        $root = Join-Path $preparedRuntime ('node_modules/'+$component.package)
        Write-Text (Join-Path $root 'package.json') ('{"main":"entry.js","version":"'+$component.version+'"}')
        Write-Text (Join-Path $root 'entry.js')
    }
    foreach ($native in $catalog.windows_native_modules) { Write-Text (Join-Path $preparedRuntime $native.Substring(8)) }
    $script:prepareFresh = [Collections.Generic.List[bool]]::new()
    function Get-LocalPreparedDependencies($Repository,$Workspace,$BuilderGo,$PreparedPath,[switch]$Fresh) {
        $script:prepareFresh.Add([bool]$Fresh)
        return @{node_distribution=$preparedNode;runtime_root=$preparedRuntime}
    }
    function Assert-PreparedDependencies($Repository,$NodeDistribution,$RuntimeRoot,$Go) { return $catalog }
    $workspace = Join-Path $OutputDirectory 'cache'
    [IO.Directory]::CreateDirectory($workspace) | Out-Null
    $first = Get-LocalInstalledRuntime $repo $workspace $program 'unused' ''
    $again = Get-LocalInstalledRuntime $repo $workspace $program 'unused' ''
    Assert ($again.reused -and $again.directory -eq $first.directory -and $script:prepareFresh.Count -eq 1) 'Warm runtime repeated preparation.'
    Write-Text (Join-Path $first.directory 'node/node.exe') 'corrupted'
    $repaired = Get-LocalInstalledRuntime $repo $workspace $program 'unused' ''
    Assert ($repaired.directory -ne $first.directory -and $script:prepareFresh[1]) 'Corrupt runtime did not force a fresh generation.'
    $explicitRepair = Get-LocalInstalledRuntime $repo $workspace $program 'unused' '' -Repair
    Assert ($script:prepareFresh[2] -and $explicitRepair.directory -ne $repaired.directory) 'Explicit repair reused prepared data.'
    $checks.Add('runtime-hit-zero-copy-key-corruption-fresh-repair-and-immutable-generations')

    $app = Join-Path $workspace 'app'
    foreach ($name in @('asterhand.exe','asterhand-mcp.exe','asterhand-cli.exe','AsterhandLauncher.exe','AsterhandInputHost.exe','packaging/start.ps1','packaging/configure-firewall.ps1')) { Write-Text (Join-Path $app $name) }
    foreach ($name in @('dependencies.json','runtime-lock.json')) { Copy-Item -LiteralPath (Join-Path $repo ('packaging/'+$name)) -Destination (Join-Path $app 'packaging') }
    $source = 'a'*64
    Write-Text (Join-Path $app 'input-host.json') ('{"schema":1,"version":"1.0.0","source_sha256":"'+$source+'"}')
    New-LocalApplicationReceipt $app $source $repaired.key
    Save-LocalApplication $workspace $app
    Assert ((Find-LocalApplication $workspace $source $repaired.key) -eq $app) 'Valid app cache missed.'
    $candidate1 = Read-LocalInstallCandidate $app $repaired.directory $program
    $candidate2 = Read-LocalInstallCandidate $app $explicitRepair.directory $program
    Assert ($candidate1.id -ne $candidate2.id) 'Same-source runtime repair reused installed version identity.'
    Write-Text (Join-Path $app 'extra.txt')
    Expect-Failure { Read-LocalInstallCandidate $app $repaired.directory $program } 'Unexpected application file'
    Remove-Item -LiteralPath (Join-Path $app 'extra.txt')
    $manifestPath = Join-Path $app 'local-build.json'
    $original = [IO.File]::ReadAllText($manifestPath)
    foreach ($field in @('source_sha256','version')) {
        $receipt = $original | ConvertFrom-Json
        $receipt.$field = if ($field -eq 'source_sha256') { 'b'*64 } else { '2.0.0' }
        Write-LocalRecord $receipt $manifestPath
        Expect-Failure { Read-LocalInstallCandidate $app $repaired.directory $program } 'identity does not match'
    }
    foreach ($fault in @('../escape','/absolute','packaging/../escape','bad:stream','bad\path','packaging./file')) {
        $receipt = $original | ConvertFrom-Json
        $receipt.files[0].path = $fault
        Write-LocalRecord $receipt $manifestPath
        Expect-Failure { Read-LocalInstallCandidate $app $repaired.directory $program } 'Invalid local receipt path'
    }
    [IO.File]::WriteAllText($manifestPath,$original)
    Write-Text (Join-Path $app 'asterhand.exe') 'broken'
    Assert ($null -eq (Find-LocalApplication $workspace $source $repaired.key)) 'Corrupted app cache was reused.'
    Expect-Failure { Read-LocalInstallCandidate $app $repaired.directory $program } 'checksum mismatch'
    Expect-Failure { & (Join-Path $repo 'install.ps1') -PackagePath 'unused' -BeforeLocalActivation {} -InstallDir $program -DataDir $data } 'require a local'
    Expect-Failure { & (Join-Path $PSScriptRoot 'install-local.ps1') -RepairDependencies -PreparedPath 'unused' } 'cannot be combined'
    $checks.Add('app-cache-hashes-exact-inventory-path-rejection-repair-version-identity-and-mode-boundaries')

    $encodingDir = Join-Path $OutputDirectory 'encoding'
    [IO.Directory]::CreateDirectory($encodingDir) | Out-Null
    $leafScript = Join-Path $encodingDir 'leaf.ps1'
    $middleScript = Join-Path $encodingDir 'middle.ps1'
    $outerScript = Join-Path $encodingDir 'outer.ps1'
    $transcriptPath = Join-Path $encodingDir 'nested.log'
    $progressPath = Join-Path $PSScriptRoot 'build-progress.ps1'
    $pwshExe = Join-Path $PSHOME 'pwsh.exe'
    Write-Text $leafScript @'
$utf8 = [Text.UTF8Encoding]::new($false)
$stdoutLine = "$([char]0x2714) $([char]0x2139) $([char]0x4E2D)$([char]0x6587) stdout`n"
$stderrLine = "$([char]0x2139) $([char]0x4E2D)$([char]0x6587) stderr`n"
$outStream = [Console]::OpenStandardOutput()
$outBytes = $utf8.GetBytes($stdoutLine)
$outStream.Write($outBytes, 0, $outBytes.Length)
$outStream.Flush()
$errStream = [Console]::OpenStandardError()
$errBytes = $utf8.GetBytes($stderrLine)
$errStream.Write($errBytes, 0, $errBytes.Length)
$errStream.Flush()
'@
    Write-Text $middleScript @"
. '$($progressPath.Replace("'","''"))'
Invoke-BuildProcess 'Leaf UTF-8 check' '$($pwshExe.Replace("'","''"))' @('-NoProfile','-File','$($leafScript.Replace("'","''"))')
"@
    Write-Text $outerScript @"
. '$($progressPath.Replace("'","''"))'
Start-Transcript -LiteralPath '$($transcriptPath.Replace("'","''"))' | Out-Null
try {
    Invoke-BuildProcess 'Nested UTF-8 check' '$($pwshExe.Replace("'","''"))' @('-NoProfile','-File','$($middleScript.Replace("'","''"))')
} finally { Stop-Transcript | Out-Null }
"@
    $cmdLine = "chcp 936 >nul && `"$pwshExe`" -NoProfile -File `"$outerScript`""
    & cmd.exe /d /c $cmdLine | Out-Null
    Assert ($LASTEXITCODE -eq 0) 'Nested UTF-8 build-progress probe failed.'
    $nestedLog = Get-Content -LiteralPath $transcriptPath -Raw -Encoding UTF8
    $expectedStdout = "$([char]0x2714) $([char]0x2139) $([char]0x4E2D)$([char]0x6587) stdout"
    $expectedStderr = "$([char]0x2139) $([char]0x4E2D)$([char]0x6587) stderr"
    Assert ($nestedLog.Contains($expectedStdout)) 'Nested Invoke-BuildProcess corrupted UTF-8 stdout under CP936.'
    Assert ($nestedLog.Contains($expectedStderr)) 'Nested Invoke-BuildProcess corrupted UTF-8 stderr under CP936.'
    $checks.Add('nested-build-progress-preserves-utf8-stdout-and-stderr-under-cp936')
    $report.status = 'passed'
} catch { $report.status='failed'; $report.error=$_.Exception.Message; throw }
finally { Write-LocalRecord $report (Join-Path $OutputDirectory 'fast-local-install.json') }
Write-Host "Fast local boundary checks passed: $($checks.Count) groups."
