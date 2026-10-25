#requires -Version 7.0
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'dependency-tools.ps1')
$repo = Split-Path -Parent $PSScriptRoot
$catalogPath = Join-Path $repo 'packaging\dependencies.json'
$lockPath = Join-Path $repo 'packaging\runtime-lock.json'
$catalog = Read-DependencyCatalog $catalogPath
$null = Assert-DependencyLock $catalog $lockPath
foreach ($fault in @('version','integrity','source')) {
    $bad = Read-DependencyCatalog $catalogPath
    $bad.components[0].$fault = 'invalid-contract-value'
    $rejected = $false
    try { $null = Assert-DependencyLock $bad $lockPath } catch { $rejected = $true }
    if (-not $rejected) { throw "Dependency guard accepted a mismatched $fault." }
}
Write-Host 'Dependency catalog mismatch guards passed.'

# Arbitrary component/runtime/provider names exercise the shared preparation path.
$fixture = Read-DependencyCatalog $catalogPath
$fixture.components = @([pscustomobject]@{id='fixture-agent';manager='npm';package='@fixture/agent';version='1.2.3';source='https://registry.npmjs.org/@fixture/agent/-/agent-1.2.3.tgz';integrity='sha512-fixture'})
$fixture.bundle = [pscustomobject]@{default_target='fixture';targets=@([pscustomobject]@{id='fixture';runtime='arbitrary-agent';provider='arbitrary-model';model='model-x';credential_ref='fixture-key'})}
$dependencies = Get-RuntimeDependencies $fixture
if ($dependencies.Count -ne 1 -or $dependencies['@fixture/agent'] -ne '1.2.3') { throw 'Dependency preparation assumes a concrete provider.' }
$fixtureDirectory = Join-Path $repo ('out\dependency-contract-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $fixtureDirectory | Out-Null
$fixtureLock = Join-Path $fixtureDirectory 'package-lock.json'
$lock = @{lockfileVersion=3;packages=@{''=@{dependencies=$dependencies};'node_modules/@fixture/agent'=@{version='1.2.3';resolved=$fixture.components[0].source;integrity=$fixture.components[0].integrity}}}
$lock | ConvertTo-Json -Depth 12 | Set-Content $fixtureLock -Encoding utf8NoBOM
$null = Assert-DependencyLock $fixture $fixtureLock
$lock.packages[''].dependencies['undeclared'] = '1.0.0'
$lock | ConvertTo-Json -Depth 12 | Set-Content $fixtureLock -Encoding utf8NoBOM
$rejected = $false
try { $null = Assert-DependencyLock $fixture $fixtureLock } catch { $rejected = $true }
if (-not $rejected) { throw 'Undeclared direct package accepted.' }
foreach ($fault in @('manager','duplicate-component','missing-default','empty-targets')) {
    $bad = $fixture | ConvertTo-Json -Depth 12 | ConvertFrom-Json
    switch ($fault) {
        'manager' { $bad.components[0].manager='unknown' }
        'duplicate-component' { $bad.components += $bad.components[0] }
        'missing-default' { $bad.bundle.default_target='missing' }
        'empty-targets' { $bad.bundle.targets=@() }
    }
    $rejected = $false
    try { $null = Get-RuntimeDependencies $bad } catch { $rejected = $true }
    if (-not $rejected) { throw "Invalid description accepted: $fault" }
}
Write-Host 'Provider-neutral preparation and bundle description contracts passed.'
