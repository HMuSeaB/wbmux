@echo off
setlocal enabledelayedexpansion
cd /d "%~dp0"

echo ============================================
echo   wbmux 本地编译
echo ============================================
echo.

rem 升级后必须三件事一起做，缺一个都看不到变化：
rem   1) 重新编译：前端资源用 go:embed 编进二进制，不编译改 CSS 无效
rem   2) 退出运行中的实例：Windows 会锁住 exe，编译直接失败
rem   3) 重新启动：不启动就没有代理，客户端也连不上
rem 本脚本按顺序全做，最后确认真的起来了。

echo [1/4] 退出运行中的 wbmux...
set HAD_RUNNING=0
rem findstr 不依赖中文输出，比 find /I 稳
tasklist /FI "IMAGENAME eq wbmux.exe" /NH 2>nul | findstr /I "wbmux.exe" >nul
if not errorlevel 1 (
    set HAD_RUNNING=1
    taskkill /IM wbmux.exe /F >nul 2>&1
    timeout /t 2 /nobreak >nul
    echo     已退出。
) else (
    echo     本来就没在跑。
)

echo [2/4] 编译...
set CGO_ENABLED=0
go build -ldflags "-s -w" -o wbmux.exe ./cmd/wbmux
if errorlevel 1 (
    echo.
    echo     失败：编译出错，上面是错误信息。
    if "!HAD_RUNNING!"=="1" echo     刚才停掉的实例没有恢复，修好后重跑本脚本即可。
    echo.
    pause
    exit /b 1
)
echo     完成。

echo [3/4] 启动...
start "" "%~dp0wbmux.exe" gui

echo [4/4] 等待就绪...
rem 起来需要几秒：读配置、开端口、写 run.json。最多等 20 秒。
set READY=0
for /l %%i in (1,1,20) do (
    if "!READY!"=="0" (
        timeout /t 1 /nobreak >nul
        tasklist /FI "IMAGENAME eq wbmux.exe" /NH 2>nul | findstr /I "wbmux.exe" >nul
        if not errorlevel 1 set READY=1
    )
)

if "!READY!"=="1" (
    echo     已就绪，代理与面板可用。
) else (
    echo     警告：等待 20 秒仍未见进程，请查看窗口报错。
)

echo.
echo ============================================
echo   全部完成
echo ============================================
echo.
pause
