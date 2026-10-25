#requires -Version 7.0
[CmdletBinding()]
param([Parameter(Mandatory)][string]$OutputDirectory)
$ErrorActionPreference = 'Stop'
$repo = Split-Path -Parent $PSScriptRoot
. (Join-Path $PSScriptRoot 'release-prepare.ps1')
. (Join-Path $PSScriptRoot 'release-github.ps1')
$OutputDirectory = $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($OutputDirectory)
if (Test-Path $OutputDirectory) { throw 'Choose a new release test directory.' }
New-Item -ItemType Directory -Path $OutputDirectory | Out-Null
$checks = [Collections.Generic.List[string]]::new()
function Expect-Failure([scriptblock]$Action, [string]$Pattern) {
    try { & $Action } catch { if ($_.Exception.Message -match $Pattern) { return }; throw }
    throw "Expected failure matching: $Pattern"
}
$target = Get-ReleaseBuilder $repo 'windows-amd64'
if ($target.installer -ne 'install.ps1') { throw 'Wrong Windows installer.' }
Expect-Failure { Get-ReleaseBuilder $repo 'linux-amd64' } 'not enabled'
Expect-Failure { Get-ReleaseBuilder $repo 'darwin-arm64' } 'not enabled'
$checks.Add('disabled-platforms-rejected')
$assets = Join-Path $OutputDirectory 'assets'
$remoteRoot = Join-Path $OutputDirectory 'remote'
New-Item -ItemType Directory -Path $assets,$remoteRoot | Out-Null
Set-Content (Join-Path $assets 'RELEASE-NOTES.md') 'Fixture release'
Set-Content (Join-Path $assets 'fixture.zip') 'Fixture package'
Write-ReleaseChecksums $assets
Assert-ReleaseChecksums $assets
Add-Content (Join-Path $assets 'fixture.zip') 'Changed'
Expect-Failure { Assert-ReleaseChecksums $assets } 'content changed'
Write-ReleaseChecksums $assets
$checks.Add('local-tamper-rejected')

# Model the external CLI boundary, including the bytes downloaded from GitHub.
$script:mockRelease = $null
$script:uploads = 0
$script:publishes = 0
$script:failUpload = $false
$script:mockTagCommit = $null
$script:mockTagType = 'commit'
function Get-ReleaseByTag([string]$Repository, [string]$Tag) { if ($script:mockRelease) { return $script:mockRelease } }
function Invoke-ReleaseGh([string[]]$Arguments) {
    if ($Arguments[0] -eq 'api') {
        if ($Arguments[1] -like '*/git/tags/*') { return '{"object":{"type":"commit","sha":"main"}}' }
        if (-not $script:mockTagCommit) { return '[]' }
        return ConvertTo-Json -InputObject @(@{ref='refs/tags/v1';object=@{type=$script:mockTagType;sha=$script:mockTagCommit}}) -Depth 5
    }
    $operation = $Arguments[1]
    switch ($operation) {
        'create' { $script:mockRelease = [pscustomobject]@{draft=$true;prerelease=$false;target_commitish='main';assets=@();html_url='https://github.com/example/test/releases/tag/v1'} }
        'upload' {
            if ($script:failUpload) { throw 'Injected upload failure' }
            if ('--clobber' -in $Arguments) { throw 'Remote overwrite is prohibited.' }
            $file = Get-Item $Arguments[3]
            Copy-Item $file.FullName (Join-Path $remoteRoot $file.Name)
            $script:mockRelease.assets += [pscustomobject]@{name=$file.Name;state='uploaded';size=$file.Length}
            $script:uploads++
        }
        'download' {
            $name = $Arguments[[array]::IndexOf($Arguments, '--pattern') + 1]
            $destination = $Arguments[[array]::IndexOf($Arguments, '--dir') + 1]
            Copy-Item (Join-Path $remoteRoot $name) (Join-Path $destination $name) -Force
        }
        'edit' {
            $script:mockRelease.draft = $false
            $script:mockRelease.prerelease = '--prerelease' -in $Arguments
            $script:publishes++
            $script:mockTagCommit = 'main'
        }
        default { throw "Unexpected mocked GitHub operation: $operation" }
    }
}
$verify = Join-Path $OutputDirectory 'verified'
$script:mockTagCommit = 'other-commit'
Expect-Failure { Publish-ReleaseAssets 'example/test' 'v1' 'Prerelease' $assets 'main' $verify } 'tag points to a different commit'
if ($script:mockRelease) { throw 'A conflicting tag must fail before creating the draft.' }
$script:mockTagType = 'tag'
Assert-ReleaseTagTarget 'example/test' 'v1' 'main' -RequireExists
$script:mockTagType = 'commit'
$script:mockTagCommit = $null
$checks.Add('tag-conflict-rejected-and-annotated-tag-resolved')
$script:failUpload = $true
Expect-Failure { Publish-ReleaseAssets 'example/test' 'v1' 'Prerelease' $assets 'main' $verify } 'Injected upload failure'
if (-not $script:mockRelease.draft -or $script:publishes -ne 0) { throw 'Failed upload must preserve draft.' }
$checks.Add('failed-upload-stays-draft')
$script:failUpload = $false
$null = Publish-ReleaseAssets 'example/test' 'v1' 'Draft' $assets 'main' $verify
$uploaded = $script:uploads
$null = Publish-ReleaseAssets 'example/test' 'v1' 'Draft' $assets 'main' $verify
if ($script:uploads -ne $uploaded) { throw 'Retry uploaded an already verified asset.' }
$checks.Add('identical-draft-retry-is-idempotent')
$remoteFile = Join-Path $remoteRoot 'fixture.zip'
$original = [IO.File]::ReadAllBytes($remoteFile)
$changed = [byte[]]$original.Clone()
$changed[0] = $changed[0] -bxor 1
[IO.File]::WriteAllBytes($remoteFile, $changed)
Expect-Failure { Publish-ReleaseAssets 'example/test' 'v1' 'Prerelease' $assets 'main' $verify } 'Remote asset checksum mismatch'
if ($script:publishes -ne 0) { throw 'Mismatched asset was published.' }
[IO.File]::WriteAllBytes($remoteFile, $original)
$checks.Add('remote-same-size-tamper-blocks-publication')
$null = Publish-ReleaseAssets 'example/test' 'v1' 'Prerelease' $assets 'main' $verify
if ($script:mockRelease.draft -or -not $script:mockRelease.prerelease -or $script:publishes -ne 1) { throw 'Prerelease was not published exactly once.' }
Expect-Failure { Publish-ReleaseAssets 'example/test' 'v1' 'Prerelease' $assets 'main' $verify } 'already public'
$checks.Add('published-release-is-immutable')
@{status='passed';checks=@($checks)} | ConvertTo-Json -Depth 5 | Tee-Object -FilePath (Join-Path $OutputDirectory 'release-contract.json')
