package walk

import (
	"io/fs"
	"os"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/yackey-labs/gripgrep/glob"
)

// coordinator holds the state shared by every worker in one Walk call:
// the active-worker count that drives quiescence detection, and a quit
// flag that gives Quit-from-Visitor immediate (best-effort, ~1ms-latency)
// effect across every worker.
type coordinator struct {
	active atomic.Int64
	quit   atomic.Bool
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
				w.coord.quit.Store(true)
				return
			}
			for {
				if w.coord.quit.Load() {
					return
				}
				time.Sleep(time.Millisecond)
				t, ok = q.pop()
				if !ok {
					t, ok = w.steal()
				}
				if ok {
					// Reactivate before processing so no other
					// worker's decrement can observe a false 0 while
					// we're about to produce more work.
					w.coord.active.Add(1)
					break
				}
			}
		}
		if w.processDir(t) {
			w.coord.quit.Store(true)
			return
		}
	}
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
		hasGit := false
		for _, d := range entries {
			if d.Name() == ".git" {
				hasGit = true
				break
			}
		}
		node = buildNode(t.ignore, t.abs, hasGit)
	}

	var symAnc *symNode
	if w.opts.FollowSymlinks {
		symAnc = pushSymAncestor(t.symAncestors, t.abs)
	}

	q := w.queues[w.idx]
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

		switch ftype {
		case TypeDir:
			st := w.doVisit(bufString(childPath), TypeDir, t.depth+1, nil)
			if st == Quit {
				return true
			}
			if st == SkipDir {
				continue
			}
			q.push(&dirTask{
				path:         string(childPath),
				abs:          string(childAbs),
				depth:        t.depth + 1,
				ignore:       node,
				symAncestors: symAnc,
			})

		case TypeSymlink:
			if w.opts.FollowSymlinks {
				if w.followSymlink(childPath, childAbs, t.depth+1, node, symAnc, q) {
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
	return false
}

// followSymlink resolves a symlink entry (stat-ing its target, since
// DirEntry.Type() can't see through it), loop-checks directories, and
// either enqueues (directory target) or visits inline (file target).
// childPath/childAbs are the worker's scratch buffers — still valid at
// entry, since no other joinPath call has happened since they were
// filled by the caller.
func (w *worker) followSymlink(childPath, childAbs []byte, depth int, node *ignoreNode, symAnc *symNode, q *dirQueue) (quit bool) {
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
		q.push(&dirTask{path: string(childPath), abs: absStr, depth: depth, ignore: node, symAncestors: symAnc})
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
			if w.opts.GlobsRequireMatch {
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
// repeated Clean/allocate overhead.
func joinPath(buf *[]byte, dir, name string) []byte {
	b := (*buf)[:0]
	if dir != "" && dir != "." {
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
