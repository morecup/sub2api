[CmdletBinding()]
param(
    [string]$OutputPath,
    [string]$ScreenshotPath
)

$ErrorActionPreference = 'Stop'
$toolRoot = Split-Path -Parent $MyInvocation.MyCommand.Path
if (-not $OutputPath) {
    $OutputPath = Join-Path $toolRoot 'runtime\ui-tree.json'
}
if (-not $ScreenshotPath) {
    $ScreenshotPath = Join-Path $toolRoot 'runtime\ui-screen.png'
}

Add-Type -AssemblyName UIAutomationClient
Add-Type -AssemblyName UIAutomationTypes
Add-Type -AssemblyName System.Windows.Forms
Add-Type -AssemblyName System.Drawing
Add-Type @'
using System;
using System.Runtime.InteropServices;
public static class ClaudeRecorderWindowApi {
    [DllImport("user32.dll")]
    public static extern bool SetForegroundWindow(IntPtr hWnd);
    [DllImport("user32.dll")]
    public static extern bool ShowWindowAsync(IntPtr hWnd, int nCmdShow);
}
'@

$main = Get-CimInstance Win32_Process -Filter "Name = 'Claude.exe'" |
    Where-Object {
        $_.ExecutablePath -like 'C:\Program Files\WindowsApps\Claude_*\app\Claude.exe' -and
        $_.CommandLine -notmatch '(?:^|\s)--type=' -and
        $_.CommandLine -like '*--proxy-server=http://127.0.0.1:18081*'
    } |
    Select-Object -First 1
if (-not $main) {
    throw 'The recorded Claude Desktop main process is not running.'
}

$process = Get-Process -Id $main.ProcessId
$handle = $process.MainWindowHandle
if ($handle -ne [IntPtr]::Zero) {
    [void][ClaudeRecorderWindowApi]::ShowWindowAsync($handle, 9)
    [void][ClaudeRecorderWindowApi]::SetForegroundWindow($handle)
    Start-Sleep -Milliseconds 500
}

$processCondition = New-Object Windows.Automation.PropertyCondition(
    [Windows.Automation.AutomationElement]::ProcessIdProperty,
    [int]$main.ProcessId
)
$windows = [Windows.Automation.AutomationElement]::RootElement.FindAll(
    [Windows.Automation.TreeScope]::Children,
    $processCondition
)
if ($windows.Count -eq 0 -and $handle -ne [IntPtr]::Zero) {
    $window = [Windows.Automation.AutomationElement]::FromHandle($handle)
} else {
    $window = @($windows | Sort-Object {
        if ($_.Current.IsOffscreen) { 1 } else { 0 }
    })[0]
}
if (-not $window) {
    throw 'Claude Desktop has no accessible top-level window.'
}

try {
    $windowPattern = $null
    if ($window.TryGetCurrentPattern(
        [Windows.Automation.WindowPattern]::Pattern,
        [ref]$windowPattern
    )) {
        $windowPattern.SetWindowVisualState(
            [Windows.Automation.WindowVisualState]::Normal
        )
    }
    $window.SetFocus()
} catch {
    # Element enumeration below is still useful when Windows denies focus stealing.
}

$elements = $window.FindAll(
    [Windows.Automation.TreeScope]::Descendants,
    [Windows.Automation.Condition]::TrueCondition
)
$rows = New-Object Collections.Generic.List[object]
for ($index = 0; $index -lt $elements.Count; $index++) {
    $element = $elements.Item($index)
    try {
        $current = $element.Current
        $patterns = @($element.GetSupportedPatterns() | ForEach-Object {
            $_.ProgrammaticName
        })
        $value = $null
        $valuePattern = $null
        if ($element.TryGetCurrentPattern(
            [Windows.Automation.ValuePattern]::Pattern,
            [ref]$valuePattern
        )) {
            $value = [string]$valuePattern.Current.Value
            if ($value.Length -gt 500) {
                $value = $value.Substring(0, 500)
            }
        }
        $text = $null
        $textPattern = $null
        if ($element.TryGetCurrentPattern(
            [Windows.Automation.TextPattern]::Pattern,
            [ref]$textPattern
        )) {
            $text = [string]$textPattern.DocumentRange.GetText(500)
        }
        $rectangle = $current.BoundingRectangle
        $rows.Add([ordered]@{
            Index = $index
            Name = $current.Name
            AutomationId = $current.AutomationId
            ClassName = $current.ClassName
            FrameworkId = $current.FrameworkId
            ControlType = $current.ControlType.ProgrammaticName
            LocalizedControlType = $current.LocalizedControlType
            IsEnabled = $current.IsEnabled
            IsOffscreen = $current.IsOffscreen
            IsKeyboardFocusable = $current.IsKeyboardFocusable
            HasKeyboardFocus = $current.HasKeyboardFocus
            Rectangle = [ordered]@{
                X = $rectangle.X
                Y = $rectangle.Y
                Width = $rectangle.Width
                Height = $rectangle.Height
            }
            Patterns = $patterns
            Value = $value
            Text = $text
        })
    } catch {
        # Ignore elements invalidated while the Electron renderer is updating.
    }
}

$screenshotError = $null
try {
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
        $bitmap.Save($ScreenshotPath, [Drawing.Imaging.ImageFormat]::Png)
    } finally {
        $graphics.Dispose()
        $bitmap.Dispose()
    }
} catch {
    $screenshotError = $_.Exception.Message
}

[ordered]@{
    CapturedAt = (Get-Date).ToString('o')
    ProcessId = $main.ProcessId
    SessionId = $main.SessionId
    MainWindowHandle = [long]$handle
    WindowName = $window.Current.Name
    WindowClass = $window.Current.ClassName
    ElementCount = $rows.Count
    ScreenshotPath = if (Test-Path -LiteralPath $ScreenshotPath) {
        $ScreenshotPath
    } else {
        $null
    }
    ScreenshotError = $screenshotError
    Elements = $rows
} | ConvertTo-Json -Depth 10 | Set-Content -LiteralPath $OutputPath -Encoding UTF8
