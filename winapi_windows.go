//go:build windows

package main

import (
	"fmt"
	"os"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// findCygwinRoot reads the Cygwin installation path from the Windows registry.
// Falls back to the compiled-in default if the registry key is absent.
func findCygwinRoot() string {
	// Try 64-bit registry view first
	k, err := registry.OpenKey(registry.LOCAL_MACHINE,
		`SOFTWARE\Cygwin\setup`, registry.QUERY_VALUE)
	if err != nil {
		// Fall back to 32-bit view on 64-bit Windows
		k, err = registry.OpenKey(registry.LOCAL_MACHINE,
			`SOFTWARE\WOW6432Node\Cygwin\setup`, registry.QUERY_VALUE)
		if err != nil {
			return defaultCygwinRoot
		}
	}
	defer k.Close()

	val, _, err := k.GetStringValue("rootdir")
	if err != nil || val == "" {
		return defaultCygwinRoot
	}
	return val
}

// getCygwinProcesses enumerates running processes whose executable path falls
// under CygwinRoot using the Win32 Toolhelp32 snapshot API.  This avoids
// spawning powershell.exe (which carries a 500 ms–2 s startup penalty).
func getCygwinProcesses() []ProcessInfo {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil
	}
	defer windows.CloseHandle(snap)

	var pe windows.ProcessEntry32
	pe.Size = uint32(unsafe.Sizeof(pe))
	if err := windows.Process32First(snap, &pe); err != nil {
		return nil
	}

	rootLower := strings.ToLower(CygwinRoot)
	var result []ProcessInfo
	for {
		if fullPath, err := queryProcessImagePath(pe.ProcessID); err == nil {
			if strings.HasPrefix(strings.ToLower(fullPath), rootLower+`\`) ||
				strings.EqualFold(fullPath, CygwinRoot) {
				name := windows.UTF16ToString(pe.ExeFile[:])
				name = strings.TrimSuffix(name, ".exe")
				result = append(result, ProcessInfo{
					Pid:  int(pe.ProcessID),
					Name: name,
				})
			}
		}
		if err := windows.Process32Next(snap, &pe); err != nil {
			break
		}
	}
	return result
}

// queryProcessImagePath returns the full executable path of a process.
func queryProcessImagePath(pid uint32) (string, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(h)

	var buf [windows.MAX_PATH]uint16
	size := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &size); err != nil {
		return "", err
	}
	return windows.UTF16ToString(buf[:size]), nil
}

// shutdownCygwin terminates all processes whose image path begins with
// CygwinRoot using TerminateProcess, avoiding a powershell.exe spawn.
func shutdownCygwin() {
	fmt.Println("Terminating Cygwin processes...")

	processes := getCygwinProcesses()
	if len(processes) == 0 {
		fmt.Println("No Cygwin processes found.")
		return
	}

	terminated := 0
	for _, p := range processes {
		h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(p.Pid))
		if err != nil {
			continue
		}
		if err := windows.TerminateProcess(h, 1); err == nil {
			terminated++
		}
		windows.CloseHandle(h)
	}

	// Verify no processes remain
	remaining := getCygwinProcesses()
	if len(remaining) == 0 {
		fmt.Printf("All %d Cygwin process(es) terminated.\n", terminated)
	} else {
		fmt.Fprintf(os.Stderr, "Warning: %d process(es) still running.\n", len(remaining))
		os.Exit(1)
	}
}
