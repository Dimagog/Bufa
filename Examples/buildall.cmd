@echo off

for /F %%i in ('dir /b hello\*.BUFA') do (
  echo.
  bufa /hello/%%~ni %*
  if ERRORLEVEL 1 (
    echo !!! FAILED !!!
    exit /b 1
  )
)

echo.
bufa /java %*
if ERRORLEVEL 1 (
  echo !!! FAILED !!!
  exit /b 1
)
