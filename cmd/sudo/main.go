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
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf8"
	"unsafe"
)

const (
	SEE_MASK_NOCLOSEPROCESS = 0x00000040
	SW_HIDE                 = 0
	SW_SHOW                 = 5

	// How long to wait for the UAC-elevated daemon to come up.
	daemonStartTimeout = 15 * time.Second
	// Daemon exits after this long with no active connections.
	daemonIdleTimeout = 5 * time.Minute
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

// daemonRequest is sent by the non-elevated server to the elevated daemon
// for each command invocation.
type daemonRequest struct {
	Environ []string
	Args    []string
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: sudo <command> [args...]")
		os.Exit(1)
	}
	switch os.Args[1] {
	case "--client":
		// Legacy one-shot elevated mode (kept for backward compatibility).
		os.Exit(runClient(os.Args[2:]))
	case "--daemon":
		// Long-lived elevated daemon mode.
		os.Exit(runDaemon(os.Args[2:]))
	default:
		os.Exit(runServer(os.Args[1:]))
	}
}

// ── Non-elevated server ────────────────────────────────────────────────────

func runServer(args []string) int {
	lockPath := daemonLockPath()

	// Try to connect to an already-running elevated daemon.
	conn, err := tryConnectDaemon(lockPath)
	if err != nil {
		// No daemon running — spawn one via UAC (one-time popup).
		token, spawnErr := spawnDaemon()
		if spawnErr != nil {
			fmt.Fprintf(os.Stderr, "sudo: %v\n", spawnErr)
			return 1
		}
		conn, err = waitAndConnectDaemon(lockPath, token)
		if err != nil {
			fmt.Fprintf(os.Stderr, "sudo: daemon did not start: %v\n", err)
			return 1
		}
	}
	defer conn.Close()

	enc := gob.NewEncoder(conn)
	dec := gob.NewDecoder(conn)

	// Send the command request to the daemon.
	if err := enc.Encode(daemonRequest{Environ: os.Environ(), Args: args}); err != nil {
		fmt.Fprintf(os.Stderr, "sudo: cannot send request: %v\n", err)
		return 1
	}

	// Forward Ctrl+C to the daemon.
	sc := make(chan os.Signal, 1)
	signal.Notify(sc, os.Interrupt)
	go func() {
		for range sc {
			enc.Encode(&msg{Name: "ctrlc"})
		}
	}()

	// Forward stdin to the daemon.
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

	// Receive and forward output / exit code from the daemon.
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

// ── Daemon helpers ─────────────────────────────────────────────────────────

func daemonLockPath() string {
	user := os.Getenv("USERNAME")
	if user == "" {
		user = "default"
	}
	// Use LOCALAPPDATA rather than os.TempDir()/TEMP because Cygwin overrides
	// TEMP to C:\cygwin64\tmp, while the elevated daemon (a pure Windows
	// process) uses the real Windows temp dir. LOCALAPPDATA is not overridden
	// by Cygwin, so both sides resolve to the same path.
	dir := os.Getenv("LOCALAPPDATA")
	if dir == "" {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "cygctl-sudo-"+user+".lock")
}

// readDaemonLock parses the lock file and returns the TCP address and token.
func readDaemonLock(lockPath string) (addr, token string, err error) {
	data, err := os.ReadFile(lockPath)
	if err != nil {
		return "", "", err
	}
	parts := strings.SplitN(strings.TrimSpace(string(data)), ":", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("malformed lock file")
	}
	return "127.0.0.1:" + parts[0], parts[1], nil
}

// tryConnectDaemon attempts to connect to and authenticate with an existing
// elevated daemon. Returns a ready-to-use, authenticated connection or an error.
func tryConnectDaemon(lockPath string) (net.Conn, error) {
	addr, token, err := readDaemonLock(lockPath)
	if err != nil {
		return nil, err
	}
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		os.Remove(lockPath) // stale lock file
		return nil, err
	}
	enc := gob.NewEncoder(conn)
	dec := gob.NewDecoder(conn)
	if err := enc.Encode(token); err != nil {
		conn.Close()
		return nil, err
	}
	var ok bool
	if err := dec.Decode(&ok); err != nil || !ok {
		conn.Close()
		return nil, fmt.Errorf("daemon auth failed")
	}
	return conn, nil
}

// spawnDaemon launches an elevated daemon via UAC (ShellExecuteEx "runas").
// The process runs as a background daemon; we do NOT wait for it to exit.
// Returns the random token that the daemon will use for authentication.
func spawnDaemon() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	tokenBytes := make([]byte, 16)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", err
	}
	token := hex.EncodeToString(tokenBytes)
	if err := shellExecuteAsync(exe, []string{"--daemon", token}); err != nil {
		return "", fmt.Errorf("failed to elevate: %v", err)
	}
	return token, nil
}

// waitAndConnectDaemon polls the lock file until the daemon writes its port,
// then connects and authenticates.
func waitAndConnectDaemon(lockPath, token string) (net.Conn, error) {
	deadline := time.Now().Add(daemonStartTimeout)
	for time.Now().Before(deadline) {
		addr, fileToken, err := readDaemonLock(lockPath)
		if err == nil && fileToken == token {
			conn, dialErr := net.DialTimeout("tcp", addr, 2*time.Second)
			if dialErr == nil {
				enc := gob.NewEncoder(conn)
				dec := gob.NewDecoder(conn)
				if encErr := enc.Encode(token); encErr == nil {
					var ok bool
					if decErr := dec.Decode(&ok); decErr == nil && ok {
						return conn, nil
					}
				}
				conn.Close()
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return nil, fmt.Errorf("timed out after %v", daemonStartTimeout)
}

// ── Elevated daemon ────────────────────────────────────────────────────────

// runDaemon is the long-lived elevated process.
// args: [token]
func runDaemon(args []string) int {
	if len(args) < 1 {
		return 1
	}
	token := args[0]

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 1
	}
	defer lis.Close()

	// Write the lock file so non-elevated sudo processes can find us.
	lockPath := daemonLockPath()
	port := lis.Addr().(*net.TCPAddr).Port
	lockData := fmt.Sprintf("%d:%s", port, token)
	if err := os.WriteFile(lockPath, []byte(lockData), 0600); err != nil {
		return 1
	}
	defer os.Remove(lockPath)

	// Enable all Windows admin privileges in our elevated token.
	enableAllPrivileges()

	// Idle timeout: exit after daemonIdleTimeout with no active connections.
	var activeConns int32
	var lastDoneNs int64
	atomic.StoreInt64(&lastDoneNs, time.Now().UnixNano())

	go func() {
		for {
			time.Sleep(30 * time.Second)
			if atomic.LoadInt32(&activeConns) == 0 {
				idle := time.Since(time.Unix(0, atomic.LoadInt64(&lastDoneNs)))
				if idle >= daemonIdleTimeout {
					lis.Close()
					return
				}
			}
		}
	}()

	for {
		conn, err := lis.Accept()
		if err != nil {
			break
		}
		atomic.AddInt32(&activeConns, 1)
		go func() {
			defer func() {
				atomic.AddInt32(&activeConns, -1)
				atomic.StoreInt64(&lastDoneNs, time.Now().UnixNano())
			}()
			handleDaemonConn(conn, token)
		}()
	}
	return 0
}

// handleDaemonConn handles one command invocation inside the elevated daemon.
func handleDaemonConn(conn net.Conn, token string) {
	defer conn.Close()

	enc := gob.NewEncoder(conn)
	dec := gob.NewDecoder(conn)

	// Authenticate the connecting client.
	var clientToken string
	if err := dec.Decode(&clientToken); err != nil {
		return
	}
	if clientToken != token {
		enc.Encode(false)
		return
	}
	if err := enc.Encode(true); err != nil {
		return
	}

	// Receive the command request.
	var req daemonRequest
	if err := dec.Decode(&req); err != nil {
		return
	}

	// Native 'id' implementation (avoids Cygwin FD-inheritance issues).
	if len(req.Args) > 0 {
		if base := strings.ToLower(filepath.Base(req.Args[0])); base == "id" || base == "id.exe" {
			out := buildIDOutput(req.Args, req.Environ)
			if out != "" {
				enc.Encode(&msg{Name: "stdout", Data: []byte(out + "\n")})
				enc.Encode(&msg{Name: "exit", Exit: 0})
				return // defer conn.Close() handles clean shutdown
			}
		}
	}

	// Convert Cygwin path to Windows path for exec.
	if len(req.Args) > 0 {
		req.Args[0] = convertCygwinPath(req.Args[0])
	}

	req.Environ = append(req.Environ, "NMAP_PRIVILEGED=1", "NPING_PRIVILEGED=1")
	cmd := exec.Command(req.Args[0], req.Args[1:]...)
	cmd.Env = req.Environ

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		enc.Encode(&msg{Name: "error", Error: err.Error()})
		return
	}

	cp := getOEMCP()
	cmd.Stdout = &msgWriter{enc: enc, name: "stdout", fromCP: cp}
	cmd.Stderr = &msgWriter{enc: enc, name: "stderr", fromCP: cp}

	// Forward stdin / signals from the non-elevated server.
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
}

// ── Legacy one-shot elevated client (--client mode) ────────────────────────

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

	var environ []string
	if err := dec.Decode(&environ); err != nil {
		return 1
	}

	enableAllPrivileges()

	if len(cmdArgs) > 0 {
		cmdArgs[0] = convertCygwinPath(cmdArgs[0])
	}

	if len(cmdArgs) > 0 {
		if base := strings.ToLower(filepath.Base(cmdArgs[0])); base == "id" || base == "id.exe" {
			out := buildIDOutput(cmdArgs, environ)
			if out != "" {
				enc.Encode(&msg{Name: "stdout", Data: []byte(out + "\n")})
				enc.Encode(&msg{Name: "exit", Exit: 0})
				if tc, ok := conn.(*net.TCPConn); ok {
					tc.CloseWrite()
				}
				io.Copy(io.Discard, conn)
				return 0
			}
		}
	}

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

// ── msgWriter ──────────────────────────────────────────────────────────────

type msgWriter struct {
	enc    *gob.Encoder
	name   string
	fromCP uint32
}

func (w *msgWriter) Write(p []byte) (n int, err error) {
	data := p
	if w.fromCP != 0 && w.fromCP != 65001 && (!utf8.Valid(p) || containsGBKExclusiveBytes(p) || containsGBKBlindZoneBytes(p)) {
		data = oemToUTF8(p, w.fromCP)
	}
	if err := w.enc.Encode(&msg{Name: w.name, Data: data}); err != nil {
		return 0, err
	}
	return len(p), nil
}

// ── Windows API helpers ────────────────────────────────────────────────────

func getOEMCP() uint32 {
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	r, _, _ := kernel32.NewProc("GetOEMCP").Call()
	return uint32(r)
}

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

	const (
		sePrivilegeEnabled = 0x00000002
		luidAndAttrsSize   = 12
	)
	count := *(*uint32)(unsafe.Pointer(&buf[0]))
	for i := uint32(0); i < count; i++ {
		attrsOff := 4 + uintptr(i)*luidAndAttrsSize + 8
		*(*uint32)(unsafe.Pointer(&buf[attrsOff])) |= sePrivilegeEnabled
	}
	adjustTokenPrivs.Call(uintptr(tok), 0, uintptr(unsafe.Pointer(&buf[0])), 0, 0, 0)
}

func convertCygwinPath(p string) string {
	if strings.HasPrefix(p, "/cygdrive/") && len(p) >= 11 {
		drive := p[10]
		if (drive >= 'a' && drive <= 'z') || (drive >= 'A' && drive <= 'Z') {
			rest := p[11:]
			return string(drive) + ":" + strings.ReplaceAll(rest, "/", `\`)
		}
	}
	if len(p) >= 3 && p[0] == '/' && p[2] == '/' {
		drive := p[1]
		if (drive >= 'a' && drive <= 'z') || (drive >= 'A' && drive <= 'Z') {
			rest := p[2:]
			return string(drive) + ":" + strings.ReplaceAll(rest, "/", `\`)
		}
	}
	return p
}

// shellExecuteAsync spawns an elevated process via ShellExecuteExW "runas"
// and returns immediately without waiting for the process to exit.
// Used to launch the background elevated daemon.
func shellExecuteAsync(exe string, args []string) error {
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
	// Close the handle immediately — daemon runs independently.
	if sei.hProcess != 0 {
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

// ── GBK / OEM codepage helpers ─────────────────────────────────────────────

func containsGBKExclusiveBytes(p []byte) bool {
	i := 0
	for i < len(p) {
		b := p[i]
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

func containsGBKBlindZoneBytes(p []byte) bool {
	suspicious2 := 0
	confirmed3 := 0
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

func oemToUTF8(data []byte, fromCP uint32) []byte {
	if len(data) == 0 {
		return data
	}
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	mbtwc := kernel32.NewProc("MultiByteToWideChar")
	wctmb := kernel32.NewProc("WideCharToMultiByte")

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

	nBytes, _, _ := wctmb.Call(
		65001, 0,
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

// ── Native 'id' implementation ─────────────────────────────────────────────

type idEntry struct {
	name string
	id   int
}

func buildIDOutput(cmdArgs []string, environ []string) string {
	flagU, flagG, flagGG, flagN := false, false, false, false
	lookupName := ""
	for _, a := range cmdArgs[1:] {
		if strings.HasPrefix(a, "-") {
			for _, c := range strings.TrimPrefix(a, "-") {
				switch c {
				case 'u':
					flagU = true
				case 'g':
					flagG = true
				case 'G':
					flagGG = true
				case 'n':
					flagN = true
				}
			}
		} else {
			lookupName = a
		}
	}

	windowsUser := ""
	for _, e := range environ {
		switch {
		case strings.HasPrefix(e, "USER=") && lookupName == "":
			lookupName = e[5:]
		case strings.HasPrefix(e, "LOGNAME=") && lookupName == "":
			lookupName = e[8:]
		case strings.HasPrefix(e, "USERNAME="):
			windowsUser = e[9:]
		}
	}

	root := findCygwinRoot()
	if root == "" {
		return ""
	}

	if lookupName == "" && windowsUser != "" {
		lookupName = findCygwinUsernameByWindowsUser(root, windowsUser)
	}
	if lookupName == "" {
		return ""
	}

	uid, primaryGID, ok := readPasswdEntry(root, lookupName)
	if !ok {
		return ""
	}
	groups := readGroupEntries(root, lookupName, primaryGID)

	primaryGrpName := ""
	for _, g := range groups {
		if g.id == primaryGID {
			primaryGrpName = g.name
			break
		}
	}

	// groupName returns the name for a group entry, falling back to the numeric
	// GID when /etc/group is absent (group info comes from Windows NSS instead).
	groupName := func(g idEntry) string {
		if g.name != "" {
			return g.name
		}
		return strconv.Itoa(g.id)
	}

	switch {
	case flagU && flagN:
		return lookupName
	case flagU:
		return strconv.Itoa(uid)
	case flagG && flagN:
		return groupName(idEntry{name: primaryGrpName, id: primaryGID})
	case flagG:
		return strconv.Itoa(primaryGID)
	case flagGG && flagN:
		names := make([]string, len(groups))
		for i, g := range groups {
			names[i] = groupName(g)
		}
		return strings.Join(names, " ")
	case flagGG:
		ids := make([]string, len(groups))
		for i, g := range groups {
			ids[i] = strconv.Itoa(g.id)
		}
		return strings.Join(ids, " ")
	}

	// Default: full output
	var sb strings.Builder
	fmt.Fprintf(&sb, "uid=%d(%s) gid=%d(%s) groups=",
		uid, lookupName, primaryGID,
		groupName(idEntry{name: primaryGrpName, id: primaryGID}))
	for i, g := range groups {
		if i > 0 {
			sb.WriteByte(',')
		}
		fmt.Fprintf(&sb, "%d(%s)", g.id, groupName(g))
	}
	return sb.String()
}

func findCygwinUsernameByWindowsUser(root, windowsUser string) string {
	data, err := os.ReadFile(filepath.Join(root, "etc", "passwd"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" || line[0] == '#' {
			continue
		}
		fields := strings.Split(line, ":")
		if len(fields) < 5 {
			continue
		}
		for _, part := range strings.Split(fields[4], ",") {
			if idx := strings.LastIndex(part, `\`); idx >= 0 {
				if strings.EqualFold(part[idx+1:], windowsUser) {
					return fields[0]
				}
			}
		}
	}
	return ""
}

func findCygwinRoot() string {
	for _, root := range []string{`C:\cygwin64`, `C:\cygwin`, `C:\tools\cygwin`} {
		if _, err := os.Stat(filepath.Join(root, "etc", "passwd")); err == nil {
			return root
		}
	}
	return ""
}

func readPasswdEntry(root, username string) (uid, gid int, ok bool) {
	data, err := os.ReadFile(filepath.Join(root, "etc", "passwd"))
	if err != nil {
		return 0, 0, false
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" || line[0] == '#' {
			continue
		}
		fields := strings.Split(line, ":")
		if len(fields) >= 4 && fields[0] == username {
			u, e1 := strconv.Atoi(fields[2])
			g, e2 := strconv.Atoi(fields[3])
			if e1 == nil && e2 == nil {
				return u, g, true
			}
		}
	}
	return 0, 0, false
}

func readGroupEntries(root, username string, primaryGID int) []idEntry {
	data, err := os.ReadFile(filepath.Join(root, "etc", "group"))
	if err != nil {
		// No /etc/group — return just the primary GID with no name.
		return []idEntry{{id: primaryGID}}
	}

	var result []idEntry
	primaryFound := false
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" || line[0] == '#' {
			continue
		}
		fields := strings.Split(line, ":")
		if len(fields) < 4 {
			continue
		}
		gid, err := strconv.Atoi(fields[2])
		if err != nil {
			continue
		}
		isPrimary := gid == primaryGID
		isMember := false
		for _, m := range strings.Split(fields[3], ",") {
			if strings.TrimSpace(m) == username {
				isMember = true
				break
			}
		}
		if isPrimary {
			primaryFound = true
			result = append([]idEntry{{name: fields[0], id: gid}}, result...)
		} else if isMember {
			result = append(result, idEntry{name: fields[0], id: gid})
		}
	}
	if !primaryFound {
		result = append([]idEntry{{id: primaryGID}}, result...)
	}
	return result
}
