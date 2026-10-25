#requires -Version 5.1
[CmdletBinding()]
param(
    [string]$AssetDirectory
)
$ErrorActionPreference = 'Stop'
if (-not $AssetDirectory) { $AssetDirectory = Join-Path (Split-Path -Parent $PSScriptRoot) 'internal\desktop\assets' }
Add-Type -AssemblyName System.Drawing
$AssetDirectory = $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($AssetDirectory)

function New-IconFrame([string]$Source, [int]$Size) {
    $image = [Drawing.Image]::FromFile($Source)
    $bitmap = [Drawing.Bitmap]::new($Size, $Size, [Drawing.Imaging.PixelFormat]::Format32bppArgb)
    $graphics = [Drawing.Graphics]::FromImage($bitmap)
    $attributes = [Drawing.Imaging.ImageAttributes]::new()
    $stream = [IO.MemoryStream]::new()
    try {
        if ($image.Width -ne $image.Height) { throw 'Icon source must be square.' }
        $graphics.Clear([Drawing.Color]::Transparent)
        $graphics.CompositingMode = [Drawing.Drawing2D.CompositingMode]::SourceCopy
        $graphics.CompositingQuality = [Drawing.Drawing2D.CompositingQuality]::HighQuality
        $graphics.InterpolationMode = [Drawing.Drawing2D.InterpolationMode]::HighQualityBicubic
        $graphics.PixelOffsetMode = [Drawing.Drawing2D.PixelOffsetMode]::HighQuality
        $attributes.SetWrapMode([Drawing.Drawing2D.WrapMode]::TileFlipXY)
        $graphics.DrawImage($image, [Drawing.Rectangle]::new(0, 0, $Size, $Size), 0, 0, $image.Width, $image.Height, [Drawing.GraphicsUnit]::Pixel, $attributes)
        $bitmap.Save($stream, [Drawing.Imaging.ImageFormat]::Png)
        return ,$stream.ToArray()
    } finally {
        $stream.Dispose()
        $attributes.Dispose()
        $graphics.Dispose()
        $bitmap.Dispose()
        $image.Dispose()
    }
}

function Write-Icon([string]$Destination, [int[]]$Sizes, [bool]$TrayOnly) {
    $frames = @()
    foreach ($size in $Sizes) {
        $source = if ($TrayOnly -or $size -le 32) { 'tray.png' } else { 'application.png' }
        $frames += ,(New-IconFrame (Join-Path $AssetDirectory $source) $size)
    }
    $stream = [IO.MemoryStream]::new()
    $writer = [IO.BinaryWriter]::new($stream)
    try {
        $writer.Write([uint16]0)
        $writer.Write([uint16]1)
        $writer.Write([uint16]$Sizes.Count)
        $offset = 6 + 16 * $Sizes.Count
        for ($i = 0; $i -lt $Sizes.Count; $i++) {
            $dimension = if ($Sizes[$i] -eq 256) { 0 } else { $Sizes[$i] }
            $writer.Write([byte]$dimension)
            $writer.Write([byte]$dimension)
            $writer.Write([byte]0)
            $writer.Write([byte]0)
            $writer.Write([uint16]1)
            $writer.Write([uint16]32)
            $writer.Write([uint32]$frames[$i].Length)
            $writer.Write([uint32]$offset)
            $offset += $frames[$i].Length
        }
        foreach ($frame in $frames) { $writer.Write([byte[]]$frame) }
        $writer.Flush()
        [IO.File]::WriteAllBytes($Destination, $stream.ToArray())
    } finally {
        $writer.Dispose()
        $stream.Dispose()
    }
}

Write-Icon (Join-Path $AssetDirectory 'application.ico') @(16, 20, 24, 32, 48, 64, 96, 128, 256) $false
Write-Icon (Join-Path $AssetDirectory 'tray.ico') @(16, 20, 24, 32, 40, 48, 64) $true
Write-Host 'Generated transparent application.ico and tray.ico from their PNG sources.'
