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

	// Step 3: Run command with the received environment
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
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	r, _, _ := kernel32.NewProc("GetOEMCP").Call()
	return uint32(r)
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
