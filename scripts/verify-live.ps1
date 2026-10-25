#requires -Version 7.0
[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$NodeHome,
    [Parameter(Mandatory)][string]$Address,
    [Parameter(Mandatory)][string]$ExpectedSourceDigest,
    [Parameter(Mandatory)][string]$OutputPath
)
$ErrorActionPreference = 'Stop'
$uri = [uri]$Address
if ($uri.Scheme -ne 'http' -or $uri.Host -notin @('127.0.0.1','[::1]','::1') -or $uri.AbsolutePath -ne '/' -or $uri.UserInfo -or $uri.Query -or $uri.Fragment) {
    throw 'Live verification only accepts a local loopback Web address.'
}
$Address = $Address.TrimEnd('/')
$session = [Microsoft.PowerShell.Commands.WebRequestSession]::new()
$headers = @{Origin=$Address}
function Invoke-Local([string]$Path,[string]$Method='GET',$Body=$null) {
    $params = @{Uri=($Address+$Path);Method=$Method;WebSession=$session;Headers=$headers;TimeoutSec=20}
    if ($null -ne $Body) { $params.ContentType='application/json'; $params.Body=[Text.Encoding]::UTF8.GetBytes(($Body | ConvertTo-Json -Depth 12 -Compress)) }
    $response = Invoke-WebRequest @params
    return ConvertFrom-Json -InputObject $response.Content -AsHashtable
}
# Read only the local Web login token. The model key stays inside the node process.
$login = Invoke-Local '/api/login' 'POST' @{token=(Get-Content (Join-Path $NodeHome 'web-login.token') -Raw).Trim()}
$headers['X-Asterhand-CSRF'] = $login.csrf
$info = Invoke-Local '/api/info'
if ($info.source_sha256 -ne $ExpectedSourceDigest) { throw 'The running node was built from different source; restart the candidate before validation.' }
$settings = Invoke-Local '/api/settings'
if (-not $settings.has_key) { throw 'Configure the local model credential in the Web settings first.' }
$projects = @($settings.config.projects)
if ($projects.Count -ne 1) { throw 'Live verification requires one configured authorized project.' }
$project = $projects[0]
$existing = @(@(Invoke-Local '/api/jobs') | Where-Object { $_.state -notin @('succeeded','failed','cancelled','completed_unverified') })
if ($existing.Count -gt 0) { throw 'Finish or reconcile existing node tasks before this isolated live verification.' }
$report = [ordered]@{status='running';created_utc=[DateTime]::UtcNow.ToString('o');source_sha256=$info.source_sha256;bundle_identity=$info.bundle_identity;runtime=$settings.config.agent_runtime;model=$settings.config.llm;checks=@()}
$startedRoutes = [Collections.Generic.List[string]]::new()

function Test-FileResult($Job,[string]$Path) {
    if ($Job.state -ne 'succeeded') { throw "File execution did not succeed: $($Job.state) $($Job.error)" }
    $bytes = [IO.File]::ReadAllBytes($Path)
    if ($bytes.Length -ne 11 -or [Text.Encoding]::UTF8.GetString($bytes) -cne 'hello world') { throw 'Actual file bytes differ from exact UTF-8 hello world.' }
    $hash = (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant()
    if (@($Job.artifacts | Where-Object { $_.sha256 -eq $hash -and $_.size -eq 11 }).Count -ne 1) { throw 'Persisted artifact does not match the actual file.' }
    return @{job_id=$Job.id;attempt=$Job.attempt;path=$Path;size=11;sha256=$hash;status='passed'}
}

function Invoke-FileScenario([string]$Kind) {
    $filename = 'asterhand-' + $Kind + '-' + [guid]::NewGuid().ToString('N') + '.txt'
    $path = Join-Path $project.root $filename
    $goal = "在本机授权目录创建 $filename，内容精确为 hello world，UTF-8，无 BOM，无换行。"
    if ($Kind -ne 'file') { $goal = "在本机授权目录创建 $filename。执行节点必须先调用 asterhand_ask_user 询问文件内容，等我回答以后才能创建。把我的回答作为全部文件内容原样写入 UTF-8，不添加 BOM 或换行。请把询问要求转交执行节点，不要在路由阶段询问。" }
    $route = Invoke-Local '/api/jobs' 'POST' @{request_id=[guid]::NewGuid().ToString('N');project_id=$project.id;goal=$goal;start_before=[DateTime]::UtcNow.AddMinutes(15).ToString('o');target=$info.node_id;target_explicit=$true}
    $startedRoutes.Add($route.id)
$report.active_scenario = @{kind=$Kind;path=$path;route_id=$route.id}
    $deadline = [DateTime]::UtcNow.AddMinutes(8)
    $answered = [Collections.Generic.HashSet[string]]::new()
    $executeQuestions = 0
    $cancelled = $false
    while ([DateTime]::UtcNow -lt $deadline) {
        $route = Invoke-Local ('/api/jobs/'+$route.id)
        if ($route.state -in @('failed','cancelled','reconciliation_required')) { throw "Routing failed: $($route.error)" }
        $outboxes = @(Invoke-Local '/api/outbox')
        $outbox = $outboxes | Where-Object { $_.id -eq $route.id } | Select-Object -First 1
        $job = if ($outbox -and $outbox['job']) { Invoke-Local ('/api/jobs/'+$outbox.job.id) } else { $route }
        if ($job.state -eq 'waiting_input' -and $job['interaction'] -and $answered.Add($job.interaction.id)) {
            if (Test-Path -LiteralPath $path) { throw 'File was created before the question was answered.' }
            if ($job.request.mode -eq 'execute') { $executeQuestions++ }
            if ($Kind -eq 'cancel' -and $job.request.mode -eq 'execute') {
                $null = Invoke-Local ('/api/jobs/'+$job.id+'/cancel') 'POST' @{}
                $cancelled = $true
            } else {
                $answer = if ($job.request.mode -eq 'route') { '请转交执行节点，并要求执行节点先询问文件内容。' } else { 'hello world' }
                $null = Invoke-Local ('/api/jobs/'+$job.id+'/answer') 'POST' @{interaction_id=$job.interaction.id;attempt=$job.attempt;answer_id=[guid]::NewGuid().ToString('N');answer=$answer}
            }
            if ($answered.Count -gt 6) { throw 'Model exceeded the verification question budget.' }
        }
        if ($job.request.mode -eq 'execute' -and $job.state -in @('succeeded','failed','cancelled','completed_unverified','reconciliation_required')) {
            if ($Kind -eq 'cancel') {
                if (-not $cancelled -or $job.state -ne 'cancelled' -or (Test-Path -LiteralPath $path)) { throw 'Cancellation contract failed.' }
                return @{kind=$Kind;status='passed';job_id=$job.id;attempt=$job.attempt;file_absent=$true;execute_questions=$executeQuestions}
            }
            if ($Kind -eq 'question' -and $executeQuestions -lt 1) { throw 'Execute stage did not ask the required question.' }
            $result = Test-FileResult $job $path
            $result.kind=$Kind; $result.execute_questions=$executeQuestions
            return $result
        }
        Start-Sleep -Seconds 2
    }
    throw 'Live scenario timed out; cancellation is requested for this test task.'
}

try {
    Write-Host 'Real model: harmless tool probe...'
    $null = Invoke-Local '/api/probe' 'POST' @{}
    $deadline = [DateTime]::UtcNow.AddMinutes(4)
    do {
        Start-Sleep -Seconds 2
        $probe = Invoke-Local '/api/probe'
    } while ($probe.state -eq 'running' -and [DateTime]::UtcNow -lt $deadline)
    if ($probe.state -ne 'passed') { throw "Real probe failed: $($probe.error)" }
    $report.checks += @{kind='probe';status='passed'}
    foreach ($kind in @('file','question','cancel')) {
        Write-Host "Real model: $kind scenario..."
        $report.checks += Invoke-FileScenario $kind
    }
    $report.status='passed'
} catch {
    $report.status='failed'; $report.error=$_.Exception.Message
    foreach ($id in $startedRoutes) {
        try { $null = Invoke-Local ('/api/jobs/'+$id+'/cancel') 'POST' @{} } catch { }
        try { $null = Invoke-Local ('/api/remote/'+$id+'/cancel') 'POST' @{} } catch { }
    }
    throw
} finally {
    $report | ConvertTo-Json -Depth 12 | Set-Content -LiteralPath $OutputPath -Encoding utf8NoBOM
}
Write-Host "Real model verification passed; evidence: $OutputPath"
