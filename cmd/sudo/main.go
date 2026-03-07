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
	"syscall"
	"time"
	"unsafe"
	"strings"
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

	cmd.Stdout = &msgWriter{enc: enc, name: "stdout"}
	cmd.Stderr = &msgWriter{enc: enc, name: "stderr"}

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
	enc  *gob.Encoder
	name string
}

func (w *msgWriter) Write(p []byte) (n int, err error) {
	if err := w.enc.Encode(&msg{Name: w.name, Data: p}); err != nil {
		return 0, err
	}
	return len(p), nil
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
		nShow:        SW_SHOW,
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
