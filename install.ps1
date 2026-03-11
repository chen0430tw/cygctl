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

# Detect Windows 11 24H2+ native sudo
# System32\sudo.exe is only created when the feature is enabled in Settings.
$NativeSudoPath = Join-Path $env:SystemRoot "System32\sudo.exe"
$NativeSudoActive = Test-Path $NativeSudoPath
if ($NativeSudoActive) {
    Write-Host "NOTICE: Windows 11 native sudo detected ($NativeSudoPath)." -ForegroundColor Yellow
    Write-Host "  The 'alias sudo=sudo.exe' lines will be skipped in shell configs" -ForegroundColor Yellow
    Write-Host "  to avoid overriding the built-in Windows sudo." -ForegroundColor Yellow
    Write-Host "  cygctl's sudo.exe is still installed and callable as 'sudo.exe'." -ForegroundColor Yellow
    Write-Host ""
}

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

# Create apt.exe and cyg.exe hardlinks so 'sudo apt' / 'sudo cyg' work.
# sudo.exe is a separate Win32 process — it cannot see PowerShell functions,
# so it needs real executables in PATH.
$hardlinks = @{ "apt.exe" = "apt-cyg.exe"; "cyg.exe" = "cygctl.exe" }
foreach ($link in $hardlinks.GetEnumerator()) {
    $linkPath   = Join-Path $InstallDir $link.Key
    $targetPath = Join-Path $InstallDir $link.Value
    if (Test-Path $linkPath) {
        Write-Host "  OK $($link.Key) already exists" -ForegroundColor Gray
    } elseif (Test-Path $targetPath) {
        try {
            New-Item -ItemType HardLink -Path $linkPath -Target $targetPath -Force | Out-Null
            Write-Host "  OK Created hardlink $($link.Key) -> $($link.Value)" -ForegroundColor Green
        } catch {
            Write-Host "  WARN Could not create hardlink $($link.Key): $_" -ForegroundColor Yellow
        }
    } else {
        Write-Host "  SKIP $($link.Value) not found, skipping $($link.Key)" -ForegroundColor Gray
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

# Write bash_env.sh to the shared ProgramData directory so all users get the
# same file regardless of which Windows account runs the installer.
$bashEnvPath = "$env:ProgramData\cygctl\bash_env.sh"
$bashEnvContent = @"
# Cygwin aliases
# MSYS_NO_PATHCONV=1 prevents Git Bash from mangling Unix paths (e.g. / -> C:/Program Files/Git/)
# before they reach cygctl / apt-cyg, which need to receive them verbatim.
cyg()    { MSYS_NO_PATHCONV=1 cygctl.exe  "`$@"; }
apt()    { MSYS_NO_PATHCONV=1 apt-cyg.exe "`$@"; }
alias cygctl='cygctl.exe'
alias apt-cyg='apt-cyg.exe'
alias su='su.exe'
"@
# Only alias sudo when Windows native sudo is not active, to avoid shadowing it.
if (-not $NativeSudoActive) {
    $bashEnvContent += "alias sudo='sudo.exe'`n"
}
[System.IO.File]::WriteAllText($bashEnvPath, $bashEnvContent.Replace("`r`n", "`n").TrimStart(), $utf8NoBom)
Write-Host "  OK Written $bashEnvPath" -ForegroundColor Green

# Set BASH_ENV machine-wide to the fixed ProgramData path.
# Non-interactive bash (agents, subprocesses, pipes) loads this automatically.
$currentBashEnv = [Environment]::GetEnvironmentVariable("BASH_ENV", "Machine")
if ($currentBashEnv -ne $bashEnvPath) {
    [Environment]::SetEnvironmentVariable("BASH_ENV", $bashEnvPath, "Machine")
    Write-Host "  OK Set BASH_ENV (all users)" -ForegroundColor Green
} else {
    Write-Host "  OK BASH_ENV already configured" -ForegroundColor Gray
}

# Git Bash (MSYS2 runtime) defaults to 'minimal' PATH mode, which only inherits
# critical Windows system directories — not machine-level user-installed programs
# (like C:\cygwin64\bin). Setting MSYS2_PATH_TYPE=inherit makes Git Bash include
# the full Windows PATH so cygctl tools are found without extra configuration.
$currentMsysPathType = [Environment]::GetEnvironmentVariable("MSYS2_PATH_TYPE", "Machine")
if ($currentMsysPathType -ne 'inherit') {
    [Environment]::SetEnvironmentVariable("MSYS2_PATH_TYPE", "inherit", "Machine")
    Write-Host "  OK Set MSYS2_PATH_TYPE=inherit (Git Bash will inherit full PATH)" -ForegroundColor Green
} else {
    Write-Host "  OK MSYS2_PATH_TYPE already set to inherit" -ForegroundColor Gray
}

# Patch Git Bash system-wide etc\bash.bashrc so interactive Git Bash sessions
# also load the aliases. The system bashrc applies to ALL users (unlike ~/.bashrc).
# For interactive shells BASH_ENV is not loaded automatically, so we source it explicitly.
$gitCmd = Get-Command git.exe -ErrorAction SilentlyContinue
if ($gitCmd) {
    $gitRoot = Split-Path (Split-Path $gitCmd.Source -Parent) -Parent
    $gitBashrc = "$gitRoot\etc\bash.bashrc"
    $gitSourceLine = @"

# Load cygctl aliases (non-interactive shells load this via BASH_ENV automatically)
[ -f "`$BASH_ENV" ] && source "`$BASH_ENV"
"@
    $gitSourceLineLF = $gitSourceLine.Replace("`r`n", "`n")

    if (Test-Path $gitBashrc) {
        $content = [System.IO.File]::ReadAllText($gitBashrc)
        if ($content -match "cygctl") {
            Write-Host "  OK Git Bash etc\bash.bashrc already patched" -ForegroundColor Gray
        } else {
            [System.IO.File]::WriteAllText($gitBashrc, $content + $gitSourceLineLF, $utf8NoBom)
            Write-Host "  OK Patched Git Bash etc\bash.bashrc (all users)" -ForegroundColor Green
        }
    } else {
        Write-Host "  SKIP Git Bash etc\bash.bashrc not found at $gitBashrc" -ForegroundColor Gray
    }
} else {
    Write-Host "  SKIP git.exe not found; skipping Git Bash bashrc patch" -ForegroundColor Gray
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
alias su='su.exe'
"@
# Only alias sudo when Windows native sudo is not active.
# In Cygwin, sudo.exe in /bin is found via PATH without an alias.
if (-not $NativeSudoActive) {
    $cygctlContent += "alias sudo='sudo.exe'`n"
}

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
if ($NativeSudoActive) {
    Write-Host "  sudo.exe - UAC elevation (call as 'sudo.exe'; 'sudo' uses Windows built-in)" -ForegroundColor Yellow
} else {
    Write-Host "  sudo    - UAC elevation"
}
Write-Host "  su      - Switch Windows user (requires Secondary Logon service)"
Write-Host ""
Write-Host "Aliases:" -ForegroundColor Cyan
Write-Host "  cyg -> cygctl"
Write-Host "  apt -> apt-cyg"
Write-Host ""
Write-Host "NOTE: Restart your terminal for changes to take effect." -ForegroundColor Yellow
