package main

import (
	"syscall"
	"unsafe"
)

// isTerminal probes fd with the same TCGETS ioctl every isatty(3)
// implementation uses on Linux: it succeeds iff the fd supports terminal
// control operations.
func isTerminal(fd uintptr) bool {
	var termios syscall.Termios
	_, _, errno := syscall.Syscall6(syscall.SYS_IOCTL, fd, uintptr(syscall.TCGETS), uintptr(unsafe.Pointer(&termios)), 0, 0, 0)
	return errno == 0
}
