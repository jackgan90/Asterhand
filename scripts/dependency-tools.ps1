#requires -Version 7.0
Set-StrictMode -Version Latest

function Get-SourceFingerprint([string]$Repository) {
    $files = @()
    foreach ($directory in @('cmd','internal','web','plugins','packaging','scripts','tests')) {
        $files += Get-ChildItem -LiteralPath (Join-Path $Repository $directory) -Recurse -File |
            Where-Object { $_.Extension -in @('.go','.js','.mjs','.html','.css','.json','.ps1','.sh','.png','.ico') }
    }
    $files += Get-Item (Join-Path $Repository 'go.mod'),(Join-Path $Repository 'go.sum'),(Join-Path $Repository 'install.ps1'),(Join-Path $Repository 'install.sh')
    $files += Get-Item (Join-Path $Repository 'LICENSE'),(Join-Path $Repository 'docs\installation\quickstart.md')
    $lines = foreach ($file in ($files | Sort-Object FullName)) {
        [IO.Path]::GetRelativePath($Repository,$file.FullName).Replace('\','/') + ':' + (Get-FileHash -LiteralPath $file.FullName).Hash.ToLowerInvariant()
    }
    return [Convert]::ToHexString([Security.Cryptography.SHA256]::HashData([Text.Encoding]::UTF8.GetBytes(($lines -join "`n")))).ToLowerInvariant()
}

function Read-DependencyCatalog([string]$Path) {
    $catalog = Get-Content -LiteralPath $Path -Raw | ConvertFrom-Json
    if ($catalog.schema -ne 2) { throw 'Unsupported dependency catalog schema.' }
    foreach ($name in @('go','node','tailscale')) {
        if ($catalog.$name.version -notmatch '^[0-9]+\.[0-9]+\.[0-9]+(?:-[A-Za-z0-9.-]+)?$') {
            throw "Use an exact version for $name."
        }
    }
    if ($catalog.vc_runtime.version -notmatch '^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$') { throw 'Use an exact four-part VC++ runtime version.' }
    foreach ($name in @('go','node','tailscale','vc_runtime')) {
        $uri = [uri]$catalog.$name.windows_amd64_url
        if ($uri.Scheme -ne 'https' -or $uri.UserInfo -or $catalog.$name.windows_amd64_sha256 -notmatch '^[a-fA-F0-9]{64}$') {
            throw "Invalid download metadata for $name."
        }
    }
    if (@($catalog.windows_native_modules).Count -eq 0) { throw 'Windows native component checks are required.' }
    foreach ($module in $catalog.windows_native_modules) {
        if ($module -notmatch '^runtime/node_modules/[A-Za-z0-9@_./-]+\.node$' -or $module -match '(^|/)\.\.(/|$)') { throw 'Invalid Windows native component check path.' }
    }
    Assert-BundleDescription $catalog
    return $catalog
}

function Assert-BundleDescription($Catalog) {
    if (@($Catalog.components).Count -eq 0 -or @($Catalog.bundle.targets).Count -eq 0) { throw 'Bundle components and targets are required.' }
    $ids = @{}; $packages = @{}
    foreach ($component in $Catalog.components) {
        if ($component.id -notmatch '^[a-z][a-z0-9_-]*$' -or $ids.ContainsKey($component.id) -or
            $component.manager -ne 'npm' -or $component.package -notmatch '^(@[a-z0-9_-]+/)?[a-z0-9_.-]+$' -or
            $packages.ContainsKey($component.package) -or $component.version -notmatch '^[0-9]+\.[0-9]+\.[0-9]+(?:-[A-Za-z0-9.-]+)?$') {
            throw 'Invalid, duplicate or unsupported bundle component.'
        }
        $ids[$component.id] = $true; $packages[$component.package] = $true
    }
    $targets = @{}
    foreach ($target in $Catalog.bundle.targets) {
        if ($target.id -notmatch '^[a-z][a-z0-9_-]*$' -or $targets.ContainsKey($target.id) -or
            -not $target.runtime -or -not $target.provider -or -not $target.model -or
            $target.credential_ref -notmatch '^[a-z][a-z0-9_-]{0,63}$') { throw 'Invalid or duplicate bundle target.' }
        $targets[$target.id] = $true
    }
    if (-not $targets.ContainsKey($Catalog.bundle.default_target)) { throw 'Bundle default target is not declared.' }
}

function Get-RuntimeDependencies($Catalog) {
    Assert-BundleDescription $Catalog
    $dependencies = [ordered]@{}
    foreach ($component in $Catalog.components) { $dependencies[$component.package] = $component.version }
    return $dependencies
}

function Assert-DependencyLock($Catalog, [string]$LockPath) {
    $expected = Get-RuntimeDependencies $Catalog
    $lock = Get-Content -LiteralPath $LockPath -Raw | ConvertFrom-Json -AsHashtable
    if ($lock.lockfileVersion -ne 3) { throw 'Expected npm lockfileVersion 3.' }
    if ($lock.packages[''].dependencies.Count -ne $expected.Count) { throw 'Lock contains missing or undeclared direct dependencies.' }
    foreach ($component in $Catalog.components) {
        $name = $component.package
        if ($lock.packages[''].dependencies[$name] -ne $component.version -or
            $lock.packages["node_modules/$name"].version -ne $component.version) {
            throw "Catalog / lock version mismatch: $name"
        }
        $package = $lock.packages["node_modules/$name"]
        if ($package.resolved -ne $component.source -or $package.integrity -ne $component.integrity) {
            throw "Catalog / lock source or integrity mismatch: $name"
        }
    }
    foreach ($entry in $lock.packages.GetEnumerator()) {
        if ($entry.Key -eq '') { continue }
        if ($entry.Value['link'] -eq $true) { throw "Linked runtime dependency is not reproducible: $($entry.Key)" }
        if ($entry.Value['inBundle'] -eq $true) {
            # npm-bundled children inherit their enclosing tarball's integrity.
            $parent = $entry.Key
            do {
                $index = $parent.LastIndexOf('/node_modules/')
                if ($index -lt 0) { throw "Bundled dependency has no locked parent: $($entry.Key)" }
                $parent = $parent.Substring(0,$index)
            } while (-not $lock.packages[$parent]['integrity'])
            continue
        }
        if ($entry.Value['resolved'] -notlike 'https://registry.npmjs.org/*' -or $entry.Value['integrity'] -notmatch '^sha(256|384|512)-') {
            throw "Unpinned runtime dependency: $($entry.Key)"
        }
    }
    return $lock
}

function Assert-PreparedDependencies([string]$Repository, [string]$NodeDistribution, [string]$RuntimeRoot, [string]$Go) {
    $catalog = Read-DependencyCatalog (Join-Path $Repository 'packaging\dependencies.json')
    $lockPath = Join-Path $Repository 'packaging\runtime-lock.json'
    $lock = Assert-DependencyLock $catalog $lockPath
    if ((Get-FileHash $lockPath).Hash -ne (Get-FileHash (Join-Path $RuntimeRoot 'package-lock.json')).Hash) {
        throw 'Runtime dependency lock differs from packaging/runtime-lock.json.'
    }
    $node = Join-Path $NodeDistribution 'node.exe'
    if ((& $node --version) -ne ('v' + $catalog.node.version) -or $LASTEXITCODE -ne 0) { throw 'Node version mismatch.' }
    $goVersion = & $Go version
    if ($LASTEXITCODE -ne 0 -or $goVersion -notmatch ('go' + [regex]::Escape($catalog.go.version) + '\s')) { throw 'Go compiler version mismatch.' }
    # Check every installed package present in the lock. Platform-specific optional
    # packages may be absent; installed copies must always match their locked version.
    foreach ($entry in $lock.packages.GetEnumerator()) {
        if ($entry.Key -eq '') { continue }
        $packagePath = Join-Path $RuntimeRoot ($entry.Key + '/package.json')
        if (-not (Test-Path -LiteralPath $packagePath)) {
            if ($entry.Value['optional'] -eq $true) { continue }
            throw "Missing locked runtime package: $($entry.Key)"
        }
        $package = Get-Content -LiteralPath $packagePath -Raw | ConvertFrom-Json
        if ($package.version -ne $entry.Value.version) { throw "Installed dependency version mismatch: $($entry.Key)" }
    }
    return $catalog
}
