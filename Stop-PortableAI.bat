@echo off
setlocal
cd /d "%~dp0"

powershell.exe -NoProfile -ExecutionPolicy Bypass -File "%~dp0Stop-PortableAI.ps1"

if errorlevel 1 (
    echo.
    echo PortableAI shutdown failed.
    pause
    exit /b 1
)

endlocal
