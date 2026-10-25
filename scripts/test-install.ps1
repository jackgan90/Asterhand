#requires -Version 7.0
[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$PackageDirectory,
    [string]$PreviousPackageDirectory,
    [Parameter(Mandatory)][string]$OutputDirectory
)
$ErrorActionPreference = 'Stop'
$OutputDirectory = $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($OutputDirectory)
$PackageDirectory = $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($PackageDirectory)
if ($PreviousPackageDirectory) { $PreviousPackageDirectory = $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($PreviousPackageDirectory) }
if (Test-Path -LiteralPath $OutputDirectory) { throw 'Use a new installation test directory to preserve earlier evidence.' }
New-Item -ItemType Directory -Path $OutputDirectory | Out-Null
$ps5 = Join-Path $env:SystemRoot 'System32\WindowsPowerShell\v1.0\powershell.exe'
$report = [ordered]@{status='running';created_utc=[DateTime]::UtcNow.ToString('o');shell='Windows PowerShell 5.1';checks=@();tailscale='skipped';firewall='skipped';two_machine_acceptance='pending'}

function Install-Candidate([string]$PackageRoot,[string]$Program,[string]$Data) {
    $manifest = Get-Content (Join-Path $PackageRoot 'release-manifest.json') -Raw | ConvertFrom-Json
    $package = $manifest.packages | Where-Object { $_.os -eq 'windows' -and $_.arch -eq 'amd64' }
    & $ps5 -NoProfile -ExecutionPolicy Bypass -File (Join-Path $PackageRoot 'install.ps1') -Version $manifest.version -PackagePath (Join-Path $PackageRoot $package.file) -InstallDir $Program -DataDir $Data -SkipTailscale -NoStart | Out-Host
    if ($LASTEXITCODE -ne 0) { throw 'Windows PowerShell installation failed.' }
    return Get-Content (Join-Path $Program 'installation.json') -Raw | ConvertFrom-Json
}
function Invoke-TestAPI($Node,[string]$Path,[string]$Method='GET',$Body=$null) {
    $params = @{Uri=($Node.address+$Path);Method=$Method;WebSession=$Node.session;Headers=$Node.headers;TimeoutSec=10}
    if ($null -ne $Body) { $params.ContentType='application/json';$params.Body=[Text.Encoding]::UTF8.GetBytes(($Body | ConvertTo-Json -Compress -Depth 8)) }
    return (Invoke-WebRequest @params).Content | ConvertFrom-Json -AsHashtable
}
function Start-TestNode($Installation) {
    $data = $Installation.data
    $arguments = @('start','--home',('"'+$data+'"'),'--web','127.0.0.1:0','--peer','127.0.0.1:0')
    $process = Start-Process $Installation.executable -ArgumentList $arguments -WindowStyle Hidden -PassThru -RedirectStandardOutput (Join-Path $data 'startup.txt') -RedirectStandardError (Join-Path $data 'error.txt')
    try {
    $deadline = [DateTime]::UtcNow.AddSeconds(30)
    while ([DateTime]::UtcNow -lt $deadline) {
        if ($process.HasExited) { throw 'Installation test node exited during startup; inspect error.txt.' }
        $runtimePath = Join-Path $data 'runtime.json'
        if (Test-Path $runtimePath) {
            $runtime = Get-Content $runtimePath -Raw | ConvertFrom-Json
            if ($runtime.pid -eq $process.Id) {
                $node = @{process=$process;address=('http://'+$runtime.address);session=[Microsoft.PowerShell.Commands.WebRequestSession]::new();headers=@{Origin=('http://'+$runtime.address)}}
                $login = Invoke-TestAPI $node '/api/login' 'POST' @{token=(Get-Content (Join-Path $data 'web-login.token') -Raw).Trim()}
                $node.headers['X-Asterhand-CSRF']=$login.csrf
                return $node
            }
        }
        Start-Sleep -Milliseconds 200
    }
    throw 'Installation node startup timed out.'
    } catch {
        if (-not $process.HasExited) { $process.Kill(); $process.WaitForExit(5000) | Out-Null }
        throw
    }
}
function Stop-TestNode($Node) {
    if ($Node -and -not $Node.process.HasExited) {
        $null = Invoke-TestAPI $Node '/api/stop' 'POST' @{}
        if (-not $Node.process.WaitForExit(10000)) { $Node.process.Kill(); throw 'Test node did not stop gracefully.' }
    }
}
function Test-InstallSequence([string]$Name,[string]$FirstPackage,[bool]$Upgrade) {
    $program = Join-Path $OutputDirectory ($Name+'\程序 空间')
    $data = Join-Path $OutputDirectory ($Name+'\节点 数据')
    $workspace = Join-Path $OutputDirectory ($Name+'\授权 目录')
    $installation = Install-Candidate $FirstPackage $program $data
    $node = $null
    try {
        $node = Start-TestNode $installation
        $before = Invoke-TestAPI $node '/api/info'
        $originalSettings = Invoke-TestAPI $node '/api/settings'
        $originalConfig = $originalSettings.config
        $payload = @{name='Installation contract';endpoint='';root=$workspace;key='synthetic-install-contract-not-a-real-key';delete_key=$false}
        if ($originalConfig.Contains('llm')) {
            $selection = $originalConfig.llm
            $payload.llm = $selection
            $payload.agent_runtime = $originalConfig.agent_runtime
        } else {
            # Legacy API only accepts a flat model. Expected migration comes from
            # the candidate description rather than a provider literal here.
            $catalog = Get-Content (Join-Path (Split-Path -Parent $PSScriptRoot) 'packaging\dependencies.json') -Raw | ConvertFrom-Json
            $target = $catalog.bundle.targets | Where-Object id -eq $catalog.bundle.default_target
            $selection = @{provider=$target.provider;model=$originalConfig.model;credential_ref=$target.credential_ref}
            $payload.model = $originalConfig.model
        }
        $null = Invoke-TestAPI $node '/api/settings' 'POST' $payload
        $credentialFile = Join-Path $data ($selection.credential_ref + '.secret')
        $secretDigest = (Get-FileHash $credentialFile).Hash
        [IO.File]::WriteAllText((Join-Path $data 'installation-sentinel.txt'),'preserve-data')
        & $ps5 -NoProfile -ExecutionPolicy Bypass -File (Join-Path $program 'start.ps1') -NoBrowser
        if ($LASTEXITCODE -ne 0) { throw 'Repeated launcher failed.' }
        $record = Get-Content (Join-Path $data 'runtime.json') -Raw | ConvertFrom-Json
        if ($record.pid -ne $node.process.Id) { throw 'Repeated launcher started a second node.' }
        Stop-TestNode $node
        $installation = Install-Candidate $PackageDirectory $program $data
        $node = Start-TestNode $installation
        $after = Invoke-TestAPI $node '/api/info'
        $settings = Invoke-TestAPI $node '/api/settings'
        if ($after.node_id -ne $before.node_id -or -not $settings.has_key -or (Get-FileHash $credentialFile).Hash -ne $secretDigest) { throw 'Installation lost node identity or credential bytes.' }
        if ((Get-Content (Join-Path $data 'installation-sentinel.txt') -Raw) -ne 'preserve-data') { throw 'Installation changed data sentinel.' }
        if ($settings.config.llm.provider -ne $selection.provider -or $settings.config.llm.model -ne $selection.model -or $settings.config.llm.credential_ref -ne $selection.credential_ref) { throw 'Configuration migration failed.' }
        if ($originalConfig.Contains('agent_runtime') -and $settings.config.agent_runtime -ne $originalConfig.agent_runtime) { throw 'Runtime selection changed.' }
        & $installation.executable doctor --home $data | Set-Content (Join-Path $OutputDirectory ($Name+'-doctor.json')) -Encoding utf8NoBOM
        if ($LASTEXITCODE -ne 0) { throw 'Doctor failed while node was running.' }
        $report.checks += @{name=$Name;status='passed';identity_preserved=$true;credential_bytes_preserved=$true;repeat_launcher_same_pid=$true;upgrade=$Upgrade}
    } finally { Stop-TestNode $node }
}
try {
    & $ps5 -NoProfile -ExecutionPolicy Bypass -File (Join-Path $PSScriptRoot 'test-install-paths.ps1') -Installer (Join-Path $PackageDirectory 'install.ps1') -PackageDirectory $PackageDirectory -OutputDirectory (Join-Path $OutputDirectory 'path-resolution') | Out-Host
    if ($LASTEXITCODE -ne 0) { throw 'Windows PowerShell relative path installation failed.' }
    $report.checks += @{name='path-resolution';status='passed'}
    Test-InstallSequence 'fresh-repeat' $PackageDirectory $false
    if ($PreviousPackageDirectory) { Test-InstallSequence 'upgrade' $PreviousPackageDirectory $true }
    else { $report.checks += @{name='upgrade';status='skipped';reason='PreviousPackageDirectory not supplied.'} }
    $manifest = Get-Content (Join-Path $PackageDirectory 'release-manifest.json') -Raw | ConvertFrom-Json
    $report.package_sha256 = ($manifest.packages | Where-Object os -eq 'windows').sha256
    $report.status='passed'
} catch { $report.status='failed';$report.error=$_.Exception.Message;throw }
finally { $report | ConvertTo-Json -Depth 10 | Set-Content (Join-Path $OutputDirectory 'installation.json') -Encoding utf8NoBOM }
