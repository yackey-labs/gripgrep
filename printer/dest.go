package printer

import (
	"io"
	"sync"
)

// Dest is a shared output destination that many worker Printers write
// to concurrently. Each Printer accumulates one file's entire output in
// a private buffer, then hands the finished buffer to Dest.Write (or
// WriteBlock), which performs its write(s) under a mutex — the only
// synchronization point in this package. This means per-file blocks are
// never torn or interleaved, and the lock is held only for the bulk
// copy, not for any formatting work.
type Dest struct {
	w  io.Writer
	mu sync.Mutex
	// hasPrinted tracks whether any block has been written yet via
	// WriteBlock, so the first block never gets a leading separator
	// and every subsequent one does — see WriteBlock.
	hasPrinted bool
}

// NewDest wraps w as a shared, lockable destination for one or more
// per-worker Printers.
func NewDest(w io.Writer) *Dest {
	return &Dest{w: w}
}

// Write performs one write of p under the lock. Empty writes are
// skipped entirely (no lock acquired) so that files with zero matches
// produce no output and no contention. Write never touches the
// hasPrinted state WriteBlock uses — the two are for different sink
// kinds (Count/FilesWithMatches/PathPrinter vs. Standard) that are
// never mixed on one Dest in practice.
func (d *Dest) Write(p []byte) error {
	if len(p) == 0 {
		return nil
	}
	d.mu.Lock()
	_, err := d.w.Write(p)
	d.mu.Unlock()
	return err
}

// WriteBlock writes one file's block, preceded by sep if (and only if)
// a previous non-empty block has already been written to this Dest.
// This mirrors rg's own shared BufferWriter: separator placement
// follows actual flush-completion order under the same lock that
// serializes the writes themselves, so it's exactly as deterministic
// (or not) as rg's own parallel output — never a leading separator
// before the first block, never a trailing one after the last,
// regardless of which worker happens to finish first. A nil or empty
// sep behaves like Write: no separator logic at all. An empty block is
// skipped entirely (no write, no separator, no state change), matching
// Write's zero-match-file behavior.
func (d *Dest) WriteBlock(block, sep []byte) error {
	if len(block) == 0 {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.hasPrinted && len(sep) > 0 {
		if _, err := d.w.Write(sep); err != nil {
			return err
		}
	}
	_, err := d.w.Write(block)
	d.hasPrinted = true
	return err
}
