package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	// defaultCygwinRoot is used as fallback when the registry key is absent.
	defaultCygwinRoot = `C:\cygwin64`
	Version           = "1.2.0"
)

// Executable names for alias detection
const (
	NameCygctl = "cygctl"
	NameCyg    = "cyg"
)

// CygwinRoot and derived paths are set in init() via registry lookup on
// Windows (or fall back to the compile-time default on other platforms).
var (
	CygwinRoot string
	CygwinBin  string
	BashExe    string
	AptCyg     string
	SudoCmd    string
	SuCmd      string
)

func init() {
	CygwinRoot = findCygwinRoot()
	CygwinBin = CygwinRoot + `\bin`
	BashExe = CygwinBin + `\bash.exe`
	AptCyg = CygwinBin + `\apt.exe`
	SudoCmd = CygwinBin + `\sudo.exe`
	SuCmd = CygwinBin + `\su.exe`
}

func main() {
	// Detect how we were invoked (supports symlinks/hardlinks)
	exeName := strings.ToLower(filepath.Base(os.Args[0]))
	// Remove .exe extension on Windows
	exeName = strings.TrimSuffix(exeName, ".exe")

	args := os.Args[1:]

	// No arguments - interactive shell
	if len(args) == 0 {
		runInteractive("")
		return
	}

	// Parse arguments with loop
	var (
		workingDir string
		command    string
		runUser    string
	)

	i := 0
	for i < len(args) {
		arg := args[i]

		switch {
		case arg == "--help" || arg == "-h" || arg == "/?":
			showHelp(exeName)
			return
		case arg == "--version":
			showVersion(exeName)
			return
		case arg == "--status":
			showStatus()
			return
		case arg == "--shutdown":
			shutdownCygwin()
			return
		case arg == "--exec" || arg == "-e":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "Error: Missing command for --exec")
				os.Exit(1)
			}
			command = strings.Join(args[i+1:], " ")
			i = len(args) // consume all remaining args
		case arg == "--cd":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "Error: Missing argument for --cd")
				os.Exit(1)
			}
			workingDir = args[i+1]
			i++
		case arg == "--user" || arg == "-u":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "Error: Missing argument for --user")
				os.Exit(1)
			}
			runUser = args[i+1]
			i++
		case arg == "wsl":
			runWslCommand(args[i+1:])
			return
		case isAptCygCommand(arg):
			runAptCyg(args[i:])
			return
		case arg == "sudo":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "Error: Missing command for sudo")
				os.Exit(1)
			}
			runSudo(args[i+1:])
			return
		default:
			// Unknown option or command - treat as command to execute
			command = strings.Join(args[i:], " ")
			i = len(args)
		}
		i++
	}

	// Execute command or launch interactive shell
	if command != "" {
		execCommand(command, workingDir, runUser)
	} else if workingDir != "" {
		// Only --cd specified, launch interactive shell in that directory
		execCommand("", workingDir, runUser)
	} else {
		runInteractive(runUser)
	}
}

func isAptCygCommand(arg string) bool {
	aptCommands := []string{
		"install", "reinstall", "remove", "uninstall", "purge",
		"update", "upgrade", "search", "searchall",
		"show", "info", "list", "listall", "listfiles",
		"check", "depends", "rdepends", "download",
		"autoremove", "clean", "mirror", "cache", "category",
	}
	for _, cmd := range aptCommands {
		if arg == cmd {
			return true
		}
	}
	return false
}

func runInteractive(user string) {
	var cmd *exec.Cmd
	if user != "" {
		// Delegate to su.exe which uses CreateProcessWithLogonW (Windows-native
		// user switching) instead of the unreliable Cygwin su package.
		if _, err := os.Stat(SuCmd); os.IsNotExist(err) {
			fmt.Fprintln(os.Stderr, "Error: su not found at", SuCmd)
			fmt.Fprintln(os.Stderr, "Please build and install su.exe first (make su).")
			os.Exit(2)
		}
		cmd = exec.Command(SuCmd, user)
	} else {
		if _, err := os.Stat(BashExe); os.IsNotExist(err) {
			fmt.Fprintln(os.Stderr, "Error: Cygwin bash not found at", BashExe)
			fmt.Fprintln(os.Stderr, "Please install Cygwin first (https://cygwin.com).")
			os.Exit(2)
		}
		cmd = exec.Command(BashExe, "-i")
	}
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(),
		"LANG=zh_TW.UTF-8",
		"LC_ALL=zh_TW.UTF-8",
	)
	if err := cmd.Run(); err != nil {
		if cmd.ProcessState != nil {
			os.Exit(cmd.ProcessState.ExitCode())
		}
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
	os.Exit(0)
}

func execCommand(command string, workingDir string, user string) {
	var cmd *exec.Cmd

	if user != "" {
		// Delegate to su.exe (Windows-native user switching via CreateProcessWithLogonW).
		if _, err := os.Stat(SuCmd); os.IsNotExist(err) {
			fmt.Fprintln(os.Stderr, "Error: su not found at", SuCmd)
			fmt.Fprintln(os.Stderr, "Please build and install su.exe first (make su).")
			os.Exit(2)
		}
	} else {
		if _, err := os.Stat(BashExe); os.IsNotExist(err) {
			fmt.Fprintln(os.Stderr, "Error: Cygwin bash not found at", BashExe)
			fmt.Fprintln(os.Stderr, "Please install Cygwin first (https://cygwin.com).")
			os.Exit(2)
		}
	}

	if user != "" {
		if workingDir != "" {
			cygPath := toCygwinPath(workingDir)
			if command != "" {
				cmd = exec.Command(SuCmd, user, fmt.Sprintf("cd '%s' && %s", cygPath, command))
			} else {
				cmd = exec.Command(SuCmd, user, fmt.Sprintf("cd '%s' && exec bash -i", cygPath))
			}
		} else {
			if command != "" {
				cmd = exec.Command(SuCmd, user, command)
			} else {
				cmd = exec.Command(SuCmd, user)
			}
		}
	} else if workingDir != "" {
		cygPath := toCygwinPath(workingDir)
		if command != "" {
			cmd = exec.Command(BashExe, "--login", "-c", fmt.Sprintf("cd '%s' && %s", cygPath, command))
		} else {
			cmd = exec.Command(BashExe, "-i")
			cmd.Dir = workingDir
		}
	} else {
		cmd = exec.Command(BashExe, "--login", "-c", command)
	}

	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(),
		"LANG=zh_TW.UTF-8",
		"LC_ALL=zh_TW.UTF-8",
		"CYGWIN=winsymlinks:native",
	)

	if err := cmd.Run(); err != nil {
		if cmd.ProcessState != nil {
			os.Exit(cmd.ProcessState.ExitCode())
		}
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
	os.Exit(0)
}

// toCygwinPath converts a Windows path to a Cygwin POSIX path using a pure-Go
// implementation.  This avoids spawning cygpath.exe as a subprocess.
// For the rare cases where a custom cygdrive prefix is configured in
// /etc/fstab the user-space default "/cygdrive" is used, which covers nearly
// all Cygwin installations.
func toCygwinPath(winPath string) string {
	// Normalise backslashes
	p := strings.ReplaceAll(winPath, `\`, "/")

	// Drive letter conversion: C:/... → /cygdrive/c/...
	if len(p) >= 2 && p[1] == ':' {
		p = "/cygdrive/" + strings.ToLower(string(p[0])) + p[2:]
	}
	return p
}


func runAptCyg(args []string) {
	if _, err := os.Stat(AptCyg); os.IsNotExist(err) {
		fmt.Fprintln(os.Stderr, "Error: apt-cyg not found at", AptCyg)
		fmt.Fprintln(os.Stderr, "Please install apt-cyg first.")
		os.Exit(2)
	}
	// Execute apt-cyg.exe directly (native Windows executable)
	cmd := exec.Command(AptCyg, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Run()
	os.Exit(cmd.ProcessState.ExitCode())
}

func runSudo(args []string) {
	if _, err := os.Stat(SudoCmd); os.IsNotExist(err) {
		fmt.Fprintln(os.Stderr, "Error: sudo not found at", SudoCmd)
		fmt.Fprintln(os.Stderr, "Please install sudo for Cygwin first.")
		os.Exit(2)
	}
	// Execute sudo.exe directly (native Windows UAC elevation)
	cmd := exec.Command(SudoCmd, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Run()
	os.Exit(cmd.ProcessState.ExitCode())
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
			fmt.Printf("Bash: %s\n", lines[0])
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
			fmt.Printf("Bash Version: %s\n", lines[0])
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

// ProcessInfo holds the PID and name of a running Cygwin process.
// getCygwinProcesses and shutdownCygwin are implemented in the
// platform-specific winapi_windows.go / winapi_other.go files.
type ProcessInfo struct {
	Pid  int
	Name string
}
