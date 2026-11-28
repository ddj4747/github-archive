@echo off
setlocal
set "ARCHIVE_LAUNCHER_PATH=%~f0"
if not "%~1"=="" set "ARCHIVE_PROJECT_DIR=%~1"
powershell.exe -NoProfile -ExecutionPolicy Bypass -Command "$parts = [IO.File]::ReadAllText($env:ARCHIVE_LAUNCHER_PATH) -split '(?m)^:__POWERSHELL__\r?$', 2; if ($parts.Count -ne 2) { Write-Error 'Launcher script is incomplete.'; exit 1 }; & ([scriptblock]::Create($parts[1])); exit $LASTEXITCODE"
set "LAUNCHER_EXIT=%ERRORLEVEL%"
echo.
if not "%LAUNCHER_EXIT%"=="0" echo Startup failed. Read the message above.
pause
exit /b %LAUNCHER_EXIT%
:__POWERSHELL__
$ErrorActionPreference = 'Stop'
try {
    $projectDir = $env:ARCHIVE_PROJECT_DIR
    if ([string]::IsNullOrWhiteSpace($projectDir)) {
        $projectDir = Split-Path -Parent $env:ARCHIVE_LAUNCHER_PATH
    }
    if (-not (Test-Path -LiteralPath $projectDir -PathType Container)) {
        throw "Project directory not found: $projectDir"
    }
    Set-Location -LiteralPath $projectDir
    if (-not (Test-Path -LiteralPath 'compose.yaml' -PathType Leaf)) {
        throw "No compose.yaml found in project directory: $projectDir"
    }
    Write-Host "Project directory: $((Get-Location).Path)"

    if (-not (Get-Command docker -ErrorAction SilentlyContinue)) {
        throw 'Docker was not found. Install Docker Desktop and reopen this launcher.'
    }

    function Test-DockerEngine {
        & cmd.exe /d /c 'docker info >nul 2>&1'
        return $LASTEXITCODE -eq 0
    }

    if (-not (Test-DockerEngine)) {
        $dockerDesktop = Join-Path $env:ProgramFiles 'Docker\Docker\Docker Desktop.exe'
        if (-not (Test-Path -LiteralPath $dockerDesktop)) {
            throw 'Docker Desktop was not found at its usual location. Start Docker Desktop manually and run this launcher again.'
        }
        Write-Host 'Starting Docker Desktop and waiting for the engine (up to 3 minutes)...'
        Start-Process -FilePath $dockerDesktop
        $deadline = (Get-Date).AddMinutes(3)
        do {
            Start-Sleep -Seconds 3
            $engineReady = Test-DockerEngine
        } while (-not $engineReady -and (Get-Date) -lt $deadline)
        if (-not $engineReady) {
            throw 'Docker did not become ready. Open Docker Desktop, check its error message, and try again.'
        }
    }

    if (-not (Test-Path -LiteralPath 'archive-config\config.yaml')) {
        New-Item -ItemType Directory -Force -Path 'archive-config' | Out-Null
        if (Test-Path -LiteralPath 'config.yaml') {
            Copy-Item -LiteralPath 'config.yaml' -Destination 'archive-config\config.yaml'
        } elseif (Test-Path -LiteralPath 'config.example.yaml') {
            Copy-Item -LiteralPath 'config.example.yaml' -Destination 'archive-config\config.yaml'
        } else {
            throw 'No configuration template found. Restore config.example.yaml and try again.'
        }
        Write-Host 'Created archive-config\config.yaml. Edit it or use the panel to select targets.'
    }

    Write-Host 'Building and starting the archive and panel...'
    & docker compose up -d --build
    if ($LASTEXITCODE -ne 0) { throw 'Docker Compose could not build or start the services.' }

    & docker compose ps
    if ($LASTEXITCODE -ne 0) { throw 'Could not check the service status.' }
    Write-Host 'Open http://127.0.0.1:8765 in your browser.'
    exit 0
} catch {
    Write-Host "Error: $($_.Exception.Message)" -ForegroundColor Red
    exit 1
}
