#requires -Version 7.0
function Write-LocalRecord($Record, [string]$Path) {
    $temporary = $Path + '.' + [guid]::NewGuid().ToString('N') + '.tmp'
    try {
        $Record | ConvertTo-Json -Depth 12 | Set-Content -LiteralPath $temporary -Encoding utf8NoBOM
        [IO.File]::Move($temporary, $Path, $true)
    } finally { if (Test-Path -LiteralPath $temporary) { Remove-Item -LiteralPath $temporary -Force } }
}

function Get-LocalFileDigest([string]$Root, [IO.FileInfo]$File) {
    return @{path=[IO.Path]::GetRelativePath($Root,$File.FullName).Replace('\','/');size=$File.Length;sha256=(Get-FileHash -LiteralPath $File.FullName).Hash.ToLowerInvariant()}
}

function Assert-LocalPlainFile([string]$Root, [string]$Relative) {
    if ($Relative -notmatch '^[^:\\]+$' -or $Relative -match '(^/|(^|/)\.{1,2}(/|$)|//|[. ](/|$))') { throw "Invalid local receipt path: $Relative" }
    $path = $Root
    foreach ($part in @('') + @($Relative.Split('/'))) {
        if ($part) { $path = Join-Path $path $part }
        $item = Get-Item -LiteralPath $path -Force
        if ($item.Attributes -band [IO.FileAttributes]::ReparsePoint) { throw "Unexpected reparse point: $path" }
    }
    if ($item.PSIsContainer) { throw "Expected a regular file: $path" }
    return $item
}

function Assert-LocalFileDigests([string]$Root, $Files) {
    $seen = @{}
    if (@($Files).Count -eq 0) { throw 'Empty local file receipt.' }
    foreach ($file in $Files) {
        if ($seen.ContainsKey($file.path) -or $file.sha256 -notmatch '^[a-f0-9]{64}$' -or $file.size -lt 0) { throw 'Invalid or duplicate local file receipt.' }
        $seen[$file.path] = $true
        $item = Assert-LocalPlainFile $Root $file.path
        if ($item.Length -ne $file.size -or (Get-FileHash -LiteralPath $item.FullName).Hash -ne $file.sha256) { throw "Local file checksum mismatch: $($file.path)" }
    }
}

function Get-LocalApplicationFiles([string]$Directory) {
    $pending = [Collections.Generic.Stack[string]]::new()
    $pending.Push($Directory)
    while ($pending.Count) {
        foreach ($entry in Get-ChildItem -LiteralPath $pending.Pop() -Force) {
            if ($entry.Attributes -band [IO.FileAttributes]::ReparsePoint) { throw "Unexpected application reparse point: $($entry.FullName)" }
            if ($entry.PSIsContainer) { $pending.Push($entry.FullName) }
            elseif ($entry.FullName -ne (Join-Path $Directory 'local-build.json')) { $entry }
        }
    }
}

function New-LocalApplicationReceipt([string]$Directory, [string]$Source, [string]$DependencyKey) {
    $identity = Get-Content -LiteralPath (Join-Path $Directory 'input-host.json') -Raw | ConvertFrom-Json
    if ($identity.source_sha256 -ne $Source) { throw 'Application source identity mismatch.' }
    $files = @(Get-LocalApplicationFiles $Directory | Sort-Object FullName | ForEach-Object { Get-LocalFileDigest $Directory $_ })
    Write-LocalRecord @{schema=1;kind='local-application';version=$identity.version;source_sha256=$Source;dependency_key=$DependencyKey;files=$files} (Join-Path $Directory 'local-build.json')
}

function Find-LocalApplication([string]$Workspace, [string]$Source, [string]$DependencyKey) {
    $path = Join-Path $Workspace 'application.json'
    if (-not (Test-Path -LiteralPath $path)) { return $null }
    try {
        $record = Get-Content -LiteralPath $path -Raw | ConvertFrom-Json
        $root = [IO.Path]::GetFullPath($record.directory)
        if (-not $root.StartsWith(($Workspace.TrimEnd('\')+'\'), [StringComparison]::OrdinalIgnoreCase)) { throw 'Application cache is outside its workspace.' }
        $manifest = Join-Path $root 'local-build.json'
        if ((Get-FileHash -LiteralPath $manifest).Hash -ne $record.sha256) { throw 'Application receipt changed.' }
        $receipt = Get-Content -LiteralPath $manifest -Raw | ConvertFrom-Json
        if ($receipt.schema -ne 1 -or $receipt.kind -ne 'local-application' -or $receipt.source_sha256 -ne $Source -or $receipt.dependency_key -ne $DependencyKey) { return $null }
        Assert-LocalFileDigests $root $receipt.files
        if (@(Get-LocalApplicationFiles $root).Count -ne @($receipt.files).Count) { throw 'Unexpected application files.' }
        return $root
    } catch { Write-Host "Application cache rejected; rebuilding: $($_.Exception.Message)"; return $null }
}

function Save-LocalApplication([string]$Workspace, [string]$Directory) {
    Write-LocalRecord @{directory=$Directory;sha256=(Get-FileHash -LiteralPath (Join-Path $Directory 'local-build.json')).Hash.ToLowerInvariant()} (Join-Path $Workspace 'application.json')
}
