[CmdletBinding()]
param()

$ErrorActionPreference = 'SilentlyContinue'
[Console]::OutputEncoding = [Text.Encoding]::UTF8

$claudeProcesses = Get-CimInstance Win32_Process |
    Where-Object { $_.Name -ieq 'Claude.exe' } |
    ForEach-Object {
        $owner = Invoke-CimMethod -InputObject $_ -MethodName GetOwner
        [ordered]@{
            ProcessId = $_.ProcessId
            ParentProcessId = $_.ParentProcessId
            ExecutablePath = $_.ExecutablePath
            CommandLine = $_.CommandLine
            SessionId = $_.SessionId
            Owner = if ($owner.ReturnValue -eq 0) {
                "$($owner.Domain)\$($owner.User)"
            } else {
                $null
            }
        }
    }

$packages = Get-AppxPackage -AllUsers |
    Where-Object {
        $_.Name -match 'Claude|Anthropic' -or
        $_.PackageFullName -match 'Claude|Anthropic'
    } |
    ForEach-Object {
        $manifest = Get-AppxPackageManifest -Package $_
        [ordered]@{
            Name = $_.Name
            PackageFullName = $_.PackageFullName
            InstallLocation = $_.InstallLocation
            PackageFamilyName = $_.PackageFamilyName
            Version = [string]$_.Version
            Status = [string]$_.Status
            Applications = @($manifest.Package.Applications.Application | ForEach-Object {
                [ordered]@{
                    Id = $_.Id
                    Executable = $_.Executable
                    EntryPoint = $_.EntryPoint
                    StartPage = $_.StartPage
                }
            })
        }
    }

$installCandidates = Get-ChildItem 'C:\Program Files\WindowsApps' -Directory -Filter 'Claude_*' |
    ForEach-Object {
        $exe = Join-Path $_.FullName 'app\Claude.exe'
        [ordered]@{
            Directory = $_.FullName
            Exe = $exe
            ExeExists = Test-Path -LiteralPath $exe
        }
    }

$shortcuts = @()
$shortcutRoots = @(
    [Environment]::GetFolderPath('Desktop'),
    [Environment]::GetFolderPath('CommonDesktopDirectory'),
    [Environment]::GetFolderPath('StartMenu'),
    [Environment]::GetFolderPath('CommonStartMenu'),
    [Environment]::GetFolderPath('Startup'),
    [Environment]::GetFolderPath('CommonStartup')
) | Where-Object { $_ -and (Test-Path -LiteralPath $_) }
$shell = New-Object -ComObject WScript.Shell
foreach ($shortcut in Get-ChildItem -LiteralPath $shortcutRoots -Recurse -Filter '*.lnk') {
    if ($shortcut.Name -match 'Claude') {
        $link = $shell.CreateShortcut($shortcut.FullName)
        $shortcuts += [ordered]@{
            Path = $shortcut.FullName
            TargetPath = $link.TargetPath
            Arguments = $link.Arguments
            WorkingDirectory = $link.WorkingDirectory
        }
    }
}

$ports = foreach ($port in @(18080, 18081)) {
    $listener = Get-NetTCPConnection -State Listen -LocalPort $port | Select-Object -First 1
    [ordered]@{
        Port = $port
        Listening = [bool]$listener
        OwningProcess = if ($listener) { $listener.OwningProcess } else { $null }
        ProcessPath = if ($listener) {
            (Get-Process -Id $listener.OwningProcess).Path
        } else {
            $null
        }
    }
}

$tasks = Get-ScheduledTask |
    Where-Object { $_.TaskName -match 'Claude.*Recorder|Claude' } |
    ForEach-Object {
        [ordered]@{
            TaskName = $_.TaskName
            State = [string]$_.State
            Execute = $_.Actions.Execute
            Arguments = $_.Actions.Arguments
            Principal = $_.Principal.UserId
            Triggers = @($_.Triggers | ForEach-Object { $_.CimClass.CimClassName })
        }
    }

$rootCertificates = foreach ($scope in @('CurrentUser', 'LocalMachine')) {
    Get-ChildItem "Cert:\$scope\Root" |
        Where-Object { $_.Subject -match 'mitmproxy' } |
        ForEach-Object {
            [ordered]@{
                Scope = $scope
                Subject = $_.Subject
                Thumbprint = $_.Thumbprint
                NotAfter = $_.NotAfter.ToString('o')
            }
        }
}

$runEntries = foreach ($keyPath in @(
    'HKCU:\Software\Microsoft\Windows\CurrentVersion\Run',
    'HKLM:\Software\Microsoft\Windows\CurrentVersion\Run'
)) {
    if (Test-Path -LiteralPath $keyPath) {
        $key = Get-ItemProperty -LiteralPath $keyPath
        foreach ($property in $key.PSObject.Properties) {
            if ($property.Name -notmatch '^PS' -and [string]$property.Value -match 'Claude') {
                [ordered]@{
                    Key = $keyPath
                    Name = $property.Name
                    Value = [string]$property.Value
                }
            }
        }
    }
}

$packageDirectories = Get-ChildItem "$env:LOCALAPPDATA\Packages" -Directory -Filter 'Claude*' |
    Select-Object -ExpandProperty FullName

[ordered]@{
    Identity = [Security.Principal.WindowsIdentity]::GetCurrent().Name
    IsElevated = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole(
        [Security.Principal.WindowsBuiltInRole]::Administrator
    )
    UserProfile = $env:USERPROFILE
    SessionName = $env:SESSIONNAME
    Processes = @($claudeProcesses)
    Packages = @($packages)
    InstallCandidates = @($installCandidates)
    Shortcuts = @($shortcuts)
    RunEntries = @($runEntries)
    ScheduledTasks = @($tasks)
    Ports = @($ports)
    MitmRootCertificates = @($rootCertificates)
    PackageDirectories = @($packageDirectories)
} | ConvertTo-Json -Depth 10
