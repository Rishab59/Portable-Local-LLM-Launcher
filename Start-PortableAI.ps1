$ErrorActionPreference = 'Stop'

$root = Split-Path -Parent $MyInvocation.MyCommand.Path
$controller = Join-Path $root 'Controller\windows-x64\portableai-controller.exe'
$runtime = Join-Path $root 'Runtime\windows-x64\llamafile-0.10.6.exe'
$configPath = Join-Path $root 'Config\controller.json'
$logs = Join-Path $root 'Logs'

function Stop-WithError($message) {
    Write-Host ""
    Write-Host "ERROR: $message" -ForegroundColor Red
    Write-Host ""
    Read-Host "Press Enter to close"
    exit 1
}

try {
    New-Item -ItemType Directory -Force -Path $logs, (Join-Path $root 'Models') | Out-Null

    if (!(Test-Path $controller)) {
        Stop-WithError "Controller executable not found: $controller"
    }

    if (!(Test-Path $runtime)) {
        Stop-WithError "Runtime executable not found: $runtime"
    }

    if (!(Test-Path $configPath)) {
        Stop-WithError "Configuration not found: $configPath"
    }

    $config = Get-Content -LiteralPath $configPath -Raw | ConvertFrom-Json
    $port = [int]$config.port
    $hostName = [string]$config.host

    if ($port -lt 1 -or $port -gt 65535) {
        Stop-WithError "Invalid port in controller.json: $port"
    }

    if ([string]::IsNullOrWhiteSpace($hostName)) {
        $hostName = '127.0.0.1'
    }

    if ($hostName -in @('0.0.0.0', '::')) {
        $hostName = '127.0.0.1'
    }

    if ($hostName.Contains(':') -and !$hostName.StartsWith('[')) {
        $hostName = "[$hostName]"
    }

    $base = "http://${hostName}:$port"

    function Test-PortableAI {
        try {
            $response = Invoke-RestMethod -Uri "$base/api/health" -TimeoutSec 2
            return ($response.ok -eq $true -and $response.version)
        }
        catch {
            return $false
        }
    }

    if (Test-PortableAI) {
        Write-Host "PortableAI is already running." -ForegroundColor Green
        Start-Process $base
        exit 0
    }

    $stdout = Join-Path $logs 'controller.stdout.log'
    $stderr = Join-Path $logs 'controller.stderr.log'

    Remove-Item $stdout, $stderr -Force -ErrorAction SilentlyContinue

    Write-Host "Starting PortableAI controller..."
    Write-Host "Root: $root"
    Write-Host "Address: $base"

    $arguments = '--root "' + $root + '"'

    $process = Start-Process `
        -FilePath $controller `
        -ArgumentList $arguments `
        -WorkingDirectory $root `
        -RedirectStandardOutput $stdout `
        -RedirectStandardError $stderr `
        -PassThru

    for ($i = 0; $i -lt 60; $i++) {
        Start-Sleep -Milliseconds 500
        $process.Refresh()

        if ($process.HasExited) {
            Write-Host "Controller exited. Exit code: $($process.ExitCode)" -ForegroundColor Red

            if (Test-Path $stderr) {
                Write-Host "`nController error output:" -ForegroundColor Yellow
                Get-Content $stderr -Tail 40
            }

            if (Test-Path $stdout) {
                Write-Host "`nController output:" -ForegroundColor Yellow
                Get-Content $stdout -Tail 20
            }

            Stop-WithError "Controller exited before becoming ready."
        }

        if (Test-PortableAI) {
            Write-Host "PortableAI started successfully." -ForegroundColor Green
            Start-Process $base
            exit 0
        }
    }

    Write-Host "Controller process is still running, but the health endpoint did not respond." -ForegroundColor Red

    foreach ($file in @($stderr, $stdout)) {
        if (Test-Path $file) {
            Write-Host "`n$file" -ForegroundColor Yellow
            Get-Content $file -Tail 40
        }
    }

    Stop-WithError "Startup timed out. The controller was not terminated."
}
catch {
    Stop-WithError $_.Exception.ToString()
}
