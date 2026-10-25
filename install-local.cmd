@echo off
setlocal
where pwsh.exe >nul 2>nul
if errorlevel 1 (
  echo PowerShell 7 is required to build Asterhand from source. Install it and try again.
  pause
  exit /b 1
)
pwsh.exe -NoProfile -ExecutionPolicy Bypass -File "%~dp0scripts\install-local.ps1" %*
set "install_result=%errorlevel%"
if not "%install_result%"=="0" echo Local installation failed. See the error above; the node may need to be started manually.
pause
exit /b %install_result%
