@echo off
setlocal
cd /d "%~dp0"

echo ============================================
echo   wbmux 本地编译
echo ============================================
echo.

rem 升级后必须三件事一起做，缺一个都看不到变化：
rem   1) 重新编译：前端资源用 go:embed 编进二进制，不编译改 CSS 无效
rem   2) 退出运行中的实例：Windows 会锁住 exe，编译直接失败
rem   3) 重新启动
rem 本脚本按顺序全做。

echo [1/3] 退出运行中的 wbmux...
taskkill /IM wbmux.exe /F >nul 2>&1
timeout /t 2 /nobreak >nul

echo [2/3] 编译...
set CGO_ENABLED=0
go build -ldflags "-s -w" -o wbmux.exe ./cmd/wbmux
if errorlevel 1 (
    echo.
    echo     失败：编译出错，上面是错误信息。
    echo.
    pause
    exit /b 1
)
echo     完成。

echo [3/3] 启动...
start "" "%~dp0wbmux.exe" gui
echo.
echo 全部完成。面板与胶囊会在几秒内出现。
echo.
pause
