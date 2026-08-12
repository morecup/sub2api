[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'
$toolRoot = Split-Path -Parent $MyInvocation.MyCommand.Path
$settingsPath = Join-Path $env:USERPROFILE '.claude\settings.json'
$proxy = 'http://127.0.0.1:18080'
$ca = Join-Path $toolRoot 'mitmproxy-home\mitmproxy-ca-cert.pem'

if (Test-Path -LiteralPath $settingsPath) {
    $settings = Get-Content -LiteralPath $settingsPath -Raw -Encoding UTF8 | ConvertFrom-Json
    if ($settings.env) {
        if ($settings.env.HTTP_PROXY -eq $proxy) { $settings.env.PSObject.Properties.Remove('HTTP_PROXY') }
        if ($settings.env.HTTPS_PROXY -eq $proxy) { $settings.env.PSObject.Properties.Remove('HTTPS_PROXY') }
        if ($settings.env.NO_PROXY -eq 'localhost,127.0.0.1,::1') { $settings.env.PSObject.Properties.Remove('NO_PROXY') }
        if ($settings.env.NODE_EXTRA_CA_CERTS -eq $ca) { $settings.env.PSObject.Properties.Remove('NODE_EXTRA_CA_CERTS') }
        if (@($settings.env.PSObject.Properties).Count -eq 0) {
            $settings.PSObject.Properties.Remove('env')
        }
    }
    $settings | ConvertTo-Json -Depth 100 | Set-Content -LiteralPath $settingsPath -Encoding UTF8
}

$taskName = 'ClaudeCodeRequestRecorderProxy'
if (Get-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue) {
    Stop-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue
    Unregister-ScheduledTask -TaskName $taskName -Confirm:$false
}

$listener = Get-NetTCPConnection -State Listen -LocalPort 18080 -ErrorAction SilentlyContinue |
    Select-Object -First 1
if ($listener) {
    $owner = Get-Process -Id $listener.OwningProcess -ErrorAction Stop
    $expected = Join-Path $toolRoot 'bin\mitmdump.exe'
    if ([string]::Equals($owner.Path, $expected, [StringComparison]::OrdinalIgnoreCase)) {
        Stop-Process -Id $owner.Id -Force
    }
}

[ordered]@{
    Enabled = $false
    RecordsPreserved = (Join-Path $toolRoot 'records')
    Note = 'Restart running claude.exe processes to remove their already-loaded proxy environment.'
} | ConvertTo-Json
