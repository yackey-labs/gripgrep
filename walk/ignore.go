package walk

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"

	"github.com/yackey-labs/gripgrep/glob"
)

// ignoreNode is one immutable link in a per-directory ignore-matcher
// chain. A node is built once (when its directory is first read) and
// shared by pointer with every descendant directory's own node, so a
// given .gitignore/.ignore/exclude file is parsed exactly once no matter
// how many descendants are searched.
//
// Matching walks innermost (the node itself) to outermost (via parent),
// per source, taking the first decisive (non-NoMatch) verdict for that
// source; sources are then combined with .ignore > .gitignore > exclude
// precedence. This mirrors crates/ignore/src/dir.rs's matched_ignore,
// restricted to the three sources this package supports (no custom
// ignore files, no global gitignore, no explicit in-process ignores).
type ignoreNode struct {
	parent *ignoreNode
	// dir is this node's directory in absolute form. Every descendant
	// path passed to matched is guaranteed to have dir as a prefix.
	dir string

	ignoreSet    *glob.Set // .ignore
	gitignoreSet *glob.Set // .gitignore
	excludeSet   *glob.Set // .git/info/exclude; only ever set on the node
	// that is itself the repo root (see loadGitExclude).

	// insideGitRepo is true if this directory or any ancestor at or below
	// the repo root contains a .git entry. Git-derived matchers
	// (gitignoreSet, excludeSet) are only consulted when true, matching
	// rg's "git ignores only apply inside a repository" behavior.
	//
	// Deviation from rg: rg re-resolves the nearest .git on every
	// directory (supporting nested worktrees/submodules with distinct
	// .git dirs). We resolve it once, at the directory that first has a
	// .git entry, and let descendants inherit it by pointer. This is
	// behaviorally equivalent for the common case (one repo root) and
	// cheaper; it will not find a *second*, nested repo's exclude file
	// (submodules) — out of v1 scope.
	insideGitRepo bool
}

// matched reports how the absolute path (a descendant of n's directory,
// or of one of its ancestors) matches the accumulated ignore stack.
// absPath is never converted to string: this is called once per walked
// entry and must not allocate in the steady state.
func (n *ignoreNode) matched(absPath []byte, isDir bool) glob.MatchResult {
	if n == nil {
		return glob.NoMatch
	}
	var mIgnore, mGitignore, mExclude glob.MatchResult
	for cur := n; cur != nil; cur = cur.parent {
		rel := absPath
		if len(cur.dir) > 0 && cur.dir != "/" {
			rel = absPath[len(cur.dir)+1:]
		}

		if mIgnore == glob.NoMatch && cur.ignoreSet != nil {
			mIgnore = cur.ignoreSet.Match(rel, isDir)
		}
		if cur.insideGitRepo {
			if mGitignore == glob.NoMatch && cur.gitignoreSet != nil {
				mGitignore = cur.gitignoreSet.Match(rel, isDir)
			}
			if mExclude == glob.NoMatch && cur.excludeSet != nil {
				mExclude = cur.excludeSet.Match(rel, isDir)
			}
		}
		if mIgnore != glob.NoMatch && mGitignore != glob.NoMatch && mExclude != glob.NoMatch {
			break
		}
	}
	// Precedence: .ignore > .gitignore > exclude (task-specified subset
	// of rg's fuller custom > ignore > gitignore > exclude > global).
	if mIgnore != glob.NoMatch {
		return mIgnore
	}
	if mGitignore != glob.NoMatch {
		return mGitignore
	}
	return mExclude
}

// buildNode compiles a new ignoreNode for dir (absolute path), chaining
// to parent. hasGit/hasIgnore/hasGitignore are whether dir itself
// directly contains a .git/.ignore/.gitignore entry; the caller supplies
// them because during a normal descent they can all be read for free off
// the directory listing already in hand (processDir's own
// entries, from the one f.ReadDir(-1) call it already made), rather than
// blindly attempting to open .ignore/.gitignore and discarding the
// ENOENT most directories produce (M3 #24: this was measured at roughly
// 10k wasted open+ENOENT syscalls walking the linux kernel tree, since
// only a small fraction of its ~10k directories carry either file).
func buildNode(parent *ignoreNode, dir string, hasGit, hasIgnore, hasGitignore bool) *ignoreNode {
	n := &ignoreNode{
		parent:        parent,
		dir:           dir,
		insideGitRepo: hasGit || (parent != nil && parent.insideGitRepo),
	}
	if hasIgnore {
		n.ignoreSet = loadGlobSet(filepath.Join(dir, ".ignore"))
	}
	if hasGitignore {
		n.gitignoreSet = loadGlobSet(filepath.Join(dir, ".gitignore"))
	}
	if hasGit {
		n.excludeSet = loadGitExclude(dir)
	}
	return n
}

// buildParentChain loads the .gitignore/.ignore files of directories
// above absRoot, up to and including the first ancestor that is itself a
// repository root (or the filesystem root, if none is found), once at
// walk start. This lets e.g. a repo-root .gitignore still apply when the
// walk root is a subdirectory of the repo.
//
// Deviation from rg: rg walks all the way to the filesystem root
// regardless of repo boundaries, applying only non-git ignore files
// above the repo root. We stop ascending at the repo root (inclusive)
// since that is what matters for the overwhelmingly common case and
// avoids stat-ing arbitrarily many ancestors (e.g. under /home) for no
// behavioral gain in v1.
//
// If absRoot itself already has a .git marker, it IS a repository root,
// and no ancestor climbing happens at all -- a real, found bug (not a
// hypothetical): a nested git repo used as a walk root (e.g. a vendored
// corpus checked out under a project that has its own outer .gitignore)
// used to have the OUTER repo's .gitignore rules leak in regardless,
// since the old code unconditionally started climbing from
// filepath.Dir(absRoot) without ever checking whether absRoot was
// already a boundary. Concretely: walking benchmark-data/linux (its own
// nested git repo) picked up this project's own top-level .gitignore
// rule `*.exe`, wrongly excluding a real, tracked Linux kernel test
// fixture (tools/perf/tests/pe-file.exe) that real rg (and `git
// check-ignore`) correctly does not exclude. --files (M3 #25) is what
// exposed this: it was the first exhaustive, unfiltered full-listing
// comparison against real rg on that corpus.
func buildParentChain(absRoot string) *ignoreNode {
	if hasGitMarker(absRoot) {
		return nil
	}
	dir := filepath.Dir(absRoot)
	if dir == absRoot {
		return nil
	}
	var dirs []string
	cur := dir
	for {
		dirs = append(dirs, cur)
		if hasGitMarker(cur) {
			break
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		cur = parent
	}
	// dirs is innermost-first; build outermost-first so parent pointers
	// chain correctly.
	for i, j := 0, len(dirs)-1; i < j; i, j = i+1, j-1 {
		dirs[i], dirs[j] = dirs[j], dirs[i]
	}
	var node *ignoreNode
	for _, d := range dirs {
		// Unlike processDir's per-directory descent, there's no
		// already-read directory listing here to check membership
		// against (this climbs a handful of ancestors once per walk, not
		// a hot path) -- attempt both unconditionally, exactly as before
		// this optimization existed.
		node = buildNode(node, d, hasGitMarker(d), true, true)
	}
	return node
}

func hasGitMarker(dir string) bool {
	_, err := os.Lstat(filepath.Join(dir, ".git"))
	return err == nil
}

var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// loadGlobSet reads path as a gitignore-syntax pattern file and compiles
// it into a glob.Set. Returns nil if the file doesn't exist, can't be
// read, or has no usable patterns — callers treat a nil set as "matches
// nothing", so this is safe to skip on any error (mirrors rg's general
// policy of silently ignoring I/O errors on ignore files).
func loadGlobSet(path string) *glob.Set {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	data = bytes.TrimPrefix(data, utf8BOM)

	var b glob.Builder
	any := false
	for _, line := range bytes.Split(data, []byte("\n")) {
		line = bytes.TrimRight(line, "\r")
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 || trimmed[0] == '#' {
			continue
		}
		b.Add(string(trimmed))
		any = true
	}
	if !any {
		return nil
	}
	set, err := b.Build()
	if err != nil || set == nil {
		return nil
	}
	return set
}

// loadGitExclude resolves dir's .git entry (a directory for a normal
// repo, or a "gitdir: <path>" pointer file for a linked worktree) and
// loads its info/exclude file. Only called for a directory that is
// itself a repo root (hasGit == true for that node).
func loadGitExclude(dir string) *glob.Set {
	gitPath := filepath.Join(dir, ".git")
	fi, err := os.Lstat(gitPath)
	if err != nil {
		return nil
	}
	gitDir := gitPath
	if !fi.IsDir() {
		data, err := os.ReadFile(gitPath)
		if err != nil {
			return nil
		}
		line := strings.TrimSpace(string(data))
		const prefix = "gitdir:"
		if !strings.HasPrefix(line, prefix) {
			return nil
		}
		target := strings.TrimSpace(line[len(prefix):])
		if !filepath.IsAbs(target) {
			target = filepath.Join(dir, target)
		}
		gitDir = target
	}
	return loadGlobSet(filepath.Join(gitDir, "info", "exclude"))
}
