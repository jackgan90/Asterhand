param([Parameter(Mandatory)][string]$Path)
$ErrorActionPreference = 'Stop'
# Test-only sharing lock. The parent releases it by closing stdin.
$lease = [IO.File]::Open($Path, [IO.FileMode]::Open, [IO.FileAccess]::Read, [IO.FileShare]::Read)
try {
    [Console]::WriteLine('locked')
    [Console]::ReadLine() | Out-Null
} finally {
    $lease.Dispose()
}
