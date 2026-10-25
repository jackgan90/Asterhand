#requires -Version 5.1
[CmdletBinding()]
param([Parameter(Mandatory)][string]$OutputDirectory)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$repo = Split-Path -Parent $PSScriptRoot
$OutputDirectory = $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($OutputDirectory)
if (Test-Path -LiteralPath $OutputDirectory) { throw 'Choose a new bootstrap test directory.' }
New-Item -ItemType Directory -Path $OutputDirectory | Out-Null
$checks = New-Object 'System.Collections.Generic.List[string]'
$bootstrapTestState = @{downloads=0;invocations=(New-Object Collections.ArrayList);downloadFailure=$false;installerFailure=$false}
$fixture = @'
[CmdletBinding()]
param([string]$Version, [string]$Repository = 'jackgan90/Asterhand', [string]$ReleaseTag)
$bootstrapTestState.invocations.Add(@{version=$Version;repository=$Repository;tag=$ReleaseTag}) | Out-Null
if ($bootstrapTestState.installerFailure) { throw 'Synthetic installer failure.' }
'@
function Assert([bool]$Condition, [string]$Message) { if (-not $Condition) { throw $Message } }
function Expect-Failure([scriptblock]$Action, [string]$Pattern) {
    try { & $Action | Out-Null } catch { if ($_.Exception.Message -match $Pattern) { return }; throw }
    throw "Expected failure: $Pattern"
}
function Invoke-RestMethod {
    [CmdletBinding()]
    param([string]$Uri)
    Assert ($Uri -eq 'https://raw.githubusercontent.com/jackgan90/Asterhand/main/install.ps1') 'Bootstrap URL changed.'
    Assert ($PSBoundParameters['ErrorAction'] -eq 'Stop') 'Download must explicitly stop on failure.'
    $bootstrapTestState.downloads++
    if ($bootstrapTestState.downloadFailure) { Write-Error 'Synthetic download failure.'; return }
    return $fixture
}
$report = [ordered]@{status='running';powershell=$PSVersionTable.PSVersion.ToString();checks=$checks;network='mocked';installation='fixture-script-no-real-install'}
try {
    foreach ($document in @('README.md','docs\installation\quickstart.md')) {
        $content = Get-Content -LiteralPath (Join-Path $repo $document) -Raw -Encoding UTF8
        $blocks = [regex]::Matches($content, '(?s)(?:~~~|```)powershell\r?\n(?<code>.*?)\r?\n(?:~~~|```)')
        Assert ($blocks.Count -gt 0) 'Missing bootstrap command.'
        $code = $blocks[0].Groups['code'].Value.Trim()
        Assert ($code -notmatch '[\r\n]') 'Default bootstrap must be one line.'
        $command = [scriptblock]::Create($code)
        $before = $bootstrapTestState.invocations.Count
        & $command
        Assert ($bootstrapTestState.invocations.Count -eq $before + 1) 'Default bootstrap did not execute exactly once.'
        Assert (-not $bootstrapTestState.invocations[$before].version) 'Default bootstrap changed version selection.'
        $bootstrapTestState.downloadFailure = $true
        Expect-Failure { $ErrorActionPreference='Continue'; & $command } 'Synthetic download failure'
        Assert ($bootstrapTestState.invocations.Count -eq $before + 1) 'Download failure executed the installer.'
        $bootstrapTestState.downloadFailure = $false
        $bootstrapTestState.installerFailure = $true
        Expect-Failure { & $command } 'Synthetic installer failure'
        $bootstrapTestState.installerFailure = $false
        $checks.Add($document + '-one-line-success-download-failure-and-installer-failure')
        if ($document -like '*quickstart*') {
            $examples = @($blocks[1].Groups['code'].Value -split '\r?\n' | Where-Object { $_ -and -not $_.StartsWith('#') })
            Assert ($examples.Count -eq 2) 'Missing version/tag examples.'
            & ([scriptblock]::Create($examples[0]))
            Assert ($bootstrapTestState.invocations[$bootstrapTestState.invocations.Count-1].version -eq '0.1.0-alpha.1') 'Version argument was lost.'
            $tagCommand = $examples[1].Replace('<owner>/<repository>', 'example/bootstrap').Replace('<published-tag>', 'test-v1')
            & ([scriptblock]::Create($tagCommand))
            $last = $bootstrapTestState.invocations[$bootstrapTestState.invocations.Count-1]
            Assert ($last.repository -eq 'example/bootstrap' -and $last.tag -eq 'test-v1') 'Repository or tag argument was lost.'
            $checks.Add('version-repository-and-tag-arguments')
        }
    }
    # The downloaded production installer must remain valid as a memory script.
    $null = [scriptblock]::Create([IO.File]::ReadAllText((Join-Path $repo 'install.ps1')))
    $checks.Add('production-installer-scriptblock-syntax')
    $report.status = 'passed'
} catch { $report.status='failed'; $report.error=$_.Exception.Message; throw }
finally { $report | ConvertTo-Json -Depth 6 | Set-Content -LiteralPath (Join-Path $OutputDirectory 'bootstrap.json') -Encoding UTF8 }
Write-Host "Installer bootstrap passed: $($checks.Count) checks."
