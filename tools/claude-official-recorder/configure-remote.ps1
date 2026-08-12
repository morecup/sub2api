[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $MyInvocation.MyCommand.Path
$ca = Join-Path $root 'mitmproxy-home\mitmproxy-ca-cert.cer'
$caCertificate = New-Object Security.Cryptography.X509Certificates.X509Certificate2($ca)

# Inject the proxy only into Claude Code. Do not set a user-wide HTTP proxy and do
# not change Anthropic auth/base-URL variables.
$settingsPath = Join-Path $env:USERPROFILE '.claude\settings.json'
if (Test-Path -LiteralPath $settingsPath) {
    $settings = Get-Content -LiteralPath $settingsPath -Raw -Encoding UTF8 | ConvertFrom-Json
} else {
    New-Item -ItemType Directory -Path (Split-Path -Parent $settingsPath) -Force | Out-Null
    $settings = [pscustomobject]@{}
}
if (-not $settings.env) {
    $settings | Add-Member -NotePropertyName env -NotePropertyValue ([pscustomobject]@{}) -Force
}

$desiredEnvironment = [ordered]@{
    HTTP_PROXY = 'http://127.0.0.1:18080'
    HTTPS_PROXY = 'http://127.0.0.1:18080'
    NO_PROXY = 'localhost,127.0.0.1,::1'
    NODE_EXTRA_CA_CERTS = (Join-Path $root 'mitmproxy-home\mitmproxy-ca-cert.pem')
}
$settingsChanged = $false
foreach ($entry in $desiredEnvironment.GetEnumerator()) {
    if ($settings.env.($entry.Key) -ne $entry.Value) {
        $settings.env | Add-Member -NotePropertyName $entry.Key -NotePropertyValue $entry.Value -Force
        $settingsChanged = $true
    }
}
if ($settingsChanged) {
    if (Test-Path -LiteralPath $settingsPath) {
        $backup = $settingsPath + '.backup-claude-recorder-' + (Get-Date -Format 'yyyyMMdd-HHmmss')
        Copy-Item -LiteralPath $settingsPath -Destination $backup
    }
    $settings | ConvertTo-Json -Depth 100 | Set-Content -LiteralPath $settingsPath -Encoding UTF8
}

# Raw OAuth credentials are stored, so only this Administrator account and SYSTEM
# may read the record, CA private-key, and runtime directories.
$currentSid = [Security.Principal.WindowsIdentity]::GetCurrent().User
$systemSid = New-Object Security.Principal.SecurityIdentifier('S-1-5-18')
foreach ($directory in @(
    (Join-Path $root 'records'),
    (Join-Path $root 'mitmproxy-home'),
    (Join-Path $root 'runtime')
)) {
    $acl = New-Object Security.AccessControl.DirectorySecurity
    $acl.SetAccessRuleProtection($true, $false)
    foreach ($sid in @($currentSid, $systemSid)) {
        $rule = New-Object Security.AccessControl.FileSystemAccessRule(
            $sid,
            'FullControl',
            'ContainerInherit,ObjectInherit',
            'None',
            'Allow'
        )
        [void]$acl.AddAccessRule($rule)
    }
    Set-Acl -LiteralPath $directory -AclObject $acl
}

# Remove the failed preload experiment. Claude 2.1.220 ignores BUN_PRELOAD.
foreach ($name in @(
    'BUN_PRELOAD',
    'CLAUDE_REQUEST_RECORD_ROOT',
    'CLAUDE_REQUEST_RECORD_DIR',
    'CLAUDE_REQUEST_RECORDER_TARGET_EXE',
    'CLAUDE_REQUEST_RECORD_SECRETS'
)) {
    [Environment]::SetEnvironmentVariable($name, $null, 'User')
}

$taskName = 'ClaudeCodeRequestRecorderProxy'
$runner = Join-Path $root 'run-proxy.ps1'
$taskArgs = '-NoLogo -NoProfile -NonInteractive -WindowStyle Hidden -ExecutionPolicy Bypass -File "' + $runner + '"'
$action = New-ScheduledTaskAction `
    -Execute 'powershell.exe' `
    -Argument $taskArgs `
    -WorkingDirectory $root
$trigger = New-ScheduledTaskTrigger -AtStartup
$principal = New-ScheduledTaskPrincipal `
    -UserId 'SYSTEM' `
    -LogonType ServiceAccount `
    -RunLevel Highest
$taskSettings = New-ScheduledTaskSettingsSet `
    -AllowStartIfOnBatteries `
    -DontStopIfGoingOnBatteries `
    -ExecutionTimeLimit ([TimeSpan]::Zero) `
    -RestartCount 999 `
    -RestartInterval (New-TimeSpan -Minutes 1) `
    -MultipleInstances IgnoreNew

Register-ScheduledTask `
    -TaskName $taskName `
    -Action $action `
    -Trigger $trigger `
    -Principal $principal `
    -Settings $taskSettings `
    -Description 'Local HTTPS capture proxy dedicated to Claude Code request recording' `
    -Force | Out-Null

# Replace the temporary Administrator-owned proxy with the persistent SYSTEM task.
$listener = Get-NetTCPConnection -State Listen -LocalPort 18080 -ErrorAction SilentlyContinue |
    Select-Object -First 1
if ($listener) {
    $owner = Get-Process -Id $listener.OwningProcess -ErrorAction Stop
    $expected = Join-Path $root 'bin\mitmdump.exe'
    if (-not [string]::Equals($owner.Path, $expected, [StringComparison]::OrdinalIgnoreCase)) {
        throw "Port 18080 owned by unexpected process: $($owner.Path)"
    }
    Stop-Process -Id $owner.Id -Force
    for ($index = 0; $index -lt 20; $index++) {
        Start-Sleep -Milliseconds 250
        if (-not (Get-NetTCPConnection -State Listen -LocalPort 18080 -ErrorAction SilentlyContinue)) {
            break
        }
    }
}

Start-ScheduledTask -TaskName $taskName
$ready = $false
for ($index = 0; $index -lt 40; $index++) {
    Start-Sleep -Milliseconds 500
    if (Get-NetTCPConnection -State Listen -LocalPort 18080 -ErrorAction SilentlyContinue) {
        $ready = $true
        break
    }
}

$task = Get-ScheduledTask -TaskName $taskName
$newListener = Get-NetTCPConnection -State Listen -LocalPort 18080 -ErrorAction SilentlyContinue |
    Select-Object -First 1

[ordered]@{
    CertificateThumbprint = $caCertificate.Thumbprint
    CertificateTrust = 'Claude-only NODE_EXTRA_CA_CERTS'
    TaskName = $taskName
    TaskState = [string]$task.State
    ProxyReady = $ready
    ProxyPid = if ($newListener) { $newListener.OwningProcess } else { $null }
    RecordAclProtected = (Get-Acl (Join-Path $root 'records')).AreAccessRulesProtected
    RawSecrets = $true
    ClaudeSettingsChanged = $settingsChanged
} | ConvertTo-Json
