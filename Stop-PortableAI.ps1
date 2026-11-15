
$ErrorActionPreference = 'Stop'

$root = Split-Path -Parent $MyInvocation.MyCommand.Path
$configPath = Join-Path $root 'Config\controller.json'
$controller = [IO.Path]::GetFullPath(
    (Join-Path $root 'Controller\windows-x64\portableai-controller.exe')
)

function Stop-WithError($message) {
    Write-Host ""
    Write-Host "ERROR: $message" -ForegroundColor Red
    Write-Host ""
    Read-Host "Press Enter to close..."
    exit 1
}

try {
    if (!(Test-Path $configPath)) {
        Stop-WithError "Configuration not found: $configPath"
    }

    $config = Get-Content -LiteralPath $configPath -Raw | ConvertFrom-Json
    $port = [int]$config.port
    $hostName = [string]$config.host

    if ($port -lt 1 -or $port -gt 65535) {
        Stop-WithError "Invalid controller port: $port"
    }

    if ([string]::IsNullOrWhiteSpace($hostName) -or $hostName -in @('0.0.0.0', '::')) {
        $hostName = '127.0.0.1'
    }

    if ($hostName.Contains(':') -and !$hostName.StartsWith('[')) {
        $hostName = "[$hostName]"
    }

    $base = "http://${hostName}:$port"

    Write-Host "Checking PortableAI controller at $base ..."

    try {
        $health = Invoke-RestMethod -Uri "$base/api/health" -TimeoutSec 3
    }
    catch {
        Stop-WithError "PortableAI controller is not responding. No process was terminated."
    }

    if ($health.ok -ne $true -or !$health.version) {
        Stop-WithError "The service on port $port did not identify itself as PortableAI. No process was terminated."
    }

    # Find the process listening on the configured port.
    $listenerIds = @(
        Get-NetTCPConnection -LocalPort $port -State Listen -ErrorAction Stop |
        Select-Object -ExpandProperty OwningProcess -Unique
    )

    $ownedIds = @()

    foreach ($id in $listenerIds) {
        $processInfo = Get-CimInstance Win32_Process `
            -Filter "ProcessId = $id" `
            -ErrorAction SilentlyContinue

        if ($processInfo.ExecutablePath) {
            $processPath = [IO.Path]::GetFullPath($processInfo.ExecutablePath)

            if ($processPath -ieq $controller) {
                $ownedIds += [int]$id
            }
        }
    }

    if ($ownedIds.Count -eq 0) {
        Stop-WithError "The health endpoint responded, but I could not verify that the listener is this PortableAI controller. No process was terminated."
    }

    Write-Host "PortableAI controller process found: $($ownedIds -join ', ')"
    Write-Host "Sending shutdown request..."

    # The server may close the HTTP connection before returning a response.
    # That is not necessarily a shutdown failure.
    try {
        Invoke-RestMethod -Method Post `
            -Uri "$base/api/shutdown" `
            -ContentType 'application/json' `
            -Body '{}' `
            -TimeoutSec 5 | Out-Null
    }
    catch {
        Write-Host "The shutdown connection closed or timed out; checking process status..."
    }

    # Wait for the controller process to exit.
    $stopped = $false

    for ($i = 0; $i -lt 30; $i++) {
        $remaining = @(
            foreach ($id in $ownedIds) {
                Get-Process -Id $id -ErrorAction SilentlyContinue
            }
        )

        if ($remaining.Count -eq 0) {
            $stopped = $true
            break
        }

        Start-Sleep -Milliseconds 500
    }

    if (!$stopped) {
        Stop-WithError "The shutdown request was sent, but the controller process is still running. It has not been forcibly terminated."
    }

    Write-Host ""
    Write-Host "PortableAI controller stopped successfully." -ForegroundColor Green
    Read-Host "Press Enter to close..."
    exit 0
}
catch {
    Stop-WithError $_.Exception.Message
}
