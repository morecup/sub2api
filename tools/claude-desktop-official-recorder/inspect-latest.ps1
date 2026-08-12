[CmdletBinding()]
param(
    [int]$Limit = 50,
    [switch]$ShowBody
)

$ErrorActionPreference = 'Stop'
$toolRoot = Split-Path -Parent $MyInvocation.MyCommand.Path
$recordRoot = Join-Path $toolRoot 'records'
$allRequestFiles = @(Get-ChildItem -LiteralPath $recordRoot -Filter request.json -File -Recurse -ErrorAction Stop |
    Sort-Object LastWriteTimeUtc -Descending)
$requestFiles = @($allRequestFiles | Select-Object -First $Limit)
Write-Host "Total request count: $($allRequestFiles.Count); showing newest $($requestFiles.Count)"

$rows = foreach ($file in $requestFiles) {
    $request = Get-Content -LiteralPath $file.FullName -Raw -Encoding UTF8 | ConvertFrom-Json
    $responseFile = Join-Path $file.DirectoryName 'response.json'
    $response = if (Test-Path -LiteralPath $responseFile) {
        Get-Content -LiteralPath $responseFile -Raw -Encoding UTF8 | ConvertFrom-Json
    } else {
        $null
    }

    [pscustomobject]@{
        Time = $request.captured_at
        Method = $request.method
        Status = if ($response) { $response.status_code } else { $null }
        Bytes = $request.body.bytes
        Url = $request.url
        Directory = $file.DirectoryName
    }

    if ($ShowBody) {
        $bodyFile = Join-Path $file.DirectoryName 'request-body.txt'
        if (Test-Path -LiteralPath $bodyFile) {
            Write-Host "`n--- $($request.method) $($request.url) ---"
            Get-Content -LiteralPath $bodyFile -Raw -Encoding UTF8
        }
    }
}

$rows | Format-Table -AutoSize -Wrap
