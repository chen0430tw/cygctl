//go:build windows

package main

import (
	"bufio"
	"crypto/rand"
	"encoding/gob"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	acceptTimeout    = 15 * time.Second
	logonWithProfile = 0x00000001 // CreateProcessWithLogonW: load target user's profile
	createNoWindow   = 0x08000000 // CreateProcessWithLogonW: do not create a console window
)

type msg struct {
	Name  string
	Data  []byte
	Error string
	Exit  int
}

type msgWriter struct {
	enc    *gob.Encoder
	name   string
	fromCP uint32 // OEM codepage for GBK→UTF-8 transcoding; 0 or 65001 = no-op
}

func (w *msgWriter) Write(p []byte) (n int, err error) {
	data := p
	// Transcode only when the system uses a non-UTF-8 OEM codepage (e.g. 936 for GBK)
	// and the bytes are not already valid UTF-8, or contain GBK-exclusive byte pairs.
	// This handles Windows native commands (icacls, net, dir, ...) that write in the
	// OEM codepage when piped, while leaving genuine UTF-8 output untouched.
	//
	// GBK lead bytes 0x81–0x9F are definitively invalid as UTF-8 lead bytes. Their
	// presence (∩ ≠ ∅ with the GBK-exclusive range) is a hard signal for GBK even
	// when the surrounding bytes might otherwise pass utf8.Valid.
	if w.fromCP != 0 && w.fromCP != 65001 && (!utf8.Valid(p) || containsGBKExclusiveBytes(p)) {
		data = oemToUTF8(p, w.fromCP)
	}
	if err := w.enc.Encode(&msg{Name: w.name, Data: data}); err != nil {
		return 0, err
	}
	return len(p), nil
}

// getOEMCP returns the system OEM code page (e.g. 936 for Simplified Chinese).
// This does not require a console to be attached.
func getOEMCP() uint32 {
	kernel32 := windows.NewLazySystemDLL("kernel32.dll")
	r, _, _ := kernel32.NewProc("GetOEMCP").Call()
	return uint32(r)
}

// containsGBKExclusiveBytes reports whether p contains any byte pair whose
// lead byte is in the GBK-exclusive range 0x81–0x9F (definitively invalid as
// a UTF-8 lead byte) followed by a valid GBK trail byte (0x40–0x7E or 0x80–0xFE).
func containsGBKExclusiveBytes(p []byte) bool {
	for i := 0; i < len(p)-1; i++ {
		if p[i] >= 0x81 && p[i] <= 0x9F {
			t := p[i+1]
			if (t >= 0x40 && t <= 0x7E) || (t >= 0x80 && t <= 0xFE) {
				return true
			}
		}
	}
	return false
}

// oemToUTF8 converts bytes from the given Windows OEM code page to UTF-8.
// On failure it returns the original slice unchanged.
func oemToUTF8(data []byte, fromCP uint32) []byte {
	if len(data) == 0 {
		return data
	}
	kernel32 := windows.NewLazySystemDLL("kernel32.dll")
	mbtwc := kernel32.NewProc("MultiByteToWideChar")
	wctmb := kernel32.NewProc("WideCharToMultiByte")

	// Pass 1: how many UTF-16 code units do we need?
	nWide, _, _ := mbtwc.Call(
		uintptr(fromCP), 0,
		uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)),
		0, 0,
	)
	if nWide == 0 {
		return data
	}
	wbuf := make([]uint16, nWide)
	mbtwc.Call(
		uintptr(fromCP), 0,
		uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)),
		uintptr(unsafe.Pointer(&wbuf[0])), nWide,
	)

	// Pass 2: how many UTF-8 bytes do we need?
	nBytes, _, _ := wctmb.Call(
		65001, 0, // CP_UTF8
		uintptr(unsafe.Pointer(&wbuf[0])), nWide,
		0, 0, 0, 0,
	)
	if nBytes == 0 {
		return data
	}
	result := make([]byte, nBytes)
	wctmb.Call(
		65001, 0,
		uintptr(unsafe.Pointer(&wbuf[0])), nWide,
		uintptr(unsafe.Pointer(&result[0])), nBytes,
		0, 0,
	)
	return result
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: su <username> [command...]")
		os.Exit(1)
	}

	if os.Args[1] == "--client" {
		os.Exit(runClient(os.Args[2:]))
	}

	username := os.Args[1]
	cmdArgs := os.Args[2:]
	os.Exit(runServer(username, cmdArgs))
}

// checkSecondaryLogon verifies that the Secondary Logon service (seclogon) is
// running.  CreateProcessWithLogonW depends on this service; if it is stopped
// or disabled the call will fail with a confusing "access denied" error.
func checkSecondaryLogon() error {
	scm, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		// Cannot open SCM — proceed anyway; the API call will surface any real error.
		return nil
	}
	defer windows.CloseServiceHandle(scm)

	svcName, _ := windows.UTF16PtrFromString("seclogon")
	svc, err := windows.OpenService(scm, svcName, windows.SERVICE_QUERY_STATUS)
	if err != nil {
		return nil // service not found on this edition — let the API decide
	}
	defer windows.CloseServiceHandle(svc)

	var status windows.SERVICE_STATUS
	if err := windows.QueryServiceStatus(svc, &status); err != nil {
		return nil
	}
	if status.CurrentState != windows.SERVICE_RUNNING {
		return fmt.Errorf(`the Secondary Logon service (seclogon) is not running.
su.exe requires this service to switch Windows users without elevation.

To start it (run as Administrator in cmd.exe or PowerShell):
    sc start seclogon
    sc config seclogon start= auto   (optional: start automatically at boot)

Or via PowerShell:
    Start-Service SecondaryLogon
    Set-Service  SecondaryLogon -StartupType Automatic`)
	}
	return nil
}

// runServer runs in the caller's Windows user context.
//
// Design (mirroring cmd/sudo but using CreateProcessWithLogonW instead of UAC):
//
//  1. Check the Secondary Logon service (seclogon) is running.
//  2. Validate the target user exists via LookupAccountNameW → get its domain.
//  3. Prompt for the target user's password (echo-off via SetConsoleMode).
//  4. Listen on a random loopback port; generate a one-time auth token.
//  5. Spawn "su.exe --client <addr> <token> [cmd...]" as the target user
//     using CreateProcessWithLogonW (Secondary Logon service, no elevation needed).
//  6. Accept the reverse connection, verify the token, then proxy stdin/stdout/stderr
//     between the caller's terminal and the target user's bash session.
func runServer(username string, cmdArgs []string) int {
	// 1. Ensure the Secondary Logon service is available before doing anything else.
	if err := checkSecondaryLogon(); err != nil {
		fmt.Fprintln(os.Stderr, "su:", err)
		return 1
	}

	// 2. Resolve username → Windows SID → domain name (also validates the account).
	domain, err := lookupUserDomain(username)
	if err != nil {
		fmt.Fprintf(os.Stderr, "su: unknown user '%s': %v\n", username, err)
		return 1
	}

	// 3. Prompt for password with echo disabled.
	password, err := readPassword(fmt.Sprintf("[su] Password for %s\\%s: ", domain, username))
	if err != nil {
		fmt.Fprintf(os.Stderr, "su: cannot read password: %v\n", err)
		return 1
	}

	// 4. Listen on a random loopback port.
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

	// 5. Generate a one-time auth token to prevent rogue local connections.
	tokenBytes := make([]byte, 16)
	rand.Read(tokenBytes)
	token := hex.EncodeToString(tokenBytes)

	clientArgs := append([]string{"--client", lis.Addr().String(), token}, cmdArgs...)

	// Spawn subprocess as target user; close listener when it exits so Accept unblocks.
	spawnErr := make(chan error, 1)
	go func() {
		err := spawnAsUser(username, domain, password, exe, clientArgs)
		spawnErr <- err
		lis.Close()
	}()

	// 6. Accept the callback with a timeout.
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
		fmt.Fprintln(os.Stderr, "su: timed out waiting for user session")
		lis.Close()
		return 1
	}
	defer conn.Close()

	enc := gob.NewEncoder(conn)
	dec := gob.NewDecoder(conn)

	// Token handshake — reject rogue connections.
	if err := enc.Encode(token); err != nil {
		return 1
	}
	var ok bool
	if err := dec.Decode(&ok); err != nil || !ok {
		fmt.Fprintln(os.Stderr, "su: authentication failed — unexpected process connected")
		return 1
	}

	// Forward Ctrl+C as a kill signal to the client process.
	sc := make(chan os.Signal, 1)
	signal.Notify(sc, os.Interrupt)
	go func() {
		for range sc {
			enc.Encode(&msg{Name: "ctrlc"})
		}
	}()

	// Stream stdin → client.
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

	// Receive output and exit code from client.
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

// runClient runs inside the target user's Windows process (spawned by runServer
// via CreateProcessWithLogonW).  It connects back to the server, verifies the
// auth token, then launches bash as a login shell and proxies I/O.
func runClient(args []string) int {
	if len(args) < 2 {
		return 1
	}
	addr, token := args[0], args[1]
	cmdArgs := args[2:]

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		return 1
	}
	defer conn.Close()

	enc := gob.NewEncoder(conn)
	dec := gob.NewDecoder(conn)

	// Verify auth token.
	var receivedToken string
	if err := dec.Decode(&receivedToken); err != nil || receivedToken != token {
		enc.Encode(false)
		return 1
	}
	enc.Encode(true)

	// Locate bash.exe next to su.exe (both live in Cygwin\bin).
	exe, _ := os.Executable()
	bashExe := filepath.Join(filepath.Dir(exe), "bash.exe")

	var cmd *exec.Cmd
	if len(cmdArgs) > 0 {
		// Run the command directly (like sudo does) to avoid MSYS2 path
		// conversion mangling Windows-style paths (e.g. C:\cygwin64 → /cygwin64).
		// Routing through bash --login -c would subject arguments to Git Bash /
		// MSYS2 path rewriting unless MSYS_NO_PATHCONV=1 is set.
		//
		// When the caller passes a single quoted shell string
		// (e.g. su user "icacls C:\cygwin64 /grant ..."), cmdArgs has one
		// element containing the whole command.  Split it with Windows
		// command-line parsing rules so we can still exec directly.
		if len(cmdArgs) == 1 {
			if parts, err := windows.DecomposeCommandLine(cmdArgs[0]); err == nil && len(parts) > 1 {
				cmdArgs = parts
			}
		}
		cmd = exec.Command(cmdArgs[0], cmdArgs[1:]...)
	} else {
		cmd = exec.Command(bashExe, "--login", "-i")
	}
	// Inherit the target user's own environment (set by CreateProcessWithLogonW
	// + LOGON_WITH_PROFILE).  Do NOT forward the caller's environment, as that
	// would leak the original user's paths / HOME / USER into the new session.
	cmd.Env = os.Environ()

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		enc.Encode(&msg{Name: "error", Error: err.Error()})
		return 1
	}
	// For interactive bash sessions (no explicit command) bash always outputs
	// UTF-8, so transcoding is neither necessary nor safe. Only enable it when
	// running an explicit Windows command that may write in the OEM code page.
	var cp uint32
	if len(cmdArgs) > 0 {
		cp = getOEMCP()
	}
	cmd.Stdout = &msgWriter{enc: enc, name: "stdout", fromCP: cp}
	cmd.Stderr = &msgWriter{enc: enc, name: "stderr", fromCP: cp}

	go func() {
		defer stdinPipe.Close()
		for {
			var m msg
			if err := dec.Decode(&m); err != nil {
				return
			}
			switch m.Name {
			case "stdin":
				stdinPipe.Write(m.Data)
			case "close":
				return
			case "ctrlc":
				if cmd.Process != nil {
					cmd.Process.Kill()
				}
				return
			}
		}
	}()

	code := 0
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			code = exitErr.ExitCode()
		} else {
			enc.Encode(&msg{Name: "error", Error: err.Error()})
			code = 1
		}
	}
	enc.Encode(&msg{Name: "exit", Exit: code})
	return 0
}

// lookupUserDomain resolves a username to its Windows domain/computer name
// using LookupAccountNameW.  This validates that the account exists on the
// local machine (or domain) and returns the authoritative domain string needed
// by CreateProcessWithLogonW.
func lookupUserDomain(username string) (string, error) {
	var sidBuf [512]byte
	sidSize := uint32(len(sidBuf))
	var domainBuf [256]uint16
	domainSize := uint32(len(domainBuf))
	var use uint32

	userPtr, err := windows.UTF16PtrFromString(username)
	if err != nil {
		return "", err
	}

	advapi32 := windows.NewLazySystemDLL("advapi32.dll")
	lookupAccountName := advapi32.NewProc("LookupAccountNameW")

	ret, _, lerr := lookupAccountName.Call(
		0, // lpSystemName = NULL → local machine / default domain
		uintptr(unsafe.Pointer(userPtr)),
		uintptr(unsafe.Pointer(&sidBuf[0])),
		uintptr(unsafe.Pointer(&sidSize)),
		uintptr(unsafe.Pointer(&domainBuf[0])),
		uintptr(unsafe.Pointer(&domainSize)),
		uintptr(unsafe.Pointer(&use)),
	)
	if ret == 0 {
		if lerr == windows.ERROR_NONE_MAPPED {
			return "", fmt.Errorf("no such user")
		}
		return "", fmt.Errorf("lookup failed: %v", lerr)
	}
	return windows.UTF16ToString(domainBuf[:domainSize]), nil
}

// readPassword prompts on stderr and reads a line from stdin without echoing
// characters, using SetConsoleMode on the Windows console handle.
func readPassword(prompt string) (string, error) {
	stdinHandle, err := windows.GetStdHandle(windows.STD_INPUT_HANDLE)
	if err != nil {
		return "", fmt.Errorf("cannot get stdin handle: %v", err)
	}

	var mode uint32
	if err := windows.GetConsoleMode(stdinHandle, &mode); err != nil {
		// Piped / non-interactive input — read as-is (no echo to suppress).
		fmt.Fprint(os.Stderr, prompt)
		reader := bufio.NewReader(os.Stdin)
		line, err := reader.ReadString('\n')
		return strings.TrimRight(line, "\r\n"), err
	}

	fmt.Fprint(os.Stderr, prompt)
	// Suppress echo for password entry.
	windows.SetConsoleMode(stdinHandle, mode&^windows.ENABLE_ECHO_INPUT)
	defer func() {
		windows.SetConsoleMode(stdinHandle, mode)
		fmt.Fprintln(os.Stderr) // newline after the hidden input
	}()

	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	return strings.TrimRight(line, "\r\n"), err
}

// spawnAsUser launches exe with args as the target Windows user via
// CreateProcessWithLogonW.  This API:
//   - Does NOT require administrator elevation.
//   - Requires the Secondary Logon service (seclogon) to be running
//     (enabled by default on all modern Windows versions).
//   - Loads the target user's profile (LOGON_WITH_PROFILE) so that HOME,
//     APPDATA, and other user-specific paths are set correctly inside bash.
func spawnAsUser(username, domain, password, exe string, args []string) error {
	advapi32 := windows.NewLazySystemDLL("advapi32.dll")
	createProcessWithLogonW := advapi32.NewProc("CreateProcessWithLogonW")

	userPtr, _ := windows.UTF16PtrFromString(username)
	domainPtr, _ := windows.UTF16PtrFromString(domain)
	passPtr, _ := windows.UTF16PtrFromString(password)
	exePtr, _ := windows.UTF16PtrFromString(exe)
	// lpCommandLine must include argv[0] (the exe path) as its first token.
	// CreateProcessWithLogonW parses the entire command line to build argv[],
	// so omitting argv[0] would shift all arguments left by one, causing the
	// child to read argv[1] ("127.0.0.1:PORT") as argv[1] instead of
	// "--client", and the --client mode check in main() would silently fail.
	cmdLine := makeCmdLine(append([]string{exe}, args...))
	cmdPtr, _ := windows.UTF16PtrFromString(cmdLine)

	var si windows.StartupInfo
	si.Cb = uint32(unsafe.Sizeof(si))
	si.Flags = 0x00000001      // STARTF_USESHOWWINDOW
	si.ShowWindow = 0          // SW_HIDE
	var pi windows.ProcessInformation

	ret, _, lerr := createProcessWithLogonW.Call(
		uintptr(unsafe.Pointer(userPtr)),
		uintptr(unsafe.Pointer(domainPtr)),
		uintptr(unsafe.Pointer(passPtr)),
		logonWithProfile,
		uintptr(unsafe.Pointer(exePtr)),
		uintptr(unsafe.Pointer(cmdPtr)),
		createNoWindow, // dwCreationFlags — prevent a new console window
		0,              // lpEnvironment — NULL: inherit target user's default env
		0,              // lpCurrentDirectory — NULL: inherit caller's cwd
		uintptr(unsafe.Pointer(&si)),
		uintptr(unsafe.Pointer(&pi)),
	)
	if ret == 0 {
		// Common errors: wrong password (ERROR_LOGON_FAILURE = 1326),
		// account locked (ERROR_ACCOUNT_LOCKED_OUT = 1909), etc.
		return fmt.Errorf("authentication failed or access denied (Windows error %v)", lerr)
	}

	windows.WaitForSingleObject(pi.Process, windows.INFINITE)
	windows.CloseHandle(pi.Process)
	windows.CloseHandle(pi.Thread)
	return nil
}

func makeCmdLine(args []string) string {
	var parts []string
	for _, arg := range args {
		if needsQuoting(arg) {
			parts = append(parts, `"`+strings.ReplaceAll(arg, `"`, `\"`)+`"`)
		} else {
			parts = append(parts, arg)
		}
	}
	return strings.Join(parts, " ")
}

func needsQuoting(s string) bool {
	for _, c := range s {
		if c == ' ' || c == '\t' || c == '"' || c == '\\' {
			return true
		}
	}
	return false
}
