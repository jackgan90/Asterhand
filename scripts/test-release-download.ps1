#requires -Version 5.1
[CmdletBinding()]
param(
    [Parameter(Mandatory)][ValidatePattern('^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$')][string]$Repository,
    [Parameter(Mandatory)][ValidatePattern('^[A-Za-z0-9][A-Za-z0-9._-]*$')][string]$Tag,
    [Parameter(Mandatory)][string]$OutputDirectory,
    [switch]$SkipTailscale,
    [switch]$RequireFreshDependencies,
    [switch]$RequireFreshVCRuntime,
    [string]$InstallerPath
)
$ErrorActionPreference = 'Stop'
$OutputDirectory = $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($OutputDirectory)
if (Test-Path -LiteralPath $OutputDirectory) { throw 'Choose a new installation test directory.' }
if ($RequireFreshDependencies -and $SkipTailscale) { throw 'Fresh-system verification must install Tailscale.' }
New-Item -ItemType Directory -Path $OutputDirectory | Out-Null
$program = Join-Path $OutputDirectory 'program'
$data = Join-Path $OutputDirectory 'data'
$installer = Join-Path $OutputDirectory 'install.ps1'
$reportPath = Join-Path $OutputDirectory 'download-install.json'
$report = [ordered]@{status='running';repository=$Repository;tag=$Tag;os=[Environment]::OSVersion.VersionString;ps_version=$PSVersionTable.PSVersion.ToString();fresh_node_and_tailscale=[bool]$RequireFreshDependencies;tailscale='not_checked';vc_runtime_before='absent';vc_runtime_after='not_checked';configuration='not_checked';repeat_install='not_checked'}
$report.installer_source = if ($InstallerPath) { 'local_candidate' } else { 'release_asset' }
$savedPath = $env:PATH
$savedProgress = $ProgressPreference
try {
    $ProgressPreference = 'SilentlyContinue'
    [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
    $systemDirectory = if ([Environment]::Is64BitProcess) { 'System32' } else { 'Sysnative' }
    $vcLibrary = Join-Path $env:SystemRoot ($systemDirectory + '/vcruntime140.dll')
    if (Test-Path -LiteralPath $vcLibrary) { $report.vc_runtime_before = (Get-Item -LiteralPath $vcLibrary).VersionInfo.FileVersion }
    if ($RequireFreshVCRuntime -and $report.vc_runtime_before -ne 'absent') { throw 'Fresh VC++ verification requires the x64 runtime DLL to be absent before installation.' }
    if ($RequireFreshDependencies) {
        if (Test-Path (Join-Path $env:ProgramFiles 'Tailscale/tailscale.exe')) { throw 'Fresh-system test requires Tailscale to be absent before installation.' }
        if (Test-Path (Join-Path $env:LOCALAPPDATA 'Programs/Asterhand')) { throw 'Fresh-system test requires no existing Asterhand installation.' }
    }
    # The installer must work without finding any preinstalled developer tools.
    $env:PATH = "$env:SystemRoot\System32;$env:SystemRoot;$env:SystemRoot\System32\WindowsPowerShell\v1.0"
    foreach ($tool in @('go', 'node', 'npm', 'git', 'gh')) {
        if (Get-Command $tool -CommandType Application -ErrorAction SilentlyContinue) { throw "Developer tool unexpectedly available to installer: $tool" }
    }
    $report.developer_tools_on_path = 'absent'
    if ($InstallerPath) { Copy-Item -LiteralPath $InstallerPath -Destination $installer }
    else { Invoke-WebRequest "https://github.com/$Repository/releases/download/$Tag/install.ps1" -OutFile $installer -UseBasicParsing }
    $report.installer_sha256 = (Get-FileHash -LiteralPath $installer).Hash.ToLowerInvariant()
    $ps5 = Join-Path $env:SystemRoot 'System32/WindowsPowerShell/v1.0/powershell.exe'
    $arguments = @('-NoProfile','-NonInteractive','-ExecutionPolicy','Bypass','-File',$installer,'-Repository',$Repository,'-ReleaseTag',$Tag,'-InstallDir',$program,'-DataDir',$data)
    if ((Get-Command $installer).Parameters.ContainsKey('DesktopShortcut')) { $arguments += @('-DesktopShortcut', 'Skip') }
    if ($SkipTailscale) { $arguments += '-SkipTailscale' }
    & $ps5 @arguments | Tee-Object -FilePath (Join-Path $OutputDirectory 'first-install.log') | Out-Host
    if ($LASTEXITCODE -ne 0) { throw 'Downloaded installer failed.' }
    $installation = Get-Content (Join-Path $program 'installation.json') -Raw | ConvertFrom-Json
    if (@(Get-ChildItem -LiteralPath $data -Force).Count -ne 0) { throw 'Installation unexpectedly initialized node configuration.' }
    $report.configuration = 'not_initialized'
    & $installation.executable bundle-check --directory (Split-Path -Parent $installation.executable) | Out-Host
    if ($LASTEXITCODE -ne 0) { throw 'Downloaded bundle failed component checks.' }
    $catalog = Get-Content (Join-Path (Split-Path -Parent $installation.executable) 'packaging/dependencies.json') -Raw | ConvertFrom-Json
    if (-not (Test-Path -LiteralPath $vcLibrary)) { throw 'Microsoft VC++ runtime is missing after installation.' }
    $report.vc_runtime_after = (Get-Item -LiteralPath $vcLibrary).VersionInfo.FileVersion
    if ([version]$report.vc_runtime_after -lt [version]$catalog.vc_runtime.version) { throw 'Microsoft VC++ runtime is below the pinned requirement.' }
    if ($SkipTailscale) { $report.tailscale = 'skipped' }
    else {
        $tailscale = Join-Path $env:ProgramFiles 'Tailscale/tailscale.exe'
        if (-not (Test-Path $tailscale)) { throw 'Tailscale is missing after installation.' }
        $report.tailscale = 'installed'
        if ($RequireFreshDependencies) {
            $savedError = $ErrorActionPreference
            try { $ErrorActionPreference='Continue'; $network = & $tailscale status --json 2>$null | ConvertFrom-Json }
            finally { $ErrorActionPreference=$savedError }
            if ($network.BackendState -notin @('NeedsLogin', 'NoState', 'Stopped')) { throw 'Fresh Tailscale unexpectedly authenticated during installation.' }
            $report.tailscale_login = $network.BackendState
        }
    }
    $sentinel = Join-Path $data 'preserve-on-repeat.txt'
    [IO.File]::WriteAllText($sentinel, 'installation must preserve existing node data')
    $before = (Get-FileHash -LiteralPath $sentinel).Hash
    & $ps5 @arguments | Tee-Object -FilePath (Join-Path $OutputDirectory 'repeat-install.log') | Out-Host
    if ($LASTEXITCODE -ne 0 -or (Get-FileHash -LiteralPath $sentinel).Hash -ne $before) { throw 'Repeated installation failed to preserve data.' }
    if (@(Get-ChildItem -LiteralPath $data -Force).Count -ne 1) { throw 'Repeated installation unexpectedly initialized configuration.' }
    $report.repeat_install = 'passed'
    $report.package_sha256 = $installation.package_sha256
    $report.status = 'passed'
} catch {
    $report.status = 'failed'
    $report.error = $_.Exception.Message
    throw
} finally {
    $env:PATH = $savedPath
    $ProgressPreference = $savedProgress
    $report | ConvertTo-Json -Depth 5 | Set-Content -LiteralPath $reportPath -Encoding UTF8
    Get-Content -LiteralPath $reportPath | Out-Host
}
