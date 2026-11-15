@echo off
setlocal EnableExtensions EnableDelayedExpansion

rem =====================================================================
rem  Compact build for workbuddy-signin (Go, standard library only)
rem
rem    build.bat             stripped build + UPX packing
rem    build.bat test        vet + full tests, then compact build
rem    build.bat noupx       stripped build only -> workbuddy-signin-unpacked.exe
rem
rem  Optional env vars:
rem    WORKBUDDY_UPX   full path to upx.exe (skips auto-detection)
rem    GOROOT          Go root (auto-derived from go.exe when unset)
rem    GOOS / GOARCH   cross-compile targets
rem =====================================================================

set "SELF=%~dp0"
set "OUT=%SELF%workbuddy-signin.exe"
set "DO_TEST=0"
set "DO_UPX=1"

:parse_args
if "%~1"=="" goto args_done
if /i "%~1"=="test"    set "DO_TEST=1"
if /i "%~1"=="noupx"   set "DO_UPX=0"
if /i "%~1"=="no-upx"  set "DO_UPX=0"
if /i "%~1"=="noup"    set "DO_UPX=0"
shift
goto parse_args
:args_done

if "%DO_UPX%"=="0" set "OUT=%SELF%workbuddy-signin-unpacked.exe"

rem ---------------------------------------------------------------- locate Go
set "GOEXE="
for /f "delims=" %%i in ('where go 2^>nul') do if not defined GOEXE set "GOEXE=%%i"
if not defined GOEXE if exist "%ProgramFiles%\Go\bin\go.exe"                        set "GOEXE=%ProgramFiles%\Go\bin\go.exe"
if not defined GOEXE if exist "%LOCALAPPDATA%\Programs\Go\bin\go.exe"               set "GOEXE=%LOCALAPPDATA%\Programs\Go\bin\go.exe"
if not defined GOEXE if exist "%USERPROFILE%\scoop\apps\go\current\bin\go.exe"      set "GOEXE=%USERPROFILE%\scoop\apps\go\current\bin\go.exe"
if not defined GOEXE if exist "%LOCALAPPDATA%\Temp\go-clean\go\bin\go.exe"          set "GOEXE=%LOCALAPPDATA%\Temp\go-clean\go\bin\go.exe"
if not defined GOEXE (
    echo [ERROR] Go toolchain not found. Install Go 1.21+ or add go.exe to PATH.
    exit /b 1
)
if not defined GOROOT for %%i in ("%GOEXE%\..\..") do set "GOROOT=%%~fi"

rem --------------------------------------------------------------- locate UPX
set "UPXEXE="
if defined WORKBUDDY_UPX if exist "%WORKBUDDY_UPX%" set "UPXEXE=%WORKBUDDY_UPX%"
if not defined UPXEXE for /f "delims=" %%i in ('where upx 2^>nul') do if not defined UPXEXE set "UPXEXE=%%i"
if not defined UPXEXE if exist "%SELF%tools\upx.exe" set "UPXEXE=%SELF%tools\upx.exe"
if not defined UPXEXE if exist "%LOCALAPPDATA%\Temp\go-clean\upx\upx-5.3.0-win64\upx.exe" set "UPXEXE=%LOCALAPPDATA%\Temp\go-clean\upx\upx-5.3.0-win64\upx.exe"

echo Go  : %GOEXE%
echo Root: %GOROOT%
echo Out : %OUT%
echo.

pushd "%SELF%" || exit /b 1
set "CGO_ENABLED=0"

if "%DO_TEST%"=="1" (
    echo [test] go fmt / vet / test
    "%GOEXE%" fmt ./...   || (popd & exit /b 1)
    "%GOEXE%" vet ./...   || (popd & exit /b 1)
    "%GOEXE%" test ./... -count=1 || (popd & exit /b 1)
    echo [test] OK
    echo.
)

rem -s -w          strip symbol table and DWARF debug info
rem -trimpath      remove local absolute paths
rem -buildvcs=false do not embed VCS metadata
rem -buildid=      drop the build id
rem -gcflags=all=-l disable inlining to shrink the text segment
echo [1/2] Building stripped binary ...
"%GOEXE%" build -trimpath -buildvcs=false "-gcflags=all=-l" "-ldflags=-s -w -buildid=" -o "%OUT%" . || (popd & exit /b 1)
for %%F in ("%OUT%") do set "RAW=%%~zF"
echo       stripped: %RAW% bytes

set "PACKED=0"
if "%DO_UPX%"=="1" if defined UPXEXE (
    echo [2/2] Packing with UPX ...
    "%UPXEXE%" -9 --lzma --force "%OUT%" >nul
    if not errorlevel 1 set "PACKED=1"
)
if "%DO_UPX%"=="1" if "%PACKED%"=="0" (
    echo [WARN] UPX unavailable or failed - keeping the stripped binary.
    echo        Put upx.exe in tools\, or set WORKBUDDY_UPX=full\path\upx.exe
)
if "%DO_UPX%"=="0" echo [2/2] UPX skipped by request.

for %%F in ("%OUT%") do set "FINAL=%%~zF"
set /a "KIB=%FINAL%/1024"
set /a "DISKS=(%FINAL%+1474559)/1474560"
set /a "PCT=%FINAL%*100/%RAW%"
echo.
echo   Final    : %FINAL% bytes ^(%KIB% KiB^)
echo   vs strip : %PCT% percent
echo   Floppies : %DISKS% x 1.44 MB disk ^(one DOS-era pair is 2949120 bytes^)
echo   Done     : %OUT%
popd
endlocal
exit /b 0
