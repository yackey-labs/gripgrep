package printer

import (
	"io"

	"github.com/yackey-labs/gripgrep/search"
)

// Standard is the default per-match printer: "path:line:text", optionally
// colored, mirroring rg's Standard printer. It owns a per-instance
// output buffer that callers should treat as single-file-at-a-time (one
// Standard per worker goroutine when searching in parallel).
type Standard struct {
	W io.Writer
	// TODO(M1-printer): color config, per-worker []byte buffer, column /
	// --only-matching support, atomic flush-per-file protocol.
}

// NewStandard returns a Standard printer writing to w.
func NewStandard(w io.Writer) *Standard {
	return &Standard{W: w}
}

var _ search.Sink = (*Standard)(nil)

// Begin implements search.Sink.
//
// TODO(M1-printer): reset/acquire the per-file output buffer.
func (p *Standard) Begin(path string) (bool, error) {
	return true, nil
}

// Matched implements search.Sink.
//
// TODO(M1-printer): append-format "path:line:text" (path.AppendUint for
// numbers, no fmt) into the buffer; use m.Line only within this call per
// its documented validity contract.
func (p *Standard) Matched(m *search.Match) (bool, error) {
	return true, nil
}

// Context implements search.Sink.
//
// TODO(M1-printer): append-format context lines with "-" separator per
// rg convention; c.Line only valid within this call.
func (p *Standard) Context(c *search.Ctx) (bool, error) {
	return true, nil
}

// Finish implements search.Sink.
//
// TODO(M1-printer): flush the accumulated per-file buffer to W as one
// locked write.
func (p *Standard) Finish(path string, stats *search.Stats) error {
	return nil
}

// Summary implements -c (count) and -l/-L (files/files-without-match)
// modes: no per-match formatting, just counting and a final line per
// matching file.
type Summary struct {
	W io.Writer
	// CountOnly selects -c behavior (print "path:count").
	CountOnly bool
	// FilesWithMatches selects -l behavior (print "path" once per
	// matching file, no count).
	FilesWithMatches bool
}

// NewSummary returns a Summary printer writing to w.
func NewSummary(w io.Writer) *Summary {
	return &Summary{W: w}
}

var _ search.Sink = (*Summary)(nil)

// Begin implements search.Sink.
func (p *Summary) Begin(path string) (bool, error) {
	return true, nil
}

// Matched implements search.Sink.
//
// TODO(M1-printer): increment a counter only; never format the line
// (per the "count/quiet modes never format lines" design decision).
func (p *Summary) Matched(m *search.Match) (bool, error) {
	return true, nil
}

// Context implements search.Sink. Summary mode never requests context,
// but the method must exist to satisfy search.Sink.
func (p *Summary) Context(c *search.Ctx) (bool, error) {
	return true, nil
}

// Finish implements search.Sink.
//
// TODO(M1-printer): write "path:count\n" (CountOnly) or "path\n"
// (FilesWithMatches) when stats.Matched, using strconv.AppendInt — no fmt.
func (p *Summary) Finish(path string, stats *search.Stats) error {
	return nil
}
