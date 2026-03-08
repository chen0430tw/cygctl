//go:build !windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// findCygwinRoot returns the default Cygwin root on non-Windows platforms.
func findCygwinRoot() string {
	return defaultCygwinRoot
}

// getCygwinProcesses falls back to PowerShell on non-Windows (compile-time
// stub; will not be called at runtime because Cygwin only runs on Windows).
func getCygwinProcesses() []ProcessInfo {
	cmd := exec.Command("powershell.exe", "-NoProfile", "-Command",
		"Get-Process | Where-Object { $_.Path -like '"+CygwinRoot+`\*' } | Select-Object Id, ProcessName | ConvertTo-Json`)
	output, err := cmd.Output()
	if err != nil {
		return nil
	}

	var processes []ProcessInfo
	outStr := strings.TrimSpace(string(output))
	if outStr == "" || outStr == "null" {
		return nil
	}

	lines := strings.Split(outStr, "\n")
	var currentPid int
	var currentName string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		line = strings.TrimSuffix(line, ",")
		if strings.Contains(line, `"Id"`) {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				fmt.Sscanf(strings.TrimSpace(parts[1]), "%d", &currentPid)
			}
		}
		if strings.Contains(line, `"ProcessName"`) {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				currentName = strings.Trim(strings.TrimSpace(parts[1]), `"`)
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

// shutdownCygwin falls back to PowerShell on non-Windows.
func shutdownCygwin() {
	fmt.Println("Terminating Cygwin processes...")

	cmd := exec.Command("powershell.exe", "-NoProfile", "-Command",
		"Get-Process | Where-Object { $_.Path -like '"+CygwinRoot+`\*' } | Stop-Process -Force`)
	output, err := cmd.CombinedOutput()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %s\n", output)
		os.Exit(1)
	}

	countCmd := exec.Command("powershell.exe", "-NoProfile", "-Command",
		"@(Get-Process | Where-Object { $_.Path -like '"+CygwinRoot+`\*' }).Count`)
	countOutput, _ := countCmd.Output()
	count := strings.TrimSpace(string(countOutput))
	if count == "0" {
		fmt.Println("All Cygwin processes terminated.")
	} else {
		fmt.Printf("%s process(es) still running.\n", count)
	}
}
