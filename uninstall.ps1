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
    [string]$CygwinRoot = "",   # auto-detected from registry if omitted
    [switch]$Verify
)

$ErrorActionPreference = "Stop"

# Require administrator privileges for machine-wide uninstallation
$currentPrincipal = New-Object Security.Principal.WindowsPrincipal([Security.Principal.WindowsIdentity]::GetCurrent())
if (-not $currentPrincipal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    Write-Host "Error: Administrator privileges required." -ForegroundColor Red
    Write-Host "  Right-click PowerShell and choose 'Run as administrator', then re-run." -ForegroundColor Yellow
    exit 1
}

# Auto-detect Cygwin installation root
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
            break
        }
    }
}
if (-not $CygwinRoot) {
    $candidates = @("C:\cygwin64", "C:\cygwin", "D:\cygwin64", "D:\cygwin")
    foreach ($c in $candidates) {
        if (Test-Path $c) { $CygwinRoot = $c; break }
    }
}
if (-not $CygwinRoot) {
    Write-Host "Warning: Cygwin installation not found; skipping Cygwin cleanup." -ForegroundColor Yellow
    $CygwinRoot = "C:\cygwin64"   # placeholder so path variables don't error
}

$InstallDir = Join-Path $CygwinRoot "bin"

Write-Host "=== Cygctl Uninstaller ===" -ForegroundColor Cyan
Write-Host ""

$Binaries = @("cygctl.exe", "apt-cyg.exe", "sudo.exe", "su.exe", "apt.exe", "cyg.exe")

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

# 2. Remove from machine-wide PATH
Write-Host "[2/6] Cleaning PATH..." -ForegroundColor Green
$machinePath = [Environment]::GetEnvironmentVariable("PATH", "Machine")

# Backup current machine PATH to a timestamped file for recovery
$backupDir = "$env:ProgramData\cygctl"
if (-not (Test-Path $backupDir)) { New-Item -ItemType Directory -Path $backupDir -Force | Out-Null }
$backupFile = "$backupDir\path_backup_$(Get-Date -Format 'yyyyMMdd_HHmmss').txt"
$machinePath | Out-File -FilePath $backupFile -Encoding utf8
Write-Host "  OK PATH backed up to $backupFile" -ForegroundColor Gray
# Keep only the 5 most recent backups
Get-ChildItem "$backupDir\path_backup_*.txt" | Sort-Object LastWriteTime -Descending | Select-Object -Skip 5 | Remove-Item -Force
if ($machinePath -like "*$InstallDir*") {
    $pathEntries = $machinePath -split ';' | Where-Object { $_ -and $_ -notlike "*$InstallDir*" }

    # Safety check: ensure essential Windows system directories survive the rewrite.
    $essentialWinPaths = @(
        "$env:SystemRoot\System32",
        "$env:SystemRoot",
        "$env:SystemRoot\System32\Wbem",
        "$env:SystemRoot\System32\WindowsPowerShell\v1.0"
    )
    $added = @()
    foreach ($p in $essentialWinPaths) {
        if ($pathEntries -notcontains $p) {
            $pathEntries += $p
            $added += $p
        }
    }
    if ($added.Count -gt 0) {
        Write-Host "  FIXED Restored missing Windows system paths: $($added -join ', ')" -ForegroundColor Yellow
    }

    $newPath = $pathEntries -join ';'
    [Environment]::SetEnvironmentVariable("PATH", $newPath, "Machine")
    Write-Host "  OK Removed from machine PATH" -ForegroundColor Green
} else {
    Write-Host "  OK Not in machine PATH" -ForegroundColor Gray
}

# 3. Remove PowerShell profile aliases (all-users profile)
Write-Host "[3/6] Cleaning PowerShell..." -ForegroundColor Green
$profilePath = $PROFILE.AllUsersAllHosts
if (Test-Path $profilePath) {
    $content = Get-Content $profilePath -Raw
    if ($content -match "function cyg") {
        # Remove the entire Cygwin aliases block (handles file start or after newline)
        $newContent = $content -replace '(?s)(\r?\n)?# Cygwin Command-Line Tool aliases\r?\nfunction cyg \{ cygctl\.exe \$args \}\r?\nfunction apt \{ apt-cyg\.exe \$args \}\r?\n?', ''
        # Also handle trailing newlines
        $newContent = $newContent -replace '^\r?\n', ''
        if ($newContent.Trim() -eq '') {
            Remove-Item $profilePath -Force
            Write-Host "  OK Removed profile (was only aliases)" -ForegroundColor Green
        } else {
            Set-Content -Path $profilePath -Value $newContent -NoNewline
            Write-Host "  OK Removed aliases" -ForegroundColor Green
        }
    } else {
        Write-Host "  OK No aliases found" -ForegroundColor Gray
    }
} else {
    Write-Host "  OK No profile found" -ForegroundColor Gray
}

# 4. Remove CMD macros (machine-wide)
Write-Host "[4/6] Cleaning CMD..." -ForegroundColor Green
$macrosFile = "$env:ProgramData\cygctl\cmd_macros.doskey"
if (Test-Path $macrosFile) {
    Remove-Item $macrosFile -Force
    Write-Host "  OK Removed macros file" -ForegroundColor Green
} else {
    Write-Host "  OK No macros file" -ForegroundColor Gray
}

$regPath = "HKLM:\Software\Microsoft\Command Processor"
$autoRun = Get-ItemProperty -Path $regPath -ErrorAction SilentlyContinue
if ($autoRun -and $autoRun.AutoRun -like '*cmd_macros*') {
    Remove-ItemProperty -Path $regPath -Name "AutoRun" -ErrorAction SilentlyContinue
    Write-Host "  OK Removed AutoRun" -ForegroundColor Green
} else {
    Write-Host "  OK No AutoRun configured" -ForegroundColor Gray
}

# 5. Remove shell aliases (bash_env.sh, BASH_ENV env var, Git Bash system bashrc patch)
Write-Host "[5/6] Cleaning shell aliases..." -ForegroundColor Green

$utf8NoBom = New-Object System.Text.UTF8Encoding $false

# Remove shared bash_env.sh from ProgramData
$bashEnvPath = "$env:ProgramData\cygctl\bash_env.sh"
if (Test-Path $bashEnvPath) {
    Remove-Item $bashEnvPath -Force
    Write-Host "  OK Removed bash_env.sh" -ForegroundColor Green
} else {
    Write-Host "  OK bash_env.sh not found" -ForegroundColor Gray
}

# Unset machine-wide BASH_ENV (only if it points to our file)
$currentBashEnv = [Environment]::GetEnvironmentVariable("BASH_ENV", "Machine")
if ($currentBashEnv -eq $bashEnvPath) {
    [Environment]::SetEnvironmentVariable("BASH_ENV", $null, "Machine")
    Write-Host "  OK Cleared BASH_ENV" -ForegroundColor Green
} else {
    Write-Host "  OK BASH_ENV not set by us" -ForegroundColor Gray
}

# Remove MSYS2_PATH_TYPE=inherit (only if we set it)
$currentMsysPathType = [Environment]::GetEnvironmentVariable("MSYS2_PATH_TYPE", "Machine")
if ($currentMsysPathType -eq 'inherit') {
    [Environment]::SetEnvironmentVariable("MSYS2_PATH_TYPE", $null, "Machine")
    Write-Host "  OK Cleared MSYS2_PATH_TYPE" -ForegroundColor Green
} else {
    Write-Host "  OK MSYS2_PATH_TYPE not set by us" -ForegroundColor Gray
}

# Remove cygctl source line from Git Bash system etc\bash.bashrc
$gitCmd = Get-Command git.exe -ErrorAction SilentlyContinue
if ($gitCmd) {
    $gitRoot = Split-Path (Split-Path $gitCmd.Source -Parent) -Parent
    $gitBashrc = "$gitRoot\etc\bash.bashrc"
    if (Test-Path $gitBashrc) {
        $content = [System.IO.File]::ReadAllText($gitBashrc)
        if ($content -match "cygctl") {
            $newContent = $content -replace '(?s)(\r?\n)?# Load cygctl aliases[^\n]*\r?\n\[ -f "\$BASH_ENV" \] && source "\$BASH_ENV"\r?\n?', ''
            $newContent = $newContent -replace '^\r?\n', ''
            [System.IO.File]::WriteAllText($gitBashrc, $newContent, $utf8NoBom)
            Write-Host "  OK Patched Git Bash etc\bash.bashrc" -ForegroundColor Green
        } else {
            Write-Host "  OK Git Bash etc\bash.bashrc has no cygctl entries" -ForegroundColor Gray
        }
    } else {
        Write-Host "  SKIP Git Bash etc\bash.bashrc not found" -ForegroundColor Gray
    }
} else {
    Write-Host "  SKIP git.exe not found" -ForegroundColor Gray
}

# 6. Remove Cygwin /etc/profile.d/cygctl.sh (all users)
Write-Host "[6/6] Cleaning Cygwin..." -ForegroundColor Green
$cygctlProfileD = "$CygwinRoot\etc\profile.d\cygctl.sh"
if (Test-Path $cygctlProfileD) {
    Remove-Item $cygctlProfileD -Force
    Write-Host "  OK Removed /etc/profile.d/cygctl.sh" -ForegroundColor Green
} else {
    Write-Host "  SKIP /etc/profile.d/cygctl.sh not found" -ForegroundColor Gray
}

# Done
Write-Host ""
Write-Host "=== Uninstall Complete ===" -ForegroundColor Green

# Verification
Write-Host ""
Write-Host "=== Verification ===" -ForegroundColor Cyan

$issues = @()

# Check binaries
foreach ($binary in $Binaries) {
    $path = Join-Path $InstallDir $binary
    if (Test-Path $path) {
        $issues += "Binary still exists: $path"
    }
}
if ($issues.Count -eq 0 -or -not ($issues | Where-Object { $_ -like "Binary*" })) {
    Write-Host "  [OK] Binaries removed" -ForegroundColor Green
}

# Check PATH
$machinePath = [Environment]::GetEnvironmentVariable("PATH", "Machine")
if ($machinePath -like "*$InstallDir*") {
    $issues += "PATH still contains: $InstallDir"
    Write-Host "  [FAIL] machine PATH still contains cygwin" -ForegroundColor Red
} else {
    Write-Host "  [OK] PATH clean" -ForegroundColor Green
}

# Check PowerShell profile
$profilePath = $PROFILE.AllUsersAllHosts
if (Test-Path $profilePath) {
    $content = Get-Content $profilePath -Raw
    if ($content -match "function cyg") {
        $issues += "PowerShell profile still has aliases"
        Write-Host "  [FAIL] PowerShell profile still has aliases" -ForegroundColor Red
    } else {
        Write-Host "  [OK] PowerShell profile clean" -ForegroundColor Green
    }
} else {
    Write-Host "  [OK] PowerShell profile clean (no file)" -ForegroundColor Green
}

# Check CMD macros
$macrosFile = "$env:ProgramData\cygctl\cmd_macros.doskey"
if (Test-Path $macrosFile) {
    $issues += "CMD macros file still exists"
    Write-Host "  [FAIL] CMD macros file still exists" -ForegroundColor Red
} else {
    Write-Host "  [OK] CMD macros clean" -ForegroundColor Green
}

# Check bash_env.sh removed
$bashEnvPath = "$env:ProgramData\cygctl\bash_env.sh"
if (Test-Path $bashEnvPath) {
    $issues += "bash_env.sh still exists"
    Write-Host "  [FAIL] bash_env.sh still exists" -ForegroundColor Red
} else {
    Write-Host "  [OK] bash_env.sh removed" -ForegroundColor Green
}

# Check BASH_ENV cleared
$currentBashEnv = [Environment]::GetEnvironmentVariable("BASH_ENV", "Machine")
if ($currentBashEnv -eq $bashEnvPath) {
    $issues += "BASH_ENV still set machine-wide"
    Write-Host "  [FAIL] BASH_ENV not cleared" -ForegroundColor Red
} else {
    Write-Host "  [OK] BASH_ENV clear" -ForegroundColor Green
}

# Check Git Bash system bashrc
$gitCmd = Get-Command git.exe -ErrorAction SilentlyContinue
if ($gitCmd) {
    $gitRoot = Split-Path (Split-Path $gitCmd.Source -Parent) -Parent
    $gitBashrc = "$gitRoot\etc\bash.bashrc"
    if (Test-Path $gitBashrc) {
        $content = Get-Content $gitBashrc -Raw
        if ($content -match "cygctl") {
            $issues += "Git Bash etc\bash.bashrc still has cygctl entries"
            Write-Host "  [FAIL] Git Bash etc\bash.bashrc still has cygctl entries" -ForegroundColor Red
        } else {
            Write-Host "  [OK] Git Bash etc\bash.bashrc clean" -ForegroundColor Green
        }
    } else {
        Write-Host "  [OK] Git Bash etc\bash.bashrc clean (no file)" -ForegroundColor Green
    }
} else {
    Write-Host "  [OK] Git Bash not found (skipped)" -ForegroundColor Gray
}

# Check Cygwin /etc/profile.d/cygctl.sh
$cygctlProfileD = "$CygwinRoot\etc\profile.d\cygctl.sh"
if (Test-Path $cygctlProfileD) {
    $issues += "/etc/profile.d/cygctl.sh still exists"
    Write-Host "  [FAIL] /etc/profile.d/cygctl.sh still exists" -ForegroundColor Red
} else {
    Write-Host "  [OK] /etc/profile.d/cygctl.sh removed" -ForegroundColor Green
}

Write-Host ""
if ($issues.Count -eq 0) {
    Write-Host "All clean!" -ForegroundColor Green
} else {
    Write-Host "Issues found:" -ForegroundColor Yellow
    foreach ($issue in $issues) {
        Write-Host "  - $issue" -ForegroundColor Yellow
    }
}

Write-Host ""
Write-Host "NOTE: Restart your terminal for changes to take effect." -ForegroundColor Yellow
