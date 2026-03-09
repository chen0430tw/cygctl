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

# Set BASH_ENV so every non-interactive bash (agents, subprocesses, pipes) auto-sources ~/.bash_env.
# Git Bash inherits Windows user env vars, so this takes effect in new processes immediately.
$currentBashEnv = [Environment]::GetEnvironmentVariable("BASH_ENV", "User")
if ($currentBashEnv -ne "$env:USERPROFILE\.bash_env") {
    [Environment]::SetEnvironmentVariable("BASH_ENV", "$env:USERPROFILE\.bash_env", "User")
    Write-Host "  OK Set BASH_ENV" -ForegroundColor Green
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

# 6. Cygwin ~/.bashrc
# In Cygwin, $HOME is the Cygwin home (/home/<user>), NOT %USERPROFILE%.
# We use cygpath to convert the Windows %USERPROFILE% path so the source line
# resolves to the same ~/.bash_env that BASH_ENV points to.
Write-Host "[6/6] Configuring Cygwin..." -ForegroundColor Green
$cygwinBashrc = "C:\cygwin64\home\$env:USERNAME\.bashrc"
$cygwinSourceLine = @"

# Load Cygwin aliases — use cygpath so this resolves even though
# `$HOME (Cygwin) != %USERPROFILE% (Windows).
[ -f "`$(cygpath -u "`$USERPROFILE")/.bash_env" ] && source "`$(cygpath -u "`$USERPROFILE")/.bash_env"
"@
$cygwinSourceLineLF = $cygwinSourceLine.Replace("`r`n", "`n")

if (Test-Path (Split-Path $cygwinBashrc)) {
    if (Test-Path $cygwinBashrc) {
        $content = Get-Content $cygwinBashrc -Raw
        if ($content -match "\.bash_env") {
            Write-Host "  OK Cygwin ~/.bashrc already sources ~/.bash_env" -ForegroundColor Gray
        } else {
            $existing = [System.IO.File]::ReadAllText($cygwinBashrc)
            [System.IO.File]::WriteAllText($cygwinBashrc, $existing + $cygwinSourceLineLF, $utf8NoBom)
            Write-Host "  OK Patched Cygwin ~/.bashrc" -ForegroundColor Green
        }
    } else {
        [System.IO.File]::WriteAllText($cygwinBashrc, $cygwinSourceLineLF.TrimStart(), $utf8NoBom)
        Write-Host "  OK Created Cygwin ~/.bashrc" -ForegroundColor Green
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
