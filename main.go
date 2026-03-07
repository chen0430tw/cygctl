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
	CygwinRoot  = `C:\cygwin64`
	CygwinBin   = CygwinRoot + `\bin`
	BashExe     = CygwinBin + `\bash.exe`
	AptCyg      = CygwinRoot + `\usr\local\bin\apt-cyg`
	SudoCmd     = CygwinRoot + `\usr\local\bin\sudo`
	Version     = "1.0.0"
)

func main() {
	args := os.Args[1:]

	// No arguments - interactive shell
	if len(args) == 0 {
		runInteractive()
		return
	}

	// Parse arguments with loop
	var (
		workingDir string
		command    string
		mode       string // "normal", "apt-cyg", "sudo"
	)

	i := 0
	for i < len(args) {
		arg := args[i]

		switch {
		case arg == "--help" || arg == "-h" || arg == "/?":
			showHelp()
			return
		case arg == "--version":
			showVersion()
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
			i += 2
		case arg == "--user":
			// Skip user argument (not fully implemented yet)
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "Error: Missing argument for --user")
				os.Exit(1)
			}
			i += 2
		case isAptCygCommand(arg):
			mode = "apt-cyg"
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

	// Execute based on mode
	switch mode {
	case "apt-cyg":
		// Already handled above
	case "sudo":
		// Already handled above
	default:
		if command != "" {
			execCommand(command, workingDir)
		} else if workingDir != "" {
			// Only --cd specified, launch interactive shell in that directory
			execCommand("", workingDir)
		} else {
			runInteractive()
		}
	}
}

func isAptCygCommand(arg string) bool {
	aptCommands := []string{"install", "remove", "update", "upgrade", "search", "show", "list", "check", "reinstall"}
	for _, cmd := range aptCommands {
		if arg == cmd {
			return true
		}
	}
	return false
}

func runInteractive() {
	cmd := exec.Command(BashExe, "-i")
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(),
		"LANG=zh_TW.UTF-8",
		"LC_ALL=zh_TW.UTF-8",
	)
	cmd.Run()
	os.Exit(cmd.ProcessState.ExitCode())
}

func execCommand(command string, workingDir string) {
	var cmd *exec.Cmd

	if workingDir != "" {
		cygPath := toCygwinPath(workingDir)
		if command != "" {
			cmd = exec.Command(BashExe, "--login", "-c", fmt.Sprintf("cd '%s' && %s", cygPath, command))
		} else {
			// Only cd, launch interactive shell in that directory
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

	cmd.Run()
	os.Exit(cmd.ProcessState.ExitCode())
}

func toCygwinPath(winPath string) string {
	// Use cygpath for accurate conversion
	cygpathExe := filepath.Join(CygwinBin, "cygpath.exe")
	cmd := exec.Command(cygpathExe, "-u", winPath)
	output, err := cmd.Output()
	if err != nil {
		// Fallback to regex
		cygPath := strings.ReplaceAll(winPath, `\`, "/")
		if len(cygPath) >= 2 && cygPath[1] == ':' {
			cygPath = "/cygdrive/" + strings.ToLower(string(cygPath[0])) + cygPath[2:]
		}
		return cygPath
	}
	return strings.TrimSpace(string(output))
}

func runAptCyg(args []string) {
	if _, err := os.Stat(AptCyg); os.IsNotExist(err) {
		fmt.Fprintln(os.Stderr, "Error: apt-cyg not found at", AptCyg)
		fmt.Fprintln(os.Stderr, "Please install apt-cyg first.")
		os.Exit(2)
	}
	command := "apt-cyg " + strings.Join(args, " ")
	execCommand(command, "")
}

func runSudo(args []string) {
	if _, err := os.Stat(SudoCmd); os.IsNotExist(err) {
		fmt.Fprintln(os.Stderr, "Error: sudo not found at", SudoCmd)
		fmt.Fprintln(os.Stderr, "Please install sudo for Cygwin first.")
		os.Exit(2)
	}
	command := "sudo " + strings.Join(args, " ")
	execCommand(command, "")
}

func showHelp() {
	help := `Cygwin Command-Line Tool v` + Version + `

Usage: cyg [OPTIONS]... [COMMAND]...

Options:
    --exec <command>         Execute the specified command
    -e <command>             Alias for --exec
    --cd <path>              Change to specified directory before executing
    --status                 Show Cygwin status information
    --shutdown               Terminate all Cygwin processes
    --help, -h               Show this help message
    --version                Show version information

Apt-cyg Commands (package management):
    install <package>        Install a package
    remove <package>         Remove a package
    update                   Update package cache
    upgrade                  Upgrade installed packages
    search <keyword>         Search for packages
    show <package>           Show package information
    list                     List installed packages
    check                    Check for hollow/stub packages

Sudo Command:
    sudo <command>           Run command with elevated privileges (UAC)

Examples:
    cyg                              Launch interactive Cygwin shell
    cyg --exec "ls -la /cygdrive/c"  List C: drive contents
    cyg --cd "D:\Projects" --exec "pwd"  Change dir and print working directory
    cyg --status                     Show Cygwin status
    cyg install vim                  Install vim package
    cyg sudo nano /etc/hosts         Edit hosts file with admin rights
`
	fmt.Print(help)
}

func showVersion() {
	fmt.Printf("cyg version %s\n", Version)
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
}

type ProcessInfo struct {
	Pid  int
	Name string
}

func getCygwinProcesses() []ProcessInfo {
	// Use PowerShell to get Cygwin processes
	cmd := exec.Command("powershell.exe", "-NoProfile", "-Command",
		"Get-Process | Where-Object { $_.Path -like '"+CygwinRoot+"\\*' } | Select-Object Id, ProcessName | ConvertTo-Json")
	output, err := cmd.Output()
	if err != nil {
		return nil
	}

	var processes []ProcessInfo
	// Parse JSON output
	lines := strings.Split(string(output), "\n")
	var currentPid int
	var currentName string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.Contains(line, `"Id"`) {
			// Extract PID
			parts := strings.Split(line, ":")
			if len(parts) == 2 {
				fmt.Sscanf(strings.TrimSpace(parts[1]), "%d", &currentPid)
			}
		}
		if strings.Contains(line, `"ProcessName"`) {
			// Extract name
			parts := strings.Split(line, ":")
			if len(parts) == 2 {
				currentName = strings.Trim(strings.TrimSpace(parts[1]), `"`,)
			}
		}
		if currentPid > 0 && currentName != "" {
			processes = append(processes, ProcessInfo{Pid: currentPid, Name: currentName})
			currentPid = 0
			currentName = ""
		}
	}
	return processes
}

func shutdownCygwin() {
	fmt.Println("Terminating Cygwin processes...")

	// Use PowerShell to kill Cygwin processes
	cmd := exec.Command("powershell.exe", "-NoProfile", "-Command",
		"Get-Process | Where-Object { $_.Path -like '"+CygwinRoot+"\\*' } | Stop-Process -Force")
	output, err := cmd.CombinedOutput()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %s\n", output)
		os.Exit(1)
	}

	// Count terminated processes
	countCmd := exec.Command("powershell.exe", "-NoProfile", "-Command",
		"@(Get-Process | Where-Object { $_.Path -like '"+CygwinRoot+"\\*' }).Count")
	countOutput, _ := countCmd.Output()
	count := strings.TrimSpace(string(countOutput))
	if count == "0" {
		fmt.Println("All Cygwin processes terminated")
	} else {
		fmt.Printf("%s process(es) still running\n", count)
	}
}
