package main

import "syscall"

// isTerminal reports whether fd is a console handle, via GetConsoleMode
// -- the Windows equivalent of the unix isatty ioctl probe (see
// tty_linux.go / tty_darwin.go): it succeeds iff the handle refers to a
// console. Redirected files and pipes fail the call and are treated as
// non-terminals, matching the unix behavior.
func isTerminal(fd uintptr) bool {
	var mode uint32
	return syscall.GetConsoleMode(syscall.Handle(fd), &mode) == nil
}
