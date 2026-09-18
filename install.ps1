# Standalone PowerShell Installer for Harness CLI on Windows
$ErrorActionPreference = "Stop"

$repo = "Koality-Assured/harness-cli"
$installDir = "$env:LOCALAPPDATA\Programs\harness"

Write-Host "=== Installing Harness CLI on Windows ===" -ForegroundColor Cyan

$arch = if ([System.Environment]::Is64BitOperatingSystem) {
    if ($env:PROCESSOR_ARCHITECTURE -eq "ARM64") { "arm64" } else { "amd64" }
} else {
    Write-Error "Unsupported 32-bit Windows architecture."
}

$releaseUrl = "https://api.github.com/repos/$repo/releases/latest"
try {
    $release = Invoke-RestMethod -Uri $releaseUrl -UseBasicParsing
    $latestTag = $release.tag_name
    $version = $latestTag.TrimStart("v")
} catch {
    $latestTag = "v0.1.0"
    $version = "0.1.0"
}

$archiveName = "harness_${version}_windows_${arch}.zip"
$downloadUrl = "https://github.com/$repo/releases/download/$latestTag/$archiveName"

$tmpDir = [System.IO.Path]::Combine([System.IO.Path]::GetTempPath(), [System.Guid]::NewGuid().ToString())
New-Item -ItemType Directory -Path $tmpDir -Force | Out-Null

try {
    $zipPath = Join-Path $tmpDir $archiveName
    Write-Host "Downloading $downloadUrl..."
    Invoke-WebRequest -Uri $downloadUrl -OutFile $zipPath -UseBasicParsing

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
