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
    [string]$InstallDir = "C:\cygwin64\bin",
    [switch]$Verify
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

# 5. Remove shell aliases (~/.bash_env, BASH_ENV env var, source line in ~/.bashrc)
Write-Host "[5/6] Cleaning shell aliases..." -ForegroundColor Green

$utf8NoBom = New-Object System.Text.UTF8Encoding $false

# Remove ~/.bash_env
$bashEnvPath = "$env:USERPROFILE\.bash_env"
if (Test-Path $bashEnvPath) {
    Remove-Item $bashEnvPath -Force
    Write-Host "  OK Removed ~/.bash_env" -ForegroundColor Green
} else {
    Write-Host "  OK ~/.bash_env not found" -ForegroundColor Gray
}

# Unset BASH_ENV (only if we set it — check it points to our file)
$currentBashEnv = [Environment]::GetEnvironmentVariable("BASH_ENV", "User")
if ($currentBashEnv -like "*\.bash_env") {
    [Environment]::SetEnvironmentVariable("BASH_ENV", $null, "User")
    Write-Host "  OK Cleared BASH_ENV" -ForegroundColor Green
} else {
    Write-Host "  OK BASH_ENV not set by us" -ForegroundColor Gray
}

# Remove source line from ~/.bashrc
$bashrcPath = "$env:USERPROFILE\.bashrc"
if (Test-Path $bashrcPath) {
    $content = Get-Content $bashrcPath -Raw
    if ($content -match "\.bash_env") {
        $newContent = $content -replace '(?s)(\r?\n)?# Load Cygwin aliases[^\n]*\r?\n\[ -f "[^"]*\.bash_env" \] && source "[^"]*\.bash_env"\r?\n?', ''
        $newContent = $newContent -replace '^\r?\n', ''
        if ($newContent.Trim() -eq '') {
            Remove-Item $bashrcPath -Force
            Write-Host "  OK Removed ~/.bashrc (was only aliases)" -ForegroundColor Green
        } else {
            [System.IO.File]::WriteAllText($bashrcPath, $newContent, $utf8NoBom)
            Write-Host "  OK Patched ~/.bashrc" -ForegroundColor Green
        }
    } else {
        Write-Host "  OK ~/.bashrc has no cygctl entries" -ForegroundColor Gray
    }
} else {
    Write-Host "  OK ~/.bashrc not found" -ForegroundColor Gray
}

# 6. Remove Cygwin bash aliases
Write-Host "[6/6] Cleaning Cygwin..." -ForegroundColor Green
$cygwinBashrc = "C:\cygwin64\home\$env:USERNAME\.bashrc"
if (Test-Path $cygwinBashrc) {
    $content = Get-Content $cygwinBashrc -Raw
    if ($content -match "\.bash_env") {
        $newContent = $content -replace '(?s)(\r?\n)?# Load Cygwin aliases[^\n]*\r?\n\[ -f "[^"]*\.bash_env" \] && source "[^"]*\.bash_env"\r?\n?', ''
        $newContent = $newContent -replace '^\r?\n', ''
        if ($newContent.Trim() -eq '') {
            Remove-Item $cygwinBashrc -Force
            Write-Host "  OK Removed Cygwin ~/.bashrc (was only aliases)" -ForegroundColor Green
        } else {
            [System.IO.File]::WriteAllText($cygwinBashrc, $newContent, $utf8NoBom)
            Write-Host "  OK Patched Cygwin ~/.bashrc" -ForegroundColor Green
        }
    } else {
        Write-Host "  OK Cygwin ~/.bashrc has no cygctl entries" -ForegroundColor Gray
    }
} else {
    Write-Host "  SKIP Cygwin .bashrc not found" -ForegroundColor Gray
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
$userPath = [Environment]::GetEnvironmentVariable("PATH", "User")
if ($userPath -like "*$InstallDir*") {
    $issues += "PATH still contains: $InstallDir"
    Write-Host "  [FAIL] PATH still contains cygwin" -ForegroundColor Red
} else {
    Write-Host "  [OK] PATH clean" -ForegroundColor Green
}

# Check PowerShell profile
$profilePath = $PROFILE
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
$macrosFile = "$env:USERPROFILE\cmd_macros.doskey"
if (Test-Path $macrosFile) {
    $issues += "CMD macros file still exists"
    Write-Host "  [FAIL] CMD macros file still exists" -ForegroundColor Red
} else {
    Write-Host "  [OK] CMD macros clean" -ForegroundColor Green
}

# Check ~/.bash_env removed
$bashEnvPath = "$env:USERPROFILE\.bash_env"
if (Test-Path $bashEnvPath) {
    $issues += "~/.bash_env still exists"
    Write-Host "  [FAIL] ~/.bash_env still exists" -ForegroundColor Red
} else {
    Write-Host "  [OK] ~/.bash_env removed" -ForegroundColor Green
}

# Check BASH_ENV cleared
$currentBashEnv = [Environment]::GetEnvironmentVariable("BASH_ENV", "User")
if ($currentBashEnv -like "*\.bash_env") {
    $issues += "BASH_ENV still points to cygctl file"
    Write-Host "  [FAIL] BASH_ENV not cleared" -ForegroundColor Red
} else {
    Write-Host "  [OK] BASH_ENV clear" -ForegroundColor Green
}

# Check Git Bash ~/.bashrc
$bashrcPath = "$env:USERPROFILE\.bashrc"
if (Test-Path $bashrcPath) {
    $content = Get-Content $bashrcPath -Raw
    if ($content -match "\.bash_env") {
        $issues += "Git Bash .bashrc still references .bash_env"
        Write-Host "  [FAIL] Git Bash .bashrc still references .bash_env" -ForegroundColor Red
    } else {
        Write-Host "  [OK] Git Bash .bashrc clean" -ForegroundColor Green
    }
} else {
    Write-Host "  [OK] Git Bash .bashrc clean (no file)" -ForegroundColor Green
}

# Check Cygwin ~/.bashrc
$cygwinBashrc = "C:\cygwin64\home\$env:USERNAME\.bashrc"
if (Test-Path $cygwinBashrc) {
    $content = Get-Content $cygwinBashrc -Raw
    if ($content -match "\.bash_env") {
        $issues += "Cygwin .bashrc still references .bash_env"
        Write-Host "  [FAIL] Cygwin .bashrc still references .bash_env" -ForegroundColor Red
    } else {
        Write-Host "  [OK] Cygwin .bashrc clean" -ForegroundColor Green
    }
} else {
    Write-Host "  [OK] Cygwin .bashrc clean (no file)" -ForegroundColor Green
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
