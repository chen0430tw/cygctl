//go:build windows

package main

import (
	"encoding/gob"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"unsafe"
)

const (
	SEE_MASK_NOCLOSEPROCESS = 0x00000040
	SW_HIDE                 = 0
	SW_SHOW                 = 5
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

	// Check if running as client (elevated process)
	if os.Args[1] == "--client" {
		os.Exit(runClient(os.Args[2:]))
	}

	args := os.Args[1:]
	os.Exit(runServer(args))
}

func runServer(args []string) int {
	// Create listener
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintf(os.Stderr, "sudo: cannot create listener: %v\n", err)
		return 1
	}
	defer lis.Close()

	// Get executable path
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "sudo: cannot find executable: %v\n", err)
		return 1
	}

	// Build command args for elevated process
	cmdArgs := []string{"--client", lis.Addr().String()}
	cmdArgs = append(cmdArgs, args...)

	// Run elevated process
	go func() {
		if err := shellExecuteAndWait(exe, cmdArgs); err != nil {
			fmt.Fprintf(os.Stderr, "sudo: failed to elevate: %v\n", err)
			lis.Close()
		}
	}()

	// Accept connection
	conn, err := lis.Accept()
	if err != nil {
		fmt.Fprintf(os.Stderr, "sudo: cannot execute command: %v\n", err)
		return 1
	}
	defer conn.Close()

	enc := gob.NewEncoder(conn)
	dec := gob.NewDecoder(conn)

	// Send environment
	if err := enc.Encode(os.Environ()); err != nil {
		fmt.Fprintf(os.Stderr, "sudo: cannot send environment: %v\n", err)
		return 1
	}

	// Handle Ctrl+C
	sc := make(chan os.Signal, 1)
	signal.Notify(sc, os.Interrupt)
	go func() {
		for range sc {
			enc.Encode(&msg{Name: "ctrlc"})
		}
	}()

	// Forward stdin
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := os.Stdin.Read(buf)
			if err != nil {
				if err == io.EOF {
					enc.Encode(&msg{Name: "close"})
				}
				return
			}
			if err := enc.Encode(&msg{Name: "stdin", Data: buf[:n]}); err != nil {
				return
			}
		}
	}()

	// Receive output
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
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "sudo: client mode requires address")
		return 1
	}

	addr := args[0]
	cmdArgs := args[1:]

	// Connect to server
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sudo: cannot connect to server: %v\n", err)
		return 1
	}
	defer conn.Close()

	enc := gob.NewEncoder(conn)
	dec := gob.NewDecoder(conn)

	// Receive environment
	var environ []string
	if err := dec.Decode(&environ); err != nil {
		return 1
	}

	// Build command
	cmd := exec.Command(cmdArgs[0], cmdArgs[1:]...)
	cmd.Env = environ

	// Setup pipes
	stdinPipe, _ := cmd.StdinPipe()
	stdoutPipe := &msgWriter{enc: enc, name: "stdout"}
	stderrPipe := &msgWriter{enc: enc, name: "stderr"}
	cmd.Stdout = stdoutPipe
	cmd.Stderr = stderrPipe

	// Forward stdin from server
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

	// Run command
	code := 0
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			if status, ok := exitErr.Sys().(syscall.WaitStatus); ok {
				code = status.ExitStatus()
			} else {
				code = 1
			}
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

func (w *msgWriter) Close() error {
	return nil
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
		return fmt.Errorf("ShellExecuteExW failed")
	}

	if sei.hProcess != 0 {
		syscall.WaitForSingleObject(syscall.Handle(sei.hProcess), syscall.INFINITE)
		syscall.CloseHandle(syscall.Handle(sei.hProcess))
	}

	return nil
}

func makeCmdLine(args []string) string {
	var cmdLine string
	for _, arg := range args {
		if cmdLine != "" {
			cmdLine += " "
		}
		if containsSpace(arg) {
			cmdLine += `"` + arg + `"`
		} else {
			cmdLine += arg
		}
	}
	return cmdLine
}

func containsSpace(s string) bool {
	for _, c := range s {
		if c == ' ' || c == '\t' || c == '"' {
			return true
		}
	}
	return false
}
