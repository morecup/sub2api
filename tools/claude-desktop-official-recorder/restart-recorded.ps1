[CmdletBinding()]
param(
    [int]$GraceSeconds = 10,
    [switch]$Force
)

$ErrorActionPreference = 'Stop'
$package = Get-AppxPackage -AllUsers -Name 'Claude' |
    Sort-Object Version -Descending |
    Select-Object -First 1
if (-not $package) {
    throw 'The official Claude MSIX package is not installed.'
}
$claudeExe = Join-Path $package.InstallLocation 'app\Claude.exe'
$proxyArgument = '--proxy-server=http://127.0.0.1:18081'

function Get-ClaudeDesktopProcesses {
    @(Get-CimInstance Win32_Process -Filter "Name = 'Claude.exe'" |
        Where-Object {
            [string]::Equals($_.ExecutablePath, $claudeExe, [StringComparison]::OrdinalIgnoreCase)
        })
}

$before = @(Get-ClaudeDesktopProcesses)
$mainProcesses = @($before | Where-Object { $_.CommandLine -notmatch '(?:^|\s)--type=' })
foreach ($main in $mainProcesses) {
    $process = Get-Process -Id $main.ProcessId -ErrorAction SilentlyContinue
    if ($process) {
        [void]$process.CloseMainWindow()
    }
}

$deadline = (Get-Date).AddSeconds($GraceSeconds)
do {
    Start-Sleep -Milliseconds 500
    $remaining = @(Get-ClaudeDesktopProcesses)
} while ($remaining.Count -gt 0 -and (Get-Date) -lt $deadline)

$forcedProcessIds = @()
if ($remaining.Count -gt 0) {
    if (-not $Force) {
        throw "Claude Desktop is still running after $GraceSeconds seconds. Fully quit it or rerun with -Force."
    }
    $forcedProcessIds = @($remaining | Select-Object -ExpandProperty ProcessId)
    foreach ($process in $remaining) {
        Stop-Process -Id $process.ProcessId -Force -ErrorAction SilentlyContinue
    }
    for ($index = 0; $index -lt 20; $index++) {
        Start-Sleep -Milliseconds 250
        if (@(Get-ClaudeDesktopProcesses).Count -eq 0) {
            break
        }
    }
}

Start-ScheduledTask -TaskName 'ClaudeDesktopRecordedLaunch'
$recordedMain = $null
for ($index = 0; $index -lt 60; $index++) {
    Start-Sleep -Milliseconds 500
    $recordedMain = Get-ClaudeDesktopProcesses |
        Where-Object {
            $_.CommandLine -notmatch '(?:^|\s)--type=' -and
            $_.CommandLine -like "*$proxyArgument*"
        } |
        Select-Object -First 1
    if ($recordedMain) {
        break
    }
}
if (-not $recordedMain) {
    throw 'Recorded Claude Desktop did not start in the interactive session.'
}

[ordered]@{
    PreviousProcessCount = $before.Count
    GracefulMainProcessIds = @($mainProcesses | Select-Object -ExpandProperty ProcessId)
    ForcedProcessIds = $forcedProcessIds
    RecordedMainProcessId = $recordedMain.ProcessId
    RecordedMainSessionId = $recordedMain.SessionId
    ProxyArgumentPresent = $recordedMain.CommandLine -like "*$proxyArgument*"
    CommandLine = $recordedMain.CommandLine
} | ConvertTo-Json -Depth 5
