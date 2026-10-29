#requires -Version 7.0
function Get-LocalRuntimeKey([string]$Repository) {
    $parts = @('windows-amd64-runtime-v1')
    foreach ($name in @('packaging/dependencies.json','packaging/runtime-lock.json','scripts/prepare-dependencies.ps1','scripts/dependency-tools.ps1','scripts/local-runtime-cache.ps1','scripts/build-progress.ps1')) {
        $parts += (Get-FileHash -LiteralPath (Join-Path $Repository $name)).Hash
    }
    return [Convert]::ToHexString([Security.Cryptography.SHA256]::HashData([Text.Encoding]::UTF8.GetBytes(($parts -join ':')))).ToLowerInvariant()
}

function Get-LocalRuntimeCriticalPaths([string]$Root, $Catalog) {
    $paths = [Collections.Generic.HashSet[string]]::new([StringComparer]::OrdinalIgnoreCase)
    foreach ($path in @('node/node.exe','package.json','package-lock.json')) { $null = $paths.Add($path) }
    foreach ($component in $Catalog.components) {
        $packageRoot = 'node_modules/' + $component.package + '/'
        $null = $paths.Add($packageRoot + 'package.json')
        $metadata = Get-Content -LiteralPath (Join-Path $Root ($packageRoot+'package.json')) -Raw | ConvertFrom-Json
        foreach ($field in @('main','module','bin')) {
            $property = $metadata.PSObject.Properties[$field]
            if (-not $property) { continue }
            $entries = if ($property.Value -is [string]) { @($property.Value) } else { @($property.Value.PSObject.Properties.Value) }
            foreach ($entry in $entries) {
                if ($entry -isnot [string]) { throw 'Unsupported package entry metadata.' }
                $relative = $packageRoot + ($entry -replace '^\./','')
                $null = Assert-LocalPlainFile $Root $relative
                $null = $paths.Add($relative)
            }
        }
    }
    foreach ($module in $Catalog.windows_native_modules) { $null = $paths.Add($module.Substring('runtime/'.Length)) }
    return @($paths | Sort-Object)
}

function Find-LocalRuntime([string]$Program, [string]$Key, $Catalog) {
    $root = Join-Path $Program 'local-runtimes'
    $index = Join-Path $root ($Key+'.json')
    if (-not (Test-Path -LiteralPath $index)) { return $null }
    try {
        $record = Get-Content -LiteralPath $index -Raw | ConvertFrom-Json
        if ($record.generation -notmatch ('^'+$Key+'-[a-f0-9]{32}$')) { throw 'Invalid runtime generation.' }
        $directory = Join-Path $root $record.generation
        $receiptFile = Assert-LocalPlainFile $root ($record.generation+'/runtime-receipt.json')
        if ((Get-FileHash -LiteralPath $receiptFile.FullName).Hash -ne $record.sha256) { throw 'Runtime receipt changed.' }
        $receipt = Get-Content -LiteralPath $receiptFile.FullName -Raw | ConvertFrom-Json
        if ($receipt.schema -ne 1 -or $receipt.key -ne $Key -or $receipt.generation -ne $record.generation) { throw 'Runtime receipt identity mismatch.' }
        Assert-LocalFileDigests $directory $receipt.files
        $expected = @(Get-LocalRuntimeCriticalPaths $directory $Catalog)
        if (@(Compare-Object $expected @($receipt.files.path)).Count) { throw 'Runtime receipt critical file set mismatch.' }
        return $directory
    } catch { Write-Host "Runtime cache rejected; preparing clean dependencies: $($_.Exception.Message)"; return $null }
}

function Get-LocalPreparedDependencies([string]$Repository, [string]$Workspace, [string]$BuilderGo, [string]$PreparedPath, [switch]$Fresh) {
    $key = Get-LocalRuntimeKey $Repository
    $cache = Join-Path $Workspace 'prepared.json'
    $prepared = $null
    if (-not $Fresh) {
        if ($PreparedPath) { $prepared = Get-Content -LiteralPath $PreparedPath -Raw | ConvertFrom-Json }
        elseif (Test-Path -LiteralPath $cache) {
            try {
                $cached = Get-Content -LiteralPath $cache -Raw | ConvertFrom-Json
                if ($cached.PSObject.Properties['key'] -and $cached.key -eq $key) { $prepared = $cached }
                # Legacy cache is accepted only when its catalog/lock still match.
                elseif ($cached.PSObject.Properties['inputs']) {
                    $inputs = @('packaging/dependencies.json','packaging/runtime-lock.json') | ForEach-Object { (Get-FileHash -LiteralPath (Join-Path $Repository $_)).Hash }
                    if ($cached.inputs -eq ($inputs -join ':')) { $prepared = $cached }
                }
            } catch { Write-Host 'Prepared record is invalid; preparing dependencies again.' }
        }
    }
    if ($null -ne $prepared) {
        try { $null = Assert-PreparedDependencies $Repository $prepared.node_distribution $prepared.runtime_root $BuilderGo }
        catch {
            if ($PreparedPath) { throw }
            Write-Host 'Prepared dependencies failed verification; preparing again.'
            $prepared = $null
        }
    }
    if ($null -eq $prepared) {
        $directory = Join-Path $Workspace ('dependencies-'+[guid]::NewGuid().ToString('N'))
        Invoke-BuildProcess 'Prepare pinned dependencies' (Join-Path $PSHOME 'pwsh.exe') @('-NoProfile','-File',(Join-Path $Repository 'scripts/prepare-dependencies.ps1'),'-OutputDirectory',$directory)
        $prepared = Get-Content -LiteralPath (Join-Path $directory 'prepared.json') -Raw | ConvertFrom-Json
        $null = Assert-PreparedDependencies $Repository $prepared.node_distribution $prepared.runtime_root $BuilderGo
    }
    Write-LocalRecord @{key=$key;node_distribution=$prepared.node_distribution;runtime_root=$prepared.runtime_root} $cache
    return $prepared
}

function Get-LocalInstalledRuntime {
    param([string]$Repository, [string]$Workspace, [string]$Program, [string]$BuilderGo, [string]$PreparedPath, [switch]$Repair)
    $catalog = Read-DependencyCatalog (Join-Path $Repository 'packaging/dependencies.json')
    $key = Get-LocalRuntimeKey $Repository
    $guard = [IO.File]::Open((Join-Path $Program '.local-runtime.lock'), [IO.FileMode]::OpenOrCreate, [IO.FileAccess]::ReadWrite, [IO.FileShare]::None)
    try {
        $runtimeRoot = Join-Path $Program 'local-runtimes'
        if (Test-Path -LiteralPath $runtimeRoot) {
            if ((Get-Item -LiteralPath $runtimeRoot -Force).Attributes -band [IO.FileAttributes]::ReparsePoint) { throw 'Runtime cache root must not be a reparse point.' }
        }
        [IO.Directory]::CreateDirectory($runtimeRoot) | Out-Null
        if (-not $Repair -and -not $PreparedPath) {
            $existing = Find-LocalRuntime $Program $key $catalog
            if ($existing) { Write-Host 'Runtime cache hit: zero dependency files copied.'; return @{directory=$existing;key=$key;reused=$true} }
        }
        # An existing index that fails validation is repaired from npm, never from
        # the same potentially damaged prepared directory.
        $fresh = $Repair -or (-not $PreparedPath -and (Test-Path -LiteralPath (Join-Path $runtimeRoot ($key+'.json'))))
        $prepared = Get-LocalPreparedDependencies $Repository $Workspace $BuilderGo $PreparedPath -Fresh:$fresh
        $generation = $key+'-'+[guid]::NewGuid().ToString('N')
        $temporary = Join-Path $runtimeRoot ('.preparing-'+$generation)
        $destination = Join-Path $runtimeRoot $generation
        [IO.Directory]::CreateDirectory($temporary) | Out-Null
        Copy-BuildTree $prepared.node_distribution (Join-Path $temporary 'node')
        Copy-BuildTree (Join-Path $prepared.runtime_root 'node_modules') (Join-Path $temporary 'node_modules')
        Copy-Item -LiteralPath (Join-Path $prepared.runtime_root 'package.json'),(Join-Path $prepared.runtime_root 'package-lock.json') -Destination $temporary
        $null = Assert-PreparedDependencies $Repository (Join-Path $temporary 'node') $temporary $BuilderGo
        $files = @(Get-LocalRuntimeCriticalPaths $temporary $catalog | ForEach-Object { Get-LocalFileDigest $temporary (Assert-LocalPlainFile $temporary $_) })
        Write-LocalRecord @{schema=1;key=$key;generation=$generation;catalog_sha256=(Get-FileHash -LiteralPath (Join-Path $Repository 'packaging/dependencies.json')).Hash.ToLowerInvariant();files=$files} (Join-Path $temporary 'runtime-receipt.json')
        [IO.Directory]::Move($temporary,$destination)
        Write-LocalRecord @{generation=$generation;sha256=(Get-FileHash -LiteralPath (Join-Path $destination 'runtime-receipt.json')).Hash.ToLowerInvariant()} (Join-Path $runtimeRoot ($key+'.json'))
        return @{directory=$destination;key=$key;reused=$false}
    } finally { $guard.Dispose() }
}
