<#
.SYNOPSIS
    Cygctl Installer - Install Cygwin CLI tools

.DESCRIPTION
    Downloads and installs cygctl, apt-cyg, sudo for Cygwin.
    Configures PATH and shell aliases.

.EXAMPLE
    irm https://raw.githubusercontent.com/chen0430tw/cygctl/master/install.ps1 | iex
#>

param(
    [string]$InstallDir = "C:\cygwin64\bin",
    [string]$Version = "latest"
)

$ErrorActionPreference = "Stop"

Write-Host "=== Cygctl Installer ===" -ForegroundColor Cyan
Write-Host ""

# Check Cygwin exists
if (-not (Test-Path "C:\cygwin64")) {
    Write-Host "Error: Cygwin not found at C:\cygwin64" -ForegroundColor Red
    Write-Host "Please install Cygwin first from https://cygwin.com" -ForegroundColor Yellow
    exit 1
}

# GitHub release URL
$BaseUrl = if ($Version -eq "latest") {
    "https://github.com/chen0430tw/cygctl/releases/latest/download"
} else {
    "https://github.com/chen0430tw/cygctl/releases/download/$Version"
}

$Binaries = @("cygctl.exe", "apt-cyg.exe", "sudo.exe", "su.exe")

# 1. Download binaries
Write-Host "[1/6] Downloading binaries..." -ForegroundColor Green
foreach ($binary in $Binaries) {
    $dest = Join-Path $InstallDir $binary
    if (Test-Path $dest) {
        Write-Host "  OK $binary already exists" -ForegroundColor Gray
        continue
    }

    $url = "$BaseUrl/$binary"
    Write-Host "  Downloading $binary..." -NoNewline
    try {
        Invoke-WebRequest -Uri $url -OutFile $dest -UseBasicParsing
        Write-Host " OK" -ForegroundColor Green
    } catch {
        Write-Host " FAILED" -ForegroundColor Red
        Write-Host "  Error: $_" -ForegroundColor Red
    }
}

# 2. Add to PATH
Write-Host "[2/6] Configuring PATH..." -ForegroundColor Green
$userPath = [Environment]::GetEnvironmentVariable("PATH", "User")
if ($userPath -like "*$InstallDir*") {
    Write-Host "  OK Already in PATH" -ForegroundColor Gray
} else {
    [Environment]::SetEnvironmentVariable("PATH", "$InstallDir;$userPath", "User")
    Write-Host "  OK Added to PATH" -ForegroundColor Green
}

# 3. PowerShell profile
Write-Host "[3/6] Configuring PowerShell..." -ForegroundColor Green
$profilePath = $PROFILE
$profileDir = Split-Path $profilePath -Parent

if (-not (Test-Path $profileDir)) {
    New-Item -ItemType Directory -Path $profileDir -Force | Out-Null
}

$aliases = @"

# Cygwin Command-Line Tool aliases
function cyg { cygctl.exe `$args }
function apt { apt-cyg.exe `$args }
"@

if (Test-Path $profilePath) {
    $content = Get-Content $profilePath -Raw
    if ($content -match "function cyg") {
        Write-Host "  OK Aliases already exist" -ForegroundColor Gray
    } else {
        Add-Content -Path $profilePath -Value $aliases
        Write-Host "  OK Added aliases" -ForegroundColor Green
    }
} else {
    Set-Content -Path $profilePath -Value $aliases.TrimStart()
    Write-Host "  OK Created profile" -ForegroundColor Green
}

# 4. CMD macros
Write-Host "[4/6] Configuring CMD..." -ForegroundColor Green
$macrosFile = "$env:USERPROFILE\cmd_macros.doskey"
$macrosContent = "cyg=cygctl.exe `$*`napt=apt-cyg.exe `$*`n"

Set-Content -Path $macrosFile -Value $macrosContent -Force

$regPath = "HKCU:\Software\Microsoft\Command Processor"
if (-not (Test-Path $regPath)) {
    New-Item -Path $regPath -Force | Out-Null
}
Set-ItemProperty -Path $regPath -Name "AutoRun" -Value "doskey /macrofile=`"$macrosFile`""
Write-Host "  OK Created CMD macros" -ForegroundColor Green

# 5. Git Bash aliases
Write-Host "[5/6] Configuring Git Bash..." -ForegroundColor Green
$bashrcPath = "$env:USERPROFILE\.bashrc"
$bashAliases = @"

# Cygwin aliases
# MSYS_NO_PATHCONV=1 prevents Git Bash from mangling Unix paths (e.g. / -> C:/Program Files/Git/)
# before they reach cygctl / apt-cyg, which need to receive them verbatim.
cyg()    { MSYS_NO_PATHCONV=1 cygctl.exe  "`$@"; }
apt()    { MSYS_NO_PATHCONV=1 apt-cyg.exe "`$@"; }
alias cygctl='cygctl.exe'
alias apt-cyg='apt-cyg.exe'
alias sudo='sudo.exe'
alias su='su.exe'
"@

if (Test-Path $bashrcPath) {
    $content = Get-Content $bashrcPath -Raw
    if ($content -match "MSYS_NO_PATHCONV") {
        Write-Host "  OK Aliases already exist" -ForegroundColor Gray
    } else {
        Add-Content -Path $bashrcPath -Value $bashAliases
        Write-Host "  OK Added aliases" -ForegroundColor Green
    }
} else {
    Set-Content -Path $bashrcPath -Value $bashAliases.TrimStart()
    Write-Host "  OK Created .bashrc" -ForegroundColor Green
}

# 6. Cygwin bash aliases
Write-Host "[6/6] Configuring Cygwin..." -ForegroundColor Green
$cygwinBashrc = "C:\cygwin64\home\$env:USERNAME\.bashrc"
if (Test-Path (Split-Path $cygwinBashrc)) {
    if (Test-Path $cygwinBashrc) {
        $content = Get-Content $cygwinBashrc -Raw
        if ($content -match "MSYS_NO_PATHCONV") {
            Write-Host "  OK Aliases already exist" -ForegroundColor Gray
        } else {
            Add-Content -Path $cygwinBashrc -Value $bashAliases
            Write-Host "  OK Added aliases" -ForegroundColor Green
        }
    } else {
        Set-Content -Path $cygwinBashrc -Value $bashAliases.TrimStart()
        Write-Host "  OK Created .bashrc" -ForegroundColor Green
    }
} else {
    Write-Host "  SKIP Cygwin home not found" -ForegroundColor Gray
}

# Done
Write-Host ""
Write-Host "=== Install Complete ===" -ForegroundColor Green
Write-Host ""
Write-Host "Installed commands:" -ForegroundColor Cyan
Write-Host "  cygctl  - Cygwin CLI tool"
Write-Host "  apt-cyg - Package manager"
Write-Host "  sudo    - UAC elevation"
Write-Host "  su      - Switch Windows user (requires Secondary Logon service)"
Write-Host ""
Write-Host "Aliases:" -ForegroundColor Cyan
Write-Host "  cyg -> cygctl"
Write-Host "  apt -> apt-cyg"
Write-Host ""
Write-Host "NOTE: Restart your terminal for changes to take effect." -ForegroundColor Yellow
