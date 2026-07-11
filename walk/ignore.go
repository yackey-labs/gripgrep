package walk

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"

	"github.com/grafana/regexp"

	"github.com/yackey-labs/gripgrep/glob"
)

// ignoreNode is one immutable link in a per-directory ignore-matcher
// chain. A node is built once (when its directory is first read) and
// shared by pointer with every descendant directory's own node, so a
// given .gitignore/.ignore/.rgignore/exclude file is parsed exactly once
// no matter how many descendants are searched.
//
// Matching walks innermost (the node itself) to outermost (via parent),
// per source, taking the first decisive (non-NoMatch) verdict for that
// source; sources are then combined with .rgignore > .ignore > .gitignore
// > exclude precedence, then the per-walk global and explicit matchers
// (see ignoreCtx.matched). This mirrors crates/ignore/src/dir.rs's
// matched_ignore.
type ignoreNode struct {
	parent *ignoreNode
	// dir is this node's directory in absolute form. Every descendant
	// path passed to matched is guaranteed to have dir as a prefix.
	dir string

	customSet    *glob.Set // .rgignore (custom per-directory ignore)
	ignoreSet    *glob.Set // .ignore
	gitignoreSet *glob.Set // .gitignore
	excludeSet   *glob.Set // .git/info/exclude; only ever set on a node
	// that itself directly contains a .git entry (see loadGitExclude).

	// hasGitSelf is the "effective" git marker for this directory: it has
	// a .git or .jj entry AND require-git is in force (NoRequireGit false).
	// Folding NoRequireGit in here reproduces rg's rule that has_git is
	// only ever computed when require_git is true (crates/ignore/src/dir.rs):
	// under --no-require-git it stays false everywhere, so the saw_git
	// ascent never trips and outer .gitignore files above a repo root
	// still apply (probe C6).
	hasGitSelf bool

	// insideGitRepo is true if this directory or ANY ancestor up to the
	// filesystem root has hasGitSelf. It gates the git-derived and global
	// matchers (rg's any_git): git ignores only apply inside a repository
	// (probes C1/E4), unless --no-require-git forces the gate open
	// regardless (folded into hasGitSelf above, so anyGit is computed as
	// NoRequireGit || insideGitRepo in matched).
	insideGitRepo bool
}

// ignoreCtx holds the per-walk ignore state that is NOT per-directory:
// the resolved global gitignore matcher, the combined explicit
// (--ignore-file) matcher, and the sub-flags that gate every source. It
// is built once at Walk start and shared by pointer with every worker, so
// matched stays allocation-free on the per-entry hot path.
type ignoreCtx struct {
	noIgnoreDot     bool // kills .ignore + .rgignore
	noIgnoreVcs     bool // kills .gitignore + exclude + global
	noIgnoreExclude bool // kills .git/info/exclude
	noRequireGit    bool // git/global matchers apply even outside a repo

	// cwd is the process working directory (absolute), used to re-anchor
	// the global and explicit matchers: rg compiles both with root = CWD
	// (walk.rs add_ignore / dir.rs build_with_cwd) and its Gitignore strips
	// that root prefix off an absolute candidate path before matching. An
	// absolute walk-root arg produces absolute display paths, so without
	// this a CWD-anchored pattern like `/anch/az.txt` would never line up
	// (probe B12). Empty disables the re-anchor. See anchorToCwd.
	cwd string

	// globalSet is the resolved global gitignore (core.excludesFile / XDG
	// default), already nil unless it should apply (killed by
	// --no-ignore-vcs/--no-ignore-global at the engine boundary before it
	// reaches here). Matched against the CWD-anchored display path, gated
	// per-entry by anyGit.
	globalSet *glob.Set
	// explicitSet is every --ignore-file's patterns compiled into one Set
	// in argument order (last-match-wins across the concatenation is
	// equivalent to rg's reverse-iterate-first-decisive across the
	// separate files -- probes B2/B3). Nil unless --ignore-file was given
	// and --no-ignore-files did not kill it. Matched against the display
	// path; never git-gated.
	explicitSet *glob.Set
}

// matched reports how the path matches the accumulated ignore stack.
// absPath (absolute) drives the per-directory tree sources, which strip
// each node's dir prefix to match relative to that ignore file's own
// directory; globPath (the CWD-relative display path) drives the global
// and explicit matchers, which rg anchors at CWD. Neither is converted to
// string: this is called once per walked entry and must not allocate in
// the steady state.
func (n *ignoreNode) matched(absPath, globPath []byte, isDir bool, c *ignoreCtx) glob.MatchResult {
	anyGit := c.noRequireGit || (n != nil && n.insideGitRepo)

	var mCustom, mIgnore, mGit, mExclude glob.MatchResult
	sawGit := false
	for cur := n; cur != nil; cur = cur.parent {
		rel := absPath
		if len(cur.dir) > 0 && cur.dir != "/" {
			rel = absPath[len(cur.dir)+1:]
		}

		if !c.noIgnoreDot {
			if mCustom == glob.NoMatch && cur.customSet != nil {
				mCustom = cur.customSet.Match(rel, isDir)
			}
			if mIgnore == glob.NoMatch && cur.ignoreSet != nil {
				mIgnore = cur.ignoreSet.Match(rel, isDir)
			}
		}
		if anyGit && !sawGit && !c.noIgnoreVcs {
			if mGit == glob.NoMatch && cur.gitignoreSet != nil {
				mGit = cur.gitignoreSet.Match(rel, isDir)
			}
			if !c.noIgnoreExclude && mExclude == glob.NoMatch && cur.excludeSet != nil {
				mExclude = cur.excludeSet.Match(rel, isDir)
			}
		}
		// Once a directory with a git marker is passed on the way up, the
		// git matchers of directories ABOVE it are skipped: an outer
		// .gitignore above a repo root does not apply inside the repo
		// (probe C5). Updated AFTER this node so the repo root's own
		// .gitignore still applies to files below it.
		sawGit = sawGit || cur.hasGitSelf

		if mCustom != glob.NoMatch && mIgnore != glob.NoMatch &&
			mGit != glob.NoMatch && mExclude != glob.NoMatch {
			break
		}
	}

	// Precedence: .rgignore > .ignore > .gitignore > exclude > global >
	// explicit (crates/ignore/src/dir.rs matched_ignore's .or() chain).
	if mCustom != glob.NoMatch {
		return mCustom
	}
	if mIgnore != glob.NoMatch {
		return mIgnore
	}
	if mGit != glob.NoMatch {
		return mGit
	}
	if mExclude != glob.NoMatch {
		return mExclude
	}
	// The global and explicit matchers are rooted at CWD (unlike the tree
	// sources, which are rooted at each node's own directory), so an
	// absolute candidate must first have the CWD prefix stripped -- see
	// anchorToCwd. Computed once for both.
	if (anyGit && c.globalSet != nil) || c.explicitSet != nil {
		gp := anchorToCwd(globPath, c.cwd)
		if anyGit && c.globalSet != nil {
			if g := c.globalSet.Match(gp, isDir); g != glob.NoMatch {
				return g
			}
		}
		if c.explicitSet != nil {
			return c.explicitSet.Match(gp, isDir)
		}
	}
	return glob.NoMatch
}

// anchorToCwd re-anchors an absolute display path to the walk's CWD,
// reproducing rg's Gitignore(root=CWD) prefix strip for the global and
// explicit matchers (crates/ignore/src/gitignore.rs strip). A relative
// candidate, or an absolute one not under cwd, is returned unchanged --
// rg matches those against the path as-is (verified against the binary:
// an absolute arg not under CWD keeps its full path, so a CWD-anchored
// pattern does not match it). Allocation-free: the `string(b) == s`
// comparison is compiler-optimized to avoid a copy, and the return is a
// sub-slice.
func anchorToCwd(globPath []byte, cwd string) []byte {
	if cwd == "" || len(globPath) == 0 || globPath[0] != '/' {
		return globPath
	}
	if cwd == "/" {
		// Root cwd: rg's strip removes just the leading separator, so every
		// absolute candidate becomes root-relative. The len(cwd)+1 slice
		// below would instead demand a SECOND '/' at index 1 (never true
		// for a normal path), skipping the strip entirely -- a real
		// divergence for gg invoked from `/`.
		return globPath[1:]
	}
	if len(globPath) > len(cwd) && globPath[len(cwd)] == '/' && string(globPath[:len(cwd)]) == cwd {
		return globPath[len(cwd)+1:]
	}
	return globPath
}

// buildNode compiles a new ignoreNode for dir (absolute path), chaining
// to parent. The has* booleans are whether dir itself directly contains a
// .git/.jj/.ignore/.gitignore/.rgignore entry; the caller supplies them
// because during a normal descent they can all be read for free off the
// directory listing already in hand (processDir's own entries, from the
// one f.ReadDir(-1) call it already made), rather than blindly attempting
// to open each and discarding the ENOENT most directories produce (M3
// #24).
func buildNode(parent *ignoreNode, dir string, hasGit, hasJJ, hasIgnore, hasGitignore, hasRgignore bool, opts *Options) *ignoreNode {
	ci := opts.IgnoreCaseInsensitive
	n := &ignoreNode{parent: parent, dir: dir}
	// .git or .jj (jujutsu, rg 15 -- probe C4) both count as a repo
	// marker; folding NoRequireGit in means the saw_git ascent stays off
	// entirely under --no-require-git (see hasGitSelf's doc).
	n.hasGitSelf = (hasGit || hasJJ) && !opts.NoRequireGit
	n.insideGitRepo = n.hasGitSelf || (parent != nil && parent.insideGitRepo)
	if hasRgignore {
		n.customSet = loadGlobSet(filepath.Join(dir, ".rgignore"), ci)
	}
	if hasIgnore {
		n.ignoreSet = loadGlobSet(filepath.Join(dir, ".ignore"), ci)
	}
	if hasGitignore {
		n.gitignoreSet = loadGlobSet(filepath.Join(dir, ".gitignore"), ci)
	}
	// Only a real .git carries info/exclude; .jj does not (brief C4).
	if hasGit {
		n.excludeSet = loadGitExclude(dir, ci)
	}
	return n
}

// buildParentChain loads the .rgignore/.ignore/.gitignore files of every
// directory above absRoot, all the way to the filesystem root, once at
// walk start. This lets e.g. a repo-root .gitignore still apply when the
// walk root is a subdirectory of the repo, and (per rg) lets .ignore/
// .rgignore files ABOVE a repo root apply too (probes D2/D5r).
//
// Ascending to the filesystem root (rather than stopping at the first
// repo boundary) matches rg exactly. The old divergence where an outer
// repo's .gitignore leaked into a nested repo used as a walk root (e.g.
// walking benchmark-data/linux picking up this project's own `*.exe`
// rule) is now prevented not by refusing to climb but by the saw_git
// ascent in matched: absRoot's own .git trips saw_git before the outer
// .gitignore above it is ever consulted, while non-git parent sources
// still apply as rg requires.
//
// Caller gates this on !NoIgnoreParent (probe D3: no parent chain at all)
// and on ignores being active at all.
func buildParentChain(absRoot string, opts *Options) *ignoreNode {
	dir := filepath.Dir(absRoot)
	if dir == absRoot {
		return nil
	}
	var dirs []string
	for cur := dir; ; {
		dirs = append(dirs, cur)
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
		// Unlike processDir's per-directory descent there is no
		// already-read listing to check membership against (this climbs a
		// handful of ancestors once per walk, not a hot path), so probe
		// the marker files directly.
		hasGit := lstatExists(filepath.Join(d, ".git"))
		hasJJ := lstatExists(filepath.Join(d, ".jj"))
		node = buildNode(node, d, hasGit, hasJJ, true, true, true, opts)
	}
	return node
}

func lstatExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// loadGlobSet reads path as a gitignore-syntax pattern file and compiles
// it into a glob.Set. caseInsensitive routes every pattern through
// glob.Builder.AddCI (for --ignore-file-case-insensitive, applied to tree
// sources only). Returns nil if the file doesn't exist, can't be read, or
// has no usable patterns.
func loadGlobSet(path string, caseInsensitive bool) *glob.Set {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return compileGlobData(data, caseInsensitive)
}

// compileGlobData compiles the raw bytes of a gitignore-syntax file into a
// glob.Set (shared by loadGlobSet, loadGitExclude, and the explicit/global
// loaders). Returns nil if no usable patterns are present.
func compileGlobData(data []byte, caseInsensitive bool) *glob.Set {
	data = bytes.TrimPrefix(data, utf8BOM)
	var b glob.Builder
	any := false
	for _, line := range bytes.Split(data, []byte("\n")) {
		line = bytes.TrimRight(line, "\r")
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 || trimmed[0] == '#' {
			continue
		}
		if caseInsensitive {
			b.AddCI(string(trimmed))
		} else {
			b.Add(string(trimmed))
		}
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
// loads its info/exclude file. Only called for a directory that itself
// directly contains a .git entry.
func loadGitExclude(dir string, caseInsensitive bool) *glob.Set {
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
	return loadGlobSet(filepath.Join(gitDir, "info", "exclude"), caseInsensitive)
}

// IgnoreFileError records a single --ignore-file that could not be loaded
// (missing, unreadable). The caller (engine) surfaces each as a warning to
// stderr; per rg these NEVER change the exit code (probes G4r-G7).
type IgnoreFileError struct {
	Path string
	Err  error
}

// LoadExplicitIgnore compiles every --ignore-file's patterns into one
// glob.Set, in argument order. Patterns are always matched
// case-sensitively (rg's --ignore-file-case-insensitive does not reach
// the explicit matcher -- probe F3). Files that can't be read are
// collected as IgnoreFileError (the caller warns; they don't affect the
// exit code) and skipped, with the remaining files still applied. Returns
// a nil Set if no file contributed any usable pattern.
func LoadExplicitIgnore(files []string) (*glob.Set, []IgnoreFileError) {
	var errs []IgnoreFileError
	var b glob.Builder
	any := false
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			errs = append(errs, IgnoreFileError{Path: f, Err: err})
			continue
		}
		data = bytes.TrimPrefix(data, utf8BOM)
		for _, line := range bytes.Split(data, []byte("\n")) {
			line = bytes.TrimRight(line, "\r")
			trimmed := bytes.TrimSpace(line)
			if len(trimmed) == 0 || trimmed[0] == '#' {
				continue
			}
			b.Add(string(trimmed))
			any = true
		}
	}
	if !any {
		return nil, errs
	}
	set, err := b.Build()
	if err != nil || set == nil {
		return nil, errs
	}
	return set, errs
}

// excludesFileRE matches a `core.excludesFile` assignment anywhere in a
// git config file, in any section, case-insensitively -- the same
// forgiving parse rg itself uses (crates/ignore/src/gitignore.rs). The
// FIRST match in the file wins.
var excludesFileRE = regexp.MustCompile(`(?im)^\s*excludesfile\s*=\s*"?\s*(\S+?)\s*"?\s*$`)

// LoadGlobalIgnore resolves and compiles the global gitignore matcher,
// exactly as rg does (crates/ignore/src/gitignore.rs global()), from the
// process environment (HOME, XDG_CONFIG_HOME). Resolution order, first
// hit wins:
//
//  1. $HOME/.gitconfig            -> core.excludesFile
//  2. $XDG_CONFIG_HOME/git/config -> core.excludesFile
//     (or $HOME/.config/git/config when XDG_CONFIG_HOME is unset/empty)
//  3. default $XDG_CONFIG_HOME/git/ignore
//     (or $HOME/.config/git/ignore when XDG_CONFIG_HOME is unset/empty)
//
// An excludesFile in either config REPLACES the default path entirely
// (probes E6/E7). caseInsensitive folds the global patterns for
// --ignore-file-case-insensitive: unlike the brief's original read, that
// flag DOES reach the global matcher (dir.rs build_with_cwd:
// `builder.case_insensitive(opts.ignore_case_insensitive)` -- probe F5);
// only the explicit --ignore-file matcher stays case-sensitive (F3).
// Returns nil when no global ignore file exists or it has no usable
// patterns. Gating by --no-ignore-global/--no-ignore-vcs is the caller's
// responsibility (it simply doesn't call this when disabled).
func LoadGlobalIgnore(caseInsensitive bool) *glob.Set {
	home := os.Getenv("HOME")
	xdg := os.Getenv("XDG_CONFIG_HOME")

	configDir := xdg
	if configDir == "" && home != "" {
		configDir = filepath.Join(home, ".config")
	}

	// 1 & 2: an excludesFile named in either git config replaces the
	// default path.
	var configPaths []string
	if home != "" {
		configPaths = append(configPaths, filepath.Join(home, ".gitconfig"))
	}
	if configDir != "" {
		configPaths = append(configPaths, filepath.Join(configDir, "git", "config"))
	}
	for _, cfgPath := range configPaths {
		data, err := os.ReadFile(cfgPath)
		if err != nil {
			continue
		}
		m := excludesFileRE.FindSubmatch(data)
		if m == nil {
			continue
		}
		p := expandTilde(strings.TrimSpace(string(m[1])), home)
		if p == "" {
			continue
		}
		return loadGlobSet(p, caseInsensitive)
	}

	// 3: default path.
	if configDir != "" {
		return loadGlobSet(filepath.Join(configDir, "git", "ignore"), caseInsensitive)
	}
	return nil
}

// expandTilde replaces every '~' in p with home, matching rg's own
// expansion (crates/ignore/src/gitignore.rs: it replaces all occurrences,
// not just a leading one).
func expandTilde(p, home string) string {
	if home == "" || !strings.Contains(p, "~") {
		return p
	}
	return strings.ReplaceAll(p, "~", home)
}
