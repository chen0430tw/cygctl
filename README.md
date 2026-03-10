English | [简体中文](./README.zh-CN.md)

# cygctl

**cygctl** is a Windows command-line tool that lets you run Cygwin commands from any shell — PowerShell, CMD, or Git Bash — with the same interface as `wsl`, without touching Cygwin's own terminal emulator.

> [!IMPORTANT]
> **cygctl does NOT install, download, or set up Cygwin or WSL.**
> It is a control tool for running commands inside an **already-installed Cygwin environment**, with a WSL-like interface.
> To install Cygwin, visit [cygwin.com](https://www.cygwin.com). To install WSL, see the [Microsoft documentation](https://learn.microsoft.com/windows/wsl/install).

**cygctl is not a shell alias or a shim.** A naive `alias cyg='bash.exe'` breaks the moment you pipe data, check exit codes, or need to switch users. cygctl is a purpose-built binary that handles:

- **Correct stdio wiring** — stdin/stdout/stderr are connected properly so pipes and redirections work as expected
- **Exit code propagation** — the child process exit code is returned to the caller, enabling reliable scripting and CI use
- **Process lifecycle management** — enumerate, inspect, and shut down Cygwin processes via the Windows job object and process APIs
- **UAC elevation** — `sudo` launches an elevated child process and bridges its I/O back, which no alias can do
- **User switching** — `su` calls `CreateProcessWithLogonW` to start a process under a different Windows account
- **WSL interop** — path format conversion and cross-environment command dispatch between Cygwin and WSL
- **Package management** — `apt-cyg` rewritten in Go with proper dependency resolution

cygctl ships as a single executable with no dependencies — drop it into PATH and it's ready. Designed for AI Agents, developer scripts, and CI/CD pipelines.

## Quick Install

```powershell
# PowerShell (one-liner)
irm https://raw.githubusercontent.com/chen0430tw/cygctl/master/install.ps1 | iex
```

Restart your terminal after installation to use `cyg` and `apt` commands.

> [!WARNING]
> **PowerShell execution policy error (common gotcha)**
>
> If you see this error after installation:
> ```
> ...Microsoft.PowerShell_profile.ps1 cannot be loaded because running scripts is disabled on this system.
> ```
> Windows defaults to the `Restricted` execution policy, which blocks profile scripts from loading. The installer automatically sets your **current-user** policy to `RemoteSigned` (allows local scripts; still blocks unsigned scripts downloaded from the internet). If you installed manually or hit this error, run once in PowerShell:
> ```powershell
> Set-ExecutionPolicy -Scope CurrentUser -ExecutionPolicy RemoteSigned -Force
> ```
> Then open a new terminal.

> [!NOTE]
> **Cygwin installed on a drive other than C:\?** The installer reads the installation path from the registry (written by Cygwin's setup.exe), so it finds the correct location automatically. If auto-detection fails, pass the path explicitly:
> ```powershell
> .\install.ps1 -CygwinRoot D:\cygwin64
> ```

## Manual Install

```powershell
# Download binaries to Cygwin bin
$bin = "C:\cygwin64\bin"
Invoke-WebRequest -Uri "https://github.com/chen0430tw/cygctl/releases/latest/download/cygctl.exe" -OutFile "$bin\cygctl.exe"
Invoke-WebRequest -Uri "https://github.com/chen0430tw/cygctl/releases/latest/download/apt-cyg.exe" -OutFile "$bin\apt-cyg.exe"
Invoke-WebRequest -Uri "https://github.com/chen0430tw/cygctl/releases/latest/download/sudo.exe" -OutFile "$bin\sudo.exe"
Invoke-WebRequest -Uri "https://github.com/chen0430tw/cygctl/releases/latest/download/su.exe" -OutFile "$bin\su.exe"

# Add to PATH
[Environment]::SetEnvironmentVariable("PATH", "$bin;" + [Environment]::GetEnvironmentVariable("PATH", "User"), "User")
```

## Components

| File | Description |
|------|-------------|
| `cygctl.exe` | Main CLI tool |
| `apt-cyg.exe` | Package manager |
| `sudo.exe` | UAC elevation |
| `su.exe` | Switch Windows user (via `CreateProcessWithLogonW`) |

## Usage

### WSL Command Equivalents

If you're familiar with WSL, here's how commands map to `cyg`:

| WSL | cyg |
|-----|-----|
| `wsl` | `cyg` |
| `wsl ls -la /tmp` | `cyg ls -la /tmp` |
| `wsl -e ls -la` | `cyg --exec "ls -la"` |
| `wsl --cd "D:\Projects" -e pwd` | `cyg --cd "D:\Projects" --exec "pwd"` |
| `wsl --shutdown` | `cyg --shutdown` |
| `wsl --status` | `cyg --status` |

### Basic Commands

```bash
# Interactive shell
cyg

# Execute command
cyg --exec "ls -la /cygdrive/c"
cyg -e "echo hello"

# Execute with directory change
cyg --cd "D:\Projects" --exec "pwd"

# Direct command (like wsl)
cyg ls -la /tmp
```

### Package Management (apt-cyg)

```bash
apt update               # Update package list
apt search python        # Search packages
apt install vim git      # Install packages
apt list --installed     # List installed packages
apt show bash            # Show package info
apt depends vim          # Show dependencies
apt upgrade              # Upgrade packages
apt remove vim           # Remove packages
```

### Sudo (UAC Elevation)

```bash
sudo netstat -an
sudo notepad C:\Windows\System32\drivers\etc\hosts
```

### Su (Switch User)

```bash
# Open an interactive login shell as another Windows user
su alice

# Run a single command as another user
su alice whoami

# Specify user via cyg
cyg --user alice --cd "D:\Projects"
```

> **Note:** `su` uses `CreateProcessWithLogonW` and requires the Windows Secondary Logon service (`seclogon`) to be running. It is enabled by default on all modern Windows versions.

> [!WARNING]
> **Accounts with empty passwords cannot log in via `su`.** Windows security policy (`LimitBlankPasswordUse`) restricts blank-password accounts to local interactive logon (console) only; network/service logon — which `CreateProcessWithLogonW` uses — is blocked. To use `su`, the target account must have a password set.

### WSL Integration (cyg wsl)

`cyg wsl` lets you manage and interact with WSL from within Cygwin.

```bash
# Launch default WSL distro interactively
cyg wsl

# List all WSL distributions with state and version
cyg wsl --list

# Convert a path between Windows / Cygwin / WSL formats
cyg wsl --path "C:\Users\alice"
# Output: windows=C:\Users\alice
#         cygwin=/cygdrive/c/Users/alice
#         wsl=/mnt/c/Users/alice

# Run a command in the default WSL distro
cyg wsl --exec -- ls -la /tmp

# Run a command in a specific WSL distro
cyg wsl --exec Ubuntu -- whoami

# Shut down all WSL VMs
cyg wsl --shutdown
```

| Option | Alias | Description |
|--------|-------|-------------|
| `--list` | `-l` | List distros with name, state, and WSL version |
| `--path <path>` | `-p` | Convert path to Windows / Cygwin / WSL formats |
| `--exec [distro] -- <cmd>` | `-e` | Run command in distro (default if omitted) |
| `--shutdown` | | Shut down all WSL2 VMs |

### Status and Management

```bash
cyg --status    # Show Cygwin status
cyg --shutdown  # Terminate all Cygwin processes
cyg --version   # Show version
cyg --help      # Show help
```

## apt-cyg Commands

| Command | Description |
|---------|-------------|
| `update` | Download fresh package list |
| `install <pkg...>` | Install package(s) with dependencies |
| `remove <pkg...>` | Remove package(s) |
| `search <pattern>` | Search for packages |
| `list [--installed]` | List all or installed packages |
| `show <package>` | Show package info |
| `depends <package>` | Show dependencies |
| `rdepends <package>` | Show reverse dependencies |
| `upgrade [pkg...]` | Upgrade packages |
| `download <pkg...>` | Download without installing |
| `autoremove` | Find unused dependencies |
| `clean` | Clear package cache |
| `mirror [url]` | Set or show mirror |

## Usage in AI Agents and Scripts

`cyg` and `apt` work in **non-interactive shells** — `bash -c`, subprocesses, pipes, and AI agent tool environments (Claude Code, Cursor, etc.) — without any extra flags.

```bash
# All of these work after installation, even without -i
bash -c 'cyg ls -la /tmp'
bash -c 'apt install vim'
echo 'cyg ls /' | bash
```

**Why it works:** Bash loads `.bashrc` only in interactive sessions. For non-interactive shells, Bash respects the `BASH_ENV` environment variable and sources whatever file it points to. The installer:

1. Writes the `cyg`/`apt` functions to `~/.bash_env` (no interactive-only guard)
2. Sets `BASH_ENV=%USERPROFILE%\.bash_env` as a Windows user environment variable

Git Bash inherits Windows user env vars, so every new bash process — interactive or not — automatically loads the aliases.

> [!NOTE]
> `BASH_ENV` takes effect for **new** processes. If your shell was already open when you ran the installer, open a new terminal window.

> [!WARNING]
> **Cygwin interactive shell gotcha:** Inside a Cygwin shell, `$HOME` is `/home/<user>` (i.e., `C:\cygwin64\home\<user>\`), while `.bash_env` lives under Windows `%USERPROFILE%` (`C:\Users\<user>\`). They are different directories, so a plain `source $HOME/.bash_env` won't find the file. The installer uses `cygpath` to bridge this:
> ```bash
> [ -f "$(cygpath -u "$USERPROFILE")/.bash_env" ] && source "$(cygpath -u "$USERPROFILE")/.bash_env"
> ```
> The install script handles this automatically. If you set up manually, add the line above to Cygwin's `~/.bashrc`.

**Manual setup (if you installed binaries without the script):**

```powershell
# PowerShell — create ~/.bash_env and configure BASH_ENV
$utf8NoBom = New-Object System.Text.UTF8Encoding $false
$bashEnvContent = @'
cyg()    { MSYS_NO_PATHCONV=1 cygctl.exe  "$@"; }
apt()    { MSYS_NO_PATHCONV=1 apt-cyg.exe "$@"; }
'@
[System.IO.File]::WriteAllText("$env:USERPROFILE\.bash_env", $bashEnvContent, $utf8NoBom)
[Environment]::SetEnvironmentVariable("BASH_ENV", "$env:USERPROFILE\.bash_env", "User")
```

## install.ps1

The installer script configures:

1. **Download** - Fetches binaries from GitHub Releases
2. **PATH** - Adds `C:\cygwin64\bin` to user PATH
3. **PowerShell** - Creates `cyg` and `apt` functions
4. **CMD** - Creates doskey macros with AutoRun
5. **Shell aliases** - Writes `~/.bash_env` with `cyg`/`apt` functions, sets `BASH_ENV` Windows env var, and patches `~/.bashrc` to source it (enables both interactive and non-interactive shells)
6. **Cygwin** - Patches Cygwin `~/.bashrc` to source `~/.bash_env`

## Building

Requires Go 1.21+

```bash
make all      # Build all
make install  # Install to Cygwin bin
make clean    # Clean
```

## License

MIT
