English | [简体中文](./README.zh-CN.md)

# cygctl

> [!IMPORTANT]
> **cygctl does NOT install, download, or set up Cygwin or WSL.**
> It is a control tool for running commands inside an **already-installed Cygwin environment**, with a WSL-like interface.
> To install Cygwin, visit [cygwin.com](https://www.cygwin.com). To install WSL, see the [Microsoft documentation](https://learn.microsoft.com/windows/wsl/install).

A WSL-like command-line tool for Cygwin, designed for AI Agents and developers.

## Features

- **Single executable** - No dependencies, just drop into PATH
- **WSL-like interface** - Familiar syntax for WSL users
- **AI Agent friendly** - Simple, predictable command structure
- **Full stdin/stdout support** - Proper pipe handling
- **Exit code propagation** - Correct exit codes for scripting
- **Package management** - apt-cyg rewritten in Go
- **UAC elevation** - sudo for Windows admin tasks
- **User switching** - su for switching between Windows user accounts

## Quick Install

```powershell
# PowerShell (one-liner)
irm https://raw.githubusercontent.com/chen0430tw/cygctl/master/install.ps1 | iex
```

Restart your terminal after installation to use `cyg` and `apt` commands.

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
# Update package list
apt update

# Search packages
apt search python

# Install packages
apt install vim git

# List installed packages
apt list --installed

# Show package info
apt show bash

# Show dependencies
apt depends vim

# Upgrade packages
apt upgrade

# Remove packages
apt remove vim
```

### Sudo (UAC Elevation)

```bash
# Run command with admin privileges
sudo netstat -an

# Edit protected files
sudo notepad C:\Windows\System32\drivers\etc\hosts
```

### Su (Switch User)

```bash
# Open an interactive login shell as another Windows user
cyg --user alice
su alice

# Run a single command as another user
cyg --user alice --exec "whoami"
su alice whoami

# Change directory then open shell as another user
cyg --user alice --cd "D:\Projects"
```

> **Note:** `su` uses `CreateProcessWithLogonW` and requires the Windows
> Secondary Logon service (`seclogon`) to be running.  It is enabled by
> default on all modern Windows versions.

### WSL Integration (cyg wsl)

`cyg wsl` lets you manage and interact with WSL from within Cygwin.

```bash
# Launch default WSL distro interactively
cyg wsl

# List all WSL distributions with state and version
cyg wsl --list

# Convert a path between Windows / Cygwin / WSL formats
cyg wsl --path "C:\Users\alice"
cyg wsl --path /cygdrive/c/Users/alice
cyg wsl --path /mnt/c/Users/alice
# Output: windows=C:\Users\alice
#         cygwin=/cygdrive/c/Users/alice
#         wsl=/mnt/c/Users/alice

# Run a command in the default WSL distro
cyg wsl --exec -- ls -la /tmp

# Run a command in a specific WSL distro
cyg wsl --exec Ubuntu -- whoami

# Shutdown all WSL VMs
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

## install.ps1

The installer script configures:

1. **Download** - Fetches binaries from GitHub Releases
2. **PATH** - Adds `C:\cygwin64\bin` to user PATH
3. **PowerShell** - Creates `cyg` and `apt` functions
4. **CMD** - Creates doskey macros with AutoRun
5. **Git Bash** - Adds aliases to `.bashrc`
6. **Cygwin** - Adds aliases to `.bashrc`

## Building

Requires Go 1.21+

```bash
# Build all
make all

# Install to Cygwin bin
make install

# Clean
make clean
```

## Why cygctl?

Existing Cygwin wrappers (like mintty) are terminal emulators, not command-line tools. cygctl fills the gap by providing a `wsl`-like interface for Cygwin, making it easy for:

- AI Agents to execute Cygwin commands
- Developers to script Cygwin operations
- CI/CD pipelines to interact with Cygwin

## License

MIT
