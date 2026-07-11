package walk

import (
	"os"
	"slices"
	"strings"
	"time"
)

// sortItem is one buffered file awaiting emission in sorted order. The
// display path is copied out of the transient Entry.Path view (which is
// only valid for the duration of a Visitor call); depth is retained so the
// reconstructed Entry handed to the caller's Visitor still reports whether
// the file was an explicit root argument (Depth == 0), which drives
// explicit-file binary/EOF handling downstream. modtime/hasTime hold the
// stat result for time-keyed sorts (unset for path sorts).
type sortItem struct {
	path    string
	depth   int
	modtime time.Time
	hasTime bool
}

// walkSorted implements Options.Sort. It forces single-threaded traversal
// and buffers files, reproducing rg's two distinct sort mechanisms:
//
//   - Ascending path sort is applied PER ROOT, in argument order, with a
//     component-wise ordering within each root. This mirrors rg's
//     during-traversal sort_by_file_name (walk_builder), which sorts each
//     directory's entries by name as it descends -- so a root's whole
//     subtree is emitted, sorted, before the next root's begins.
//   - Every other sort (descending path, modified, accessed) collects the
//     files from ALL roots into one buffer and sorts globally, mirroring
//     rg's collect-then-sort path (HiArgs::sort). Multi-root output is
//     therefore interleaved by key, not grouped by root.
//
// Both mechanisms preserve gg's per-root readdir discovery order in the
// buffer (single worker, raw f.ReadDir), so a stable sort keeps equal-key
// files in the exact order rg's own single-threaded traversal discovered
// them. Duplicate paths reached via overlapping roots are never deduped.
func walkSorted(roots []string, opts *Options, visit Visitor) error {
	if opts.Sort.Kind == SortPath && !opts.Sort.Reverse {
		for _, root := range roots {
			items, quit := collectRoot(root, opts, visit)
			if quit {
				return nil
			}
			sortByPath(items, false)
			if emitSorted(items, visit) {
				return nil
			}
		}
		return nil
	}

	var all []sortItem
	for _, root := range roots {
		items, quit := collectRoot(root, opts, visit)
		if quit {
			return nil
		}
		all = append(all, items...)
	}
	if opts.Sort.Kind == SortPath {
		sortByPath(all, opts.Sort.Reverse)
	} else {
		sortByTime(all, opts.Sort.Reverse)
	}
	emitSorted(all, visit)
	return nil
}

// collectRoot runs a single-threaded traversal of one root, buffering every
// discovered regular file (with its stat time, for time-keyed sorts) in
// readdir order. Non-file entries are dropped (directories recurse
// internally; they produce no output). Walk errors are forwarded to visit
// as they occur -- their stderr order is not contractual, and they never
// participate in the sort -- so a Quit returned for one aborts the walk,
// reported back as quit.
func collectRoot(root string, opts *Options, visit Visitor) (items []sortItem, quit bool) {
	needTime := opts.Sort.Kind == SortModified || opts.Sort.Kind == SortAccessed
	collector := func(e *Entry) WalkState {
		if e.Err != nil {
			if visit(e) == Quit {
				quit = true
				return Quit
			}
			return Continue
		}
		if e.Type != TypeFile {
			return Continue
		}
		it := sortItem{path: strings.Clone(e.Path), depth: e.Depth}
		if needTime {
			it.modtime, it.hasTime = statTime(e.Path, opts.Sort.Kind)
		}
		items = append(items, it)
		return Continue
	}
	runWorkers([]string{root}, opts, collector, 1)
	return items, quit
}

// emitSorted replays buffered files through visit in order, reconstructing
// each Entry (always a regular file; Depth preserved). It returns true if
// visit asked to Quit (e.g. -q found its match, or -m/early-stop), so the
// caller stops without touching any remaining roots.
func emitSorted(items []sortItem, visit Visitor) (quit bool) {
	for i := range items {
		e := Entry{Path: items[i].path, Type: TypeFile, Depth: items[i].depth}
		if visit(&e) == Quit {
			return true
		}
	}
	return false
}

// sortByPath stably orders items by comparePath, negating the comparator
// (not reversing the slice) for descending order. Display paths are unique
// so ties never arise here; stability is kept for uniformity with
// sortByTime.
func sortByPath(items []sortItem, reverse bool) {
	slices.SortStableFunc(items, func(a, b sortItem) int {
		c := comparePath(a.path, b.path)
		if reverse {
			c = -c
		}
		return c
	})
}

// sortByTime stably orders items by compareTime, negating the comparator
// for descending order so equal timestamps keep discovery order in BOTH
// directions (rg parity: a stable sort with a reversed comparator, never
// sort-then-reverse).
func sortByTime(items []sortItem, reverse bool) {
	slices.SortStableFunc(items, func(a, b sortItem) int {
		c := compareTime(a, b)
		if reverse {
			c = -c
		}
		return c
	})
}

// comparePath compares two displayed paths component-wise, mirroring Rust's
// Path ordering (which rg's --sort/--sortr path uses). Each '/'-separated
// component is compared as raw bytes; when components are equal but one
// path has more to come, the shorter (fewer-component) path sorts first.
// This is deliberately NOT a byte-wise comparison of the whole string: at a
// component boundary the '/' is not compared as byte 0x2F, so a directory
// "b" (in "b/z.txt") sorts before the file "b.txt" even though '/' > '.'.
func comparePath(a, b string) int {
	for {
		ai := strings.IndexByte(a, '/')
		bi := strings.IndexByte(b, '/')
		aHasSep := ai >= 0
		bHasSep := bi >= 0

		var ca, cb string
		if aHasSep {
			ca = a[:ai]
		} else {
			ca = a
		}
		if bHasSep {
			cb = b[:bi]
		} else {
			cb = b
		}
		if c := strings.Compare(ca, cb); c != 0 {
			return c
		}
		// Leading components tie. If exactly one path continues past this
		// component, it is the longer (deeper) path and sorts after the
		// one that ends here.
		if aHasSep != bHasSep {
			if aHasSep {
				return 1
			}
			return -1
		}
		if !aHasSep {
			// Both ended here: identical paths.
			return 0
		}
		a, b = a[ai+1:], b[bi+1:]
	}
}

// compareTime implements rg's timestamp ordering rule: files whose stat
// failed (hasTime false) sort AFTER files with a time when ascending
// (rg treats a missing time as greater), and two missing times compare
// equal. The reversal for --sortr is applied by the caller negating the
// result, so this always describes the ascending relation.
func compareTime(a, b sortItem) int {
	switch {
	case a.hasTime && b.hasTime:
		return a.modtime.Compare(b.modtime)
	case a.hasTime: // b missing -> a before b
		return -1
	case b.hasTime: // a missing -> a after b
		return 1
	default:
		return 0
	}
}

// statTime stats path (FOLLOWING symlinks -- rg reads Path::metadata(), so
// a symlink is keyed by its target's time, not the link's) and extracts the
// requested timestamp. A stat failure returns hasTime false, which
// compareTime orders last in ascending order. Only ever called for the
// time-keyed kinds.
func statTime(path string, kind SortKind) (t time.Time, ok bool) {
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}, false
	}
	if kind == SortAccessed {
		if at, ok := accessTime(info); ok {
			return at, true
		}
		return time.Time{}, false
	}
	return info.ModTime(), true
}
