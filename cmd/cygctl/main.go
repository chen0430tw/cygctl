package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	// defaultCygwinRoot is used as fallback when the registry key is absent.
	defaultCygwinRoot = `C:\cygwin64`
	Version           = "1.2.1"
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
	AptCyg = CygwinBin + `\apt-cyg.exe`
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
