@echo off
setlocal

rem hello/all aggregates these same units
for /F %%i in ('dir /b hello\*.BUFA') do if /I not "%%~ni"=="all" (
  echo.
  bufa /hello/%%~ni %*
  if ERRORLEVEL 1 (
    echo !!! FAILED !!!
    exit /b 1
  )
)

rem rust links with the machine's own linker, which CI (it sets the variable) has and a user may not
set DIRS=java go clj
if defined BUFA_TEST_EXAMPLES_REQUIRED set DIRS=%DIRS% rust

for %%d in (%DIRS%) do (
  echo.
  bufa /%%d %*
  if ERRORLEVEL 1 (
    echo !!! FAILED !!!
    exit /b 1
  )
)
