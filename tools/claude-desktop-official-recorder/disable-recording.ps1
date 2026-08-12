[CmdletBinding()]
param(
    [switch]$StopClaude
)

$ErrorActionPreference = 'Stop'
$toolRoot = Split-Path -Parent $MyInvocation.MyCommand.Path
$proxyArgument = '--proxy-server=http://127.0.0.1:18081'

if ($StopClaude) {
    $desktopProcesses = @(Get-CimInstance Win32_Process -Filter "Name = 'Claude.exe'" |
        Where-Object {
            $_.ExecutablePath -like 'C:\Program Files\WindowsApps\Claude_*\app\Claude.exe' -and
            $_.CommandLine -like "*$proxyArgument*"
        })
    foreach ($process in $desktopProcesses) {
        Stop-Process -Id $process.ProcessId -Force -ErrorAction SilentlyContinue
    }
}

foreach ($taskName in @(
    'ClaudeDesktopRecordedLaunch',
    'ClaudeDesktopRequestRecorderProxy'
)) {
    if (Get-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue) {
        Stop-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue
        Unregister-ScheduledTask -TaskName $taskName -Confirm:$false
    }
}

$listener = Get-NetTCPConnection -State Listen -LocalPort 18081 -ErrorAction SilentlyContinue |
    Select-Object -First 1
if ($listener) {
    $owner = Get-Process -Id $listener.OwningProcess -ErrorAction Stop
    $expected = Join-Path $toolRoot 'bin\mitmdump.exe'
    if ([string]::Equals($owner.Path, $expected, [StringComparison]::OrdinalIgnoreCase)) {
        Stop-Process -Id $owner.Id -Force
    }
}

$caPath = Join-Path $toolRoot 'mitmproxy-home\mitmproxy-ca-cert.cer'
$removedThumbprint = $null
if (Test-Path -LiteralPath $caPath -PathType Leaf) {
    $ca = New-Object Security.Cryptography.X509Certificates.X509Certificate2($caPath)
    $store = New-Object Security.Cryptography.X509Certificates.X509Store(
        'Root',
        [Security.Cryptography.X509Certificates.StoreLocation]::LocalMachine
    )
    try {
        $store.Open([Security.Cryptography.X509Certificates.OpenFlags]::ReadWrite)
        $trusted = @($store.Certificates | Where-Object { $_.Thumbprint -eq $ca.Thumbprint })
        foreach ($certificate in $trusted) {
            $store.Remove($certificate)
            $removedThumbprint = $certificate.Thumbprint
        }
    } finally {
        $store.Close()
    }
}

$launcher = Join-Path $toolRoot 'launch-recorded.ps1'
$shortcutPaths = @(
    (Join-Path ([Environment]::GetFolderPath('Desktop')) 'Claude (Recorded).lnk'),
    (Join-Path ([Environment]::GetFolderPath('Programs')) 'Claude (Recorded).lnk')
)
$shell = New-Object -ComObject WScript.Shell
foreach ($shortcutPath in $shortcutPaths) {
    if (Test-Path -LiteralPath $shortcutPath -PathType Leaf) {
        $shortcut = $shell.CreateShortcut($shortcutPath)
        if ($shortcut.Arguments -like "*$launcher*") {
            Remove-Item -LiteralPath $shortcutPath -Force
        }
    }
}

[ordered]@{
    Enabled = $false
    ClaudeStopped = [bool]$StopClaude
    CertificateRemoved = $removedThumbprint
    RecordsPreserved = (Join-Path $toolRoot 'records')
    Note = if ($StopClaude) {
        'The recorded Claude Desktop process was stopped. The normal Claude entry can now be used.'
    } else {
        'Fully quit the running recorded Claude Desktop instance before using the normal Claude entry.'
    }
} | ConvertTo-Json
