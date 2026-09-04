<#
.SYNOPSIS
    Stop and remove the Purser Windows services created by install-service.ps1.

.PARAMETER Component
    Which service(s) to remove: 'agent', 'control-plane', or 'all' (default).

.EXAMPLE
    # From an elevated (Administrator) PowerShell:
    .\uninstall-service.ps1 -Component all
#>
[CmdletBinding()]
param(
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

$names = @{
    'agent'         = 'PurserAgent'
    'control-plane' = 'PurserControlPlane'
}

function Remove-PurserService($name) {
    if (-not (Get-Service -Name $name -ErrorAction SilentlyContinue)) {
        Write-Host ">> $name is not installed; nothing to do."
        return
    }
    Write-Host ">> Stopping $name"
    Stop-Service -Name $name -Force -ErrorAction SilentlyContinue
    Start-Sleep -Seconds 1
    Write-Host ">> Deleting $name"
    sc.exe delete $name | Out-Null
    Write-Host ">> $name removed."
}

$targets = if ($Component -eq 'all') { @('agent', 'control-plane') } else { @($Component) }
foreach ($t in $targets) {
    Remove-PurserService $names[$t]
}

Write-Host ""
Write-Host "Done. Binaries under the install directory and any state under"
Write-Host "C:\ProgramData\Purser were left in place; remove them manually if desired."
