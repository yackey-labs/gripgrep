// Package printer implements search.Sink: rg's "grep-printer" equivalent.
// Standard formats matched (and context) lines as path:line:text (or
// heading mode: path once, then line:text), with color and TTY-elision
// handling. Count (-c), FilesWithMatches (-l), and Quiet (-q) implement
// the summary modes without ever formatting a matched line. PathPrinter
// implements --files (walk-only, no search.Sink involved).
//
// Every per-file sink (Standard, Count, FilesWithMatches) is designed
// around a private []byte buffer: Begin resets it lazily, Matched/
// Context fill it with append-based formatting (no fmt on the hot
// path), and Finish flushes it to a shared Dest as one locked write —
// one per-file block, never interleaved with another worker's. Quiet
// and PathPrinter are the exceptions: Quiet is meant to be shared
// across every worker (there is no per-file output to keep separate),
// and PathPrinter runs its own single writer goroutine.
package printer
