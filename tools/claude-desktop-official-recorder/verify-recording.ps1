[CmdletBinding()]
param(
    [int]$Limit = 20
)

$ErrorActionPreference = 'Stop'
$toolRoot = Split-Path -Parent $MyInvocation.MyCommand.Path
$recordRoot = Join-Path $toolRoot 'records'
$proxyArgument = '--proxy-server=http://127.0.0.1:18081'

$listener = Get-NetTCPConnection -State Listen -LocalPort 18081 -ErrorAction SilentlyContinue |
    Select-Object -First 1
$mainProcesses = @(Get-CimInstance Win32_Process -Filter "Name = 'Claude.exe'" |
    Where-Object {
        $_.ExecutablePath -like 'C:\Program Files\WindowsApps\Claude_*\app\Claude.exe' -and
        $_.CommandLine -notmatch '(?:^|\s)--type='
    } |
    ForEach-Object {
        [ordered]@{
            ProcessId = $_.ProcessId
            ExecutablePath = $_.ExecutablePath
            RecorderProxyArgument = $_.CommandLine -like "*$proxyArgument*"
            CommandLine = $_.CommandLine
        }
    })

$requests = @(Get-ChildItem -LiteralPath $recordRoot -Filter request.json -File -Recurse -ErrorAction SilentlyContinue |
    Sort-Object LastWriteTimeUtc -Descending)
$recent = @($requests | Select-Object -First $Limit | ForEach-Object {
    $request = Get-Content -LiteralPath $_.FullName -Raw -Encoding UTF8 | ConvertFrom-Json
    $responsePath = Join-Path $_.DirectoryName 'response.json'
    $errorPath = Join-Path $_.DirectoryName 'error.json'
    $response = if (Test-Path -LiteralPath $responsePath) {
        Get-Content -LiteralPath $responsePath -Raw -Encoding UTF8 | ConvertFrom-Json
    } else {
        $null
    }
    [ordered]@{
        CapturedAt = $request.captured_at
        Method = $request.method
        Url = $request.url
        BodyBytes = $request.body.bytes
        Status = if ($response) { $response.status_code } else { $null }
        Error = Test-Path -LiteralPath $errorPath
        Directory = $_.DirectoryName
    }
})

$caPath = Join-Path $toolRoot 'mitmproxy-home\mitmproxy-ca-cert.cer'
$ca = if (Test-Path -LiteralPath $caPath) {
    New-Object Security.Cryptography.X509Certificates.X509Certificate2($caPath)
} else {
    $null
}
$caTrusted = if ($ca) {
    Test-Path -LiteralPath "Cert:\LocalMachine\Root\$($ca.Thumbprint)"
} else {
    $false
}

[ordered]@{
    ProxyTaskState = [string](Get-ScheduledTask -TaskName 'ClaudeDesktopRequestRecorderProxy').State
    ProxyListening = [bool]$listener
    ProxyPid = if ($listener) { $listener.OwningProcess } else { $null }
    CertificateThumbprint = if ($ca) { $ca.Thumbprint } else { $null }
    CertificateTrustedInLocalMachineRoot = $caTrusted
    ClaudeMainProcesses = $mainProcesses
    TotalRequests = $requests.Count
    RecentRequests = $recent
} | ConvertTo-Json -Depth 8
