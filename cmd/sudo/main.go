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
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
	"unsafe"
)

const (
	SEE_MASK_NOCLOSEPROCESS = 0x00000040
	SW_HIDE                 = 0
	SW_SHOW                 = 5

	// How long to wait for the elevated process to connect back.
	// If UAC is denied or the user dismisses the prompt, we give up after this.
	acceptTimeout = 15 * time.Second
)

type SHELLEXECUTEINFO struct {
	cbSize         uint32
	fMask          uint32
	hwnd           uintptr
	lpVerb         uintptr
	lpFile         uintptr
	lpParameters   uintptr
	lpDirectory    uintptr
	nShow          int32
	hInstApp       uintptr
	lpIDList       uintptr
	lpClass        uintptr
	hkeyClass      uintptr
	dwHotKey       uint32
	hIconOrMonitor uintptr
	hProcess       uintptr
}

type msg struct {
	Name  string
	Data  []byte
	Error string
	Exit  int
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: sudo <command> [args...]")
		os.Exit(1)
	}

	// Internal client mode (runs elevated)
	if os.Args[1] == "--client" {
		os.Exit(runClient(os.Args[2:]))
	}

	args := os.Args[1:]
	os.Exit(runServer(args))
}

func runServer(args []string) int {
	// Create TCP listener on a random local port
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintf(os.Stderr, "sudo: cannot create listener: %v\n", err)
		return 1
	}
	defer lis.Close()

	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "sudo: cannot find executable: %v\n", err)
		return 1
	}

	// Generate a random auth token so only our elevated process can connect.
	tokenBytes := make([]byte, 16)
	if _, err := rand.Read(tokenBytes); err != nil {
		fmt.Fprintf(os.Stderr, "sudo: cannot generate token: %v\n", err)
		return 1
	}
	token := hex.EncodeToString(tokenBytes)

	// Pass token as first argument after address so the elevated process knows it
	cmdArgs := []string{"--client", lis.Addr().String(), token}
	cmdArgs = append(cmdArgs, args...)

	// Launch the elevated process in a goroutine; if it fails (UAC denied)
	// close the listener so Accept() unblocks.
	elevateErr := make(chan error, 1)
	go func() {
		err := shellExecuteAndWait(exe, cmdArgs)
		elevateErr <- err
		lis.Close()
	}()

	// Accept connection with timeout (guards against UAC denial / user dismissal)
	type acceptResult struct {
		conn net.Conn
		err  error
	}
	ch := make(chan acceptResult, 1)
	go func() {
		conn, err := lis.Accept()
		ch <- acceptResult{conn, err}
	}()

	var conn net.Conn
	select {
	case res := <-ch:
		if res.err != nil {
			// Could be UAC denial or genuine network error
			select {
			case err := <-elevateErr:
				if err != nil {
					fmt.Fprintf(os.Stderr, "sudo: failed to elevate: %v\n", err)
				} else {
					fmt.Fprintln(os.Stderr, "sudo: elevated process did not connect")
				}
			default:
				fmt.Fprintln(os.Stderr, "sudo: cannot accept connection (UAC denied?)")
			}
			return 1
		}
		conn = res.conn
	case <-time.After(acceptTimeout):
		fmt.Fprintln(os.Stderr, "sudo: timed out waiting for elevated process (UAC denied?)")
		lis.Close()
		return 1
	}
	defer conn.Close()

	enc := gob.NewEncoder(conn)
	dec := gob.NewDecoder(conn)

	// Step 1: Send token for client to verify (prevents rogue local connections)
	if err := enc.Encode(token); err != nil {
		fmt.Fprintf(os.Stderr, "sudo: cannot send token: %v\n", err)
		return 1
	}

	// Step 2: Wait for client authentication acknowledgement
	var ok bool
	if err := dec.Decode(&ok); err != nil || !ok {
		fmt.Fprintln(os.Stderr, "sudo: authentication failed — unexpected process connected")
		return 1
	}

	// Step 3: Send environment to the elevated process
	if err := enc.Encode(os.Environ()); err != nil {
		fmt.Fprintf(os.Stderr, "sudo: cannot send environment: %v\n", err)
		return 1
	}

	// Handle Ctrl+C: forward interrupt signal to elevated process
	sc := make(chan os.Signal, 1)
	signal.Notify(sc, os.Interrupt)
	go func() {
		for range sc {
			enc.Encode(&msg{Name: "ctrlc"})
		}
	}()

	// Forward stdin to elevated process
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				if encErr := enc.Encode(&msg{Name: "stdin", Data: buf[:n]}); encErr != nil {
					return
				}
			}
			if err != nil {
				if err == io.EOF {
					enc.Encode(&msg{Name: "close"})
				}
				return
			}
		}
	}()

	// Receive and forward output / exit code from elevated process
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

func runClient(args []string) int {
	// args: <addr> <token> <cmd> [cmd-args...]
	if len(args) < 3 {
		fmt.Fprintln(os.Stderr, "sudo: client mode requires address, token, and command")
		return 1
	}

	addr := args[0]
	token := args[1]
	cmdArgs := args[2:]

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sudo: cannot connect to server: %v\n", err)
		return 1
	}
	defer conn.Close()

	enc := gob.NewEncoder(conn)
	dec := gob.NewDecoder(conn)

	// Step 1: Receive and verify the auth token
	var receivedToken string
	if err := dec.Decode(&receivedToken); err != nil {
		return 1
	}
	if receivedToken != token {
		enc.Encode(false)
		fmt.Fprintln(os.Stderr, "sudo: auth token mismatch")
		return 1
	}
	if err := enc.Encode(true); err != nil {
		return 1
	}

	// Step 2: Receive environment from the (non-elevated) server
	var environ []string
	if err := dec.Decode(&environ); err != nil {
		return 1
	}

	// Enable all privileges in our elevated token so that Windows-level access
	// checks (raw sockets, SeDebugPrivilege, SeSecurityPrivilege, …) succeed
	// for any program — not just nmap.  UAC elevation gives us the full admin
	// privilege set but leaves most entries disabled; this enables them all.
	enableAllPrivileges()

	// Step 3: Run command with the received environment
	// Convert Cygwin/Git-Bash paths to Windows paths so Windows can find the executable.
	if len(cmdArgs) > 0 {
		cmdArgs[0] = convertCygwinPath(cmdArgs[0])
	}
	// Set NMAP_PRIVILEGED/NPING_PRIVILEGED so Cygwin-compiled nmap/nping treat
	// this elevated process as privileged (geteuid() doesn't return 0 on Windows
	// even when the process has admin rights).
	environ = append(environ, "NMAP_PRIVILEGED=1", "NPING_PRIVILEGED=1")
	cmd := exec.Command(cmdArgs[0], cmdArgs[1:]...)
	cmd.Env = environ

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		enc.Encode(&msg{Name: "error", Error: err.Error()})
		return 1
	}

	cp := getOEMCP()
	cmd.Stdout = &msgWriter{enc: enc, name: "stdout", fromCP: cp}
	cmd.Stderr = &msgWriter{enc: enc, name: "stderr", fromCP: cp}

	// Forward stdin / signals from server
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

	// Run command and report exit code
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
	if w.fromCP != 0 && w.fromCP != 65001 && (!utf8.Valid(p) || containsGBKExclusiveBytes(p) || containsGBKBlindZoneBytes(p)) {
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
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	r, _, _ := kernel32.NewProc("GetOEMCP").Call()
	return uint32(r)
}

// enableAllPrivileges enables every privilege that is present (but possibly
// disabled) in the current process token.  UAC elevation populates the token
// with the full administrator privilege set but starts most of them disabled.
// Enabling them all means any subsequent Windows-level privilege check
// (raw sockets, SeDebugPrivilege, SeSecurityPrivilege, SeBackupPrivilege, …)
// will succeed for whatever program sudo runs — no per-program workarounds needed.
func enableAllPrivileges() {
	advapi32 := syscall.NewLazyDLL("advapi32.dll")
	openProcessToken    := advapi32.NewProc("OpenProcessToken")
	getTokenInformation := advapi32.NewProc("GetTokenInformation")
	adjustTokenPrivs    := advapi32.NewProc("AdjustTokenPrivileges")

	proc, err := syscall.GetCurrentProcess()
	if err != nil {
		return
	}
	var tok syscall.Handle
	r, _, _ := openProcessToken.Call(
		uintptr(proc),
		uintptr(syscall.TOKEN_ADJUST_PRIVILEGES|syscall.TOKEN_QUERY),
		uintptr(unsafe.Pointer(&tok)),
	)
	if r == 0 {
		return
	}
	defer syscall.CloseHandle(tok)

	// First call: find out how large the TOKEN_PRIVILEGES buffer needs to be.
	const tokenPrivilegesClass = 3
	var needed uint32
	getTokenInformation.Call(uintptr(tok), tokenPrivilegesClass, 0, 0, uintptr(unsafe.Pointer(&needed)))
	if needed == 0 {
		return
	}

	buf := make([]byte, needed)
	r, _, _ = getTokenInformation.Call(
		uintptr(tok), tokenPrivilegesClass,
		uintptr(unsafe.Pointer(&buf[0])), uintptr(needed),
		uintptr(unsafe.Pointer(&needed)),
	)
	if r == 0 {
		return
	}

	// TOKEN_PRIVILEGES layout:
	//   uint32 PrivilegeCount                     — offset 0, 4 bytes
	//   LUID_AND_ATTRIBUTES Privileges[count]     — offset 4, each 12 bytes
	//     LUID (uint32 LowPart + int32 HighPart)  — 8 bytes
	//     uint32 Attributes                       — 4 bytes  ← set SE_PRIVILEGE_ENABLED here
	const (
		sePrivilegeEnabled  = 0x00000002
		luidAndAttrsSize    = 12 // 8-byte LUID + 4-byte Attributes
	)
	count := *(*uint32)(unsafe.Pointer(&buf[0]))
	for i := uint32(0); i < count; i++ {
		attrsOff := 4 + uintptr(i)*luidAndAttrsSize + 8
		*(*uint32)(unsafe.Pointer(&buf[attrsOff])) |= sePrivilegeEnabled
	}

	// Passing the modified buffer re-enables everything in one call.
	// Errors are intentionally ignored: partial success is still an improvement.
	adjustTokenPrivs.Call(uintptr(tok), 0, uintptr(unsafe.Pointer(&buf[0])), 0, 0, 0)
}

// convertCygwinPath converts Cygwin-style paths to Windows paths.
// /cygdrive/c/path -> C:\path
// /c/path -> C:\path (Git Bash style)
// Relative paths and already-Windows paths are returned unchanged.
func convertCygwinPath(p string) string {
	// /cygdrive/X/... format
	if strings.HasPrefix(p, "/cygdrive/") && len(p) >= 11 {
		drive := p[10]
		if (drive >= 'a' && drive <= 'z') || (drive >= 'A' && drive <= 'Z') {
			rest := p[11:]
			return string(drive) + ":" + strings.ReplaceAll(rest, "/", `\`)
		}
	}
	// /X/... format (Git Bash / MSYS2 short mount, e.g. /c/Users/...)
	if len(p) >= 3 && p[0] == '/' && p[2] == '/' {
		drive := p[1]
		if (drive >= 'a' && drive <= 'z') || (drive >= 'A' && drive <= 'Z') {
			rest := p[2:]
			return string(drive) + ":" + strings.ReplaceAll(rest, "/", `\`)
		}
	}
	return p
}

// containsGBKExclusiveBytes reports whether p contains any byte pair whose
// lead byte is in the GBK-exclusive range 0x81–0x9F (definitively invalid as
// a UTF-8 lead byte) followed by a valid GBK trail byte (0x40–0x7E or 0x80–0xFE).
//
// Valid UTF-8 multi-byte sequences are skipped so that 0x81–0x9F bytes
// appearing as UTF-8 continuation bytes do not cause false positives.
func containsGBKExclusiveBytes(p []byte) bool {
	i := 0
	for i < len(p) {
		b := p[i]
		// Skip over valid UTF-8 multi-byte sequences so their continuation
		// bytes (which may fall in 0x81–0x9F) are not mistaken for GBK lead bytes.
		if b >= 0xC2 && b <= 0xF4 && i+1 < len(p) && p[i+1]&0xC0 == 0x80 {
			switch {
			case b < 0xE0:
				i += 2
			case b < 0xF0:
				i += 3
			default:
				i += 4
			}
			continue
		}
		// A byte in 0x81–0x9F that is not inside a valid UTF-8 sequence is a
		// GBK-exclusive lead byte. Confirm with a valid GBK trail byte.
		if b >= 0x81 && b <= 0x9F && i+1 < len(p) {
			t := p[i+1]
			if (t >= 0x40 && t <= 0x7E) || (t >= 0x80 && t <= 0xFE) {
				return true
			}
		}
		i++
	}
	return false
}

// containsGBKBlindZoneBytes reports whether p looks like GBK bytes that
// accidentally pass utf8.Valid. GBK pairs whose lead byte is in 0xC2–0xDF
// and trail byte is in 0xA1–0xBF are valid 2-byte UTF-8 sequences, forming
// a blind zone that containsGBKExclusiveBytes cannot reach.
//
// The detection uses ASCII bytes (spaces, newlines, colons, …) as phase-anchor
// points: after each ASCII anchor we know we are at a character boundary, so
// the length of the next multibyte sequence is meaningful. Real CJK Unicode
// characters (U+4E00–U+9FFF) encode to 3-byte UTF-8 sequences (lead 0xE4–0xEF),
// while GBK blind-zone pairs are always 2-byte. A buffer with suspicious
// 2-byte sequences after anchors and no confirming 3-byte CJK sequences is
// treated as GBK.
func containsGBKBlindZoneBytes(p []byte) bool {
	suspicious2 := 0 // 2-byte pairs in blind zone (0xC2–0xDF lead, 0xA1–0xBF trail)
	confirmed3 := 0  // 3-byte CJK sequences (0xE4–0xEF lead) — proof of real UTF-8
	afterAnchor := true

	for i := 0; i < len(p); {
		b := p[i]
		if b < 0x80 {
			afterAnchor = true
			i++
			continue
		}
		if afterAnchor {
			afterAnchor = false
			if b >= 0xE4 && b <= 0xEF && i+2 < len(p) &&
				p[i+1]&0xC0 == 0x80 && p[i+2]&0xC0 == 0x80 {
				confirmed3++
				i += 3
				continue
			}
			if b >= 0xC2 && b <= 0xDF && i+1 < len(p) &&
				p[i+1] >= 0xA1 && p[i+1] <= 0xBF {
				suspicious2++
				i += 2
				continue
			}
		}
		i++
	}
	return suspicious2 > 0 && confirmed3 == 0
}

// oemToUTF8 converts bytes from the given Windows OEM code page to UTF-8.
// On failure it returns the original slice unchanged.
func oemToUTF8(data []byte, fromCP uint32) []byte {
	if len(data) == 0 {
		return data
	}
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
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

func shellExecuteAndWait(exe string, args []string) error {
	cmdLine := makeCmdLine(args)

	shell32 := syscall.NewLazyDLL("shell32.dll")
	proc := shell32.NewProc("ShellExecuteExW")

	verbPtr, _ := syscall.UTF16PtrFromString("runas")
	filePtr, _ := syscall.UTF16PtrFromString(exe)
	paramsPtr, _ := syscall.UTF16PtrFromString(cmdLine)

	sei := SHELLEXECUTEINFO{
		cbSize:       uint32(unsafe.Sizeof(SHELLEXECUTEINFO{})),
		fMask:        SEE_MASK_NOCLOSEPROCESS,
		lpVerb:       uintptr(unsafe.Pointer(verbPtr)),
		lpFile:       uintptr(unsafe.Pointer(filePtr)),
		lpParameters: uintptr(unsafe.Pointer(paramsPtr)),
		nShow:        SW_HIDE,
	}

	ret, _, _ := proc.Call(uintptr(unsafe.Pointer(&sei)))
	if ret == 0 {
		return fmt.Errorf("ShellExecuteExW failed (UAC denied or error)")
	}

	if sei.hProcess != 0 {
		syscall.WaitForSingleObject(syscall.Handle(sei.hProcess), syscall.INFINITE)
		syscall.CloseHandle(syscall.Handle(sei.hProcess))
	}

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
