// Package printer implements search.Sink: rg's "grep-printer" equivalent.
// Standard formats matched (and context) lines as path:line:text, with
// color and TTY-elision handling; Summary implements count (-c) and
// files-with-matches (-l) modes without per-match formatting. Both are
// designed around a per-worker []byte buffer, filled with append-based
// formatting (no fmt on the hot path) and flushed to the shared writer
// as one write per file.
package printer
