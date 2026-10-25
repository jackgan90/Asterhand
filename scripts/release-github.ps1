# GitHub transport for release.ps1. Tokens remain in process memory only.
function Invoke-ReleaseGh([string[]]$Arguments) {
    $output = & $script:ReleaseGh @Arguments 2>&1
    if ($LASTEXITCODE -ne 0) { throw "GitHub CLI failed: $($output -join [Environment]::NewLine)" }
    return ($output -join [Environment]::NewLine)
}

function Connect-ReleaseGitHub([string]$Executable) {
    $script:ReleaseGh = (Get-Command $Executable -ErrorAction Stop).Source
    $null = & $script:ReleaseGh auth status 2>&1
    if ($LASTEXITCODE -eq 0) { return }
    # Reuse the existing Git Credential Manager login without prompting or persisting it.
    $savedPrompt = $env:GIT_TERMINAL_PROMPT
    $savedInteractive = $env:GCM_INTERACTIVE
    try {
        $env:GIT_TERMINAL_PROMPT = '0'
        $env:GCM_INTERACTIVE = 'Never'
        $lines = "protocol=https`nhost=github.com`n`n" | git credential fill 2>$null
        $secret = @($lines | Where-Object { $_ -like 'password=*' })
        if ($LASTEXITCODE -ne 0 -or $secret.Count -ne 1) { throw 'Sign in with gh auth login before publishing.' }
        $env:GH_TOKEN = $secret[0].Substring(9)
    } finally {
        $lines = $null
        $secret = $null
        $env:GIT_TERMINAL_PROMPT = $savedPrompt
        $env:GCM_INTERACTIVE = $savedInteractive
    }
    $null = Invoke-ReleaseGh @('api', 'user', '--jq', '.login')
}

function Get-ReleaseByTag([string]$Repository, [string]$Tag) {
    # Listing includes drafts for the authenticated publisher; API failures are not treated as absence.
    $pages = Invoke-ReleaseGh @('api', '--paginate', '--slurp', "repos/$Repository/releases?per_page=100") | ConvertFrom-Json
    return @($pages | ForEach-Object { $_ } | Where-Object tag_name -eq $Tag)
}

function Assert-ReleaseTagTarget([string]$Repository, [string]$Tag, [string]$Target, [switch]$RequireExists) {
    $references = @(Invoke-ReleaseGh @('api', "repos/$Repository/git/matching-refs/tags/$Tag") | ConvertFrom-Json | Where-Object ref -eq "refs/tags/$Tag")
    if ($references.Count -eq 0) {
        if ($RequireExists) { throw 'Published release tag is missing.' }
        return
    }
    if ($references.Count -ne 1) { throw 'Ambiguous release tag.' }
    $object = $references[0].object
    for ($depth = 0; $depth -lt 10; $depth++) {
        if ($object.type -eq 'commit') {
            if ($object.sha -ne $Target) { throw 'Existing release tag points to a different commit.' }
            return
        }
        if ($object.type -ne 'tag') { throw 'Release tag does not resolve to a commit.' }
        $annotation = Invoke-ReleaseGh @('api', "repos/$Repository/git/tags/$($object.sha)") | ConvertFrom-Json
        $object = $annotation.object
    }
    throw 'Release tag annotation depth exceeds the supported limit.'
}

function Assert-ReleaseAsset($Asset, [string]$File, [string]$Repository, [string]$Tag, [string]$VerifyDirectory) {
    $local = Get-Item -LiteralPath $File
    if ($Asset.state -ne 'uploaded' -or $Asset.size -ne $local.Length) { throw "Remote asset size/state mismatch: $($local.Name)" }
    $hash = (Get-FileHash -LiteralPath $File).Hash.ToLowerInvariant()
    $remoteFile = Join-Path $VerifyDirectory $local.Name
    $null = Invoke-ReleaseGh @('release', 'download', $Tag, '--repo', $Repository, '--pattern', $local.Name, '--dir', $VerifyDirectory, '--clobber')
    if ((Get-FileHash -LiteralPath $remoteFile).Hash.ToLowerInvariant() -ne $hash) { throw "Remote asset checksum mismatch: $($local.Name)" }
}

function Publish-ReleaseAssets([string]$Repository, [string]$Tag, [string]$Mode, [string]$Directory, [string]$Target, [string]$VerifyDirectory) {
    Assert-ReleaseTagTarget $Repository $Tag $Target
    $files = @(Get-ChildItem -LiteralPath $Directory -File | Sort-Object Name)
    $matches = @(Get-ReleaseByTag $Repository $Tag)
    if ($matches.Count -gt 1) { throw 'Multiple releases matched the tag.' }
    if ($matches.Count -eq 1 -and -not $matches[0].draft) { throw 'The release is already public. Choose a new version/tag; published assets are never overwritten.' }
    if ($matches.Count -eq 0) {
        $args = @('release', 'create', $Tag, '--repo', $Repository, '--draft', '--title', "Asterhand $Tag", '--notes-file', (Join-Path $Directory 'RELEASE-NOTES.md'), '--target', $Target)
        if ($Mode -eq 'Prerelease') { $args += '--prerelease' }
        $null = Invoke-ReleaseGh $args
    }
    $release = @(Get-ReleaseByTag $Repository $Tag)[0]
    if ($release.target_commitish -ne $Target) { throw 'Existing release draft points to a different source target.' }
    $expected = @($files.Name)
    foreach ($asset in $release.assets) {
        if ($asset.name -notin $expected) { throw "Unexpected existing release asset: $($asset.name)" }
    }
    New-Item -ItemType Directory -Path $VerifyDirectory -Force | Out-Null
    foreach ($file in $files) {
        $existing = @($release.assets | Where-Object name -eq $file.Name)
        if ($existing.Count -gt 1) { throw "Duplicate release asset: $($file.Name)" }
        if ($existing.Count -eq 0) {
            $null = Invoke-ReleaseGh @('release', 'upload', $Tag, $file.FullName, '--repo', $Repository)
        }
        $fresh = @(Get-ReleaseByTag $Repository $Tag)[0]
        $asset = @($fresh.assets | Where-Object name -eq $file.Name)
        if ($asset.Count -ne 1) { throw "Missing uploaded asset: $($file.Name)" }
        Assert-ReleaseAsset $asset[0] $file.FullName $Repository $Tag $VerifyDirectory
    }
    if ($Mode -ne 'Draft') {
        Assert-ReleaseTagTarget $Repository $Tag $Target
        $args = @('release', 'edit', $Tag, '--repo', $Repository, '--draft=false')
        if ($Mode -eq 'Prerelease') { $args += @('--prerelease', '--latest=false') }
        else { $args += @('--prerelease=false', '--latest') }
        $null = Invoke-ReleaseGh $args
        Assert-ReleaseTagTarget $Repository $Tag $Target -RequireExists
    }
    $result = @(Get-ReleaseByTag $Repository $Tag)[0]
    if ($result.draft -ne ($Mode -eq 'Draft') -or ($Mode -eq 'Prerelease' -and -not $result.prerelease)) { throw 'GitHub release visibility does not match the requested mode.' }
    Write-Host "Release $Mode verified: $($result.html_url)"
    return $result.html_url
}
