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

// findCygwinRoot returns the Cygwin installation root by checking three
// locations in priority order:
//
//  1. HKCU\SOFTWARE\Cygwin\Installations  (modern multi-install key, user scope)
//  2. HKLM\SOFTWARE\Cygwin\Installations  (modern multi-install key, machine scope)
//  3. HKLM\SOFTWARE\Cygwin\setup → rootdir (legacy setup.exe key)
//  4. Compile-time default (C:\cygwin64)
func findCygwinRoot() string {
	// 1 & 2: Modern Installations key (written by every Cygwin since ~2016).
	// Values are named by a 64-bit hash of cygwin1.dll's path; the data is
	// the installation root, optionally prefixed with "\\?\" (NT long path).
	for _, root := range []registry.Key{registry.CURRENT_USER, registry.LOCAL_MACHINE} {
		k, err := registry.OpenKey(root,
			`SOFTWARE\Cygwin\Installations`,
			registry.QUERY_VALUE|registry.ENUMERATE_SUB_KEYS)
		if err != nil {
			continue
		}
		names, _ := k.ReadValueNames(-1)
		for _, name := range names {
			val, _, err := k.GetStringValue(name)
			k.Close()
			if err != nil || val == "" {
				continue
			}
			// Strip NT long path prefix if present
			val = strings.TrimPrefix(val, `\\?\`)
			return val
		}
		k.Close()
	}

	// 3: Legacy setup.exe key (still written by the official Cygwin installer).
	for _, path := range []string{
		`SOFTWARE\Cygwin\setup`,
		`SOFTWARE\WOW6432Node\Cygwin\setup`, // 32-bit view on 64-bit Windows
	} {
		k, err := registry.OpenKey(registry.LOCAL_MACHINE, path, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		val, _, err := k.GetStringValue("rootdir")
		k.Close()
		if err == nil && val != "" {
			return val
		}
	}

	// 4: Fallback
	return defaultCygwinRoot
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

// shutdownCygwin terminates all Cygwin processes atomically using a Job Object.
// Processes that cannot be assigned to the job (e.g. already in a non-breakaway
// job on older Windows) are killed individually via TerminateProcess as fallback.
func shutdownCygwin() {
	fmt.Println("Terminating Cygwin processes...")

	processes := getCygwinProcesses()
	if len(processes) == 0 {
		fmt.Println("No Cygwin processes found.")
		return
	}

	// Create a temporary job object to group all Cygwin processes.
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		// Fall back to per-process termination if job creation fails.
		killProcessesDirect(processes)
		return
	}
	defer windows.CloseHandle(job)

	var unassigned []ProcessInfo
	for _, p := range processes {
		h, err := windows.OpenProcess(
			windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(p.Pid))
		if err != nil {
			continue
		}
		if err := windows.AssignProcessToJobObject(job, h); err != nil {
			// Already in a non-breakaway job — fall back for this process.
			unassigned = append(unassigned, p)
		}
		windows.CloseHandle(h)
	}

	// Atomically terminate all assigned processes.
	windows.TerminateJobObject(job, 1)

	// Kill any processes that couldn't be assigned to the job.
	killProcessesDirect(unassigned)

	// Verify.
	remaining := getCygwinProcesses()
	if len(remaining) == 0 {
		fmt.Printf("All %d Cygwin process(es) terminated.\n", len(processes))
	} else {
		fmt.Fprintf(os.Stderr, "Warning: %d process(es) still running.\n", len(remaining))
		os.Exit(1)
	}
}

// killProcessesDirect terminates a list of processes via TerminateProcess.
func killProcessesDirect(processes []ProcessInfo) {
	for _, p := range processes {
		h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(p.Pid))
		if err != nil {
			continue
		}
		windows.TerminateProcess(h, 1)
		windows.CloseHandle(h)
	}
}
