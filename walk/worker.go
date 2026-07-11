package walk

import (
	"io/fs"
	"os"
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/yackey-labs/gripgrep/glob"
)

// coordinator holds the state shared by every worker in one Walk call:
// the active-worker count that drives quiescence detection, a quit flag
// that gives Quit-from-Visitor immediate effect across every worker, and
// the condition variable an idle worker parks on instead of polling.
//
// active/quit stay plain atomics for their hot-path reads (every
// iteration of every worker's loop checks quit.Load(), and active.Add is
// the per-transition fast path) — only pushGen and the writes to quit
// that matter for parking go through mu, exactly the state park's
// check-then-Wait loop depends on. See notifyWork/requestQuit's docs for
// why both must hold mu across the mutation: sync.Cond only wakes
// goroutines already parked in Wait when Broadcast is called, so any
// state change a parked worker's wake condition depends on must happen
// while excluding a waiter from concurrently deciding to park in the
// first place, or the wakeup is silently lost.
type coordinator struct {
	active atomic.Int64
	quit   atomic.Bool

	mu      sync.Mutex
	cond    *sync.Cond
	pushGen int64 // bumped under mu by notifyWork every time work is pushed
}

func newCoordinator() *coordinator {
	c := &coordinator{}
	c.cond = sync.NewCond(&c.mu)
	return c
}

// notifyWork wakes any parked worker to recheck for work, called after
// every push to any queue (a new subdirectory enqueued, or the walk's
// initial seeding). Broadcast (not Signal) because any parked worker
// might be the one able to steal the new task, not just one in
// particular.
func (c *coordinator) notifyWork() {
	c.mu.Lock()
	c.pushGen++
	c.mu.Unlock()
	c.cond.Broadcast()
}

// requestQuit signals every worker to stop and wakes any currently
// parked one immediately, rather than leaving it blocked until some
// unrelated notifyWork happens to fire (which, at true quiescence, never
// will).
func (c *coordinator) requestQuit() {
	c.mu.Lock()
	c.quit.Store(true)
	c.mu.Unlock()
	c.cond.Broadcast()
}

// worker owns one queue slot and runs a pop/steal/process loop until
// every worker's queue is simultaneously empty (quiescence) or a
// Visitor returns Quit anywhere.
type worker struct {
	idx    int
	queues []*dirQueue
	coord  *coordinator
	opts   *Options
	visit  Visitor

	// Scratch buffers reused across every entry this worker visits (no
	// per-entry filepath.Join, no per-entry allocation for the common
	// non-directory case — see joinPath/bufString).
	pbuf []byte
	abuf []byte

	// entry is reused across every Visitor call this worker makes (see
	// doVisit): its address is stable for the worker's whole lifetime, so
	// passing &w.entry doesn't allocate per visit the way a fresh `Entry{}`
	// composite literal would (its address would otherwise escape through
	// the opaque Visitor func value on every single call). Entry's own
	// doc already requires this: valid only during the call.
	entry Entry
}

// doVisit populates the worker's single reused Entry and calls Visitor.
func (w *worker) doVisit(path string, typ FileType, depth int, err error) WalkState {
	w.entry.Path = path
	w.entry.Type = typ
	w.entry.Depth = depth
	w.entry.Err = err
	return w.visit(&w.entry)
}

// run is the worker's main loop: pop local work, else steal, else
// participate in quiescence detection.
func (w *worker) run() {
	q := w.queues[w.idx]
	for {
		if w.coord.quit.Load() {
			return
		}
		t, ok := q.pop()
		if !ok {
			t, ok = w.steal()
		}
		if !ok {
			if w.coord.active.Add(-1) == 0 {
				// Every worker was simultaneously between its own
				// decrement and re-increment, i.e. every queue was
				// empty at once with nobody mid-processing: done.
				w.coord.requestQuit()
				return
			}
			t, ok = w.park()
			if !ok {
				return
			}
		}
		if w.processDir(t) {
			w.coord.requestQuit()
			return
		}
	}
}

// park blocks the calling (already-decremented-active) worker until
// either work becomes available to try again or the walk is ending, and
// reports which. It loops through spurious wakeups and lost races
// (another worker grabbing the same task first) exactly as the spin-poll
// loop this replaced did, but via a real condition variable instead of a
// fixed 1ms nanosleep -- rg parks via futex and only ever blocks/wakes a
// couple dozen times per run; the polling version this replaced measured
// in the tens of thousands of nanosleep calls on the linux kernel tree
// (M3 #24), almost all of them pure waste once the walk's tail is down
// to a handful of long-running directories and everyone else is idle.
//
// Reactivates (bumping active back up) before returning true, exactly
// like the loop it replaced, so no other worker's decrement can observe
// a false zero while this worker is about to produce more work.
func (w *worker) park() (t *dirTask, ok bool) {
	c := w.coord
	q := w.queues[w.idx]
	for {
		c.mu.Lock()
		myGen := c.pushGen
		for !c.quit.Load() && c.pushGen == myGen {
			c.cond.Wait()
		}
		c.mu.Unlock()
		if c.quit.Load() {
			return nil, false
		}
		if t, ok = q.pop(); ok {
			break
		}
		if t, ok = w.steal(); ok {
			break
		}
		// Woken (a push happened somewhere), but this worker lost the
		// race for it -- park again.
	}
	c.active.Add(1)
	return t, true
}

// steal tries every other worker's queue, starting at idx+1 and
// wrapping, for fairness (matches rg's steal order).
func (w *worker) steal() (*dirTask, bool) {
	n := len(w.queues)
	my := w.queues[w.idx]
	for i := 1; i < n; i++ {
		j := (w.idx + i) % n
		if w.queues[j] == my {
			continue
		}
		if w.queues[j].stealBatch(my) {
			return my.pop()
		}
	}
	return nil, false
}

// processDir reads one directory, applies the fast-rejection chain to
// each entry, visits files/symlinks inline, and enqueues subdirectories
// for later (possibly stolen) processing. Returns true if the whole walk
// should stop (a Visitor returned Quit).
func (w *worker) processDir(t *dirTask) bool {
	if t.depth == 0 {
		switch t.rootKind {
		case rootInvalid:
			return w.doVisit(t.path, TypeUnknown, 0, t.rootErr) == Quit
		case rootFile:
			return w.doVisit(t.path, TypeFile, 0, nil) == Quit
		}
		st := w.doVisit(t.path, TypeDir, 0, nil)
		if st == Quit {
			return true
		}
		if st == SkipDir {
			return false
		}
	}

	f, err := os.Open(t.abs)
	if err != nil {
		return w.doVisit(t.path, TypeDir, t.depth, err) == Quit
	}
	// Unsorted: File.ReadDir (not the package-level os.ReadDir, which
	// sorts) returns entries in raw readdir(3) order.
	entries, _ := f.ReadDir(-1)
	f.Close()

	var node *ignoreNode
	if !w.opts.NoIgnore {
		// Single pass over the directory listing already in hand (from
		// f.ReadDir(-1) above) checks membership for all three ignore-
		// related names at once, instead of buildNode blindly attempting
		// to open .ignore/.gitignore and discarding the ENOENT most
		// directories produce (M3 #24).
		var hasGit, hasIgnore, hasGitignore bool
		for _, d := range entries {
			switch d.Name() {
			case ".git":
				hasGit = true
			case ".ignore":
				hasIgnore = true
			case ".gitignore":
				hasGitignore = true
			}
		}
		node = buildNode(t.ignore, t.abs, hasGit, hasIgnore, hasGitignore)
	}

	var symAnc *symNode
	if w.opts.FollowSymlinks {
		symAnc = pushSymAncestor(t.symAncestors, t.abs)
	}

	q := w.queues[w.idx]
	// single is true for -j1 (exactly one queue exists -- see walk.go's
	// queues := make([]*dirQueue, n)). With only one worker, nobody can
	// ever steal a deferred subdirectory task, so descending into it
	// immediately, right where it's encountered in the readdir loop,
	// reproduces rg's true per-entry recursive-descent order (each
	// subdirectory's entire subtree completes at its exact readdir
	// position, interleaved with sibling files -- verified empirically,
	// round #38) for free: no buffering or push-order reversal needed,
	// and the dirQueue push/pop is skipped entirely for this path. n>1
	// (default parallelism) is untouched -- it keeps deferring to the
	// queue exactly as before, since there is no -j1-style ordering
	// contract for parallel mode (see dirQueue's doc). Computed once per
	// processDir call, not per entry.
	single := len(w.queues) == 1
	for _, d := range entries {
		name := d.Name()
		if name == "" {
			continue
		}
		childPath := joinPath(&w.pbuf, t.path, name)
		childAbs := joinPath(&w.abuf, t.abs, name)
		ftype := fileTypeOf(d)

		// Order matters here, confirmed against real rg behavior (see
		// TestOracleRgFiles): a whitelist verdict from Globs or the
		// ignore stack (e.g. a `!/.github/` line in .ignore) overrides
		// the hidden-file rule, exactly like rg's matched_dir_entry
		// (which only applies the hidden fallback when the ignore stack
		// produced no verdict at all). So hidden can't be the very first
		// check; it only applies once we know neither matcher explicitly
		// whitelisted this entry.
		skip, whitelisted := w.classify(node, childPath, childAbs, ftype == TypeDir)
		if skip {
			continue
		}
		if !whitelisted && name[0] == '.' && !w.opts.Hidden {
			continue
		}
		if w.opts.MaxDepth != nil && t.depth+1 > *w.opts.MaxDepth {
			// -d/--max-depth: this entry is one level too deep to visit or
			// (for a directory) descend into. Applies uniformly to every
			// entry type -- a pruned directory is never enqueued, so
			// nothing beneath it is reached either. See Options.MaxDepth's
			// doc for why a root itself (t.depth == 0, handled above this
			// loop) is never subject to this check.
			continue
		}

		switch ftype {
		case TypeDir:
			st := w.doVisit(bufString(childPath), TypeDir, t.depth+1, nil)
			if st == Quit {
				return true
			}
			if st == SkipDir {
				continue
			}
			child := &dirTask{
				path:         string(childPath),
				abs:          string(childAbs),
				depth:        t.depth + 1,
				ignore:       node,
				symAncestors: symAnc,
			}
			if single {
				if w.processDir(child) {
					return true
				}
				continue
			}
			q.push(child)

		case TypeSymlink:
			if w.opts.FollowSymlinks {
				if w.followSymlink(childPath, childAbs, t.depth+1, node, symAnc, q, single) {
					return true
				}
				continue
			}
			if w.doVisit(bufString(childPath), TypeSymlink, t.depth+1, nil) == Quit {
				return true
			}

		default: // TypeFile, TypeUnknown
			if w.opts.MaxFileSize > 0 {
				if info, err := d.Info(); err == nil && info.Size() > w.opts.MaxFileSize {
					continue
				}
			}
			if w.doVisit(bufString(childPath), ftype, t.depth+1, nil) == Quit {
				return true
			}
		}
	}
	// Wake any parked worker so it can steal whatever this directory just
	// enqueued (directly, or via followSymlink). Unconditional rather than
	// tracked-per-push: a Broadcast with no current waiters is a cheap
	// userspace check, not a syscall, so this costs nothing on the (far
	// more common) case where nobody happens to be parked right now --
	// see coordinator's doc for why the Quit-returning paths above don't
	// need their own call (requestQuit, from the caller, broadcasts too).
	w.coord.notifyWork()
	return false
}

// followSymlink resolves a symlink entry (stat-ing its target, since
// DirEntry.Type() can't see through it), loop-checks directories, and
// either enqueues (directory target) or visits inline (file target).
// childPath/childAbs are the worker's scratch buffers — still valid at
// entry, since no other joinPath call has happened since they were
// filled by the caller.
func (w *worker) followSymlink(childPath, childAbs []byte, depth int, node *ignoreNode, symAnc *symNode, q *dirQueue, single bool) (quit bool) {
	absStr := string(childAbs)
	pathStr := bufString(childPath)
	target, err := os.Stat(absStr)
	if err != nil {
		return w.doVisit(pathStr, TypeSymlink, depth, err) == Quit
	}
	if target.IsDir() {
		if loops(symAnc, target) {
			return false
		}
		st := w.doVisit(pathStr, TypeDir, depth, nil)
		if st == Quit {
			return true
		}
		if st == SkipDir {
			return false
		}
		child := &dirTask{path: string(childPath), abs: absStr, depth: depth, ignore: node, symAncestors: symAnc}
		if single {
			return w.processDir(child)
		}
		q.push(child)
		return false
	}
	if w.opts.MaxFileSize > 0 && target.Size() > w.opts.MaxFileSize {
		return false
	}
	return w.doVisit(pathStr, TypeFile, depth, nil) == Quit
}

// classify applies Options.Globs (highest precedence, whitelist-capable)
// then the ignore-matcher stack (unless NoIgnore), and reports whether
// the entry should be skipped and, separately, whether it was explicitly
// whitelisted — which the caller must treat as overriding the hidden-file
// rule (see the call site). globPath is the root-relative display path;
// ignorePath is the absolute path used for stack matching (see
// ignoreNode.matched).
func (w *worker) classify(node *ignoreNode, globPath, ignorePath []byte, isDir bool) (skip, whitelisted bool) {
	if w.opts.Globs != nil {
		switch w.opts.Globs.Match(globPath, isDir) {
		case glob.Ignored:
			return true, false
		case glob.Whitelisted:
			return false, true
		case glob.NoMatch:
			// GlobsRequireMatch's exclusion only applies to files, not
			// directories: a `-g '*.rs'` override is a filter over file
			// content, and a directory whose own name doesn't happen to
			// end in ".rs" (nearly all of them) must still be descended
			// into so the files inside it get their own chance to
			// match -- pruning here would silently exclude every file
			// below the first directory that fails the glob, which is
			// most of the tree in practice. Verified against the real
			// rg binary: `rg -g '*.rs' pat .` finds matches at every
			// depth, not just files directly under the walk root (see
			// M2's handoff notes / TestGlobsRequireMatchDoesNotPruneDirs).
			if w.opts.GlobsRequireMatch && !isDir {
				return true, false
			}
		}
	}
	if w.opts.NoIgnore || node == nil {
		return false, false
	}
	switch node.matched(ignorePath, isDir) {
	case glob.Ignored:
		return true, false
	case glob.Whitelisted:
		return false, true
	default:
		return false, false
	}
}

// fileTypeOf classifies a DirEntry using only the type bits readdir(3)
// already gave us — never a stat.
func fileTypeOf(d os.DirEntry) FileType {
	m := d.Type()
	switch {
	case m&fs.ModeSymlink != 0:
		return TypeSymlink
	case m.IsDir():
		return TypeDir
	case m.IsRegular():
		return TypeFile
	default:
		return TypeUnknown
	}
}

// joinPath appends dir+"/"+name into the reused scratch slice pointed to
// by buf and returns the result. It approximates filepath.Join for the
// already-clean inputs this walker produces itself, without Join's
// repeated Clean/allocate overhead -- except that, unlike filepath.Join,
// it never cleans away a literal "." component. filepath.Join(".", "x")
// collapses to "x", but a bare "." walk root is a real, common CLI
// invocation (`rg`/`gg` with no PATH argument defaults to searching
// "."), and the real rg binary echoes the "./" prefix verbatim in that
// case (verified: `rg -n pat .` prints "./crates/...", not "crates/...").
// Collapsing it here would make every discovered path diverge from rg's
// output the moment a search starts from the current directory -- easily
// gg's single most common invocation shape. See TestDotRootPreservesPrefix.
func joinPath(buf *[]byte, dir, name string) []byte {
	b := (*buf)[:0]
	if dir != "" {
		b = append(b, dir...)
		b = append(b, '/')
	}
	b = append(b, name...)
	*buf = b
	return b
}

// bufString views b as a string with no copy. Safe only for the
// duration of a single synchronous Visitor call on the buffer that
// produced b — the same "valid only during the call" contract Entry.Path
// documents — since the owning worker reuses the backing array on its
// very next joinPath call.
func bufString(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	return unsafe.String(&b[0], len(b))
}
