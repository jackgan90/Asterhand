#requires -Version 7.0
[CmdletBinding()]
param([Parameter(Mandatory)][string]$OutputDirectory)
$ErrorActionPreference = 'Stop'
$OutputDirectory = $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($OutputDirectory)
if (Test-Path $OutputDirectory) { throw 'Choose a new system-dependency test directory.' }
New-Item -ItemType Directory -Path $OutputDirectory | Out-Null
$installer = Join-Path (Split-Path -Parent $PSScriptRoot) 'install.ps1'
$tokens = $null
$parseErrors = $null
$ast = [Management.Automation.Language.Parser]::ParseFile($installer, [ref]$tokens, [ref]$parseErrors)
if ($parseErrors.Count -ne 0) { throw 'Installer syntax is invalid.' }
foreach ($name in @('Initialize-VCRuntime', 'Test-WindowsNativeModules')) {
    $functions = @($ast.FindAll({ param($node) $node -is [Management.Automation.Language.FunctionDefinitionAst] -and $node.Name -eq $name }, $true))
    if ($functions.Count -ne 1) { throw "Missing installer function: $name" }
    . ([scriptblock]::Create($functions[0].Extent.Text))
}
$dependency = [pscustomobject]@{version='14.51.36247.0';windows_amd64_url='https://example.invalid/vc.exe';windows_amd64_sha256=('0'*64)}
$script:downloads = 0
$script:installs = 0
$script:signatureValid = $true
$script:installed = $null
$script:installResult = 0
$script:registerVersion = $true
function Get-InstalledVCRuntime { return $script:installed }
function Get-VerifiedFile($Url, $Destination, $SHA256) { $script:downloads++ }
function Get-AuthenticodeSignature($LiteralPath) {
    $status = if ($script:signatureValid) { 'Valid' } else { 'HashMismatch' }
    return [pscustomobject]@{Status=$status;SignerCertificate=[pscustomobject]@{Subject='CN=Microsoft Corporation'}}
}
function Start-Process($FilePath, $ArgumentList, $Verb, $WindowStyle, [switch]$Wait, [switch]$PassThru) {
    if ($Verb -ne 'RunAs' -or $WindowStyle -ne 'Hidden' -or '/norestart' -notin $ArgumentList) { throw 'Incorrect system installer invocation.' }
    $script:installs++
    if ($script:registerVersion) { $script:installed = [version]$dependency.version }
    return [pscustomobject]@{ExitCode=$script:installResult}
}
function Expect-Failure([scriptblock]$Action, [string]$Pattern) {
    try { & $Action } catch { if ($_.Exception.Message -match $Pattern) { return }; throw }
    throw "Expected failure matching: $Pattern"
}
$checks = [Collections.Generic.List[string]]::new()
$script:installed = [version]'14.52.0.0'
Initialize-VCRuntime $OutputDirectory $dependency
if ($script:downloads -ne 0 -or $script:installs -ne 0) { throw 'A compatible newer runtime must be reused.' }
$checks.Add('newer-runtime-reused')
$script:installed = $null
Initialize-VCRuntime $OutputDirectory $dependency
if ($script:downloads -ne 1 -or $script:installs -ne 1) { throw 'Missing runtime was not installed.' }
$checks.Add('missing-runtime-installed')
$script:installed = [version]'14.0.0.0'
Initialize-VCRuntime $OutputDirectory $dependency
if ($script:installs -ne 2) { throw 'An insufficient runtime was not upgraded.' }
$checks.Add('old-runtime-upgraded')
$script:installed = $null
$script:signatureValid = $false
Expect-Failure { Initialize-VCRuntime $OutputDirectory $dependency } 'signature is not valid'
if ($script:installs -ne 2) { throw 'Invalid signature reached the system installer.' }
$checks.Add('invalid-signature-rejected-before-execution')
$script:signatureValid = $true
$script:installResult = 1603
Expect-Failure { Initialize-VCRuntime $OutputDirectory $dependency } 'installation failed'
$checks.Add('installer-error-propagated')
$script:installed = $null
$script:installResult = 0
$script:registerVersion = $false
Expect-Failure { Initialize-VCRuntime $OutputDirectory $dependency } 'still missing or too old'
$checks.Add('success-exit-without-runtime-rejected')
Expect-Failure { Test-WindowsNativeModules $OutputDirectory ([pscustomobject]@{windows_native_modules=@('runtime/node_modules/../../escape.node')}) } 'Invalid native module check path'
$checks.Add('native-module-path-escape-rejected')
@{status='passed';checks=@($checks);real_system_installer='not_executed_mocked_boundary'} |
    ConvertTo-Json -Depth 5 | Tee-Object -FilePath (Join-Path $OutputDirectory 'system-dependencies.json')
