#Requires -Version 5.1
# restart-workbuddy-cdp.ps1
# Restart WorkBuddy with a local CDP debugging port, then verify the port is live.
# - Only touches the selected instance (CN by default); the other instance stays running.
# - Does NOT modify any WorkBuddy installation file. The debug flag is a standard
#   Chromium command-line switch, effective for this launch only.
# Usage:
#   powershell -NoProfile -ExecutionPolicy Bypass -File restart-workbuddy-cdp.ps1
#   ... -Intl          # restart the international instance instead (port 9223)
#   ... -Port 9333     # custom CDP port
param(
    [switch]$Intl,
    [int]$Port = 0,
    [string]$Exe = ''
)
$ErrorActionPreference = 'Stop'

# 客户端主程序：先看 -Exe，再按常见安装位置探测。
# 不要在这里写死某个人的安装路径——脚本跟着仓库分发，别人的机器不长这样。
if (-not $Exe) {
    $candidates = @(
        (Join-Path $env:LOCALAPPDATA 'Programs\WorkBuddy\WorkBuddy.exe'),
        (Join-Path $env:ProgramFiles 'WorkBuddy\WorkBuddy.exe'),
        (Join-Path ${env:ProgramFiles(x86)} 'WorkBuddy\WorkBuddy.exe'),
        'D:\Tools\WorkBuddy\WorkBuddy.exe'
    )
    foreach ($c in $candidates) {
        if ($c -and (Test-Path $c)) { $Exe = $c; break }
    }
}
if (-not $Exe -or -not (Test-Path $Exe)) {
    throw "找不到 WorkBuddy.exe，请用 -Exe <路径> 指定"
}

if ($Intl) {
    $userData = Join-Path $env:USERPROFILE '.workbuddy-ai'
    if ($Port -eq 0) { $Port = 9223 }
    $label = 'WorkBuddy AI (International)'
} else {
    $userData = Join-Path $env:USERPROFILE '.workbuddy\app'
    if ($Port -eq 0) { $Port = 9222 }
    $label = 'WorkBuddy (CN)'
}

$logPath = Join-Path $PSScriptRoot 'restart-workbuddy-cdp.log'
Start-Transcript -Path $logPath -Append | Out-Null

try {
    Write-Host "==> Target   : $label"
    Write-Host "    userData : $userData"
    Write-Host "    CDP port : $Port"
    Write-Host ""
    Write-Host "NOTE: the current chat in this client will be interrupted; history is preserved."

    $all  = Get-CimInstance Win32_Process -Filter "Name='WorkBuddy.exe'"
    $mine = @($all | Where-Object { $_.CommandLine -and $_.CommandLine.Contains($userData) })
    $main = @($mine | Where-Object { $_.CommandLine -notmatch '--type=' })

    if ($main.Count -gt 0) {
        Write-Host "==> Closing running instance (PID: $($main.ProcessId -join ', ')) ..."
        foreach ($m in $main) {
            try { [void](Get-Process -Id $m.ProcessId -ErrorAction Stop).CloseMainWindow() } catch {}
        }
        $deadline = (Get-Date).AddSeconds(10)
        while ((Get-Date) -lt $deadline) {
            $alive = Get-Process -Id $main.ProcessId -ErrorAction SilentlyContinue
            if (-not $alive) { break }
            Start-Sleep -Milliseconds 500
        }
        $alive = Get-Process -Id $main.ProcessId -ErrorAction SilentlyContinue
        if ($alive) {
            Write-Host "==> Still running (possibly minimized to tray), forcing stop ..."
            $alive | Stop-Process -Force
        }
        Start-Sleep -Seconds 1
        $left = @(Get-CimInstance Win32_Process -Filter "Name='WorkBuddy.exe'" |
                  Where-Object { $_.CommandLine -and $_.CommandLine.Contains($userData) })
        if ($left.Count -gt 0) {
            Write-Host "==> Cleaning $($left.Count) leftover child process(es) ..."
            foreach ($p in $left) { Stop-Process -Id $p.ProcessId -Force -ErrorAction SilentlyContinue }
        }
    } else {
        Write-Host "==> No running instance found, starting directly."
    }

    Write-Host "==> Launching with --remote-debugging-port=$Port ..."
    Start-Process -FilePath $Exe -ArgumentList "--user-data-dir=`"$userData`"", "--remote-debugging-port=$Port"

    Write-Host "==> Waiting for CDP to come up ..."
    $ready = $false
    $deadline = (Get-Date).AddSeconds(30)
    while ((Get-Date) -lt $deadline) {
        try {
            $v = Invoke-RestMethod -Uri "http://127.0.0.1:$Port/json/version" -TimeoutSec 2
            $ready = $true
            break
        } catch {
            Start-Sleep -Milliseconds 800
        }
    }

    if ($ready) {
        Write-Host ""
        Write-Host "[OK] CDP is live on http://127.0.0.1:$Port  ($($v.Browser))"
        try {
            $targets = Invoke-RestMethod -Uri "http://127.0.0.1:$Port/json/list" -TimeoutSec 3
            foreach ($t in ($targets | Where-Object { $_.type -eq 'page' })) {
                Write-Host "     page: $($t.title)"
            }
        } catch {}
        Write-Host ""
        Write-Host "Next: go back to the chat and say 'continue' to run the injection probe."
    } else {
        Write-Warning "CDP not reachable within 30s. The client may ignore/block the flag. See log: $logPath"
    }
} finally {
    Stop-Transcript | Out-Null
}
