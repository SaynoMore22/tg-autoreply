@echo off
cd /d "%~dp0"
start "Ollama" /min cmd /c ollama-serve.bat
timeout /t 5 >nul
tg-autoreply.exe %*
pause
