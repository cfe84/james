//go:build windows

package transport

// clearNonBlock is a no-op on Windows (FIFOs are not used).
func clearNonBlock(fd int) {}
