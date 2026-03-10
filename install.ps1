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
    [string]$CygwinRoot = "",   # auto-detected from registry if omitted
    [string]$Version = "latest"
)

$ErrorActionPreference = "Stop"

# Require administrator privileges for machine-wide installation
$currentPrincipal = New-Object Security.Principal.WindowsPrincipal([Security.Principal.WindowsIdentity]::GetCurrent())
if (-not $currentPrincipal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    Write-Host "Error: Administrator privileges required for system-wide installation." -ForegroundColor Red
    Write-Host "  Right-click PowerShell and choose 'Run as administrator', then re-run." -ForegroundColor Yellow
    exit 1
}

Write-Host "=== Cygctl Installer ===" -ForegroundColor Cyan
Write-Host ""

# Auto-detect Cygwin installation root
# Cygwin's setup.exe writes the install path to the registry; try that first.
if (-not $CygwinRoot) {
    $regPaths = @(
        "HKLM:\SOFTWARE\Cygwin\setup",
        "HKCU:\SOFTWARE\Cygwin\setup",
        "HKLM:\SOFTWARE\WOW6432Node\Cygwin\setup"
    )
    foreach ($rp in $regPaths) {
        $key = Get-ItemProperty $rp -ErrorAction SilentlyContinue
        if ($key -and $key.rootdir -and (Test-Path $key.rootdir)) {
            $CygwinRoot = $key.rootdir.TrimEnd('\')
            Write-Host "  Detected Cygwin at $CygwinRoot (registry)" -ForegroundColor Gray
            break
        }
    }
}

# Registry not found — fall back to common locations
if (-not $CygwinRoot) {
    $candidates = @("C:\cygwin64", "C:\cygwin", "D:\cygwin64", "D:\cygwin")
    foreach ($c in $candidates) {
        if (Test-Path $c) {
            $CygwinRoot = $c
            Write-Host "  Detected Cygwin at $CygwinRoot (filesystem scan)" -ForegroundColor Gray
            break
        }
    }
}

if (-not $CygwinRoot) {
    Write-Host "Error: Cygwin installation not found." -ForegroundColor Red
    Write-Host "  Pass the path explicitly:  install.ps1 -CygwinRoot D:\MyCygwin" -ForegroundColor Yellow
    Write-Host "  Or install Cygwin first from https://cygwin.com" -ForegroundColor Yellow
    exit 1
}

$InstallDir = Join-Path $CygwinRoot "bin"

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

# 2. Add to machine-wide PATH (all users)
Write-Host "[2/6] Configuring PATH..." -ForegroundColor Green
$machinePath = [Environment]::GetEnvironmentVariable("PATH", "Machine")
if ($machinePath -like "*$InstallDir*") {
    Write-Host "  OK Already in machine PATH" -ForegroundColor Gray
} else {
    [Environment]::SetEnvironmentVariable("PATH", "$InstallDir;$machinePath", "Machine")
    Write-Host "  OK Added to machine PATH (all users)" -ForegroundColor Green
}

# 3. PowerShell profile (all users)
Write-Host "[3/6] Configuring PowerShell..." -ForegroundColor Green

# Ensure scripts can run for all users. RemoteSigned allows local scripts
# (including the all-users profile) while blocking unsigned internet scripts.
$execPolicy = Get-ExecutionPolicy -Scope LocalMachine
if ($execPolicy -eq "Restricted" -or $execPolicy -eq "Undefined") {
    Set-ExecutionPolicy -Scope LocalMachine -ExecutionPolicy RemoteSigned -Force
    Write-Host "  OK Set machine execution policy to RemoteSigned (was: $execPolicy)" -ForegroundColor Green
} else {
    Write-Host "  OK Machine execution policy: $execPolicy" -ForegroundColor Gray
}

$profilePath = $PROFILE.AllUsersAllHosts
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

# 4. CMD macros (all users via HKLM)
Write-Host "[4/6] Configuring CMD..." -ForegroundColor Green
$macrosDir = "$env:ProgramData\cygctl"
$macrosFile = "$macrosDir\cmd_macros.doskey"
$macrosContent = "cyg=cygctl.exe `$*`napt=apt-cyg.exe `$*`n"

if (-not (Test-Path $macrosDir)) {
    New-Item -ItemType Directory -Path $macrosDir -Force | Out-Null
}
Set-Content -Path $macrosFile -Value $macrosContent -Force

$regPath = "HKLM:\Software\Microsoft\Command Processor"
if (-not (Test-Path $regPath)) {
    New-Item -Path $regPath -Force | Out-Null
}
Set-ItemProperty -Path $regPath -Name "AutoRun" -Value "doskey /macrofile=`"$macrosFile`""
Write-Host "  OK Created CMD macros (all users)" -ForegroundColor Green

# 5. Shell aliases via ~/.bash_env + BASH_ENV env var
# This makes aliases available in BOTH interactive shells and non-interactive subprocesses
# (AI agents, pipes, scripts) without needing -i or explicit source calls.
Write-Host "[5/6] Configuring shell aliases..." -ForegroundColor Green

# UTF-8 without BOM — a BOM at the start of a shell file causes bash to crash.
$utf8NoBom = New-Object System.Text.UTF8Encoding $false

# ~/.bash_env: no early-exit guard, so it is safe to source non-interactively.
# When BASH_ENV points here, bash loads it automatically for every non-interactive shell.
$bashEnvPath = "$env:USERPROFILE\.bash_env"
$bashEnvContent = @"
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
[System.IO.File]::WriteAllText($bashEnvPath, $bashEnvContent.Replace("`r`n", "`n").TrimStart(), $utf8NoBom)
Write-Host "  OK Written ~/.bash_env" -ForegroundColor Green

# Set BASH_ENV machine-wide so every non-interactive bash auto-sources ~/.bash_env.
# Using %USERPROFILE% (unexpanded) so Windows expands it per-user at process creation time.
$currentBashEnv = [Environment]::GetEnvironmentVariable("BASH_ENV", "Machine")
if ($currentBashEnv -ne '%USERPROFILE%\.bash_env') {
    [Environment]::SetEnvironmentVariable("BASH_ENV", '%USERPROFILE%\.bash_env', "Machine")
    Write-Host "  OK Set BASH_ENV (all users)" -ForegroundColor Green
} else {
    Write-Host "  OK BASH_ENV already configured" -ForegroundColor Gray
}

# Patch ~/.bashrc to source ~/.bash_env for interactive Git Bash sessions.
# Must appear before any early-exit guard (case $- in *i*) ;; *) return ;; esac).
$bashrcPath = "$env:USERPROFILE\.bashrc"
$bashrcSourceLine = @"

# Load Cygwin aliases (non-interactive shells load this via BASH_ENV automatically)
[ -f "`$HOME/.bash_env" ] && source "`$HOME/.bash_env"
"@
$bashrcSourceLineLF = $bashrcSourceLine.Replace("`r`n", "`n")

if (Test-Path $bashrcPath) {
    $content = Get-Content $bashrcPath -Raw
    if ($content -match "\.bash_env") {
        Write-Host "  OK ~/.bashrc already sources ~/.bash_env" -ForegroundColor Gray
    } else {
        $existing = [System.IO.File]::ReadAllText($bashrcPath)
        [System.IO.File]::WriteAllText($bashrcPath, $existing + $bashrcSourceLineLF, $utf8NoBom)
        Write-Host "  OK Patched ~/.bashrc" -ForegroundColor Green
    }
} else {
    [System.IO.File]::WriteAllText($bashrcPath, $bashrcSourceLineLF.TrimStart(), $utf8NoBom)
    Write-Host "  OK Created ~/.bashrc" -ForegroundColor Green
}

# 6. Cygwin /etc/profile.d/cygctl.sh (all users)
# Files in /etc/profile.d/ are sourced by /etc/profile for every user on login.
# This makes aliases available to all Cygwin users without touching individual home dirs.
Write-Host "[6/6] Configuring Cygwin (all users)..." -ForegroundColor Green
$profileDDir = "$CygwinRoot\etc\profile.d"
$cygctlProfileD = "$profileDDir\cygctl.sh"
$cygctlContent = @"
# cygctl aliases — sourced for all Cygwin users via /etc/profile.d/
cyg()    { cygctl.exe  "`$@"; }
apt()    { apt-cyg.exe "`$@"; }
alias cygctl='cygctl.exe'
alias apt-cyg='apt-cyg.exe'
alias sudo='sudo.exe'
alias su='su.exe'
"@

if (Test-Path $profileDDir) {
    [System.IO.File]::WriteAllText($cygctlProfileD, $cygctlContent.Replace("`r`n", "`n").TrimStart(), $utf8NoBom)
    Write-Host "  OK Written /etc/profile.d/cygctl.sh (all users)" -ForegroundColor Green
} else {
    Write-Host "  SKIP Cygwin /etc/profile.d not found" -ForegroundColor Gray
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
