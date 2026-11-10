@echo off
chcp 65001 >nul 2>&1
title Zhuban - Remove font registrations
cd /d "%~dp0"

echo.
echo   撤销「铸版」登记到当前用户的公文字体 / Remove per-user font entries
echo.
echo   只删指向本文件夹的登记项，系统自带字体一条不动。
echo   Only entries pointing into this folder are removed.
echo.
pause

"%~dp0app\python\python.exe" "%~dp0app\unregister_fonts.py"

echo.
echo   完成。再次双击「启动铸版.bat」会自动重新登记。
echo.
pause
