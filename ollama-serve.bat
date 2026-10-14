@echo off
rem Локальная модель для бота. Всё лежит на G:, системные настройки не меняются.
set OLLAMA_MODELS=G:\Ollama\models
set OLLAMA_HOST=127.0.0.1:11434
rem Экономия видеопамяти: 14B + контекст 16k влезают в 12 ГБ.
set OLLAMA_FLASH_ATTENTION=1
set OLLAMA_KV_CACHE_TYPE=q8_0
"G:\Ollama\ollama.exe" serve
