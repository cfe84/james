//go:build unix

package main

import (
	"os"
	"syscall"
)

// createFifo creates a Unix FIFO (named pipe) at the given path.
func createFifo(path string) error {
	// Remove stale FIFO if it exists
	os.Remove(path)
	return syscall.Mkfifo(path, 0660)
}

// openFifoInput opens a FIFO for reading.
// We use O_RDWR so the kernel keeps it alive even when all writers disconnect.
func openFifoInput(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDWR, os.ModeNamedPipe)
}

// openFifoOutput opens a FIFO for writing.
// We use O_RDWR to avoid blocking until a reader connects.
func openFifoOutput(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDWR, os.ModeNamedPipe)
}
