package main

import (
	"fmt"
	"io"

	"github.com/yackey-labs/gripgrep/internal/engine"
	"github.com/yackey-labs/gripgrep/search"
)

// countingWriter wraps an io.Writer, tallying the CONTENT bytes written
// through it -- every byte except ANSI SGR color escapes (ESC '[' ... final
// byte). execute installs one between printer.Dest and the real stdout ONLY
// under --stats in a line-displaying mode (standard/-o/-v), so its running
// total is exactly rg's "bytes printed": the bytes the match/context
// printer emitted. It is deliberately absent for -c/-l/--count-matches/-q,
// whose count/path output reaches stdout but is reported as "0 bytes
// printed" (rg's summary sink snapshots bytes_printed before writing those
// fields -- see crates/printer/src/summary.rs). The stats block itself is
// written above this writer, so it never counts toward its own total.
//
// The SGR-stripping mirrors rg exactly: rg emits color via termcolor's
// set_color, which writes escapes straight to the underlying stream and
// BYPASSES its CounterWriter, so rg's bytes_printed under --color=always is
// the uncolored content length (verified: `rg --color=always --stats`
// reports the same 65 as plain). gg instead bakes escapes inline into the
// printer buffer, so the counter must skip them to agree. gg's coloring is
// SGR-only (ESC '[' params 'm'), so skipping any CSI sequence (final byte
// 0x40-0x7e) cancels every color byte with no effect when color is off. The
// only untracked edge is a literal ESC in searched CONTENT, which rg would
// count and this skips -- vanishingly rare and never in the golden matrix.
type countingWriter struct {
	w io.Writer
	n int64
	// escState tracks CSI-sequence parsing across bytes (and across Write
	// calls, since a per-file buffer boundary could in principle split a
	// sequence): escNormal -> escAfterEsc on ESC, -> escInCSI on the '['
	// introducer, back to escNormal on the final byte (0x40..0x7e).
	escState uint8
}

const (
	escNormal uint8 = iota
	escAfterEsc
	escInCSI
)

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	// Tally content bytes only, skipping ANSI CSI (color) escape sequences.
	// Iterate the bytes actually written (n), so a short write never
	// over-counts. The '[' introducer falls in the CSI final-byte range
	// itself, which is why entering the sequence needs its own state rather
	// than a flat "in escape" flag.
	for _, b := range p[:n] {
		switch c.escState {
		case escAfterEsc:
			if b == '[' {
				c.escState = escInCSI
			} else {
				// Not a CSI; gg emits only CSI, so treat this rare 2-byte
				// escape as non-content and resume counting after it.
				c.escState = escNormal
			}
		case escInCSI:
			if b >= 0x40 && b <= 0x7e {
				c.escState = escNormal
			}
		default:
			if b == 0x1b {
				c.escState = escAfterEsc
			} else {
				c.n++
			}
		}
	}
	return n, err
}

// discardSink is the worker sink used for -q under --stats: it produces no
// output (rg's -q shows nothing, ever) yet, unlike printer.Quiet, never
// aborts a file (Begin/Matched/Context all keep searching), so the shared
// StatsAccumulator wrapped around it sees every match of every file. Plain
// -q (no --stats) never reaches this: engine.Run overrides the worker sink
// with its fast early-exit QuitSink there instead. Its exit-code
// contribution flows through matchTracker's own matched-signal aggregation
// (Result.Matched), not through this sink, so it needs no Found() of its
// own.
type discardSink struct{}

var _ search.Sink = discardSink{}

func (discardSink) Begin(string) (bool, error)          { return true, nil }
func (discardSink) Matched(*search.Match) (bool, error) { return true, nil }
func (discardSink) Context(*search.Ctx) (bool, error)   { return true, nil }
func (discardSink) Finish(string, *search.Stats) error  { return nil }

// writeStatsBlock renders rg's --stats summary to w, byte-for-byte
// identical to crates/core/main.rs's print_stats (the non-JSON arm), down
// to the leading blank line, the fixed field order, and the deliberate
// lack of any pluralization ("1 matched lines"). The two timing lines use
// %.6f to mirror rg's NiceDuration `{:0.6}` formatting; their values are
// nondeterministic, so e2e golden tests normalize exactly those two lines.
// bytesPrinted and total are supplied by the caller (the accumulator does
// not track them): bytesPrinted from the countingWriter, total from
// execute's own wall-clock measurement of the whole search phase.
func writeStatsBlock(w io.Writer, snap engine.StatsSnapshot, bytesPrinted int64, total float64) {
	fmt.Fprintf(w,
		"\n%d matches\n%d matched lines\n%d files contained matches\n%d files searched\n%d bytes printed\n%d bytes searched\n%.6f seconds spent searching\n%.6f seconds total\n",
		snap.Matches,
		snap.MatchedLines,
		snap.FilesWithMatch,
		snap.FilesSearched,
		bytesPrinted,
		snap.BytesSearched,
		snap.SearchTime.Seconds(),
		total,
	)
}

// resolveBlockBuffered reports whether execute should wrap stdout in a
// flushed block buffer. gg buffers ONLY on an explicit --block-buffered:
// the default (BufferAuto) and --line-buffered both write through directly,
// which is gg's long-standing, proven output path (a flush per per-file
// block). This is byte-identical to rg in every case -- the buffering
// choice only changes flush cadence to a live consumer, never the emitted
// bytes -- while keeping bufio (and any flush-coverage risk) off the path
// every existing mode already uses. rg's own default block-buffers a pipe;
// gg's line-cadence default differs only in that unobservable cadence.
func resolveBlockBuffered(mode BufferMode) bool {
	return mode == BufferBlock
}
