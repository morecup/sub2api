[CmdletBinding()]
param(
    [string]$SourceRecorderRoot = 'C:\Users\Administrator\ClaudeCodeRequestRecorder'
)

$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $MyInvocation.MyCommand.Path
$bin = Join-Path $root 'bin'
$mitmdump = Join-Path $bin 'mitmdump.exe'

if (-not (Test-Path -LiteralPath $mitmdump -PathType Leaf)) {
    $sourceBin = Join-Path $SourceRecorderRoot 'bin'
    $sourceMitmdump = Join-Path $sourceBin 'mitmdump.exe'
    if (-not (Test-Path -LiteralPath $sourceMitmdump -PathType Leaf)) {
        throw "Source mitmdump not found: $sourceMitmdump"
    }
    New-Item -ItemType Directory -Path $bin -Force | Out-Null
    Copy-Item -Path (Join-Path $sourceBin '*') -Destination $bin -Recurse -Force
}

foreach ($directory in @(
    (Join-Path $root 'records'),
    (Join-Path $root 'mitmproxy-home'),
    (Join-Path $root 'runtime')
)) {
    New-Item -ItemType Directory -Path $directory -Force | Out-Null
}

# Raw credentials are deliberately retained. Restrict records, the CA private key,
# and runtime logs to this Administrator account and SYSTEM.
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

$proxyTaskName = 'ClaudeDesktopRequestRecorderProxy'
$runner = Join-Path $root 'run-proxy.ps1'

if (Get-ScheduledTask -TaskName $proxyTaskName -ErrorAction SilentlyContinue) {
    Stop-ScheduledTask -TaskName $proxyTaskName -ErrorAction SilentlyContinue
}

$listener = Get-NetTCPConnection -State Listen -LocalPort 18081 -ErrorAction SilentlyContinue |
    Select-Object -First 1
if ($listener) {
    $owner = Get-Process -Id $listener.OwningProcess -ErrorAction Stop
    if (-not [string]::Equals($owner.Path, $mitmdump, [StringComparison]::OrdinalIgnoreCase)) {
        throw "Port 18081 is owned by an unexpected process: $($owner.Path)"
    }
    Stop-Process -Id $owner.Id -Force
    for ($index = 0; $index -lt 20; $index++) {
        Start-Sleep -Milliseconds 250
        if (-not (Get-NetTCPConnection -State Listen -LocalPort 18081 -ErrorAction SilentlyContinue)) {
            break
        }
    }
}

$proxyArguments = '-NoLogo -NoProfile -NonInteractive -WindowStyle Hidden -ExecutionPolicy Bypass -File "' + $runner + '"'
$proxyAction = New-ScheduledTaskAction `
    -Execute 'powershell.exe' `
    -Argument $proxyArguments `
    -WorkingDirectory $root
$proxyTrigger = New-ScheduledTaskTrigger -AtStartup
$proxyPrincipal = New-ScheduledTaskPrincipal `
    -UserId 'SYSTEM' `
    -LogonType ServiceAccount `
    -RunLevel Highest
$proxySettings = New-ScheduledTaskSettingsSet `
    -AllowStartIfOnBatteries `
    -DontStopIfGoingOnBatteries `
    -ExecutionTimeLimit ([TimeSpan]::Zero) `
    -RestartCount 999 `
    -RestartInterval (New-TimeSpan -Minutes 1) `
    -MultipleInstances IgnoreNew

Register-ScheduledTask `
    -TaskName $proxyTaskName `
    -Action $proxyAction `
    -Trigger $proxyTrigger `
    -Principal $proxyPrincipal `
    -Settings $proxySettings `
    -Description 'Local HTTPS capture proxy dedicated to official Claude Desktop request recording' `
    -Force | Out-Null

Start-ScheduledTask -TaskName $proxyTaskName
$proxyReady = $false
$caPath = Join-Path $root 'mitmproxy-home\mitmproxy-ca-cert.cer'
for ($index = 0; $index -lt 60; $index++) {
    Start-Sleep -Milliseconds 500
    if (
        (Get-NetTCPConnection -State Listen -LocalPort 18081 -ErrorAction SilentlyContinue) -and
        (Test-Path -LiteralPath $caPath -PathType Leaf)
    ) {
        $proxyReady = $true
        break
    }
}
if (-not $proxyReady) {
    $stderrPath = Join-Path $root 'runtime\proxy-stderr.log'
    $stderr = if (Test-Path -LiteralPath $stderrPath) {
        Get-Content -LiteralPath $stderrPath -Raw -ErrorAction SilentlyContinue
    } else {
        ''
    }
    throw "Desktop recorder proxy failed to start. $stderr"
}

# Chromium uses the Windows trust store. Import only this recorder's CA, without
# setting a machine-wide HTTP proxy. X509Store avoids certificate-import UI prompts.
$caCertificate = New-Object Security.Cryptography.X509Certificates.X509Certificate2($caPath)
$store = New-Object Security.Cryptography.X509Certificates.X509Store(
    'Root',
    [Security.Cryptography.X509Certificates.StoreLocation]::LocalMachine
)
try {
    $store.Open([Security.Cryptography.X509Certificates.OpenFlags]::ReadWrite)
    $alreadyTrusted = @($store.Certificates | Where-Object {
        $_.Thumbprint -eq $caCertificate.Thumbprint
    }).Count -gt 0
    if (-not $alreadyTrusted) {
        $store.Add($caCertificate)
    }
} finally {
    $store.Close()
}
Set-Content -LiteralPath (Join-Path $root 'runtime\ca-thumbprint.txt') `
    -Value $caCertificate.Thumbprint `
    -Encoding ASCII

$package = Get-AppxPackage -AllUsers -Name 'Claude' |
    Sort-Object Version -Descending |
    Select-Object -First 1
if (-not $package) {
    throw 'The official Claude MSIX package is not installed.'
}
$claudeExe = Join-Path $package.InstallLocation 'app\Claude.exe'
if (-not (Test-Path -LiteralPath $claudeExe -PathType Leaf)) {
    throw "Claude.exe not found: $claudeExe"
}

$launcher = Join-Path $root 'launch-recorded.ps1'
$launcherArguments = '-NoLogo -NoProfile -WindowStyle Hidden -ExecutionPolicy Bypass -File "' + $launcher + '"'
$shell = New-Object -ComObject WScript.Shell
$shortcutPaths = @(
    (Join-Path ([Environment]::GetFolderPath('Desktop')) 'Claude (Recorded).lnk'),
    (Join-Path ([Environment]::GetFolderPath('Programs')) 'Claude (Recorded).lnk')
)
foreach ($shortcutPath in $shortcutPaths) {
    New-Item -ItemType Directory -Path (Split-Path -Parent $shortcutPath) -Force | Out-Null
    $shortcut = $shell.CreateShortcut($shortcutPath)
    $shortcut.TargetPath = "$env:SystemRoot\System32\WindowsPowerShell\v1.0\powershell.exe"
    $shortcut.Arguments = $launcherArguments
    $shortcut.WorkingDirectory = $root
    $shortcut.IconLocation = "$claudeExe,0"
    $shortcut.Description = 'Launch official Claude Desktop through the local request recorder'
    $shortcut.Save()
}

# This interactive logon task makes the recorded instance the primary Electron
# single instance after a server login. It can also be started manually over SSH.
$launchTaskName = 'ClaudeDesktopRecordedLaunch'
$launchAction = New-ScheduledTaskAction `
    -Execute 'powershell.exe' `
    -Argument ($launcherArguments + ' -NoPopup') `
    -WorkingDirectory $root
$launchTrigger = New-ScheduledTaskTrigger `
    -AtLogOn `
    -User ([Security.Principal.WindowsIdentity]::GetCurrent().Name)
try {
    $launchTrigger.Delay = 'PT10S'
} catch {
    # Delay is optional on older Task Scheduler PowerShell bindings.
}
$launchPrincipal = New-ScheduledTaskPrincipal `
    -UserId ([Security.Principal.WindowsIdentity]::GetCurrent().Name) `
    -LogonType Interactive `
    -RunLevel Highest
$launchSettings = New-ScheduledTaskSettingsSet `
    -AllowStartIfOnBatteries `
    -DontStopIfGoingOnBatteries `
    -ExecutionTimeLimit (New-TimeSpan -Minutes 2) `
    -MultipleInstances IgnoreNew `
    -StartWhenAvailable
Register-ScheduledTask `
    -TaskName $launchTaskName `
    -Action $launchAction `
    -Trigger $launchTrigger `
    -Principal $launchPrincipal `
    -Settings $launchSettings `
    -Description 'Launch official Claude Desktop with the dedicated request recorder at interactive logon' `
    -Force | Out-Null

$newListener = Get-NetTCPConnection -State Listen -LocalPort 18081 -ErrorAction SilentlyContinue |
    Select-Object -First 1
[ordered]@{
    Root = $root
    PackageVersion = [string]$package.Version
    ClaudeExecutable = $claudeExe
    ProxyTask = $proxyTaskName
    ProxyTaskState = [string](Get-ScheduledTask -TaskName $proxyTaskName).State
    ProxyReady = $proxyReady
    ProxyPid = if ($newListener) { $newListener.OwningProcess } else { $null }
    Proxy = 'http://127.0.0.1:18081'
    CertificateScope = 'LocalMachine\Root'
    CertificateThumbprint = $caCertificate.Thumbprint
    CertificateAlreadyTrusted = $alreadyTrusted
    LaunchTask = $launchTaskName
    Shortcuts = $shortcutPaths
    RawSecrets = $true
    RecordsAclProtected = (Get-Acl (Join-Path $root 'records')).AreAccessRulesProtected
} | ConvertTo-Json -Depth 6
