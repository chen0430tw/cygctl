package main

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// ProcessInfo holds the PID and name of a running Cygwin process.
// getCygwinProcesses and shutdownCygwin are implemented in the
// platform-specific winapi_windows.go / winapi_other.go files.
type ProcessInfo struct {
	Pid  int
	Name string
}

func showHelp(exeName string) {
	// Use shorter name in examples
	exampleName := exeName
	if exeName == NameCygctl {
		exampleName = NameCyg // Show 'cyg' in examples for brevity
	}

	help := `Cygwin Command-Line Tool v` + Version + `

Usage: ` + exampleName + ` [OPTIONS]... [COMMAND]...

Options:
    --exec <command>         Execute the specified command
    -e <command>             Alias for --exec
    --cd <path>              Change to specified directory before executing
    --user <name>            Run command (or shell) as the specified Windows user
    -u <name>                Alias for --user
    --status                 Show Cygwin status information
    --shutdown               Terminate all Cygwin processes
    --help, -h               Show this help message
    --version                Show version information

Apt Commands (package management):
    update                   Update package list (setup.ini)
    install <pkg...>         Install package(s) with dependencies
    reinstall <pkg...>       Force reinstall package(s)
    remove <pkg...>          Remove package(s)
    upgrade [pkg...]         Upgrade installed packages
    search <pattern>         Search packages by name/description
    searchall <term>         Search cygwin.com for packages containing file
    show <package>           Show package information
    list [pattern]           List installed packages
    listall <pattern>        Search all available packages
    listfiles <pkg...>       List files installed by package
    depends <package>        Show dependency tree
    rdepends <package>       Show reverse dependency tree
    check <pkg...>           Inspect packages for hollow/stub installs
    download <pkg...>        Download without installing
    category <cat>           List packages in category
    autoremove               Remove unused dependencies
    clean                    Clear package cache
    mirror [url]             Set or show mirror URL
    cache [dir]              Set or show package cache directory

Sudo Command:
    sudo <command>           Run command with elevated privileges (UAC)

WSL Commands:
    wsl                      Launch default WSL distro interactively
    wsl --list               List WSL distributions with state and version
    wsl --path <path>        Convert path between Windows / Cygwin / WSL formats
    wsl --exec [<distro>] -- <cmd...>  Run command in WSL distro
    wsl --shutdown           Shut down all WSL2 VMs

Examples:
    ` + exampleName + `                              Launch interactive Cygwin shell
    ` + exampleName + ` --exec "ls -la /cygdrive/c"  List C: drive contents
    ` + exampleName + ` --cd "D:\Projects" --exec "pwd"  Change dir and print working directory
    ` + exampleName + ` --status                     Show Cygwin + WSL status
    ` + exampleName + ` --user alice                 Open shell as alice
    ` + exampleName + ` --user alice --exec "whoami"  Run whoami as alice
    ` + exampleName + ` install vim                  Install vim package
    ` + exampleName + ` sudo nano /etc/hosts         Edit hosts file with admin rights
    ` + exampleName + ` wsl --list                   List WSL distros
    ` + exampleName + ` wsl --path "C:\Users\foo"    Show path in all three formats
    ` + exampleName + ` wsl --exec Ubuntu -- uname -a  Run uname in Ubuntu distro
`
	fmt.Print(help)
}

func showVersion(exeName string) {
	fmt.Printf("%s version %s\n", exeName, Version)
	fmt.Printf("Cygwin root: %s\n", CygwinRoot)
	fmt.Printf("Go runtime: %s/%s\n", runtime.GOOS, runtime.GOARCH)

	// Check bash version
	cmd := exec.Command(BashExe, "--version")
	output, err := cmd.Output()
	if err == nil {
		lines := strings.Split(string(output), "\n")
		if len(lines) > 0 {
			fmt.Printf("Bash: %s\n", strings.TrimRight(lines[0], "\r"))
		}
	}
}

func showStatus() {
	fmt.Println("=== Cygwin Status ===")
	fmt.Println()
	fmt.Printf("Installation Path: %s\n", CygwinRoot)

	// Bash version
	cmd := exec.Command(BashExe, "--version")
	output, err := cmd.Output()
	if err == nil {
		lines := strings.Split(string(output), "\n")
		if len(lines) > 0 {
			fmt.Printf("Bash Version: %s\n", strings.TrimRight(lines[0], "\r"))
		}
	}

	// Running processes
	fmt.Println()
	fmt.Println("Running Cygwin Processes:")
	processes := getCygwinProcesses()
	if len(processes) > 0 {
		for _, p := range processes {
			fmt.Printf("  PID: %d, Name: %s\n", p.Pid, p.Name)
		}
	} else {
		fmt.Println("  No Cygwin processes running")
	}

	// apt-cyg status
	fmt.Println()
	fmt.Println("Package Manager:")
	if _, err := os.Stat(AptCyg); os.IsNotExist(err) {
		fmt.Println("  apt-cyg: Not installed")
	} else {
		fmt.Println("  apt-cyg: Installed")
	}

	// sudo status
	if _, err := os.Stat(SudoCmd); os.IsNotExist(err) {
		fmt.Println("  sudo: Not installed")
	} else {
		fmt.Println("  sudo: Installed")
	}

	// WSL status
	fmt.Println()
	fmt.Println("=== WSL Status ===")
	fmt.Println()
	distros, err := wslListDistros()
	if err != nil {
		fmt.Println("  WSL not available:", err)
	} else {
		printWslDistros(distros)
	}
}
