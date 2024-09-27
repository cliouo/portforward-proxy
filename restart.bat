@echo off
call stop.bat
timeout /t 2 /nobreak > nul
call start.bat
echo 端口转发程序已重启