[CmdletBinding()]
param(
    [switch]$NoPopup
)

$ErrorActionPreference = 'Stop'
$toolRoot = Split-Path -Parent $MyInvocation.MyCommand.Path
$proxyUrl = 'http://127.0.0.1:18081'
$proxyArgument = "--proxy-server=$proxyUrl"
$launchLog = Join-Path $toolRoot 'runtime\launch-events.jsonl'

function Write-LaunchEvent {
    param(
        [string]$Event,
        [hashtable]$Details = @{}
    )

    $entry = [ordered]@{
        At = (Get-Date).ToString('o')
        Event = $Event
        User = [Security.Principal.WindowsIdentity]::GetCurrent().Name
        Details = $Details
    }
    $entry | ConvertTo-Json -Compress -Depth 8 | Add-Content -LiteralPath $launchLog -Encoding UTF8
}

function Show-RecorderMessage {
    param(
        [string]$Text,
        [string]$Title = 'Claude Desktop Request Recorder'
    )

    if ($NoPopup) {
        return
    }
    try {
        $shell = New-Object -ComObject WScript.Shell
        [void]$shell.Popup($Text, 15, $Title, 0x30)
    } catch {
        # The launch log remains available when no interactive desktop is attached.
    }
}

function Test-ProxyPort {
    $client = New-Object Net.Sockets.TcpClient
    try {
        $connect = $client.BeginConnect('127.0.0.1', 18081, $null, $null)
        if (-not $connect.AsyncWaitHandle.WaitOne(1500)) {
            return $false
        }
        $client.EndConnect($connect)
        return $true
    } catch {
        return $false
    } finally {
        $client.Dispose()
    }
}

if (-not (Test-ProxyPort)) {
    try {
        Start-ScheduledTask -TaskName 'ClaudeDesktopRequestRecorderProxy'
        Start-Sleep -Seconds 2
    } catch {
        Write-LaunchEvent -Event 'proxy_start_failed' -Details @{ Error = $_.Exception.Message }
    }
}
if (-not (Test-ProxyPort)) {
    $message = 'The dedicated recorder proxy is not listening on 127.0.0.1:18081.'
    Write-LaunchEvent -Event 'proxy_unavailable'
    Show-RecorderMessage -Text $message
    throw $message
}

$package = Get-AppxPackage -Name 'Claude' |
    Sort-Object Version -Descending |
    Select-Object -First 1
if (-not $package) {
    $package = Get-AppxPackage -AllUsers -Name 'Claude' |
        Sort-Object Version -Descending |
        Select-Object -First 1
}
if (-not $package) {
    $message = 'The official Claude MSIX package is not installed.'
    Write-LaunchEvent -Event 'package_missing'
    Show-RecorderMessage -Text $message
    throw $message
}

$claudeExe = Join-Path $package.InstallLocation 'app\Claude.exe'
if (-not (Test-Path -LiteralPath $claudeExe -PathType Leaf)) {
    $message = "Claude.exe is missing: $claudeExe"
    Write-LaunchEvent -Event 'executable_missing' -Details @{ Path = $claudeExe }
    Show-RecorderMessage -Text $message
    throw $message
}

$mainProcesses = @(Get-CimInstance Win32_Process -Filter "Name = 'Claude.exe'" |
    Where-Object {
        [string]::Equals($_.ExecutablePath, $claudeExe, [StringComparison]::OrdinalIgnoreCase) -and
        $_.CommandLine -notmatch '(?:^|\s)--type='
    })
$unrecorded = @($mainProcesses | Where-Object {
    $_.CommandLine -notlike "*$proxyArgument*"
})
if ($unrecorded.Count -gt 0) {
    $message = 'Claude Desktop is already running without the recorder. Fully quit it, then open Claude (Recorded).'
    Write-LaunchEvent -Event 'blocked_by_unrecorded_instance' -Details @{
        ProcessIds = @($unrecorded | Select-Object -ExpandProperty ProcessId)
    }
    Show-RecorderMessage -Text $message
    throw $message
}

$env:HTTP_PROXY = $proxyUrl
$env:HTTPS_PROXY = $proxyUrl
$env:NO_PROXY = 'localhost,127.0.0.1,::1'
$env:NODE_EXTRA_CA_CERTS = Join-Path $toolRoot 'mitmproxy-home\mitmproxy-ca-cert.pem'

$process = Start-Process -FilePath $claudeExe -ArgumentList @($proxyArgument) -PassThru
Write-LaunchEvent -Event 'launched' -Details @{
    ProcessId = $process.Id
    Executable = $claudeExe
    Proxy = $proxyUrl
}

[ordered]@{
    Started = $true
    ProcessId = $process.Id
    Executable = $claudeExe
    Proxy = $proxyUrl
} | ConvertTo-Json
