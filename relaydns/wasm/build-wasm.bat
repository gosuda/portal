@echo off
setlocal

REM Build script for RelayDNS WASM client
echo ====================================
echo Building RelayDNS WASM Client
echo ====================================
echo.

REM Get the directory where this script is located
set SCRIPT_DIR=%~dp0
cd /d "%SCRIPT_DIR%"

echo Current directory: %CD%
echo.

REM Check if Cargo.toml exists
if not exist "Cargo.toml" (
    echo Error: Cargo.toml not found in current directory!
    echo Current directory: %CD%
    exit /b 1
)

echo [1/3] Building WASM module...
wasm-pack build --target web --out-dir pkg
if %ERRORLEVEL% neq 0 (
    echo.
    echo Error: Build failed!
    exit /b %ERRORLEVEL%
)

echo.
echo [2/3] Creating SDK directory...
set SDK_DIR=..\..\sdk\wasm
if not exist "%SDK_DIR%" mkdir "%SDK_DIR%"

echo [3/3] Copying files to SDK...
copy /Y pkg\relaydns_wasm.js "%SDK_DIR%\"
copy /Y pkg\relaydns_wasm_bg.wasm "%SDK_DIR%\"
copy /Y pkg\relaydns_wasm.d.ts "%SDK_DIR%\"
copy /Y pkg\package.json "%SDK_DIR%\"

echo.
echo ====================================
echo Build Complete!
echo ====================================
echo.
echo Output location: %SDK_DIR%
echo.

dir /B "%SDK_DIR%\relaydns_wasm*"

endlocal
