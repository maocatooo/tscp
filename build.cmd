@echo off
rem Build all platform binaries into binaries\ for the npm package.
setlocal
cd /d %~dp0
set GOTOOLCHAIN=auto

call :build windows amd64 tscp.exe || exit /b 1
call :build linux   amd64 tscp     || exit /b 1
call :build linux   arm64 tscp     || exit /b 1
call :build darwin  amd64 tscp     || exit /b 1
call :build darwin  arm64 tscp     || exit /b 1

set GOOS=
set GOARCH=
echo done.
exit /b 0

:build
set GOOS=%1
set GOARCH=%2
if not exist "binaries\%1-%2" mkdir "binaries\%1-%2"
echo build %1-%2 ...
go build -trimpath -ldflags "-s -w" -o "binaries\%1-%2\%3" .
if errorlevel 1 (
  echo build %1-%2 FAILED
  exit /b 1
)
exit /b 0
