package walk

import (
	"errors"
	"os"
)

// errSymlinkLoop is the Entry.Err reported when following a symlink would
// revisit an ancestor directory (a file-system loop). rg treats this as a
// non-fatal error: results outside the loop are still emitted, one message
// goes to stderr, and the exit code becomes 2 (error overrides match). The
// engine's visitor surfaces it exactly like any other per-entry error, so
// --no-messages suppresses the message while the exit code stays 2.
var errSymlinkLoop = errors.New("file system loop detected")

// statDev returns the device number of the file at path (following
// symlinks), for Options.OneFileSystem boundary checks. ok is false when
// the path can't be stat'd or the platform doesn't expose a device number
// (see devIno). Only ever called when OneFileSystem is set, so the default
// walk pays for none of it.
func statDev(path string) (dev uint64, ok bool) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, false
	}
	dev, _, ok = devIno(fi)
	return dev, ok
}

// symNode is an immutable link in a chain of directory identities
// (device + inode), used to detect symlink loops when Options.FollowSymlinks
// is set. Built lazily: nil whenever FollowSymlinks is off, since plain
// (non-followed) symlinks can never introduce a cycle.
type symNode struct {
	parent   *symNode
	dev, ino uint64
}

// devIno (per-OS: symlink_unix.go, symlink_windows.go) extracts the
// (device, inode) pair from a FileInfo, if the platform exposes one via
// Sys(). Returns ok=false on platforms where it doesn't (Windows) —
// callers degrade gracefully by skipping loop detection rather than
// failing (a followed loop still terminates: path construction fails
// once the generated path exceeds the OS limit).

// pushSymAncestor extends chain with dir's identity, for use as the
// symAncestors of dir's children. Returns the input chain unchanged if
// the identity can't be determined.
//
// Uses Stat, not Lstat: dir may itself be a symlink's resolved target
// (followSymlink calls this with the followed path), and loops() below
// compares against another Stat'd (followed) identity. Lstat here would
// record the symlink's own inode for a directory only ever reached
// through a symlink, which loops() could never match — silently
// defeating cycle detection for exactly the case that matters.
func pushSymAncestor(chain *symNode, dir string) *symNode {
	fi, err := os.Stat(dir)
	if err != nil {
		return chain
	}
	dev, ino, ok := devIno(fi)
	if !ok {
		return chain
	}
	return &symNode{parent: chain, dev: dev, ino: ino}
}

// loops reports whether target's identity already appears in chain,
// i.e. following the symlink that led to target would revisit an
// ancestor directory.
func loops(chain *symNode, target os.FileInfo) bool {
	dev, ino, ok := devIno(target)
	if !ok {
		return false
	}
	for n := chain; n != nil; n = n.parent {
		if n.dev == dev && n.ino == ino {
			return true
		}
	}
	return false
}
