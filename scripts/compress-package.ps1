#requires -Version 7.0
param([Parameter(Mandatory)][string]$Directory, [Parameter(Mandatory)][string]$Archive)
$ErrorActionPreference = 'Stop'
# The parent reports elapsed-time heartbeats while this blocking native operation runs.
[IO.Compression.ZipFile]::CreateFromDirectory($Directory, $Archive, [IO.Compression.CompressionLevel]::Optimal, $false)
