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

rem deploy/* are tasks: the token after the dir is their `out` argument
for /F %%i in ('dir /b deploy\*.BUFA') do (
  echo.
  bufa /deploy/%%~ni "%TEMP%\bufa-deploy" %*
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
