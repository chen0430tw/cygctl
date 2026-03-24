//go:build windows

package main

import (
	"crypto/rand"
	"encoding/gob"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// isSpecialUser reports whether username refers to a built-in Windows identity
// accessible via token duplication (no password required).
func isSpecialUser(username string) bool {
	switch strings.ToLower(username) {
	case "root", "system", "sys", "ti", "trustedinstaller":
		return true
	}
	return false
}

// enableDebugPrivilege adds SeDebugPrivilege to the current process token,
// which is required to open protected processes such as winlogon.exe.
func enableDebugPrivilege() error {
	var tok windows.Token
	if err := windows.OpenProcessToken(
		windows.CurrentProcess(),
		windows.TOKEN_ADJUST_PRIVILEGES|windows.TOKEN_QUERY,
		&tok,
	); err != nil {
		return fmt.Errorf("OpenProcessToken: %w", err)
	}
	defer tok.Close()

	var luid windows.LUID
	namePtr, _ := windows.UTF16PtrFromString("SeDebugPrivilege")
	if err := windows.LookupPrivilegeValue(nil, namePtr, &luid); err != nil {
		return fmt.Errorf("LookupPrivilegeValue: %w", err)
	}

	tp := windows.Tokenprivileges{
		PrivilegeCount: 1,
		Privileges: [1]windows.LUIDAndAttributes{
			{Luid: luid, Attributes: windows.SE_PRIVILEGE_ENABLED},
		},
	}
	return windows.AdjustTokenPrivileges(tok, false, &tp, 0, nil, nil)
}

// findPidByName returns the PID of the first process whose image name (without
// .exe suffix) matches name (case-insensitive) via Toolhelp32 snapshot.
func findPidByName(name string) (uint32, error) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(snap)

	var pe windows.ProcessEntry32
	pe.Size = uint32(unsafe.Sizeof(pe))
	if err := windows.Process32First(snap, &pe); err != nil {
		return 0, err
	}
	want := strings.ToLower(name)
	for {
		got := strings.ToLower(strings.TrimSuffix(windows.UTF16ToString(pe.ExeFile[:]), ".exe"))
		if got == want {
			return pe.ProcessID, nil
		}
		if err := windows.Process32Next(snap, &pe); err != nil {
			break
		}
	}
	return 0, fmt.Errorf("process '%s' not found", name)
}

// duplicateTokenFrom opens the process with the given PID and duplicates its
// primary token as a new primary token with TOKEN_ALL_ACCESS.
func duplicateTokenFrom(pid uint32) (windows.Token, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_INFORMATION, false, pid)
	if err != nil {
		return 0, fmt.Errorf("OpenProcess(%d): %w", pid, err)
	}
	defer windows.CloseHandle(h)

	var src windows.Token
	const access = windows.TOKEN_DUPLICATE | windows.TOKEN_QUERY | windows.TOKEN_IMPERSONATE
	if err := windows.OpenProcessToken(h, access, &src); err != nil {
		return 0, fmt.Errorf("OpenProcessToken: %w", err)
	}
	defer src.Close()

	var dup windows.Token
	if err := windows.DuplicateTokenEx(
		src,
		windows.TOKEN_ALL_ACCESS,
		nil,
		windows.SecurityImpersonation,
		windows.TokenPrimary,
		&dup,
	); err != nil {
		return 0, fmt.Errorf("DuplicateTokenEx: %w", err)
	}
	return dup, nil
}

// fixTokenSession sets the SessionId on tok to match the calling process's
// interactive session.  This prevents CLI tools spawned via CreateProcessWithTokenW
// from accidentally landing in Session 0 (the service isolation session).
func fixTokenSession(tok windows.Token) {
	var callerTok windows.Token
	if err := windows.OpenProcessToken(
		windows.CurrentProcess(), windows.TOKEN_QUERY, &callerTok,
	); err != nil {
		return
	}
	defer callerTok.Close()

	var sessionID uint32
	var retLen uint32
	if err := windows.GetTokenInformation(
		callerTok, windows.TokenSessionId,
		(*byte)(unsafe.Pointer(&sessionID)),
		uint32(unsafe.Sizeof(sessionID)), &retLen,
	); err != nil {
		return
	}
	// Ignore error — non-fatal; the process will still spawn, just possibly
	// in the wrong session for GUI apps (CLI tools are unaffected).
	windows.SetTokenInformation(
		tok, windows.TokenSessionId,
		(*byte)(unsafe.Pointer(&sessionID)),
		uint32(unsafe.Sizeof(sessionID)),
	)
}

// getSystemToken acquires a SYSTEM primary token by duplicating winlogon.exe's
// token (winlogon always runs as NT AUTHORITY\SYSTEM).
func getSystemToken() (windows.Token, error) {
	if err := enableDebugPrivilege(); err != nil {
		return 0, fmt.Errorf("enableDebugPrivilege: %w", err)
	}
	pid, err := findPidByName("winlogon")
	if err != nil {
		return 0, fmt.Errorf("findPidByName(winlogon): %w", err)
	}
	tok, err := duplicateTokenFrom(pid)
	if err != nil {
		return 0, err
	}
	fixTokenSession(tok)
	return tok, nil
}

// getTrustedInstallerToken acquires a TrustedInstaller primary token.
// It starts the TrustedInstaller service if it is not already running,
// then duplicates the service host process token.
func getTrustedInstallerToken() (windows.Token, error) {
	if err := enableDebugPrivilege(); err != nil {
		return 0, fmt.Errorf("enableDebugPrivilege: %w", err)
	}

	scm, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return 0, fmt.Errorf("OpenSCManager: %w", err)
	}
	defer windows.CloseServiceHandle(scm)

	svcNameW, _ := windows.UTF16PtrFromString("TrustedInstaller")
	svc, err := windows.OpenService(scm, svcNameW, windows.SERVICE_START|windows.SERVICE_QUERY_STATUS)
	if err != nil {
		return 0, fmt.Errorf("OpenService(TrustedInstaller): %w", err)
	}
	defer windows.CloseServiceHandle(svc)

	var status windows.SERVICE_STATUS
	if err := windows.QueryServiceStatus(svc, &status); err != nil {
		return 0, fmt.Errorf("QueryServiceStatus: %w", err)
	}

	if status.CurrentState != windows.SERVICE_RUNNING {
		if err := windows.StartService(svc, 0, nil); err != nil {
			return 0, fmt.Errorf("StartService(TrustedInstaller): %w", err)
		}
		// Poll up to 5 s for the service to reach RUNNING state.
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			time.Sleep(100 * time.Millisecond)
			if err := windows.QueryServiceStatus(svc, &status); err != nil {
				break
			}
			if status.CurrentState == windows.SERVICE_RUNNING {
				break
			}
		}
		if status.CurrentState != windows.SERVICE_RUNNING {
			return 0, fmt.Errorf("TrustedInstaller service did not start in time")
		}
	}

	pid, err := findPidByName("TrustedInstaller")
	if err != nil {
		return 0, fmt.Errorf("findPidByName(TrustedInstaller): %w", err)
	}

	// TrustedInstaller's process token can only be duplicated from a SYSTEM
	// context.  Impersonate SYSTEM (via winlogon) for the duration of the call,
	// then revert immediately after.
	sysTok, err := getSystemToken()
	if err != nil {
		return 0, fmt.Errorf("getSystemToken (needed to open TrustedInstaller): %w", err)
	}
	ret, _, lerr := procImpersonateLoggedOnUser.Call(uintptr(sysTok))
	if ret == 0 {
		sysTok.Close()
		return 0, fmt.Errorf("ImpersonateLoggedOnUser: %w", lerr)
	}
	tiTok, tiErr := duplicateTokenFrom(pid)
	procRevertToSelf.Call()
	sysTok.Close()
	if tiErr == nil {
		fixTokenSession(tiTok)
	}
	return tiTok, tiErr
}

var (
	advapi32su                  = windows.NewLazySystemDLL("advapi32.dll")
	procCreateProcessWithTokenW = advapi32su.NewProc("CreateProcessWithTokenW")
	procImpersonateLoggedOnUser = advapi32su.NewProc("ImpersonateLoggedOnUser")
	procRevertToSelf            = advapi32su.NewProc("RevertToSelf")
)

// spawnWithToken launches exe with args under the identity represented by tok
// using CreateProcessWithTokenW (requires SeImpersonatePrivilege, which every
// Administrator process holds by default).
func spawnWithToken(tok windows.Token, exe string, args []string) error {
	exePtr, _ := windows.UTF16PtrFromString(exe)
	cmdLine := makeCmdLine(append([]string{exe}, args...))
	cmdPtr, _ := windows.UTF16PtrFromString(cmdLine)

	var si windows.StartupInfo
	si.Cb = uint32(unsafe.Sizeof(si))
	si.Flags = 0x00000001 // STARTF_USESHOWWINDOW
	si.ShowWindow = 0     // SW_HIDE
	var pi windows.ProcessInformation

	ret, _, lerr := procCreateProcessWithTokenW.Call(
		uintptr(tok),
		0, // dwLogonFlags = 0 (no profile load)
		uintptr(unsafe.Pointer(exePtr)),
		uintptr(unsafe.Pointer(cmdPtr)),
		createNoWindow,
		0, // lpEnvironment = NULL: inherit caller's environment
		0, // lpCurrentDirectory = NULL: inherit caller's cwd
		uintptr(unsafe.Pointer(&si)),
		uintptr(unsafe.Pointer(&pi)),
	)
	if ret == 0 {
		return fmt.Errorf("CreateProcessWithTokenW: %w", lerr)
	}
	windows.WaitForSingleObject(pi.Process, windows.INFINITE)
	windows.CloseHandle(pi.Process)
	windows.CloseHandle(pi.Thread)
	return nil
}

// runServerAsToken is the server-side entry point for `su system` and `su ti`.
// It acquires the appropriate token, then runs the same TCP reverse-connection
// proxy as runServer — spawning the client via CreateProcessWithTokenW instead
// of CreateProcessWithLogonW (no password needed).
func runServerAsToken(username string, cmdArgs []string) int {
	var (
		tok windows.Token
		err error
	)
	switch strings.ToLower(username) {
	case "root", "system", "sys":
		fmt.Fprintln(os.Stderr, "[su] Acquiring SYSTEM token via winlogon.exe...")
		tok, err = getSystemToken()
	case "ti", "trustedinstaller":
		fmt.Fprintln(os.Stderr, "[su] Acquiring TrustedInstaller token...")
		tok, err = getTrustedInstallerToken()
	default:
		fmt.Fprintf(os.Stderr, "su: unknown special user '%s'\n", username)
		return 1
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "su: cannot acquire %s token: %v\n", username, err)
		fmt.Fprintln(os.Stderr, "     (make sure you are running as Administrator)")
		return 1
	}
	defer tok.Close()

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintf(os.Stderr, "su: cannot create listener: %v\n", err)
		return 1
	}
	defer lis.Close()

	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "su: cannot locate su.exe: %v\n", err)
		return 1
	}

	otpBytes := make([]byte, 16)
	rand.Read(otpBytes)
	otp := hex.EncodeToString(otpBytes)

	clientArgs := append([]string{"--client", lis.Addr().String(), otp}, cmdArgs...)

	spawnErr := make(chan error, 1)
	go func() {
		spawnErr <- spawnWithToken(tok, exe, clientArgs)
		lis.Close()
	}()

	type result struct {
		conn net.Conn
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		c, e := lis.Accept()
		ch <- result{c, e}
	}()

	var conn net.Conn
	select {
	case res := <-ch:
		if res.err != nil {
			select {
			case serr := <-spawnErr:
				if serr != nil {
					fmt.Fprintf(os.Stderr, "su: %v\n", serr)
				} else {
					fmt.Fprintln(os.Stderr, "su: subprocess did not connect back")
				}
			default:
				fmt.Fprintln(os.Stderr, "su: accept error")
			}
			return 1
		}
		conn = res.conn
	case <-time.After(acceptTimeout):
		fmt.Fprintln(os.Stderr, "su: timed out waiting for elevated session")
		lis.Close()
		return 1
	}
	defer conn.Close()

	enc := gob.NewEncoder(conn)
	dec := gob.NewDecoder(conn)

	// One-time token handshake — prevents rogue local connections.
	if err := enc.Encode(otp); err != nil {
		return 1
	}
	var ok bool
	if err := dec.Decode(&ok); err != nil || !ok {
		fmt.Fprintln(os.Stderr, "su: authentication failed — unexpected process connected")
		return 1
	}

	sc := make(chan os.Signal, 1)
	signal.Notify(sc, os.Interrupt)
	go func() {
		for range sc {
			enc.Encode(&msg{Name: "ctrlc"})
		}
	}()

	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				enc.Encode(&msg{Name: "stdin", Data: buf[:n]})
			}
			if err != nil {
				if err == io.EOF {
					enc.Encode(&msg{Name: "close"})
				}
				return
			}
		}
	}()

	for {
		var m msg
		if err := dec.Decode(&m); err != nil {
			return 1
		}
		switch m.Name {
		case "stdout":
			os.Stdout.Write(m.Data)
		case "stderr":
			os.Stderr.Write(m.Data)
		case "error":
			fmt.Fprintln(os.Stderr, m.Error)
		case "exit":
			return m.Exit
		}
	}
}
