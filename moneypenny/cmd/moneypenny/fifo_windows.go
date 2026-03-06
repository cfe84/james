//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

var (
	kernel32         = syscall.NewLazyDLL("kernel32.dll")
	procCreateNamedPipe = kernel32.NewProc("CreateNamedPipeW")
)

const (
	PIPE_ACCESS_DUPLEX     = 0x00000003
	FILE_FLAG_FIRST_PIPE_INSTANCE = 0x00080000
	PIPE_TYPE_BYTE         = 0x00000000
	PIPE_READMODE_BYTE     = 0x00000000
	PIPE_WAIT              = 0x00000000
	PIPE_UNLIMITED_INSTANCES = 255
)

// createFifo creates a Windows named pipe.
func createFifo(path string) error {
	// Remove stale pipe marker file if it exists
	os.Remove(path)

	// Create a marker file to indicate the pipe exists
	// The actual pipe will be created when opened
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	f.Close()
	return nil
}

// openFifoInput opens a Windows named pipe for reading.
func openFifoInput(path string) (*os.File, error) {
	return openWindowsNamedPipe(path, true)
}

// openFifoOutput opens a Windows named pipe for writing.
func openFifoOutput(path string) (*os.File, error) {
	return openWindowsNamedPipe(path, false)
}

func openWindowsNamedPipe(path string, isInput bool) (*os.File, error) {
	// Convert path to Windows pipe name format
	pipeName := filepath.Base(path)
	pipePath := `\\.\pipe\` + pipeName

	pipePathUTF16, err := syscall.UTF16PtrFromString(pipePath)
	if err != nil {
		return nil, err
	}

	// Create the named pipe
	handle, _, err := procCreateNamedPipe.Call(
		uintptr(unsafe.Pointer(pipePathUTF16)),
		PIPE_ACCESS_DUPLEX,
		PIPE_TYPE_BYTE|PIPE_READMODE_BYTE|PIPE_WAIT,
		PIPE_UNLIMITED_INSTANCES,
		4096, // output buffer size
		4096, // input buffer size
		0,    // default timeout
		0,    // default security attributes
	)

	if handle == uintptr(syscall.InvalidHandle) {
		// If pipe already exists, try to open it
		if strings.Contains(err.Error(), "already exists") {
			return openExistingPipe(pipePath)
		}
		return nil, fmt.Errorf("failed to create named pipe: %v", err)
	}

	// Convert handle to os.File
	return os.NewFile(handle, pipePath), nil
}

func openExistingPipe(pipePath string) (*os.File, error) {
	pipePathUTF16, err := syscall.UTF16PtrFromString(pipePath)
	if err != nil {
		return nil, err
	}

	handle, err := syscall.CreateFile(
		pipePathUTF16,
		syscall.GENERIC_READ|syscall.GENERIC_WRITE,
		0,
		nil,
		syscall.OPEN_EXISTING,
		0,
		0,
	)

	if err != nil {
		return nil, fmt.Errorf("failed to open existing pipe: %v", err)
	}

	return os.NewFile(uintptr(handle), pipePath), nil
}
