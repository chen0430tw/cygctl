<#
.SYNOPSIS
    Cygctl Uninstaller - Remove Cygwin CLI tools

.DESCRIPTION
    Removes cygctl, apt-cyg, sudo, su from Cygwin.
    Cleans up PATH and shell aliases.

.EXAMPLE
    irm https://raw.githubusercontent.com/chen0430tw/cygctl/master/uninstall.ps1 | iex
#>

param(
    [string]$InstallDir = "C:\cygwin64\bin"
)

$ErrorActionPreference = "Stop"

Write-Host "=== Cygctl Uninstaller ===" -ForegroundColor Cyan
Write-Host ""

$Binaries = @("cygctl.exe", "apt-cyg.exe", "sudo.exe", "su.exe")

# 1. Remove binaries
Write-Host "[1/6] Removing binaries..." -ForegroundColor Green
foreach ($binary in $Binaries) {
    $path = Join-Path $InstallDir $binary
    if (Test-Path $path) {
        Remove-Item $path -Force
        Write-Host "  OK Removed $binary" -ForegroundColor Green
    } else {
        Write-Host "  SKIP $binary not found" -ForegroundColor Gray
    }
}

# 2. Remove from PATH
Write-Host "[2/6] Cleaning PATH..." -ForegroundColor Green
$userPath = [Environment]::GetEnvironmentVariable("PATH", "User")
if ($userPath -like "*$InstallDir*") {
    $newPath = ($userPath -split ';' | Where-Object { $_ -and $_ -notlike "*$InstallDir*" }) -join ';'
    [Environment]::SetEnvironmentVariable("PATH", $newPath, "User")
    Write-Host "  OK Removed from PATH" -ForegroundColor Green
} else {
    Write-Host "  OK Not in PATH" -ForegroundColor Gray
}

# 3. Remove PowerShell profile aliases
Write-Host "[3/6] Cleaning PowerShell..." -ForegroundColor Green
$profilePath = $PROFILE
if (Test-Path $profilePath) {
    $content = Get-Content $profilePath -Raw
    if ($content -match "function cyg") {
        $newContent = $content -replace '(?s)\r?\n# Cygwin Command-Line Tool aliases.*', ''
        Set-Content -Path $profilePath -Value $newContent -NoNewline
        Write-Host "  OK Removed aliases" -ForegroundColor Green
    } else {
        Write-Host "  OK No aliases found" -ForegroundColor Gray
    }
} else {
    Write-Host "  OK No profile found" -ForegroundColor Gray
}

# 4. Remove CMD macros
Write-Host "[4/6] Cleaning CMD..." -ForegroundColor Green
$macrosFile = "$env:USERPROFILE\cmd_macros.doskey"
if (Test-Path $macrosFile) {
    Remove-Item $macrosFile -Force
    Write-Host "  OK Removed macros file" -ForegroundColor Green
} else {
    Write-Host "  OK No macros file" -ForegroundColor Gray
}

$regPath = "HKCU:\Software\Microsoft\Command Processor"
$autoRun = Get-ItemProperty -Path $regPath -ErrorAction SilentlyContinue
if ($autoRun -and $autoRun.AutoRun -like '*cmd_macros*') {
    Remove-ItemProperty -Path $regPath -Name "AutoRun" -ErrorAction SilentlyContinue
    Write-Host "  OK Removed AutoRun" -ForegroundColor Green
} else {
    Write-Host "  OK No AutoRun configured" -ForegroundColor Gray
}

# 5. Remove Git Bash aliases
Write-Host "[5/6] Cleaning Git Bash..." -ForegroundColor Green
$bashrcPath = "$env:USERPROFILE\.bashrc"
if (Test-Path $bashrcPath) {
    $content = Get-Content $bashrcPath -Raw
    if ($content -match "# Cygwin aliases") {
        $newContent = $content -replace '(?s)\r?\n# Cygwin aliases.*', ''
        Set-Content -Path $bashrcPath -Value $newContent -NoNewline
        Write-Host "  OK Removed aliases" -ForegroundColor Green
    } else {
        Write-Host "  OK No aliases found" -ForegroundColor Gray
    }
} else {
    Write-Host "  OK No .bashrc found" -ForegroundColor Gray
}

# 6. Remove Cygwin bash aliases
Write-Host "[6/6] Cleaning Cygwin..." -ForegroundColor Green
$cygwinBashrc = "C:\cygwin64\home\$env:USERNAME\.bashrc"
if (Test-Path $cygwinBashrc) {
    $content = Get-Content $cygwinBashrc -Raw
    if ($content -match "# Cygwin aliases") {
        $newContent = $content -replace '(?s)\r?\n# Cygwin aliases.*', ''
        Set-Content -Path $cygwinBashrc -Value $newContent -NoNewline
        Write-Host "  OK Removed aliases" -ForegroundColor Green
    } else {
        Write-Host "  OK No aliases found" -ForegroundColor Gray
    }
} else {
    Write-Host "  SKIP Cygwin .bashrc not found" -ForegroundColor Gray
}

# Done
Write-Host ""
Write-Host "=== Uninstall Complete ===" -ForegroundColor Green
Write-Host ""
Write-Host "NOTE: Restart your terminal for changes to take effect." -ForegroundColor Yellow
