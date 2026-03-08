package main

import (
	"fmt"
	"os"
	"os/exec"
)

// isAptCygCommand reports whether arg is an apt-cyg sub-command that should
// be forwarded directly to apt-cyg.exe.
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

// runInteractive launches an interactive Cygwin shell, optionally as user.
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

// execCommand runs a shell command inside Cygwin, optionally in a working
// directory and/or as a different Windows user.
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
			cygPath := winToCygwin(workingDir)
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
		cygPath := winToCygwin(workingDir)
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

// runAptCyg forwards args to apt-cyg.exe.
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
	if err := cmd.Run(); err != nil {
		if cmd.ProcessState != nil {
			os.Exit(cmd.ProcessState.ExitCode())
		}
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
	os.Exit(0)
}

// runSudo forwards args to sudo.exe for UAC-elevated execution.
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
	if err := cmd.Run(); err != nil {
		if cmd.ProcessState != nil {
			os.Exit(cmd.ProcessState.ExitCode())
		}
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
	os.Exit(0)
}
