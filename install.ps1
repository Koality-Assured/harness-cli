# Standalone PowerShell Installer for Harness CLI on Windows
$ErrorActionPreference = "Stop"

param(
    [string]$Version = ""
)

$repo = "Koality-Assured/harness-cli"
$installDir = "$env:LOCALAPPDATA\Programs\harness"

Write-Host "=== Installing Harness CLI on Windows ===" -ForegroundColor Cyan

$arch = if ([System.Environment]::Is64BitOperatingSystem) {
    if ($env:PROCESSOR_ARCHITECTURE -eq "ARM64") { "arm64" } else { "amd64" }
} else {
    Write-Error "Unsupported 32-bit Windows architecture."
}

if ($Version) {
    $latestTag = if ($Version.StartsWith("v")) { $Version } else { "v$Version" }
    $versionStr = $latestTag.TrimStart("v")
} else {
    $releaseUrl = "https://api.github.com/repos/$repo/releases/latest"
    try {
        $release = Invoke-RestMethod -Uri $releaseUrl -UseBasicParsing
        $latestTag = $release.tag_name
        $versionStr = $latestTag.TrimStart("v")
    } catch {
        Write-Error "Failed to fetch latest release from GitHub API. Please specify a -Version or check your connection."
        exit 1
    }
}

$archiveName = "harness_${versionStr}_windows_${arch}.zip"
$downloadUrl = "https://github.com/$repo/releases/download/$latestTag/$archiveName"
$checksumsUrl = "https://github.com/$repo/releases/download/$latestTag/checksums.txt"

$tmpDir = [System.IO.Path]::Combine([System.IO.Path]::GetTempPath(), [System.Guid]::NewGuid().ToString())
New-Item -ItemType Directory -Path $tmpDir -Force | Out-Null

try {
    $zipPath = Join-Path $tmpDir $archiveName
    Write-Host "Downloading $downloadUrl..."
    Invoke-WebRequest -Uri $downloadUrl -OutFile $zipPath -UseBasicParsing

    $checksumsPath = Join-Path $tmpDir "checksums.txt"
    Write-Host "Downloading checksums.txt..."
    Invoke-WebRequest -Uri $checksumsUrl -OutFile $checksumsPath -UseBasicParsing

    $expectedHashLine = Select-String -Path $checksumsPath -Pattern $archiveName | Select-Object -ExpandProperty Line
    if (-not $expectedHashLine) {
        Write-Error "Checksum for $archiveName not found in checksums.txt"
        exit 1
    }
    $expectedHash = $expectedHashLine.Split(' ', [StringSplitOptions]::RemoveEmptyEntries)[0]
    $actualHash = (Get-FileHash -Path $zipPath -Algorithm SHA256).Hash.ToLower()
    if ($actualHash -ne $expectedHash) {
        Write-Error "Checksum verification failed! Expected: $expectedHash, Actual: $actualHash"
        exit 1
    }
    Write-Host "Checksum verified." -ForegroundColor Green

    Write-Host "Extracting archive..."
    Expand-Archive -Path $zipPath -DestinationPath $tmpDir -Force

    New-Item -ItemType Directory -Path $installDir -Force | Out-Null
    Copy-Item -Path (Join-Path $tmpDir "harness.exe") -Destination (Join-Path $installDir "harness.exe") -Force

    Write-Host "Successfully installed harness to $installDir\harness.exe" -ForegroundColor Green

    # Add to User PATH if not present
    $userPath = [Environment]::GetEnvironmentVariable("PATH", "User")
    if ($userPath -notlike "*$installDir*") {
        [Environment]::SetEnvironmentVariable("PATH", "$userPath;$installDir", "User")
        Write-Host "Added $installDir to User PATH." -ForegroundColor Green
    }
} finally {
    Remove-Item -Path $tmpDir -Recurse -Force -ErrorAction SilentlyContinue
}
