package printer

import (
	"io"
	"sync"
)

// Dest is a shared output destination that many worker Printers write
// to concurrently. Each Printer accumulates one file's entire output in
// a private buffer, then hands the finished buffer to Dest.Write, which
// performs exactly one Write call under a mutex — the only
// synchronization point in this package. This means per-file blocks are
// never torn or interleaved, and the lock is held only for the bulk
// copy, not for any formatting work.
type Dest struct {
	w  io.Writer
	mu sync.Mutex
}

// NewDest wraps w as a shared, lockable destination for one or more
// per-worker Printers.
func NewDest(w io.Writer) *Dest {
	return &Dest{w: w}
}

// Write performs one write of p under the lock. Empty writes are
// skipped entirely (no lock acquired) so that files with zero matches
// produce no output and no contention.
func (d *Dest) Write(p []byte) error {
	if len(p) == 0 {
		return nil
	}
	d.mu.Lock()
	_, err := d.w.Write(p)
	d.mu.Unlock()
	return err
}
