#requires -Version 5.1
[CmdletBinding()]
param(
    [string]$Go = 'go',
    [string]$OutputDirectory
)
$ErrorActionPreference = 'Stop'
$repo = Split-Path -Parent $PSScriptRoot
if (-not $OutputDirectory) { $OutputDirectory = Join-Path $repo 'out\desktop-lifecycle\icon-resources' }
$assets = Join-Path $repo 'internal\desktop\assets'
$OutputDirectory = $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($OutputDirectory)
New-Item -ItemType Directory -Path $OutputDirectory -Force | Out-Null
Add-Type -AssemblyName System.Drawing
$report = [ordered]@{status='running';powershell=$PSVersionTable.PSVersion.ToString();checks=@();visual_review='pending'}

function Read-Frames([string]$Path, [int[]]$ExpectedSizes) {
    $bytes = [IO.File]::ReadAllBytes($Path)
    if ([BitConverter]::ToUInt16($bytes, 0) -ne 0 -or [BitConverter]::ToUInt16($bytes, 2) -ne 1) { throw 'Invalid ICO header.' }
    $count = [BitConverter]::ToUInt16($bytes, 4)
    if ($count -ne $ExpectedSizes.Count) { throw 'Unexpected ICO frame count.' }
    $frames = @()
    for ($i = 0; $i -lt $count; $i++) {
        $entry = 6 + 16 * $i
        $size = if ($bytes[$entry] -eq 0) { 256 } else { [int]$bytes[$entry] }
        if ($size -ne $ExpectedSizes[$i] -or $bytes[$entry] -ne $bytes[$entry + 1]) { throw 'Unexpected ICO dimension.' }
        if ([BitConverter]::ToUInt16($bytes, $entry + 4) -ne 1 -or [BitConverter]::ToUInt16($bytes, $entry + 6) -ne 32) { throw 'ICO must use 32-bit RGBA frames.' }
        $length = [BitConverter]::ToUInt32($bytes, $entry + 8)
        $offset = [BitConverter]::ToUInt32($bytes, $entry + 12)
        if ($offset -lt 6 + 16 * $count -or [long]$offset + $length -gt $bytes.Length) { throw 'ICO frame outside file.' }
        $data = [byte[]]::new($length)
        [Array]::Copy($bytes, $offset, $data, 0, $length)
        $stream = [IO.MemoryStream]::new($data)
        $bitmap = [Drawing.Bitmap]::new($stream)
        try {
            if ($bitmap.Width -ne $size -or $bitmap.Height -ne $size) { throw 'Frame size differs from ICO directory.' }
            $visible = 0
            $transparent = 0
            for ($y = 0; $y -lt $size; $y++) {
                for ($x = 0; $x -lt $size; $x++) {
                    $alpha = $bitmap.GetPixel($x, $y).A
                    if ($alpha -eq 0) { $transparent++ }
                    if ($alpha -ge 240) { $visible++ }
                }
            }
            if ($transparent -lt $size * $size * 0.1 -or $visible -lt $size * $size * 0.1) { throw 'Icon transparency or opaque silhouette was lost.' }
            if ($bitmap.GetPixel(0, 0).A -ne 0 -or $bitmap.GetPixel($size - 1, $size - 1).A -ne 0) { throw 'Icon corners must remain transparent.' }
        } finally { $bitmap.Dispose(); $stream.Dispose() }
        $frames += @{size=$size;data=$data}
    }
    return $frames
}

function Write-Preview($Frames) {
    $canvas = [Drawing.Bitmap]::new(840, 280, [Drawing.Imaging.PixelFormat]::Format32bppArgb)
    $graphics = [Drawing.Graphics]::FromImage($canvas)
    $font = [Drawing.Font]::new('Segoe UI', 11)
    try {
        for ($row = 0; $row -lt 2; $row++) {
            $background = if ($row -eq 0) { [Drawing.Color]::FromArgb(245, 246, 248) } else { [Drawing.Color]::FromArgb(22, 27, 34) }
            $foreground = if ($row -eq 0) { [Drawing.Brushes]::Black } else { [Drawing.Brushes]::White }
            $brush = [Drawing.SolidBrush]::new($background)
            try { $graphics.FillRectangle($brush, 0, $row * 140, 840, 140) } finally { $brush.Dispose() }
            $label = if ($row -eq 0) { 'Light' } else { 'Dark' }
            $graphics.DrawString($label, $font, $foreground, 12, $row * 140 + 12)
            for ($i = 0; $i -lt $Frames.Count; $i++) {
                $frame = $Frames[$i]
                $stream = [IO.MemoryStream]::new([byte[]]$frame.data)
                $image = [Drawing.Image]::FromStream($stream)
                try {
                    $x = 110 + $i * 100
                    $graphics.DrawString(($frame.size.ToString() + ' px'), $font, $foreground, $x - 16, $row * 140 + 16)
                    $graphics.DrawImageUnscaled($image, $x, $row * 140 + 56)
                } finally { $image.Dispose(); $stream.Dispose() }
            }
        }
        $canvas.Save((Join-Path $OutputDirectory 'tray-size-preview.png'), [Drawing.Imaging.ImageFormat]::Png)
    } finally { $font.Dispose(); $graphics.Dispose(); $canvas.Dispose() }
}

function Add-FixtureResource([string]$Path) {
    if (-not ('Asterhand.Build.IconFixture' -as [type])) {
        Add-Type -TypeDefinition @'
using System;
using System.IO;
using System.ComponentModel;
using System.Runtime.InteropServices;
namespace Asterhand.Build {
    public static class IconFixture {
        [DllImport("kernel32.dll", CharSet=CharSet.Unicode, SetLastError=true)]
        private static extern IntPtr BeginUpdateResourceW(string file, bool deleteExisting);
        [DllImport("kernel32.dll", SetLastError=true)]
        private static extern bool UpdateResourceW(IntPtr update, IntPtr type, IntPtr name, ushort language, byte[] data, uint length);
        [DllImport("kernel32.dll", SetLastError=true)]
        private static extern bool EndUpdateResourceW(IntPtr update, bool discard);
        public static void Add(string file, byte[] data) {
            IntPtr update = BeginUpdateResourceW(file, false);
            if (update == IntPtr.Zero) throw new Win32Exception(Marshal.GetLastWin32Error());
            if (!UpdateResourceW(update, new IntPtr(10), new IntPtr(71), 0, data, (uint)data.Length)) {
                int error = Marshal.GetLastWin32Error();
                EndUpdateResourceW(update, true);
                throw new Win32Exception(error);
            }
            if (!EndUpdateResourceW(update, false)) throw new Win32Exception(Marshal.GetLastWin32Error());
        }
    }
}
'@
    }
    [Asterhand.Build.IconFixture]::Add($Path, [Text.Encoding]::UTF8.GetBytes('preserve-existing-resource'))
}

try {
    $application = @(Read-Frames (Join-Path $assets 'application.ico') @(16, 20, 24, 32, 48, 64, 96, 128, 256))
    $tray = @(Read-Frames (Join-Path $assets 'tray.ico') @(16, 20, 24, 32, 40, 48, 64))
    for ($i = 0; $i -lt 4; $i++) {
        if ([Convert]::ToBase64String($application[$i].data) -ne [Convert]::ToBase64String($tray[$i].data)) { throw 'Small desktop icons must match the tray variant.' }
    }
    $report.checks += @{name='ico-dimensions-and-alpha';status='passed';application_frames=$application.Count;tray_frames=$tray.Count}
    Write-Preview $tray
    $source = Join-Path $OutputDirectory 'fixture.go'
    $executable = Join-Path $OutputDirectory 'fixture.exe'
    [IO.File]::WriteAllText($source, "package main`nimport `"fmt`"`nfunc main() { fmt.Print(`"asterhand-icon-fixture`") }`n", [Text.UTF8Encoding]::new($false))
    & $Go build -trimpath -o $executable $source
    if ($LASTEXITCODE -ne 0) { throw 'Cannot build the executable fixture.' }
    $report.go = (& $Go version)
    Add-FixtureResource $executable
    & (Join-Path $PSScriptRoot 'set-executable-icon.ps1') -Executable $executable -Icon (Join-Path $assets 'application.ico')
    $group = [Asterhand.Build.IconResources]::Read($executable, 14, 1)
    if ([BitConverter]::ToUInt16($group, 4) -ne $application.Count) { throw 'PE icon group frame count differs.' }
    for ($i = 0; $i -lt $application.Count; $i++) {
        $id = [BitConverter]::ToUInt16($group, 6 + 14 * $i + 12)
        $embedded = [Asterhand.Build.IconResources]::Read($executable, 3, $id)
        if ([Convert]::ToBase64String($embedded) -ne [Convert]::ToBase64String($application[$i].data)) { throw 'PE icon image differs from ICO.' }
    }
    $retained = [Asterhand.Build.IconResources]::Read($executable, 10, 71)
    if ([Text.Encoding]::UTF8.GetString($retained) -ne 'preserve-existing-resource') { throw 'Icon embedding destroyed another resource.' }
    $output = & $executable
    if ($LASTEXITCODE -ne 0 -or $output -ne 'asterhand-icon-fixture') { throw 'Executable no longer runs after icon embedding.' }
    $report.checks += @{name='pe-resource-roundtrip-and-execution';status='passed'}
    $before = (Get-FileHash -LiteralPath $executable -Algorithm SHA256).Hash
    $invalid = Join-Path $OutputDirectory 'invalid.ico'
    [IO.File]::WriteAllBytes($invalid, [byte[]]@(0, 0, 1, 0, 1, 0))
    $rejected = $false
    try { & (Join-Path $PSScriptRoot 'set-executable-icon.ps1') -Executable $executable -Icon $invalid }
    catch { $rejected = $true }
    if (-not $rejected -or (Get-FileHash -LiteralPath $executable -Algorithm SHA256).Hash -ne $before) { throw 'Invalid ICO changed the executable.' }
    $report.checks += @{name='invalid-icon-preserves-executable';status='passed'}
    $report.status = 'passed'
} catch {
    $report.status = 'failed'
    $report.error = $_.Exception.Message
    throw
} finally {
    $report | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath (Join-Path $OutputDirectory 'verification.json') -Encoding UTF8
}
Write-Host 'Icon resource validation passed; review tray-size-preview.png at native scale.'
