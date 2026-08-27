# Forge Binary Installer for Windows
# Usage: iwr -useb https://raw.githubusercontent.com/JBraunsmaJr/Forge/main/scripts/install.ps1 | iex

$ErrorActionPreference = 'Stop'

$repo = "JBraunsmaJr/Forge"
$binName = "forge.exe"
$installDir = "$HOME\bin"

# Detect Architecture
$arch = if ($Is64BitOperatingSystem) { "amd64" } else { "386" }
if ([System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture -eq 'Arm64') {
    $arch = "arm64"
}

Write-Host "--- Installing Forge for windows/$arch ---" -ForegroundColor Cyan

# Ensure install directory exists
if (-not (Test-Path $installDir)) {
    New-Item -ItemType Directory -Path $installDir | Out-Null
}

# Add to path for current session
if ($env:Path -notlike "*$installDir*") {
    $env:Path += ";$installDir"
    # Permanent add
    [Environment]::SetEnvironmentVariable("Path", [Environment]::GetEnvironmentVariable("Path", "User") + ";$installDir", "User")
    Write-Host "Added $installDir to your PATH" -ForegroundColor Gray
}

# Get latest release tag
try {
    $release = Invoke-RestMethod -Uri "https://api.github.com/repos/$repo/releases/latest"
    $tag = $release.tag_name
} catch {
    $tag = "latest"
}

$downloadUrl = "https://github.com/$repo/releases/download/$tag/forge-windows-$arch.exe"
$dest = "$installDir\$binName"

Write-Host "Downloading from: $downloadUrl"
try {
    Invoke-WebRequest -Uri $downloadUrl -OutFile $dest
} catch {
    Write-Error "Failed to download binary. It might not be released yet.`nYou can build it manually: go build ./cmd/forge"
    return
}

Write-Host "--- Forge installed successfully! ---" -ForegroundColor Green
Write-Host "Run 'forge --help' to get started."
