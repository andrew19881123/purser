<#
.SYNOPSIS
    Register the Purser agent and/or control-plane as Windows services.

.DESCRIPTION
    Uses New-Service to create the services and sc.exe to configure automatic
    crash recovery (equivalent to systemd Restart=on-failure / RestartSec=2).

    Windows services have no EnvironmentFile equivalent. This script writes the
    per-service configuration as *machine-scoped* environment variables so the
    service inherits them on start. Re-run after changing them, or edit them
    under Computer\HKLM\SYSTEM\CurrentControlSet\Services\<name>\Environment.

    NOTE: A proper MSI installer (WiX) with a bundled service definition,
    per-service accounts and an uninstall entry is a planned follow-up. This
    script is the interim, scriptable install path.

.PARAMETER BinDir
    Directory containing purser-agent.exe / control-plane.exe.
    Defaults to C:\Program Files\Purser\bin.

.PARAMETER Component
    Which service(s) to install: 'agent', 'control-plane', or 'all' (default).

.EXAMPLE
    # From an elevated (Administrator) PowerShell:
    .\install-service.ps1 -BinDir 'C:\Program Files\Purser\bin' -Component all
#>
[CmdletBinding()]
param(
    [string]$BinDir = "$env:ProgramFiles\Purser\bin",
    [ValidateSet('agent', 'control-plane', 'all')]
    [string]$Component = 'all'
)

$ErrorActionPreference = 'Stop'

# --- Require Administrator ---------------------------------------------------
$principal = New-Object Security.Principal.WindowsPrincipal(
    [Security.Principal.WindowsIdentity]::GetCurrent())
if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    throw "This script must be run from an elevated (Administrator) PowerShell."
}

# --- Service definitions -----------------------------------------------------
# SECURITY (agent): bind only on a trusted-subnet address; the inference engine
# worker is NOT sandboxed and must never face the public internet.
$services = @{
    'agent' = @{
        Name        = 'PurserAgent'
        DisplayName = 'Purser Fleet Agent'
        Description = 'Purser node-local agent: hardware probe, engine supervisor, AgentService gRPC.'
        Exe         = 'purser-agent.exe'
        Env         = [ordered]@{
            'PURSER_AGENT_BIND'          = '0.0.0.0:50151'
            'PURSER_INFERENCE_PORT'      = '8000'
            'PURSER_CONTROL_PLANE_ADDR'  = 'http://cp.internal:9443'
            'PURSER_CLUSTER_ID'          = 'default'
            'PURSER_JOIN_TOKEN'          = 'replace-me'
        }
    }
    'control-plane' = @{
        Name        = 'PurserControlPlane'
        DisplayName = 'Purser Control Plane'
        Description = 'Purser control plane: registry, orchestrator, reconciler, internal PKI, REST + gRPC API.'
        Exe         = 'control-plane.exe'
        Env         = [ordered]@{
            'PURSER_ADDR'      = ':8080'
            'PURSER_GRPC_ADDR' = ':9443'
            'PURSER_DB'        = 'C:\ProgramData\Purser\purser-registry.db'
            'PURSER_PKI_DIR'   = 'C:\ProgramData\Purser\pki-state'
        }
    }
}

function Install-PurserService($def) {
    $name = $def.Name
    $exePath = Join-Path $BinDir $def.Exe
    if (-not (Test-Path $exePath)) {
        throw "Executable not found: $exePath (copy the release binaries there first)."
    }

    if (Get-Service -Name $name -ErrorAction SilentlyContinue) {
        Write-Host ">> $name already exists; stopping and removing before re-install."
        Stop-Service -Name $name -Force -ErrorAction SilentlyContinue
        sc.exe delete $name | Out-Null
        Start-Sleep -Seconds 1
    }

    Write-Host ">> Creating service $name -> $exePath"
    New-Service -Name $name -BinaryPathName "`"$exePath`"" `
        -DisplayName $def.DisplayName -Description $def.Description `
        -StartupType Automatic | Out-Null

    # Crash recovery: restart after 2s on the 1st/2nd/subsequent failures,
    # reset the failure counter daily. (systemd Restart=on-failure/RestartSec=2.)
    sc.exe failure $name reset= 86400 actions= restart/2000/restart/2000/restart/2000 | Out-Null

    # Machine-scoped environment for the service (no EnvironmentFile on Windows).
    $regPath = "HKLM:\SYSTEM\CurrentControlSet\Services\$name"
    $envLines = $def.Env.GetEnumerator() | ForEach-Object { "$($_.Key)=$($_.Value)" }
    New-ItemProperty -Path $regPath -Name 'Environment' -PropertyType MultiString `
        -Value $envLines -Force | Out-Null

    Write-Host ">> $name installed. Review its environment before starting:"
    $envLines | ForEach-Object { Write-Host "     $_" }
    Write-Host ">> Start with:  Start-Service $name"
}

$targets = if ($Component -eq 'all') { @('agent', 'control-plane') } else { @($Component) }
foreach ($t in $targets) {
    Install-PurserService $services[$t]
}

Write-Host ""
Write-Host "Done. Edit each service's Environment (esp. PURSER_JOIN_TOKEN / secrets)"
Write-Host "then run Start-Service <name>. Uninstall with .\uninstall-service.ps1."
