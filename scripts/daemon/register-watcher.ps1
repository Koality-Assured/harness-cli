<#
.SYNOPSIS
    Configures and registers the Harness Claim Watcher daemon as a Windows Scheduled Task.

.DESCRIPTION
    Periodically executes 'harness claim watch' to proactively detect and warn on stale worktree
    claims across all registered domain harnesses.

.PARAMETER TaskName
    Name of the scheduled task (default: "HarnessClaimWatcher").

.PARAMETER IntervalMinutes
    Polling repetition interval in minutes (default: 15).

.PARAMETER StaleHours
    Threshold in hours before a claim is considered stale (default: 24).

.PARAMETER HarnessPath
    Path to harness.exe executable. If omitted, searches user PATH, Scoop shims, and local AppData.

.PARAMETER Unregister
    If specified, removes the registered scheduled task.

.PARAMETER Status
    If specified, queries and displays the current state of the scheduled task.

.EXAMPLE
    .\register-watcher.ps1 -IntervalMinutes 15 -StaleHours 24
    .\register-watcher.ps1 -Status
    .\register-watcher.ps1 -Unregister
#>

[CmdletBinding()]
param(
    [string]$TaskName = "HarnessClaimWatcher",
    [int]$IntervalMinutes = 15,
    [double]$StaleHours = 24.0,
    [string]$HarnessPath = "",
    [switch]$Unregister,
    [switch]$Status
)

$ErrorActionPreference = "Stop"

if ($Status) {
    $task = Get-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue
    if ($null -eq $task) {
        Write-Host "Scheduled task '$TaskName' is NOT registered." -ForegroundColor Yellow
        exit 0
    }
    Write-Host "=== Harness Claim Watcher Task Status ===" -ForegroundColor Cyan
    Write-Host "Task Name:   $($task.TaskName)"
    Write-Host "State:       $($task.State)"
    Write-Host "Description: $($task.Description)"
    $info = Get-ScheduledTaskInfo -TaskName $TaskName -ErrorAction SilentlyContinue
    if ($null -ne $info) {
        Write-Host "Last Run:    $($info.LastRunTime) (Result: $($info.LastTaskResult))"
        Write-Host "Next Run:    $($info.NextRunTime)"
    }
    exit 0
}

if ($Unregister) {
    $existing = Get-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue
    if ($null -eq $existing) {
        Write-Host "Scheduled task '$TaskName' is not registered; nothing to remove." -ForegroundColor Yellow
        exit 0
    }
    Unregister-ScheduledTask -TaskName $TaskName -Confirm:$false
    Write-Host "Successfully unregistered scheduled task '$TaskName'." -ForegroundColor Green
    exit 0
}

# Resolve harness.exe binary path
if ([string]::IsNullOrWhiteSpace($HarnessPath)) {
    $candidates = @(
        (Get-Command harness -ErrorAction SilentlyContinue | Select-Object -ExpandProperty Source -ErrorAction SilentlyContinue),
        "$env:USERPROFILE\scoop\shims\harness.exe",
        "$env:LOCALAPPDATA\Programs\harness\harness.exe",
        (Join-Path $PSScriptRoot "..\..\harness.exe")
    )
    foreach ($cand in $candidates) {
        if (![string]::IsNullOrWhiteSpace($cand) -and (Test-Path $cand)) {
            $HarnessPath = (Resolve-Path $cand).Path
            break
        }
    }
}

if ([string]::IsNullOrWhiteSpace($HarnessPath) -or !(Test-Path $HarnessPath)) {
    Write-Error "Unable to locate harness.exe. Please pass -HarnessPath or ensure harness is installed."
    exit 1
}

Write-Host "Configuring Harness Claim Watcher using: $HarnessPath" -ForegroundColor Cyan
Write-Host "Interval: ${IntervalMinutes}m | Stale Threshold: ${StaleHours}h"

$argument = "claim watch --interval ${IntervalMinutes}m --stale-hours $StaleHours"
$action = New-ScheduledTaskAction -Execute $HarnessPath -Argument $argument

# Trigger repeating every IntervalMinutes indefinitely starting at logon
$trigger = New-ScheduledTaskTrigger -AtLogOn
$settings = New-ScheduledTaskSettingsSet `
    -AllowStartIfOnBatteries `
    -DontStopIfGoingOnBatteries `
    -StartWhenAvailable `
    -RestartCount 3 `
    -RestartInterval (New-TimeSpan -Minutes 1) `
    -ExecutionTimeLimit (New-TimeSpan -Days 0)

# Unregister previous instance if present
Unregister-ScheduledTask -TaskName $TaskName -Confirm:$false -ErrorAction SilentlyContinue

Register-ScheduledTask `
    -TaskName $TaskName `
    -Action $action `
    -Trigger $trigger `
    -Settings $settings `
    -Description "Harness CLI claim watcher daemon monitoring active worktree claims across domain harnesses" | Out-Null

Write-Host "Successfully registered scheduled task '$TaskName'." -ForegroundColor Green
Write-Host "Run '.\register-watcher.ps1 -Status' to verify or '.\register-watcher.ps1 -Unregister' to remove."
