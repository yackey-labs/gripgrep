package main

import (
	"io"
	"syscall"
	"unsafe"
)

// isTerminal reports whether fd refers to a terminal, via the same
// TCGETS ioctl probe every isatty(3) implementation uses on Linux: it
// succeeds iff the fd supports terminal control operations. This drives
// gg's --color=auto and default heading/line-number behavior (rg: both
// auto-detect from isatty(stdout), matching PLAN.md's "CLI startup"
// row's "nothing heavy at init" -- one syscall, no dependency pulled in
// just for this).
//
// linux-only: this module has no other-OS build target yet (see
// PLAN.md's module scope), so no build-tag fallback is provided.
func isTerminal(fd uintptr) bool {
	var termios syscall.Termios
	_, _, errno := syscall.Syscall6(syscall.SYS_IOCTL, fd, uintptr(syscall.TCGETS), uintptr(unsafe.Pointer(&termios)), 0, 0, 0)
	return errno == 0
}

// isTerminalWriter reports whether w is connected to a terminal. Most
// writers (including any bytes.Buffer used by in-process tests) don't
// expose a file descriptor at all, and are treated as non-terminals --
// the same safe default a piped rg process gets.
func isTerminalWriter(w io.Writer) bool {
	fd, ok := w.(interface{ Fd() uintptr })
	if !ok {
		return false
	}
	return isTerminal(fd.Fd())
}
