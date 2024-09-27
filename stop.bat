@echo off
powershell -Command "Stop-Process -Name 'portforward*' -Force"
pause