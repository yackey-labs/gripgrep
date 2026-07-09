package walk

import "sync"

// dirTask is one unit of work: a directory to be read and expanded. Files
// are never enqueued (see package doc); only directories become tasks.
type dirTask struct {
	// path is the entry's path in the "root-relative join" form handed to
	// the Visitor (see Entry.Path doc).
	path string
	// abs is the same directory in absolute form, used for ignore-stack
	// matching, .git detection, and symlink stat calls. Kept separate from
	// path so the caller-facing path format is unaffected by how we do
	// internal bookkeeping.
	abs string
	// depth is the number of directory levels below the walk root (root
	// tasks are depth 0).
	depth int
	// ignore is the immutable ignore-matcher-stack node for this
	// directory's *parent* (i.e. what should be consulted, together with
	// entries read from this directory itself, to build this directory's
	// own node). Nil when ignore processing is disabled.
	ignore *ignoreNode
	// symAncestors is the device/inode chain of directories above this one
	// reached during the walk, used for symlink loop detection. Only
	// populated when Options.FollowSymlinks is set.
	symAncestors *symNode

	// The following fields are only meaningful for depth == 0 (root)
	// tasks, which unlike every other task have not yet been classified
	// or visited by a parent's directory scan.
	rootKind rootKind
	rootErr  error
}

type rootKind uint8

const (
	rootDir rootKind = iota
	rootFile
	rootInvalid
)

// dirQueue is a mutex-guarded per-worker LIFO deque of dirTasks. It is not
// lock-free: at the thread counts this walker targets (single digits to
// low tens), a mutex is simpler and just as fast as a lock-free deque, and
// it stays trivially provable under -race.
//
// The owning worker pushes and pops from the tail (LIFO, i.e.
// depth-first). Other workers steal a batch from the head (the oldest
// entries, which correspond to the shallowest/fattest un-expanded
// subtrees) when their own deque runs dry.
type dirQueue struct {
	mu    sync.Mutex
	items []*dirTask
}

// push adds a task to the tail. Called by the owning worker.
func (q *dirQueue) push(t *dirTask) {
	q.mu.Lock()
	q.items = append(q.items, t)
	q.mu.Unlock()
}

// pop removes and returns the tail task, if any. Called by the owning
// worker.
func (q *dirQueue) pop() (*dirTask, bool) {
	q.mu.Lock()
	n := len(q.items)
	if n == 0 {
		q.mu.Unlock()
		return nil, false
	}
	t := q.items[n-1]
	q.items[n-1] = nil
	q.items = q.items[:n-1]
	q.mu.Unlock()
	return t, true
}

// stealBatch moves roughly half of q's items (from the head) into dst's
// tail, returning true if anything was moved. The two locks are never
// held simultaneously, avoiding any lock-ordering concern.
func (q *dirQueue) stealBatch(dst *dirQueue) bool {
	q.mu.Lock()
	n := len(q.items)
	if n == 0 {
		q.mu.Unlock()
		return false
	}
	take := (n + 1) / 2
	stolen := make([]*dirTask, take)
	copy(stolen, q.items[:take])
	copy(q.items, q.items[take:])
	for i := n - take; i < n; i++ {
		q.items[i] = nil
	}
	q.items = q.items[:n-take]
	q.mu.Unlock()

	dst.mu.Lock()
	dst.items = append(dst.items, stolen...)
	dst.mu.Unlock()
	return true
}
