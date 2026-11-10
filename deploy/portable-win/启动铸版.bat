@echo off
chcp 65001 >nul 2>&1
title Zhuban - Word Template Render
cd /d "%~dp0"

if not exist "%~dp0app\python\python.exe" goto :broken
if not exist "%~dp0app\zhuban.exe" goto :broken

"%~dp0app\python\python.exe" "%~dp0app\launcher.py"
if errorlevel 1 pause
exit /b 0

:broken
echo.
echo   [x] The package is incomplete / 安装包不完整
echo       Missing: app\python\python.exe or app\zhuban.exe
echo       Please extract the whole archive again (do not extract partially).
echo       请重新解压完整的压缩包，不要只解压一部分文件。
echo.
pause
exit /b 1
