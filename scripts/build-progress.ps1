#requires -Version 7.0
# Progress for build-owned processes. Never used to control installed nodes.
function Invoke-BuildProcess {
    param([string]$Label, [string]$Executable, [string[]]$Arguments, [int]$MaximumExitCode = 0, [int]$TimeoutSeconds = 1800)
    Write-Host "[start] $Label"
    $watch = [Diagnostics.Stopwatch]::StartNew()
    $process = [Diagnostics.Process]::new()
    $process.StartInfo = [Diagnostics.ProcessStartInfo]::new($Executable)
    $process.StartInfo.UseShellExecute = $false
    $process.StartInfo.CreateNoWindow = $true
    $process.StartInfo.RedirectStandardOutput = $true
    $process.StartInfo.RedirectStandardError = $true
    foreach ($argument in $Arguments) { $process.StartInfo.ArgumentList.Add($argument) }
    $started = $false
    try {
        $started = $process.Start()
        $streams = @($process.StandardOutput, $process.StandardError)
        $pending = @($streams[0].ReadLineAsync(), $streams[1].ReadLineAsync())
        $nextProgress = 5
        while ($null -ne $pending[0] -or $null -ne $pending[1] -or -not $process.HasExited) {
            for ($i = 0; $i -lt 2; $i++) {
                # Bound each drain so one chatty stream cannot starve the other.
                for ($lineCount = 0; $lineCount -lt 64 -and $null -ne $pending[$i] -and $pending[$i].IsCompleted; $lineCount++) {
                    $line = $pending[$i].GetAwaiter().GetResult()
                    if ($null -eq $line) { $pending[$i] = $null; break }
                    Write-Host $line
                    $pending[$i] = $streams[$i].ReadLineAsync()
                }
            }
            if ($watch.Elapsed.TotalSeconds -ge $TimeoutSeconds) { throw "$Label exceeded $TimeoutSeconds seconds." }
            if ($watch.Elapsed.TotalSeconds -ge $nextProgress) {
                Write-Host ("[running] {0}: {1:n1}s elapsed" -f $Label, $watch.Elapsed.TotalSeconds)
                $nextProgress = $watch.Elapsed.TotalSeconds + 5
            }
            Start-Sleep -Milliseconds 100
        }
        $process.WaitForExit()
        if ($process.ExitCode -lt 0 -or $process.ExitCode -gt $MaximumExitCode) { throw "$Label failed with exit code $($process.ExitCode)." }
        Write-Host ("[done] {0}: {1:n1}s" -f $Label, $watch.Elapsed.TotalSeconds)
    } finally {
        if ($started -and -not $process.HasExited) { $process.Kill($true); $process.WaitForExit() }
        $process.Dispose()
    }
}

function Copy-BuildTree([string]$Source, [string]$Destination) {
    $sourceInfo = Get-Item -LiteralPath $Source
    if ($sourceInfo.Attributes -band [IO.FileAttributes]::ReparsePoint) { throw "Copy source is a reparse point: $Source" }
    $watch = [Diagnostics.Stopwatch]::StartNew()
    $nextProgress = 5
    $count = 0
    $pending = [Collections.Generic.Stack[string]]::new()
    $pending.Push($sourceInfo.FullName)
    while ($pending.Count) {
        $directory = $pending.Pop()
        $relative = [IO.Path]::GetRelativePath($sourceInfo.FullName, $directory)
        $target = Join-Path $Destination $relative
        [IO.Directory]::CreateDirectory($target) | Out-Null
        foreach ($entry in [IO.DirectoryInfo]::new($directory).EnumerateFileSystemInfos()) {
            if ($entry.Attributes -band [IO.FileAttributes]::ReparsePoint) { throw "Copy source contains a reparse point: $($entry.FullName)" }
            if ($entry -is [IO.DirectoryInfo]) { $pending.Push($entry.FullName) }
            else { [IO.File]::Copy($entry.FullName, (Join-Path $target $entry.Name), $false); $count++ }
            if ($watch.Elapsed.TotalSeconds -ge $nextProgress) {
                Write-Host ("[copying] {0}: {1} files, {2:n1}s elapsed" -f $sourceInfo.Name, $count, $watch.Elapsed.TotalSeconds)
                $nextProgress = $watch.Elapsed.TotalSeconds + 5
            }
        }
    }
    Write-Host ("[done] Copied {0}: {1} files, {2:n1}s" -f $sourceInfo.Name, $count, $watch.Elapsed.TotalSeconds)
}
