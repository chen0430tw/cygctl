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

// findCygwinRoot returns the Cygwin installation root by checking several
// locations in priority order:
//
//  1. HKCU\SOFTWARE\Cygwin\Installations           (modern key, user scope, 64-bit)
//  2. HKCU\SOFTWARE\WOW6432Node\Cygwin\Installations (modern key, user scope, 32-bit)
//  3. HKLM\SOFTWARE\Cygwin\Installations           (modern key, machine scope, 64-bit)
//  4. HKLM\SOFTWARE\WOW6432Node\Cygwin\Installations (modern key, machine scope, 32-bit)
//  5. HKLM\SOFTWARE\Cygwin\setup → rootdir         (legacy setup.exe key)
//  6. HKLM\SOFTWARE\WOW6432Node\Cygwin\setup       (legacy key, 32-bit Cygwin)
//  7. Compile-time default (C:\cygwin64)
func findCygwinRoot() string {
	// 1 & 2: Modern Installations key (written by every Cygwin since ~2016).
	// Values are named by a 64-bit hash of cygwin1.dll's path; the data is
	// the installation root, optionally prefixed with "\\?\" (NT long path).
	for _, root := range []registry.Key{registry.CURRENT_USER, registry.LOCAL_MACHINE} {
		for _, subkey := range []string{
			`SOFTWARE\Cygwin\Installations`,
			`SOFTWARE\WOW6432Node\Cygwin\Installations`, // 32-bit Cygwin on 64-bit Windows
		} {
			k, err := registry.OpenKey(root, subkey,
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

var (
	modPsapi                    = windows.NewLazySystemDLL("psapi.dll")
	procGetProcessImageFileName = modPsapi.NewProc("GetProcessImageFileNameW")
)

// queryProcessImagePath returns the full executable path of a process.
// Uses GetProcessImageFileName (XP+) instead of QueryFullProcessImageName
// (Vista+) for broader compatibility.
func queryProcessImagePath(pid uint32) (string, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_INFORMATION, false, pid)
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(h)

	var buf [windows.MAX_PATH]uint16
	r, _, err := procGetProcessImageFileName.Call(
		uintptr(h),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(len(buf)),
	)
	if r == 0 {
		return "", err
	}
	ntPath := windows.UTF16ToString(buf[:r])
	return devicePathToWin32(ntPath), nil
}

// devicePathToWin32 converts an NT device path (e.g. \Device\HarddiskVolume1\foo.exe)
// to a Win32 drive-letter path (e.g. C:\foo.exe) using QueryDosDevice.
func devicePathToWin32(ntPath string) string {
	for c := 'A'; c <= 'Z'; c++ {
		drive := string(c) + ":"
		drivePtr, err := windows.UTF16PtrFromString(drive)
		if err != nil {
			continue
		}
		var devBuf [windows.MAX_PATH]uint16
		n, err := windows.QueryDosDevice(drivePtr, &devBuf[0], uint32(len(devBuf)))
		if err != nil || n == 0 {
			continue
		}
		devPath := windows.UTF16ToString(devBuf[:n])
		if strings.HasPrefix(ntPath, devPath+`\`) {
			return drive + ntPath[len(devPath):]
		}
	}
	return ntPath // fallback: return NT path as-is
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
