#requires -Version 5.1
[CmdletBinding()]
param([Parameter(Mandatory)][string]$OutputDirectory)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
New-Item -ItemType Directory -Path $OutputDirectory -Force | Out-Null
$tokens = $null; $parseErrors = $null
$ast = [Management.Automation.Language.Parser]::ParseFile((Join-Path (Split-Path -Parent $PSScriptRoot) 'install.ps1'), [ref]$tokens, [ref]$parseErrors)
if ($parseErrors.Count) { throw 'Installer syntax is invalid.' }
$definition = @($ast.FindAll({param($node) $node -is [Management.Automation.Language.FunctionDefinitionAst] -and $node.Name -eq 'Initialize-WebView2'}, $true))
if ($definition.Count -ne 1) { throw 'Missing WebView2 preparation function.' }
. ([scriptblock]::Create($definition[0].Extent.Text))
$dependency = [pscustomobject]@{version='1.3.299.15';minimum_runtime_version='125.0.0.0';windows_amd64_url='https://example.invalid/pinned.exe';windows_amd64_sha256=('a'*64)}
$script:installed = [version]'155.0.4283.62'
$script:downloads = 0; $script:installs = 0; $script:exit = 0; $script:valid = $true; $script:register = $true; $script:downloadFails = $false
function Get-InstalledWebView2 { return $script:installed }
function Get-VerifiedFile($Url,$Destination,$SHA256) {
    if ($Url -ne $dependency.windows_amd64_url -or $SHA256 -ne $dependency.windows_amd64_sha256) { throw 'Lost fixed download metadata.' }
    $script:downloads++; if ($script:downloadFails) { throw 'offline or hash mismatch' }
}
function Get-AuthenticodeSignature($LiteralPath) { return [pscustomobject]@{Status=$(if ($script:valid) {'Valid'} else {'HashMismatch'});SignerCertificate=[pscustomobject]@{Subject='CN=Microsoft Corporation'}} }
function Start-Process($FilePath,$ArgumentList,$WindowStyle,$Verb,[switch]$Wait,[switch]$PassThru) {
    if ($Verb -or $WindowStyle -ne 'Hidden' -or ($ArgumentList -join ' ') -ne '/silent /install') { throw 'Wrong WebView2 installer invocation.' }
    $script:installs++; if ($script:register) { $script:installed = [version]'155.0.4283.62' }
    return [pscustomobject]@{ExitCode=$script:exit}
}
function Expect-Failure([scriptblock]$Action,[string]$Pattern) {
    try { & $Action } catch { if ($_.Exception.Message -match $Pattern) { return }; throw }
    throw "Expected failure: $Pattern"
}
Initialize-WebView2 $OutputDirectory $dependency
if ($script:downloads -ne 0 -or $script:installs -ne 0) { throw 'Compatible shared Runtime was changed.' }
$script:installed = $null
Initialize-WebView2 $OutputDirectory $dependency
if ($script:installs -ne 1) { throw 'Missing Runtime was not prepared.' }
$script:installed = [version]'100.0.0.0'
Initialize-WebView2 $OutputDirectory $dependency
if ($script:installs -ne 2) { throw 'Old Runtime was not prepared.' }
$script:installed = $null; $script:valid = $false
Expect-Failure { Initialize-WebView2 $OutputDirectory $dependency } 'signature'
if ($script:installs -ne 2) { throw 'Invalid signature reached execution.' }
$script:valid = $true; $script:downloadFails = $true
Expect-Failure { Initialize-WebView2 $OutputDirectory $dependency } 'offline or hash'
if ($script:installs -ne 2) { throw 'Download failure reached execution.' }
$script:downloadFails = $false; $script:exit = 3010
Expect-Failure { Initialize-WebView2 $OutputDirectory $dependency } 'running node was retained'
$script:installed = $null; $script:exit = 0; $script:register = $false
Expect-Failure { Initialize-WebView2 $OutputDirectory $dependency } 'still unavailable'
@{status='passed';checks=7;boundary='mocked vendor installation; no system changes'} | ConvertTo-Json | Set-Content -LiteralPath (Join-Path $OutputDirectory 'webview2-install.json') -Encoding UTF8
