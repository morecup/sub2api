[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'
$toolRoot = Split-Path -Parent $MyInvocation.MyCommand.Path
$recordRoot = Join-Path $toolRoot 'records'
$requestFiles = @(Get-ChildItem -LiteralPath $recordRoot -Filter request.json -File -Recurse |
    Sort-Object LastWriteTimeUtc)

$hostCounts = @{}
$methodCounts = @{}
$statusCounts = @{}
$officialCount = 0
$authorizationCount = 0
$cookieCount = 0
$authorizationLengths = @()
$cookieLengths = @()
$rawBodyCount = 0
$textBodyCount = 0
$bodyHashMismatchCount = 0
$errorFileCount = 0
$unredactedCount = 0
$largestRequest = $null

foreach ($requestFile in $requestFiles) {
    $request = Get-Content -LiteralPath $requestFile.FullName -Raw -Encoding UTF8 |
        ConvertFrom-Json
    $hostName = ([Uri]$request.url).DnsSafeHost
    if (-not $hostCounts.ContainsKey($hostName)) { $hostCounts[$hostName] = 0 }
    $hostCounts[$hostName]++
    if (-not $methodCounts.ContainsKey($request.method)) { $methodCounts[$request.method] = 0 }
    $methodCounts[$request.method]++
    if (
        $hostName -eq 'claude.ai' -or
        $hostName.EndsWith('.claude.ai') -or
        $hostName -eq 'anthropic.com' -or
        $hostName.EndsWith('.anthropic.com')
    ) {
        $officialCount++
    }
    if ($request.redacted -eq $false) { $unredactedCount++ }

    # Indexing preserves each two-item nested array on Windows PowerShell 5.1.
    # Sending nested arrays through a pipeline can flatten them into strings.
    for ($headerIndex = 0; $headerIndex -lt $request.headers.Count; $headerIndex++) {
        $header = $request.headers[$headerIndex]
        $name = [string]$header[0]
        $value = [string]$header[1]
        if ($name -ieq 'authorization' -or $name -ieq 'x-api-key') {
            $authorizationCount++
            $authorizationLengths += $value.Length
        }
        if ($name -ieq 'cookie') {
            $cookieCount++
            $cookieLengths += $value.Length
        }
    }

    $bodyBytes = [long]$request.body.bytes
    if (-not $largestRequest -or $bodyBytes -gt $largestRequest.Bytes) {
        $largestRequest = [ordered]@{
            Bytes = $bodyBytes
            Method = $request.method
            Host = $hostName
            Path = ([Uri]$request.url).PathAndQuery
            Directory = $requestFile.DirectoryName
        }
    }
    if ($request.body.raw_file) {
        $rawBodyCount++
        $bodyPath = Join-Path $requestFile.DirectoryName $request.body.raw_file
        if (-not (Test-Path -LiteralPath $bodyPath -PathType Leaf)) {
            $bodyHashMismatchCount++
        } else {
            $bodyFile = Get-Item -LiteralPath $bodyPath
            $hash = (Get-FileHash -LiteralPath $bodyPath -Algorithm SHA256).Hash.ToLowerInvariant()
            if ($bodyFile.Length -ne $bodyBytes -or $hash -ne $request.body.sha256) {
                $bodyHashMismatchCount++
            }
        }
    }
    if ($request.body.text_file) { $textBodyCount++ }

    $responsePath = Join-Path $requestFile.DirectoryName 'response.json'
    if (Test-Path -LiteralPath $responsePath -PathType Leaf) {
        $response = Get-Content -LiteralPath $responsePath -Raw -Encoding UTF8 |
            ConvertFrom-Json
        $status = [string]$response.status_code
        if (-not $statusCounts.ContainsKey($status)) { $statusCounts[$status] = 0 }
        $statusCounts[$status]++
    }
    if (Test-Path -LiteralPath (Join-Path $requestFile.DirectoryName 'error.json')) {
        $errorFileCount++
    }
}

$topHosts = @($hostCounts.GetEnumerator() |
    Sort-Object Value -Descending |
    Select-Object -First 12 |
    ForEach-Object { [ordered]@{ Host = $_.Key; Count = $_.Value } })

$ports = foreach ($port in @(18080, 18081)) {
    $listener = Get-NetTCPConnection -State Listen -LocalPort $port -ErrorAction SilentlyContinue |
        Select-Object -First 1
    [ordered]@{
        Port = $port
        Listening = [bool]$listener
        ProcessId = if ($listener) { $listener.OwningProcess } else { $null }
        ProcessPath = if ($listener) {
            (Get-Process -Id $listener.OwningProcess).Path
        } else {
            $null
        }
    }
}

$tasks = foreach ($taskName in @(
    'ClaudeCodeRequestRecorderProxy',
    'ClaudeDesktopRequestRecorderProxy',
    'ClaudeDesktopRecordedLaunch'
)) {
    $task = Get-ScheduledTask -TaskName $taskName
    $info = Get-ScheduledTaskInfo -TaskName $taskName
    [ordered]@{
        Name = $taskName
        State = [string]$task.State
        LastTaskResult = $info.LastTaskResult
        LastRunTime = $info.LastRunTime.ToString('o')
    }
}

$acls = foreach ($directoryName in @('records', 'mitmproxy-home', 'runtime')) {
    $acl = Get-Acl (Join-Path $toolRoot $directoryName)
    [ordered]@{
        Name = $directoryName
        Protected = $acl.AreAccessRulesProtected
        Identities = @($acl.Access | ForEach-Object {
            $_.IdentityReference.Value
        } | Sort-Object -Unique)
    }
}

$mainProcesses = @(Get-CimInstance Win32_Process -Filter "Name = 'Claude.exe'" |
    Where-Object {
        $_.ExecutablePath -like 'C:\Program Files\WindowsApps\Claude_*\app\Claude.exe' -and
        $_.CommandLine -notmatch '(?:^|\s)--type='
    } |
    ForEach-Object {
        [ordered]@{
            ProcessId = $_.ProcessId
            SessionId = $_.SessionId
            RecorderProxyArgument = $_.CommandLine -like '*--proxy-server=http://127.0.0.1:18081*'
            ExecutablePath = $_.ExecutablePath
        }
    })

$caPath = Join-Path $toolRoot 'mitmproxy-home\mitmproxy-ca-cert.cer'
$ca = New-Object Security.Cryptography.X509Certificates.X509Certificate2($caPath)
$proxyStderrPath = Join-Path $toolRoot 'runtime\proxy-stderr.log'
$proxyStderrBytes = if (Test-Path -LiteralPath $proxyStderrPath) {
    (Get-Item -LiteralPath $proxyStderrPath).Length
} else {
    0
}

[ordered]@{
    RequestCount = $requestFiles.Count
    OfficialClaudeAnthropicRequestCount = $officialCount
    TopHosts = $topHosts
    MethodCounts = $methodCounts
    StatusCounts = $statusCounts
    AuthorizationOrApiKeyHeaderCount = $authorizationCount
    AuthorizationValueLengthRange = if ($authorizationLengths.Count) {
        @((($authorizationLengths | Measure-Object -Minimum).Minimum), (($authorizationLengths | Measure-Object -Maximum).Maximum))
    } else {
        $null
    }
    CookieHeaderCount = $cookieCount
    CookieValueLengthRange = if ($cookieLengths.Count) {
        @((($cookieLengths | Measure-Object -Minimum).Minimum), (($cookieLengths | Measure-Object -Maximum).Maximum))
    } else {
        $null
    }
    RequestsMarkedUnredacted = $unredactedCount
    RawBodyFileCount = $rawBodyCount
    TextBodyFileCount = $textBodyCount
    BodyHashMismatchCount = $bodyHashMismatchCount
    ErrorFileCount = $errorFileCount
    LargestRequest = $largestRequest
    ProxyStderrBytes = $proxyStderrBytes
    CertificateThumbprint = $ca.Thumbprint
    CertificateTrustedInLocalMachineRoot = Test-Path -LiteralPath "Cert:\LocalMachine\Root\$($ca.Thumbprint)"
    Ports = $ports
    Tasks = $tasks
    Acls = $acls
    ClaudeMainProcesses = $mainProcesses
    Shortcuts = @(
        [ordered]@{
            Path = 'C:\Users\Administrator\Desktop\Claude (Recorded).lnk'
            Exists = Test-Path -LiteralPath 'C:\Users\Administrator\Desktop\Claude (Recorded).lnk'
        },
        [ordered]@{
            Path = 'C:\Users\Administrator\AppData\Roaming\Microsoft\Windows\Start Menu\Programs\Claude (Recorded).lnk'
            Exists = Test-Path -LiteralPath 'C:\Users\Administrator\AppData\Roaming\Microsoft\Windows\Start Menu\Programs\Claude (Recorded).lnk'
        }
    )
} | ConvertTo-Json -Depth 10
