[CmdletBinding()]
param(
    [string]$Prompt = 'Reply with exactly DESKTOP_RECORDER_OK',
    [string]$OutputPath,
    [string]$ScreenshotPath
)

$ErrorActionPreference = 'Stop'
$toolRoot = Split-Path -Parent $MyInvocation.MyCommand.Path
if (-not $OutputPath) {
    $OutputPath = Join-Path $toolRoot 'runtime\ui-send-result.json'
}
if (-not $ScreenshotPath) {
    $ScreenshotPath = Join-Path $toolRoot 'runtime\ui-after-send.png'
}

Add-Type -AssemblyName UIAutomationClient
Add-Type -AssemblyName UIAutomationTypes
Add-Type -AssemblyName System.Windows.Forms
Add-Type -AssemblyName System.Drawing
Add-Type @'
using System;
using System.Runtime.InteropServices;
public static class ClaudeRecorderSendWindowApi {
    [DllImport("user32.dll")]
    public static extern bool SetForegroundWindow(IntPtr hWnd);
    [DllImport("user32.dll")]
    public static extern bool ShowWindowAsync(IntPtr hWnd, int nCmdShow);
}
'@

function Get-RecordedClaudeMainProcess {
    Get-CimInstance Win32_Process -Filter "Name = 'Claude.exe'" |
        Where-Object {
            $_.ExecutablePath -like 'C:\Program Files\WindowsApps\Claude_*\app\Claude.exe' -and
            $_.CommandLine -notmatch '(?:^|\s)--type=' -and
            $_.CommandLine -like '*--proxy-server=http://127.0.0.1:18081*'
        } |
        Select-Object -First 1
}

function Get-ClaudeWindow {
    param([int]$ProcessId)

    $condition = New-Object Windows.Automation.PropertyCondition(
        [Windows.Automation.AutomationElement]::ProcessIdProperty,
        $ProcessId
    )
    $windows = [Windows.Automation.AutomationElement]::RootElement.FindAll(
        [Windows.Automation.TreeScope]::Children,
        $condition
    )
    if ($windows.Count -eq 0) {
        return $null
    }
    @($windows | Sort-Object {
        if ($_.Current.IsOffscreen) { 1 } else { 0 }
    })[0]
}

function Get-TextPatternText {
    param([Windows.Automation.AutomationElement]$Element)

    $pattern = $null
    if ($Element.TryGetCurrentPattern(
        [Windows.Automation.TextPattern]::Pattern,
        [ref]$pattern
    )) {
        return [string]$pattern.DocumentRange.GetText(1000)
    }
    return $null
}

function Save-DesktopScreenshot {
    param([string]$Path)

    $bounds = [Windows.Forms.SystemInformation]::VirtualScreen
    $bitmap = New-Object Drawing.Bitmap($bounds.Width, $bounds.Height)
    $graphics = [Drawing.Graphics]::FromImage($bitmap)
    try {
        $graphics.CopyFromScreen(
            $bounds.Left,
            $bounds.Top,
            0,
            0,
            $bitmap.Size,
            [Drawing.CopyPixelOperation]::SourceCopy
        )
        $bitmap.Save($Path, [Drawing.Imaging.ImageFormat]::Png)
    } finally {
        $graphics.Dispose()
        $bitmap.Dispose()
    }
}

$startedAt = (Get-Date).ToString('o')
$requestCountBefore = @(Get-ChildItem (Join-Path $toolRoot 'records') `
    -Filter request.json `
    -File `
    -Recurse `
    -ErrorAction SilentlyContinue).Count
$main = Get-RecordedClaudeMainProcess
if (-not $main) {
    throw 'The recorded Claude Desktop main process is not running.'
}

$nativeProcess = Get-Process -Id $main.ProcessId
$handle = $nativeProcess.MainWindowHandle
if ($handle -ne [IntPtr]::Zero) {
    [void][ClaudeRecorderSendWindowApi]::ShowWindowAsync($handle, 9)
    [void][ClaudeRecorderSendWindowApi]::SetForegroundWindow($handle)
}
Start-Sleep -Milliseconds 600

$window = Get-ClaudeWindow -ProcessId $main.ProcessId
if (-not $window) {
    throw 'Claude Desktop has no accessible top-level window.'
}
try { $window.SetFocus() } catch {}

$elements = $window.FindAll(
    [Windows.Automation.TreeScope]::Descendants,
    [Windows.Automation.Condition]::TrueCondition
)
$promptElement = $null
for ($index = 0; $index -lt $elements.Count; $index++) {
    $candidate = $elements.Item($index)
    if (
        $candidate.Current.Name -eq 'Prompt' -and
        $candidate.Current.ClassName -like 'tiptap ProseMirror*' -and
        -not $candidate.Current.IsOffscreen -and
        $candidate.Current.IsEnabled
    ) {
        $promptElement = $candidate
        break
    }
}
if (-not $promptElement) {
    throw 'The visible Code Prompt element was not found.'
}

$promptTextBefore = Get-TextPatternText -Element $promptElement
$promptElement.SetFocus()
Start-Sleep -Milliseconds 300

# The inspected composer is empty. Clear any race-created draft, then type through
# the real keyboard path so ProseMirror receives normal input events.
[Windows.Forms.SendKeys]::SendWait('^a')
[Windows.Forms.SendKeys]::SendWait('{BACKSPACE}')
[Windows.Forms.SendKeys]::SendWait($Prompt)
Start-Sleep -Milliseconds 900

$window = Get-ClaudeWindow -ProcessId $main.ProcessId
$elements = $window.FindAll(
    [Windows.Automation.TreeScope]::Descendants,
    [Windows.Automation.Condition]::TrueCondition
)
$sendElement = $null
$updatedPromptElement = $null
for ($index = 0; $index -lt $elements.Count; $index++) {
    $candidate = $elements.Item($index)
    if (
        -not $updatedPromptElement -and
        $candidate.Current.Name -eq 'Prompt' -and
        $candidate.Current.ClassName -like 'tiptap ProseMirror*'
    ) {
        $updatedPromptElement = $candidate
    }
    if (
        -not $sendElement -and
        $candidate.Current.Name -eq 'Send' -and
        $candidate.Current.ControlType -eq [Windows.Automation.ControlType]::Button -and
        -not $candidate.Current.IsOffscreen
    ) {
        $sendElement = $candidate
    }
}
if (-not $sendElement) {
    throw 'The Code Send button was not found after typing.'
}

$promptTextAfterTyping = if ($updatedPromptElement) {
    Get-TextPatternText -Element $updatedPromptElement
} else {
    $null
}
$sendWasEnabled = $sendElement.Current.IsEnabled
$sendMethod = $null
if ($sendWasEnabled) {
    $invoke = $null
    if ($sendElement.TryGetCurrentPattern(
        [Windows.Automation.InvokePattern]::Pattern,
        [ref]$invoke
    )) {
        $invoke.Invoke()
        $sendMethod = 'InvokePattern'
    }
}
if (-not $sendMethod) {
    $promptElement.SetFocus()
    [Windows.Forms.SendKeys]::SendWait('{ENTER}')
    $sendMethod = 'EnterKeyFallback'
}

Start-Sleep -Seconds 2
Save-DesktopScreenshot -Path $ScreenshotPath
$requestCountAfter = @(Get-ChildItem (Join-Path $toolRoot 'records') `
    -Filter request.json `
    -File `
    -Recurse `
    -ErrorAction SilentlyContinue).Count

[ordered]@{
    StartedAt = $startedAt
    CompletedAt = (Get-Date).ToString('o')
    ProcessId = $main.ProcessId
    SessionId = $main.SessionId
    Prompt = $Prompt
    PromptTextBefore = $promptTextBefore
    PromptTextAfterTyping = $promptTextAfterTyping
    SendWasEnabled = $sendWasEnabled
    SendMethod = $sendMethod
    RequestCountBefore = $requestCountBefore
    RequestCountAfterTwoSeconds = $requestCountAfter
    ScreenshotPath = $ScreenshotPath
} | ConvertTo-Json -Depth 6 | Set-Content -LiteralPath $OutputPath -Encoding UTF8
