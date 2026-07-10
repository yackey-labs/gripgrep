package main

import "io"

// isTerminal (per-OS: tty_linux.go, tty_darwin.go, tty_windows.go)
// reports whether fd refers to a terminal. This drives gg's --color=auto
// and default heading/line-number behavior (rg: both auto-detect from
// isatty(stdout), matching PLAN.md's "CLI startup" row's "nothing heavy
// at init" -- one syscall per platform, no dependency pulled in just for
// this).

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
