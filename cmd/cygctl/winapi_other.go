//go:build !windows

package main

import (
	"encoding/json"
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

	type psEntry struct {
		Id          int    `json:"Id"`
		ProcessName string `json:"ProcessName"`
	}

	// ConvertTo-Json emits an object (not array) when there is only one result.
	// Try array first, then fall back to single object.
	var entries []psEntry
	if err := json.Unmarshal(output, &entries); err != nil {
		var single psEntry
		if err := json.Unmarshal(output, &single); err != nil || single.Id == 0 {
			return nil
		}
		entries = []psEntry{single}
	}

	var processes []ProcessInfo
	for _, e := range entries {
		if e.Id > 0 && e.ProcessName != "" {
			processes = append(processes, ProcessInfo{Pid: e.Id, Name: e.ProcessName})
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
