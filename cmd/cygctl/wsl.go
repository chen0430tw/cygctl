package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// wslDistro holds parsed output from `wsl.exe --list --verbose`.
type wslDistro struct {
	Name    string
	State   string // "Running" | "Stopped"
	Version string // "1" | "2"
	Default bool
}

// runWslCommand is the entry point for `cyg wsl [options]`.
//
// Supported flags:
//
//	cyg wsl                           launch default distro interactively
//	cyg wsl --list                    list distros with state and version
//	cyg wsl --path <path>             convert path between all three formats
//	cyg wsl --exec [<distro>] -- <cmd...>  run command in distro (default if omitted)
//	cyg wsl --shutdown                shutdown all WSL2 VMs
func runWslCommand(args []string) {
	if len(args) == 0 {
		wslInteractive("")
		return
	}

	switch args[0] {
	case "--list", "-l":
		distros, err := wslListDistros()
		if err != nil {
			fmt.Fprintln(os.Stderr, "Error:", err)
			os.Exit(1)
		}
		printWslDistros(distros)

	case "--path", "-p":
		rest := args[1:]
		if len(rest) == 0 {
			fmt.Fprintln(os.Stderr, "Error: --path requires a path argument")
			fmt.Fprintln(os.Stderr, "Usage: cyg wsl --path <path>")
			os.Exit(1)
		}
		wslShowPathConversions(rest[0])

	case "--exec", "-e":
		wslExec(args[1:])

	case "--shutdown":
		wslShutdown()

	default:
		fmt.Fprintf(os.Stderr, "Error: unknown wsl option: %s\n", args[0])
		fmt.Fprintln(os.Stderr, "Run 'cyg --help' for usage.")
		os.Exit(1)
	}
}

// wslInteractive launches the given distro interactively (empty = default).
func wslInteractive(distro string) {
	var cmd *exec.Cmd
	if distro == "" {
		cmd = exec.Command("wsl.exe")
	} else {
		cmd = exec.Command("wsl.exe", "-d", distro)
	}
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		if cmd.ProcessState != nil {
			os.Exit(cmd.ProcessState.ExitCode())
		}
		fmt.Fprintln(os.Stderr, "Error: WSL is not available:", err)
		fmt.Fprintln(os.Stderr, "Please enable WSL first (https://learn.microsoft.com/windows/wsl).")
		os.Exit(1)
	}
	os.Exit(0)
}

// wslListDistros calls `wsl.exe --list --verbose` and parses the output.
// Returns an error if wsl.exe is not found or returns a non-zero exit code.
func wslListDistros() ([]wslDistro, error) {
	out, err := exec.Command("wsl.exe", "--list", "--verbose").Output()
	if err != nil {
		return nil, fmt.Errorf("wsl.exe not found or not available: %w", err)
	}

	// wsl.exe --list --verbose outputs UTF-16 on some Windows versions,
	// but modern builds emit UTF-8.  Strip BOM and NUL bytes just in case.
	cleaned := strings.Map(func(r rune) rune {
		if r == 0 || r == '\ufeff' {
			return -1
		}
		return r
	}, string(out))

	var distros []wslDistro
	for i, line := range strings.Split(cleaned, "\n") {
		if i == 0 { // header line (NAME STATE VERSION)
			continue
		}
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}

		isDefault := strings.HasPrefix(line, "*")
		line = strings.TrimLeft(line, "* ")

		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		distros = append(distros, wslDistro{
			Name:    fields[0],
			State:   fields[1],
			Version: fields[2],
			Default: isDefault,
		})
	}
	return distros, nil
}

// printWslDistros prints distro list in a human-readable table.
func printWslDistros(distros []wslDistro) {
	if len(distros) == 0 {
		fmt.Println("No WSL distributions installed.")
		return
	}
	fmt.Printf("  %-28s %-10s %s\n", "NAME", "STATE", "VERSION")
	for _, d := range distros {
		marker := "  "
		if d.Default {
			marker = "* "
		}
		fmt.Printf("%s%-28s %-10s WSL%s\n", marker, d.Name, d.State, d.Version)
	}
}

// wslShowPathConversions auto-detects the format of path and prints all three
// equivalent forms (Windows, Cygwin, WSL) in key=value format for easy parsing.
func wslShowPathConversions(path string) {
	win, cyg, wsl := convertPathAllFormats(path)
	fmt.Printf("windows=%s\n", win)
	fmt.Printf("cygwin=%s\n", cyg)
	fmt.Printf("wsl=%s\n", wsl)
}

// convertPathAllFormats returns (windows, cygwin, wsl) equivalents for path.
// It auto-detects whether path is Windows, Cygwin (/cygdrive/X/...) or WSL (/mnt/X/...).
func convertPathAllFormats(path string) (win, cyg, wsl string) {
	switch {
	case isWindowsPath(path):
		win = normaliseWin(path)
		cyg = winToCygwin(win)
		wsl = winToWsl(win)

	case isCygwinPath(path):
		win = cygwinToWin(path)
		cyg = normaliseUnix(path)
		wsl = winToWsl(win)

	case isWslPath(path):
		win = wslToWin(path)
		cyg = winToCygwin(win)
		wsl = normaliseUnix(path)

	default:
		// Generic POSIX path — cannot determine drive letter.
		win = path
		cyg = path
		wsl = path
	}
	return
}

func isWindowsPath(p string) bool {
	return len(p) >= 2 && p[1] == ':' ||
		strings.HasPrefix(p, `\\`)
}

func isCygwinPath(p string) bool {
	low := strings.ToLower(p)
	return strings.HasPrefix(low, "/cygdrive/") && len(p) >= 11
}

func isWslPath(p string) bool {
	low := strings.ToLower(p)
	// /mnt/X/ or /mnt/X (single drive letter mount)
	if !strings.HasPrefix(low, "/mnt/") || len(p) < 6 {
		return false
	}
	after := p[5:] // strip "/mnt/"
	return len(after) >= 1 && isLetter(after[0]) &&
		(len(after) == 1 || after[1] == '/')
}

func isLetter(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// normaliseWin normalises backslashes and ensures a trailing-slash-free path.
func normaliseWin(p string) string {
	return strings.TrimRight(strings.ReplaceAll(p, "/", `\`), `\`)
}

func normaliseUnix(p string) string {
	return strings.TrimRight(strings.ReplaceAll(p, `\`, "/"), "/")
}

// winToCygwin converts C:\foo\bar → /cygdrive/c/foo/bar
func winToCygwin(p string) string {
	p = strings.ReplaceAll(p, `\`, "/")
	if len(p) >= 2 && p[1] == ':' {
		return "/cygdrive/" + strings.ToLower(string(p[0])) + p[2:]
	}
	return p
}

// winToWsl converts C:\foo\bar → /mnt/c/foo/bar
func winToWsl(p string) string {
	p = strings.ReplaceAll(p, `\`, "/")
	if len(p) >= 2 && p[1] == ':' {
		return "/mnt/" + strings.ToLower(string(p[0])) + p[2:]
	}
	return p
}

// mountSuffixToWin converts the suffix after a Unix mount prefix
// (e.g. "c/foo/bar") to a Win32 path ("C:\foo\bar").
func mountSuffixToWin(rest, original string) string {
	if len(rest) == 0 {
		return original
	}
	drive := strings.ToUpper(string(rest[0]))
	tail := ""
	if len(rest) > 1 {
		tail = strings.ReplaceAll(rest[1:], "/", `\`)
	}
	return drive + ":" + tail
}

// cygwinToWin converts /cygdrive/c/foo/bar → C:\foo\bar
func cygwinToWin(p string) string {
	return mountSuffixToWin(p[len("/cygdrive/"):], p)
}

// wslToWin converts /mnt/c/foo/bar → C:\foo\bar
func wslToWin(p string) string {
	return mountSuffixToWin(p[len("/mnt/"):], p)
}

// wslExec runs a command inside a WSL distro.
//
// Syntax (after stripping leading "wsl --exec"):
//
//	[<distro>] -- <cmd> [args...]
//	-- <cmd> [args...]        (use default distro)
func wslExec(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Error: --exec requires [<distro>] -- <command>")
		os.Exit(1)
	}

	var distro string
	var cmdArgs []string

	// Determine if first arg is a distro name or "--"
	if args[0] == "--" {
		cmdArgs = args[1:]
	} else {
		distro = args[0]
		// Expect "--" separator next
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "Error: missing '--' separator before command")
			os.Exit(1)
		}
		if args[1] != "--" {
			fmt.Fprintln(os.Stderr, "Error: expected '--' after distro name, got:", args[1])
			os.Exit(1)
		}
		cmdArgs = args[2:]
	}

	if len(cmdArgs) == 0 {
		fmt.Fprintln(os.Stderr, "Error: no command specified after '--'")
		os.Exit(1)
	}

	var wslArgs []string
	if distro != "" {
		wslArgs = append(wslArgs, "-d", distro)
	}
	wslArgs = append(wslArgs, "--")
	wslArgs = append(wslArgs, cmdArgs...)

	cmd := exec.Command("wsl.exe", wslArgs...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		if cmd.ProcessState != nil {
			os.Exit(cmd.ProcessState.ExitCode())
		}
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}

// wslShutdown shuts down all WSL2 VMs.
func wslShutdown() {
	fmt.Println("Shutting down WSL...")
	out, err := exec.Command("wsl.exe", "--shutdown").CombinedOutput()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %s\n%s\n", err, out)
		os.Exit(1)
	}
	fmt.Println("WSL shutdown complete.")
}
