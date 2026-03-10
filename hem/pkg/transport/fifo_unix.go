//go:build !windows

package transport

import "golang.org/x/sys/unix"

// clearNonBlock clears O_NONBLOCK on a file descriptor so reads block normally.
func clearNonBlock(fd int) {
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
	if err == nil {
		unix.FcntlInt(uintptr(fd), unix.F_SETFL, flags&^unix.O_NONBLOCK)
	}
}
