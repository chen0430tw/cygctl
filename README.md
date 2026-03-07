# cygctl

A WSL-like command-line tool for Cygwin, designed for AI Agents and developers.

## Features

- **Single executable** - No dependencies, just drop `cygctl.exe` into your PATH
- **WSL-like interface** - Familiar syntax for WSL users
- **AI Agent friendly** - Simple, predictable command structure
- **Full stdin/stdout support** - Proper pipe handling
- **Exit code propagation** - Correct exit codes for scripting

## Installation

```bash
# Copy cygctl.exe to Cygwin bin directory
cp cygctl.exe C:\cygwin64\bin\
```

## Usage

```bash
# Interactive shell
cygctl

# Execute command
cygctl --exec "ls -la /cygdrive/c"
cygctl -e "echo hello"

# Execute with directory change
cygctl --cd "D:\Projects" --exec "pwd"

# Direct command (like wsl)
cygctl ls -la /tmp

# Package management (apt-cyg)
cygctl install vim
cygctl search python

# Status and management
cygctl --status
cygctl --shutdown

# Help
cygctl --help
cygctl --version
```

## Building

Requires Go 1.26+

```bash
go build -o cygctl.exe .
```

## Why cygctl?

Existing Cygwin wrappers (like mintty) are terminal emulators, not command-line tools. cygctl fills the gap by providing a `wsl`-like interface for Cygwin, making it easy for:

- AI Agents to execute Cygwin commands
- Developers to script Cygwin operations
- CI/CD pipelines to interact with Cygwin

## License

MIT
