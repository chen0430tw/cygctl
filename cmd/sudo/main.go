//go:build windows

package main

import (
	"bytes"
	"crypto/rand"
	"encoding/gob"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf8"
	"unsafe"

	"golang.org/x/sys/windows"
)

// kernel32 procs not exported by golang.org/x/sys/windows in this version.
var (
	_kernel32        = syscall.NewLazyDLL("kernel32.dll")
	_waitNamedPipe   = _kernel32.NewProc("WaitNamedPipeW")
	_attachConsole   = _kernel32.NewProc("AttachConsole")
	_freeConsole     = _kernel32.NewProc("FreeConsole")
	_cancelIoEx      = _kernel32.NewProc("CancelIoEx")

	_secur32        = syscall.NewLazyDLL("secur32.dll")
	_getUserNameExW = _secur32.NewProc("GetUserNameExW")
)

const fileFlagOverlapped = 0x40000000

func waitNamedPipe(namePtr *uint16, timeoutMs uint32) error {
	r, _, e := _waitNamedPipe.Call(uintptr(unsafe.Pointer(namePtr)), uintptr(timeoutMs))
	if r == 0 {
		return e
	}
	return nil
}

func attachConsole(pid uint32) error {
	r, _, e := _attachConsole.Call(uintptr(pid))
	if r == 0 {
		return e
	}
	return nil
}

func freeConsole() {
	_freeConsole.Call()
}

const (
	fileFlagFirstPipeInstance = 0x00080000

	daemonStartTimeout = 15 * time.Second
	daemonIdleTimeout  = 24 * time.Hour
)

// msg is the gob-encoded envelope carried over the named pipe.
type msg struct {
	Name  string
	Data  []byte
	Error string
	Exit  int
}

// daemonRequest is sent by the non-elevated client to the elevated cygsec daemon
// for each command invocation.
type daemonRequest struct {
	Environ   []string
	Args      []string
	Mode      string // "attached" | "piped" | "kill"
	ParentPID uint32 // for attached mode: PID of the non-elevated sudo process
}

// ── Named-pipe transport ────────────────────────────────────────────────────
//
// Gob encoding is preserved unchanged; only the transport layer switches from
// TCP sockets to named pipes. This keeps XP-era wire compatibility (gob+pipes
// both work on Windows XP) while gaining the security properties of pipes.

// pipeConn wraps a named pipe handle opened with FILE_FLAG_OVERLAPPED.
// Using overlapped I/O allows concurrent ReadFile and WriteFile on the
// same handle, which is required for full-duplex named pipe communication.
type pipeConn struct {
	h      windows.Handle
	rEvent windows.Handle // manual-reset event for overlapped reads
	wEvent windows.Handle // manual-reset event for overlapped writes
}

func newPipeConn(h windows.Handle) (*pipeConn, error) {
	rEvent, err := windows.CreateEvent(nil, 1, 0, nil) // manual-reset, initially non-signaled
	if err != nil {
		return nil, err
	}
	wEvent, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		windows.CloseHandle(rEvent)
		return nil, err
	}
	return &pipeConn{h: h, rEvent: rEvent, wEvent: wEvent}, nil
}

func (c *pipeConn) Read(b []byte) (int, error) {
	windows.ResetEvent(c.rEvent)
	var ov windows.Overlapped
	ov.HEvent = c.rEvent
	var n uint32
	err := windows.ReadFile(c.h, b, &n, &ov)
	if err == windows.ERROR_IO_PENDING {
		err = windows.GetOverlappedResult(c.h, &ov, &n, true)
	}
	if err != nil {
		return 0, err
	}
	return int(n), nil
}

func (c *pipeConn) Write(b []byte) (int, error) {
	windows.ResetEvent(c.wEvent)
	var ov windows.Overlapped
	ov.HEvent = c.wEvent
	var n uint32
	err := windows.WriteFile(c.h, b, &n, &ov)
	if err == windows.ERROR_IO_PENDING {
		err = windows.GetOverlappedResult(c.h, &ov, &n, true)
	}
	if err != nil {
		return 0, err
	}
	return int(n), nil
}

func (c *pipeConn) Close() error {
	// Cancel any pending overlapped I/O before closing handles.
	_cancelIoEx.Call(uintptr(c.h), 0)
	windows.CloseHandle(c.rEvent)
	windows.CloseHandle(c.wEvent)
	return windows.CloseHandle(c.h)
}

// pipeListener is a simple named-pipe accept loop: one pending instance is
// always ready, and a new one is created before the current connection is
// handed to the caller.
type pipeListener struct {
	name string
	curr windows.Handle
	mu   sync.Mutex
}

func listenPipe(name string) (*pipeListener, error) {
	h, err := createPipeInstance(name, true)
	if err != nil {
		return nil, err
	}
	return &pipeListener{name: name, curr: h}, nil
}

func (l *pipeListener) Accept() (*pipeConn, error) {
	l.mu.Lock()
	curr := l.curr
	l.mu.Unlock()
	if curr == windows.InvalidHandle {
		return nil, fmt.Errorf("listener closed")
	}
	// Use overlapped ConnectNamedPipe so we don't block the OS thread.
	event, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return nil, err
	}
	var ov windows.Overlapped
	ov.HEvent = event
	err = windows.ConnectNamedPipe(curr, &ov)
	if err == windows.ERROR_IO_PENDING {
		// Wait for a client to connect.
		windows.WaitForSingleObject(event, windows.INFINITE)
		var n uint32
		err = windows.GetOverlappedResult(curr, &ov, &n, false)
		if err == windows.ERROR_PIPE_CONNECTED {
			err = nil
		}
	} else if err == windows.ERROR_PIPE_CONNECTED {
		err = nil
	}
	windows.CloseHandle(event)
	if err != nil {
		return nil, err
	}
	// Pre-create the next instance before handing off this one.
	next, _ := createPipeInstance(l.name, false)
	l.mu.Lock()
	l.curr = next
	l.mu.Unlock()
	conn, err := newPipeConn(curr)
	if err != nil {
		windows.CloseHandle(curr)
		return nil, err
	}
	return conn, nil
}

func (l *pipeListener) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.curr != windows.InvalidHandle {
		windows.CloseHandle(l.curr)
		l.curr = windows.InvalidHandle
	}
	return nil
}

func createPipeInstance(name string, first bool) (windows.Handle, error) {
	namePtr, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return windows.InvalidHandle, err
	}
	openMode := uint32(windows.PIPE_ACCESS_DUPLEX | fileFlagOverlapped)
	if first {
		openMode |= fileFlagFirstPipeInstance
	}
	// Grant all users connect+read+write access so the non-elevated client
	// (medium integrity) can connect to a pipe created by the elevated daemon.
	// Token-based auth in the protocol provides the actual security boundary.
	sa := pipeSA()
	return windows.CreateNamedPipe(
		namePtr,
		openMode,
		windows.PIPE_TYPE_BYTE|windows.PIPE_READMODE_BYTE|windows.PIPE_WAIT,
		windows.PIPE_UNLIMITED_INSTANCES,
		65536, 65536, 0, sa,
	)
}

// pipeSA returns a SECURITY_ATTRIBUTES that allows any local user to connect
// to the named pipe.  "D:(A;;GA;;;WD)" = DACL, Allow, GenericAll, Everyone.
func pipeSA() *windows.SecurityAttributes {
	sd, err := windows.SecurityDescriptorFromString("D:(A;;GA;;;WD)")
	if err != nil {
		return nil // fallback: OS default (may block medium-integrity clients)
	}
	return &windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: sd,
	}
}

// dialPipe connects to a named pipe, retrying until timeout.
func dialPipe(name string, timeout time.Duration) (*pipeConn, error) {
	namePtr, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(timeout)
	for {
		h, err := windows.CreateFile(
			namePtr,
			windows.GENERIC_READ|windows.GENERIC_WRITE,
			0, nil, windows.OPEN_EXISTING, fileFlagOverlapped, 0,
		)
		if err == nil {
			pc, err := newPipeConn(h)
			if err != nil {
				windows.CloseHandle(h)
				return nil, err
			}
			return pc, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("pipe connect timeout")
		}
		switch err {
		case windows.ERROR_PIPE_BUSY:
			waitNamedPipe(namePtr, 1000)
		case windows.ERROR_FILE_NOT_FOUND:
			time.Sleep(200 * time.Millisecond)
		default:
			return nil, err
		}
	}
}

// ── Naming helpers ──────────────────────────────────────────────────────────

// cygsecPipeName returns the per-user named pipe path for the cygsec tunnel.
func cygsecPipeName() string {
	u := strings.ToLower(os.Getenv("USERNAME"))
	if u == "" {
		u = "default"
	}
	return `\\.\pipe\cygsec-` + u
}

// cygsecLockPath returns the path of the token lock file used to verify that
// the running daemon belongs to the current session.
func cygsecLockPath() string {
	u := strings.ToLower(os.Getenv("USERNAME"))
	if u == "" {
		u = "default"
	}
	dir := os.Getenv("LOCALAPPDATA")
	if dir == "" {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "cygsec-"+u+".lock")
}

// ── Daemon connection helpers ───────────────────────────────────────────────

// tryConnectDaemon opens and authenticates a connection to an existing daemon.
func tryConnectDaemon() (io.ReadWriteCloser, error) {
	token, err := os.ReadFile(cygsecLockPath())
	if err != nil {
		return nil, err
	}
	t := strings.TrimSpace(string(token))
	conn, err := dialPipe(cygsecPipeName(), 2*time.Second)
	if err != nil {
		os.Remove(cygsecLockPath())
		return nil, err
	}
	enc := gob.NewEncoder(conn)
	dec := gob.NewDecoder(conn)
	if err := enc.Encode(t); err != nil {
		conn.Close()
		return nil, err
	}
	var ok bool
	if err := dec.Decode(&ok); err != nil || !ok {
		conn.Close()
		os.Remove(cygsecLockPath())
		return nil, fmt.Errorf("auth failed")
	}
	return conn, nil
}

// spawnDaemon generates a fresh session token, then tries to spawn an elevated
// daemon: first via the silent fodhelper bypass, then via ShellExecuteEx (UAC).
func spawnDaemon() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := hex.EncodeToString(raw)

	// ShellExecuteEx "runas" — shows the UAC prompt once per session.
	if err := shellExecuteAsync(exe, []string{"--cygsec", token}); err != nil {
		return "", fmt.Errorf("elevation failed: %v", err)
	}
	return token, nil
}

// waitAndConnectDaemon polls until the daemon has written its token lock file
// and opened its pipe, then connects and authenticates.
func waitAndConnectDaemon(token string) (io.ReadWriteCloser, error) {
	lockPath := cygsecLockPath()
	deadline := time.Now().Add(daemonStartTimeout)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(lockPath)
		if err == nil && strings.TrimSpace(string(raw)) == token {
			conn, err := dialPipe(cygsecPipeName(), 2*time.Second)
			if err == nil {
				enc := gob.NewEncoder(conn)
				dec := gob.NewDecoder(conn)
				if enc.Encode(token) == nil {
					var ok bool
					if dec.Decode(&ok) == nil && ok {
						return conn, nil
					}
				}
				conn.Close()
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return nil, fmt.Errorf("cygsec daemon did not start within %v", daemonStartTimeout)
}

// ── Console detection ───────────────────────────────────────────────────────

// isConsoleHandle reports whether the file descriptor is connected to a real
// Windows console (not a pipe or file redirection).
func isConsoleHandle(fd uintptr) bool {
	var mode uint32
	return windows.GetConsoleMode(windows.Handle(fd), &mode) == nil
}

// detectMode chooses between "attached" (stdin/stdout are a real console) and
// "piped" (at least one handle is redirected). Attached mode lets the elevated
// process share the caller's console directly with no I/O proxying.
func detectMode() (mode string, parentPID uint32) {
	if isConsoleHandle(os.Stdin.Fd()) &&
		isConsoleHandle(os.Stdout.Fd()) &&
		isConsoleHandle(os.Stderr.Fd()) {
		return "attached", windows.GetCurrentProcessId()
	}
	return "piped", 0
}

// consoleMu serialises attached-mode commands because AttachConsole /
// FreeConsole are process-wide operations in Windows.
var consoleMu sync.Mutex

// ── main ────────────────────────────────────────────────────────────────────

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: sudo <command> [args...]")
		fmt.Fprintln(os.Stderr, "       sudo -k          stop the cygsec tunnel daemon")
		os.Exit(1)
	}
	switch os.Args[1] {
	case "--client":
		os.Exit(runClient(os.Args[2:]))
	case "--cygsec":
		os.Exit(runDaemon(os.Args[2:]))
	case "-k":
		os.Exit(killDaemon())
	default:
		os.Exit(runServer(os.Args[1:]))
	}
}

// ── X11 forwarding ──────────────────────────────────────────────────────────

// detectXWinDisplay probes for a running Cygwin/X (XWin) instance by checking
// for X11 Unix-domain socket files under the Cygwin temp directory.
// Returns the display string (e.g. ":0") or "" if none found.
func detectXWinDisplay() string {
	cygRoot := findCygwinRoot()
	for i := 0; i <= 3; i++ {
		socket := filepath.Join(cygRoot, "tmp", ".X11-unix", fmt.Sprintf("X%d", i))
		if _, err := os.Stat(socket); err == nil {
			return fmt.Sprintf(":%d", i)
		}
	}
	return ""
}

// forwardX11 adjusts the environment for the elevated daemon so that X11 GUI
// apps work correctly.  The elevated daemon is a new Win32 process — it has no
// fork relationship with the caller and therefore cannot use Unix-domain X
// sockets (DISPLAY=:N).  Switching to the TCP form (localhost:N.0) lets the
// elevated process reach the X server over loopback instead.
//
// If DISPLAY is not set in the caller's environment (e.g. launched from a
// plain Windows terminal rather than an X11 terminal), detectXWinDisplay()
// is used to locate a running XWin instance automatically.
//
// If an XAUTHORITY file exists for the current display, its MIT-MAGIC-COOKIE
// is written to a temp file that the elevated process can read, and XAUTHORITY
// is updated to point at that file.
func forwardX11(environ []string) []string {
	display := ""
	xauth := ""
	for _, e := range environ {
		switch {
		case strings.HasPrefix(e, "DISPLAY="):
			display = e[8:]
		case strings.HasPrefix(e, "XAUTHORITY="):
			xauth = e[11:]
		}
	}
	if display == "" {
		// Caller has no DISPLAY — try to find a running XWin instance.
		display = detectXWinDisplay()
		if display == "" {
			return environ // no X server found, nothing to forward
		}
		// Inject DISPLAY so the rebuild loop below includes it.
		environ = append(environ, "DISPLAY="+display)
	}

	// Convert Unix-socket display (:N or :N.S) to TCP (localhost:N.S).
	// Elevated Win32 processes cannot share Cygwin's /tmp/.X11-unix sockets.
	tcpDisplay := display
	if strings.HasPrefix(display, ":") {
		// :0 → localhost:0.0,  :0.1 → localhost:0.1
		rest := display[1:] // "0" or "0.1"
		if !strings.Contains(rest, ".") {
			rest += ".0"
		}
		tcpDisplay = "localhost:" + rest
	}

	// Try to copy the xauth cookie so the elevated process can authenticate.
	// XWin (Cygwin/X) uses -auth /home/user/.serverauth.PID rather than
	// ~/.Xauthority, so we probe several candidate paths including the
	// Cygwin home directory (C:\cygwin64\home\<user>) which differs from
	// the Windows home (C:\Users\<user>).
	if xauth == "" {
		winHome, _ := os.UserHomeDir()
		cygHome := filepath.Join(findCygwinRoot(), "home", os.Getenv("USERNAME"))
		homes := []string{cygHome, winHome}

		var candidates []string
		for _, home := range homes {
			candidates = append(candidates, filepath.Join(home, ".Xauthority"))
			if matches, err := filepath.Glob(filepath.Join(home, ".serverauth.*")); err == nil {
				candidates = append(candidates, matches...)
			}
		}
		for _, c := range candidates {
			if _, err := os.Stat(c); err == nil {
				xauth = c
				break
			}
		}
	}
	// Don't convert to TCP for Cygwin/X — it uses shared memory (no -listen tcp).
	// Keep DISPLAY as-is so the elevated process connects via the same socket.
	tcpDisplay = display

	newXauth := ""
	if xauth != "" {
		if data, err := os.ReadFile(xauth); err == nil && len(data) > 0 {
			tmp, err := os.CreateTemp("", "cygsudo-xauth-*")
			if err == nil {
				if _, err := tmp.Write(data); err == nil {
					newXauth = tmp.Name()
				}
				tmp.Close()
			}
		}
	}

	// Rebuild environ with updated DISPLAY (and XAUTHORITY if we got a cookie).
	out := make([]string, 0, len(environ))
	for _, e := range environ {
		switch {
		case strings.HasPrefix(e, "DISPLAY="):
			out = append(out, "DISPLAY="+tcpDisplay)
		case strings.HasPrefix(e, "XAUTHORITY=") && newXauth != "":
			// replaced below
		default:
			out = append(out, e)
		}
	}
	if newXauth != "" {
		out = append(out, "XAUTHORITY="+newXauth)
	}
	return out
}

// ── Non-elevated server ─────────────────────────────────────────────────────

func runServer(args []string) int {
	conn, err := tryConnectDaemon()
	if err != nil {
		fmt.Fprintln(os.Stderr, "sudo: approve the UAC prompt to start cygsec daemon")
		token, spawnErr := spawnDaemon()
		if spawnErr != nil {
			fmt.Fprintf(os.Stderr, "sudo: spawn failed: %v\n", spawnErr)
			return 1
		}
		conn, err = waitAndConnectDaemon(token)
		if err != nil {
			fmt.Fprintf(os.Stderr, "sudo: %v\n", err)
			return 1
		}
	}
	defer conn.Close()

	enc := gob.NewEncoder(conn)
	dec := gob.NewDecoder(conn)

	mode, parentPID := detectMode()
	if err := enc.Encode(daemonRequest{
		Environ:   forwardX11(os.Environ()),
		Args:      args,
		Mode:      mode,
		ParentPID: parentPID,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "sudo: send error: %v\n", err)
		return 1
	}
	if mode == "attached" {
		// Console is shared directly; the client just waits for the exit code.
		for {
			var m msg
			if err := dec.Decode(&m); err != nil {
				return 1
			}
			switch m.Name {
			case "exit":
				return m.Exit
			case "error":
				fmt.Fprintln(os.Stderr, m.Error)
			}
		}
	}

	// Piped mode: proxy stdin / receive stdout+stderr via gob.
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
				if e := enc.Encode(&msg{Name: "stdin", Data: buf[:n]}); e != nil {
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
			// Close the connection explicitly so the daemon's drain loop
			// (io.Copy(io.Discard, conn)) unblocks and handleDaemonConn
			// can finish cleanly.
			conn.Close()
			return m.Exit
		}
	}
}

// ── sudo -k ─────────────────────────────────────────────────────────────────

func killDaemon() int {
	conn, err := tryConnectDaemon()
	if err != nil {
		fmt.Fprintln(os.Stderr, "sudo: no active cygsec session")
		return 1
	}
	defer conn.Close()
	enc := gob.NewEncoder(conn)
	dec := gob.NewDecoder(conn)
	enc.Encode(daemonRequest{Mode: "kill"})
	var m msg
	dec.Decode(&m)
	fmt.Println("sudo: cygsec daemon stopped")
	return 0
}

// ── Elevated cygsec daemon ───────────────────────────────────────────────────

// runDaemon is the long-lived elevated daemon.  It is spawned either silently
// via the fodhelper bypass or via ShellExecuteEx "runas" as a fallback.
// args: [session-token]
func runDaemon(args []string) int {
	if len(args) < 1 {
		return 1
	}
	token := args[0]

	lis, err := listenPipe(cygsecPipeName())
	if err != nil {
		// Write to a debug file since daemon has no console.
		os.WriteFile(os.Getenv("LOCALAPPDATA")+"\\cygsec-debug.txt",
			[]byte("listenPipe failed: "+err.Error()), 0600)
		return 1
	}
	defer lis.Close()

	// Signal readiness: write the token so the non-elevated client can connect.
	if err := os.WriteFile(cygsecLockPath(), []byte(token), 0600); err != nil {
		os.WriteFile(os.Getenv("LOCALAPPDATA")+"\\cygsec-debug.txt",
			[]byte("WriteFile lock failed: "+err.Error()), 0600)
		return 1
	}

	enableAllPrivileges()

	var (
		activeConns int32
		lastDoneNs  int64
		stopOnce    sync.Once
	)
	atomic.StoreInt64(&lastDoneNs, time.Now().UnixNano())
	stopFn := func() {
		stopOnce.Do(func() {
			os.Remove(cygsecLockPath())
			os.Exit(0)
		})
	}

	// Idle watchdog: shut down after daemonIdleTimeout with no active connections.
	go func() {
		for {
			time.Sleep(30 * time.Second)
			if atomic.LoadInt32(&activeConns) == 0 &&
				time.Since(time.Unix(0, atomic.LoadInt64(&lastDoneNs))) >= daemonIdleTimeout {
				stopFn()
				return
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
			handleDaemonConn(conn, token, stopFn)
		}()
	}
	return 0
}

func handleDaemonConn(conn io.ReadWriteCloser, token string, stopFn func()) {
	defer conn.Close()
	enc := gob.NewEncoder(conn)
	dec := gob.NewDecoder(conn)

	var clientToken string
	if err := dec.Decode(&clientToken); err != nil {
		daemonLog("handleDaemonConn: auth decode error: %v", err)
		return
	}
	if clientToken != token {
		enc.Encode(false)
		return
	}
	enc.Encode(true)

	var req daemonRequest
	if err := dec.Decode(&req); err != nil {
		daemonLog("handleDaemonConn: request decode error: %v", err)
		return
	}
	daemonLog("handleDaemonConn: mode=%s args=%v", req.Mode, req.Args)

	switch req.Mode {
	case "kill":
		enc.Encode(&msg{Name: "exit", Exit: 0})
		go func() {
			time.Sleep(50 * time.Millisecond)
			stopFn()
		}()

	case "attached":
		execAttached(enc, req)

	default: // "piped" + XP-compatible gob path
		// stdinDone is closed by execPiped's internal stdin goroutine when it
		// exits.  We wait for it before returning so that conn is no longer
		// being read by two goroutines at once (the goroutine and io.Copy
		// racing on the same pipe handle causes hangs in Git Bash / mintty).
		stdinDone := make(chan struct{})
		daemonLog("handleDaemonConn: execPiped starting")
		execPiped(enc, dec, req, stdinDone)
		daemonLog("handleDaemonConn: execPiped returned, waiting for stdin goroutine")
		// The stdin goroutine holds dec and blocks on conn.Read. We must wait
		// for it to exit before doing any further reads on conn, otherwise the
		// two readers race and one may hang forever.
		<-stdinDone
		daemonLog("handleDaemonConn: stdin goroutine done, draining")
		// Drain so the exit message reaches the client before conn.Close().
		io.Copy(io.Discard, conn)
		daemonLog("handleDaemonConn: drain done")
	}
}

// daemonLog appends a timestamped message to the cygsec debug log file.
// The daemon has no console so this is the only way to observe its behaviour.
func daemonLog(format string, args ...interface{}) {
	path := os.Getenv("LOCALAPPDATA") + `\cygsec-debug.txt`
	line := fmt.Sprintf("[%s] "+format+"\n", append([]interface{}{time.Now().Format("15:04:05.000")}, args...)...)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	defer f.Close()
	f.WriteString(line)
}

// getCurrentUserName returns the current user in DOMAIN\user format (lowercase)
// using secur32!GetUserNameExW — same output as Windows' whoami.exe but without
// spawning a subprocess (which can hang 37+ seconds in the elevated daemon when
// Cygwin's whoami.exe ends up on PATH and times out on NSS/domain lookups).
func getCurrentUserName() string {
	const nameSamCompatible = 2
	var buf [256]uint16
	n := uint32(len(buf))
	r, _, _ := _getUserNameExW.Call(
		nameSamCompatible,
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&n)),
	)
	if r != 0 && n > 0 {
		return strings.ToLower(windows.UTF16ToString(buf[:n]))
	}
	// Fallback: environment variables (always available in elevated context).
	domain := os.Getenv("USERDOMAIN")
	user := os.Getenv("USERNAME")
	if domain != "" && user != "" {
		return strings.ToLower(domain + `\` + user)
	}
	return strings.ToLower(user)
}

// execAttached runs a command with the daemon attached to the caller's console.
// The elevated process shares the terminal directly — no I/O proxying occurs.
// consoleMu ensures only one attached command runs at a time (AttachConsole is
// a process-wide operation).
func execAttached(enc *gob.Encoder, req daemonRequest) {
	consoleMu.Lock()
	defer consoleMu.Unlock()

	freeConsole()
	if err := attachConsole(req.ParentPID); err != nil {
		enc.Encode(&msg{Name: "error", Error: fmt.Sprintf("AttachConsole(%d): %v", req.ParentPID, err)})
		enc.Encode(&msg{Name: "exit", Exit: 1})
		return
	}
	defer freeConsole()

	// Open the attached console's stdin/stdout handles explicitly, because the
	// daemon process itself was spawned hidden (SW_HIDE) with no console.
	conIn, _ := os.OpenFile("CONIN$", os.O_RDONLY, 0)
	conOut, _ := os.OpenFile("CONOUT$", os.O_RDWR, 0)
	defer func() {
		if conIn != nil {
			conIn.Close()
		}
		if conOut != nil {
			conOut.Close()
		}
	}()

	if len(req.Args) > 0 {
		winPath := resolveWithEnvPath(req.Args[0], req.Environ)
		if interp := resolveShebangInterpreter(winPath, req.Environ); interp != nil {
			// Pass script as Cygwin path (not Windows path) so Cygwin interpreters understand it.
			cygPath := winPathToCygwin(winPath, findCygwinRoot())
			daemonLog("execAttached: shebang -> interp %v script %q", interp, cygPath)
			req.Args[0] = cygPath
			req.Args = append(interp, req.Args...)
		} else {
			req.Args[0] = winPath
		}
	}
	daemonLog("execAttached: running %v", req.Args)
	req.Environ = append(req.Environ, "NMAP_PRIVILEGED=1", "NPING_PRIVILEGED=1")

	// Save console input mode and restore after command exits,
	// in case the child process disables echo or changes terminal settings.
	var savedConsoleMode uint32
	conInHandle := windows.Handle(conIn.Fd())
	savedOK := windows.GetConsoleMode(conInHandle, &savedConsoleMode) == nil

	cmd := exec.Command(req.Args[0], req.Args[1:]...)
	cmd.Env = req.Environ
	cmd.Stdin, cmd.Stdout, cmd.Stderr = conIn, conOut, conOut

	code := 0
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			code = exitErr.ExitCode()
		} else {
			daemonLog("execAttached: exec error: %v", err)
			code = 1
		}
	}
	if savedOK {
		windows.SetConsoleMode(conInHandle, savedConsoleMode)
	}
	daemonLog("execAttached: exit=%d", code)
	enc.Encode(&msg{Name: "exit", Exit: code})
}

// execPiped runs a command with stdin/stdout/stderr relayed through the named
// pipe via gob encoding.  This is the XP-compatible path used whenever I/O is
// redirected (pipes, files, or Cygwin PTY).  It also intercepts 'id' natively
// to work around Cygwin FD-inheritance limitations in elevated processes.
func execPiped(enc *gob.Encoder, dec *gob.Decoder, req daemonRequest, stdinDone chan<- struct{}) {
	// Native command interception — avoids spawning subprocesses that may
	// misbehave in the elevated daemon (no console, unusual PATH, etc.).
	if len(req.Args) > 0 {
		base := strings.ToLower(filepath.Base(req.Args[0]))

		// 'id' — Cygwin FD inheritance doesn't work when the parent process is
		// a Windows ShellExecuteEx/fodhelper-spawned process.
		if base == "id" || base == "id.exe" {
			if out := buildIDOutput(req.Args, req.Environ); out != "" {
				enc.Encode(&msg{Name: "stdout", Data: []byte(out + "\n")})
				enc.Encode(&msg{Name: "exit", Exit: 0})
				close(stdinDone)
				return
			}
		}

		// 'whoami' (no flags) — the elevated daemon's PATH may resolve to
		// Cygwin's whoami.exe which hangs ~37 s on NSS/domain lookups then
		// exits 1.  Use GetUserNameExW directly instead.
		if (base == "whoami" || base == "whoami.exe") && len(req.Args) == 1 {
			out := getCurrentUserName()
			daemonLog("execPiped: whoami intercepted -> %q", out)
			enc.Encode(&msg{Name: "stdout", Data: []byte(out + "\n")})
			enc.Encode(&msg{Name: "exit", Exit: 0})
			close(stdinDone)
			return
		}
	}

	if len(req.Args) > 0 {
		winPath := resolveWithEnvPath(req.Args[0], req.Environ)
		if interp := resolveShebangInterpreter(winPath, req.Environ); interp != nil {
			cygPath := winPathToCygwin(winPath, findCygwinRoot())
			daemonLog("execPiped: shebang %q -> interp %v script %q", winPath, interp, cygPath)
			req.Args[0] = cygPath
			req.Args = append(interp, req.Args...)
		} else {
			req.Args[0] = winPath
		}
	}
	// Log which executable will actually be run so we can diagnose PATH issues.
	if resolved, err := exec.LookPath(req.Args[0]); err != nil {
		daemonLog("execPiped: LookPath(%q): %v", req.Args[0], err)
	} else {
		daemonLog("execPiped: LookPath(%q) -> %q", req.Args[0], resolved)
	}
	daemonLog("execPiped: running %v", req.Args)
	req.Environ = append(req.Environ, "NMAP_PRIVILEGED=1", "NPING_PRIVILEGED=1")

	cmd := exec.Command(req.Args[0], req.Args[1:]...)
	cmd.Env = req.Environ
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x08000000, // CREATE_NO_WINDOW
	}

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		enc.Encode(&msg{Name: "error", Error: err.Error()})
		enc.Encode(&msg{Name: "exit", Exit: 1})
		close(stdinDone)
		return
	}

	cp := getOEMCP()
	cmd.Stdout = &msgWriter{enc: enc, name: "stdout", fromCP: cp}
	// Tee stderr to the daemon log so subprocess error messages are visible
	// even when the client disconnects before the command finishes.
	var stderrBuf bytes.Buffer
	cmd.Stderr = io.MultiWriter(&msgWriter{enc: enc, name: "stderr", fromCP: cp}, &stderrBuf)

	// cmdDone is closed once cmd.Run() returns so the stdin goroutine can
	// exit promptly without waiting for the client to send EOF/close.
	cmdDone := make(chan struct{})

	go func() {
		defer close(stdinDone)
		defer stdinPipe.Close()
		for {
			// Check if the command has already finished; if so, stop reading
			// from the connection so we don't race with io.Copy in the caller.
			select {
			case <-cmdDone:
				return
			default:
			}
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
	if stderrBuf.Len() > 0 {
		daemonLog("execPiped: subprocess stderr: %s", strings.TrimSpace(stderrBuf.String()))
	}
	daemonLog("execPiped: cmd done, exit=%d, sending exit msg", code)
	close(cmdDone) // signal goroutine to stop reading
	enc.Encode(&msg{Name: "exit", Exit: code})
	daemonLog("execPiped: exit msg sent")
}

// ── Legacy one-shot elevated client (--client mode) ────────────────────────
//
// Kept for backward compatibility only.  New code always uses the cygsec daemon.

// runClient is the legacy one-shot elevation path (TCP-based, pre-cygsec).
// Kept for backward compatibility; the cygsec daemon supersedes it.
func runClient(args []string) int {
	fmt.Fprintln(os.Stderr, "sudo: --client is a legacy mode; cygsec daemon is now used instead")
	return 1
}

// ── msgWriter ──────────────────────────────────────────────────────────────

type msgWriter struct {
	enc    *gob.Encoder
	name   string
	fromCP uint32
}

func (w *msgWriter) Write(p []byte) (n int, err error) {
	data := p
	if w.fromCP != 0 && w.fromCP != 65001 &&
		(!utf8.Valid(p) || containsGBKExclusiveBytes(p) || containsGBKBlindZoneBytes(p)) {
		data = oemToUTF8(p, w.fromCP)
	}
	if err := w.enc.Encode(&msg{Name: w.name, Data: data}); err != nil {
		return 0, err
	}
	return len(p), nil
}

// ── Windows API helpers ────────────────────────────────────────────────────

func getOEMCP() uint32 {
	k32 := syscall.NewLazyDLL("kernel32.dll")
	r, _, _ := k32.NewProc("GetOEMCP").Call()
	return uint32(r)
}

func enableAllPrivileges() {
	adv := syscall.NewLazyDLL("advapi32.dll")
	openProcessToken    := adv.NewProc("OpenProcessToken")
	getTokenInformation := adv.NewProc("GetTokenInformation")
	adjustTokenPrivs    := adv.NewProc("AdjustTokenPrivileges")

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
		off := 4 + uintptr(i)*luidAndAttrsSize + 8
		*(*uint32)(unsafe.Pointer(&buf[off])) |= sePrivilegeEnabled
	}
	adjustTokenPrivs.Call(uintptr(tok), 0, uintptr(unsafe.Pointer(&buf[0])), 0, 0, 0)
}

// shellExecuteAsync spawns an elevated process via PowerShell Start-Process
// -Verb RunAs.  PowerShell has a proper message pump so the UAC consent dialog
// is reliably visible even when the caller is a Cygwin PTY process (where a
// raw ShellExecuteExW call may produce an invisible background dialog).
func shellExecuteAsync(exe string, args []string) error {
	// Quote each argument for PowerShell's -ArgumentList array syntax.
	quotedArgs := make([]string, len(args))
	for i, a := range args {
		quotedArgs[i] = "'" + strings.ReplaceAll(a, "'", "''") + "'"
	}
	psExe := strings.ReplaceAll(exe, "'", "''")
	psCmd := fmt.Sprintf(
		"Start-Process -FilePath '%s' -ArgumentList %s -Verb RunAs -WindowStyle Hidden",
		psExe, strings.Join(quotedArgs, ","),
	)
	cmd := exec.Command("powershell", "-NoProfile", "-Command", psCmd)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to launch elevation: %v", err)
	}
	// Enforce a 60-second timeout so a missed UAC dialog does not leave
	// the sudo process blocked indefinitely.
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("UAC denied or cancelled: %v", err)
		}
		return nil
	case <-time.After(60 * time.Second):
		cmd.Process.Kill()
		return fmt.Errorf("elevation timed out — UAC prompt not approved within 60s")
	}
}

func convertCygwinPath(p string) string {
	// /cygdrive/X/... → X:\...
	if strings.HasPrefix(p, "/cygdrive/") && len(p) >= 11 {
		drive := p[10]
		if (drive >= 'a' && drive <= 'z') || (drive >= 'A' && drive <= 'Z') {
			return string(drive) + ":" + strings.ReplaceAll(p[11:], "/", `\`)
		}
	}
	// /X/... where X is a single drive letter → X:\...
	if len(p) >= 3 && p[0] == '/' && p[2] == '/' {
		drive := p[1]
		if (drive >= 'a' && drive <= 'z') || (drive >= 'A' && drive <= 'Z') {
			return string(drive) + ":" + strings.ReplaceAll(p[2:], "/", `\`)
		}
	}
	// Cygwin bind-mounts: /usr/bin → cygroot\bin, /usr/lib → cygroot\lib
	if root := findCygwinRoot(); root != "" {
		if strings.HasPrefix(p, "/usr/bin/") || p == "/usr/bin" {
			return root + `\bin` + strings.ReplaceAll(p[8:], "/", `\`)
		}
		if strings.HasPrefix(p, "/usr/lib/") || p == "/usr/lib" {
			return root + `\lib` + strings.ReplaceAll(p[8:], "/", `\`)
		}
		// Any other absolute Cygwin path (/opt/..., /usr/..., /home/...) →
		// prepend the Cygwin installation root (e.g. C:\cygwin64).
		if len(p) > 0 && p[0] == '/' {
			return root + strings.ReplaceAll(p, "/", `\`)
		}
	} else if len(p) > 0 && p[0] == '/' {
		return p
	}
	return p
}

// winPathToCygwin converts a Windows path under cygwinRoot back to a Cygwin path.
// e.g. C:\cygwin64\usr\local\bin\zenmap → /usr/local/bin/zenmap
func winPathToCygwin(p, cygwinRoot string) string {
	if cygwinRoot == "" {
		return p
	}
	rootLow := strings.ToLower(cygwinRoot)
	pLow := strings.ToLower(p)
	if strings.HasPrefix(pLow, rootLow) {
		rel := p[len(cygwinRoot):]
		return strings.ReplaceAll(rel, `\`, `/`)
	}
	return p
}

// readCygwinSymlink reads a Cygwin-format symlink file ("!<symlink>target").
// The target may be UTF-8 or UTF-16 LE (with BOM \xff\xfe).
// Returns the target path, or "" if not a Cygwin symlink.
func readCygwinSymlink(path string) string {
	data, err := os.ReadFile(path)
	if err != nil || len(data) < 10 {
		return ""
	}
	const magic = "!<symlink>"
	if !strings.HasPrefix(string(data), magic) {
		return ""
	}
	rest := data[len(magic):]
	// UTF-16 LE with BOM
	if len(rest) >= 2 && rest[0] == 0xff && rest[1] == 0xfe {
		rest = rest[2:] // strip BOM
		// decode UTF-16 LE pairs
		var out []byte
		for i := 0; i+1 < len(rest); i += 2 {
			lo, hi := rest[i], rest[i+1]
			if lo == 0 && hi == 0 {
				break // null terminator
			}
			if hi == 0 {
				out = append(out, lo) // ASCII range
			}
		}
		return strings.TrimRight(string(out), "\r\n")
	}
	return strings.TrimRight(string(rest), "\x00\r\n")
}

// resolveWithEnvPath resolves a command name to a full Windows path.
// If name is a Cygwin absolute path (/usr/...) it converts it.
// If name is a bare command (no slashes/backslashes), it searches the
// Cygwin PATH entries from environ so the daemon can find binaries in
// directories like /usr/local/bin that aren't in its Windows PATH.
func resolveWithEnvPath(name string, environ []string) string {
	// Already a Cygwin absolute path — convert it, follow symlinks, try .exe.
	if strings.HasPrefix(name, "/") {
		p := convertCygwinPath(name)
		if _, err := os.Stat(p); err == nil {
			// Follow Cygwin-format symlinks (files starting with "!<symlink>")
			if target := readCygwinSymlink(p); target != "" {
				daemonLog("resolveWithEnvPath: cyglink %q -> %q", p, target)
				return resolveWithEnvPath(target, environ)
			}
			return p
		}
		if _, err2 := os.Stat(p + ".exe"); err2 == nil {
			return p + ".exe"
		}
		return p
	}
	// Already a Windows absolute or relative path — leave it.
	if strings.ContainsAny(name, `\`) || (len(name) >= 2 && name[1] == ':') {
		return name
	}
	// Bare name: search Cygwin PATH from environ.
	var envPath string
	for _, e := range environ {
		if strings.HasPrefix(e, "PATH=") {
			envPath = e[5:]
			break
		}
	}
	if envPath != "" {
		// PATH may be Windows-style (semicolon-separated) or Cygwin-style
		// (colon-separated). Detect by presence of semicolon.
		sep := ":"
		if strings.Contains(envPath, ";") {
			sep = ";"
		}
		for _, dir := range strings.Split(envPath, sep) {
			if dir == "" {
				continue
			}
			winDir := convertCygwinPath(dir)
			for _, ext := range []string{"", ".exe"} {
				p := filepath.Join(winDir, name+ext)
				if _, err := os.Stat(p); err == nil {
					daemonLog("resolveWithEnvPath: %q -> %q", name, p)
					return p
				}
			}
		}
	}
	return name
}

// resolveShebangInterpreter checks if path is a script (starts with #!).
// If so, returns the interpreter args to prepend; otherwise returns nil.
// Handles both #!/usr/bin/python3 and #!/usr/bin/env python3 forms.
func resolveShebangInterpreter(path string, environ []string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	buf := make([]byte, 256)
	n, _ := f.Read(buf)
	buf = buf[:n]
	if len(buf) < 2 || buf[0] != '#' || buf[1] != '!' {
		return nil
	}
	line := string(buf[2:])
	if idx := strings.IndexByte(line, '\n'); idx >= 0 {
		line = line[:idx]
	}
	parts := strings.Fields(strings.TrimSpace(line))
	if len(parts) == 0 {
		return nil
	}
	// #!/usr/bin/env python3 → resolve "python3" via PATH
	if filepath.Base(parts[0]) == "env" && len(parts) > 1 {
		resolved := resolveWithEnvPath(parts[1], environ)
		result := []string{resolved}
		return append(result, parts[2:]...)
	}
	// #!/usr/bin/python3 → convert Cygwin path
	resolved := resolveWithEnvPath(parts[0], environ)
	result := []string{resolved}
	return append(result, parts[1:]...)
}

func makeCmdLine(args []string) string {
	var parts []string
	for _, a := range args {
		if needsQuoting(a) {
			parts = append(parts, `"`+strings.ReplaceAll(a, `"`, `\"`)+`"`)
		} else {
			parts = append(parts, a)
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
	suspicious2, confirmed3 := 0, 0
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
	k32 := syscall.NewLazyDLL("kernel32.dll")
	mbtwc := k32.NewProc("MultiByteToWideChar")
	wctmb := k32.NewProc("WideCharToMultiByte")

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
