package main

import (
	"syscall"
	"unsafe"
)

// isTerminal probes fd with the TIOCGETA ioctl -- the BSD/darwin
// spelling of Linux's TCGETS (see tty_linux.go): it succeeds iff the fd
// supports terminal control operations, which is exactly what isatty(3)
// does on macOS.
func isTerminal(fd uintptr) bool {
	var termios syscall.Termios
	_, _, errno := syscall.Syscall6(syscall.SYS_IOCTL, fd, uintptr(syscall.TIOCGETA), uintptr(unsafe.Pointer(&termios)), 0, 0, 0)
	return errno == 0
}
