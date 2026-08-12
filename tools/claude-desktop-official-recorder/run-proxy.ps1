[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'
$toolRoot = Split-Path -Parent $MyInvocation.MyCommand.Path
$mitmdump = Join-Path $toolRoot 'bin\mitmdump.exe'
$addon = Join-Path $toolRoot 'mitm_request_recorder.py'
$confdir = Join-Path $toolRoot 'mitmproxy-home'
$runtime = Join-Path $toolRoot 'runtime'

New-Item -ItemType Directory -Path $confdir -Force | Out-Null
New-Item -ItemType Directory -Path $runtime -Force | Out-Null

if (-not (Test-Path -LiteralPath $mitmdump -PathType Leaf)) {
    throw "mitmdump not found: $mitmdump"
}
if (-not (Test-Path -LiteralPath $addon -PathType Leaf)) {
    throw "Recorder addon not found: $addon"
}

$arguments = @(
    '--quiet',
    '--listen-host', '127.0.0.1',
    '--listen-port', '18081',
    '--set', "confdir=$confdir",
    '--set', 'connection_strategy=lazy',
    '--scripts', $addon
)

& $mitmdump @arguments 1>> (Join-Path $runtime 'proxy-stdout.log') 2>> (Join-Path $runtime 'proxy-stderr.log')
exit $LASTEXITCODE
