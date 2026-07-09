package walk

import (
	"os"
	"syscall"
)

// symNode is an immutable link in a chain of directory identities
// (device + inode), used to detect symlink loops when Options.FollowSymlinks
// is set. Built lazily: nil whenever FollowSymlinks is off, since plain
// (non-followed) symlinks can never introduce a cycle.
type symNode struct {
	parent   *symNode
	dev, ino uint64
}

// devIno extracts the (device, inode) pair from a FileInfo, if the
// platform exposes one via Sys(). Returns ok=false on platforms where it
// doesn't (e.g. Windows) — callers degrade gracefully by skipping loop
// detection rather than failing.
func devIno(fi os.FileInfo) (dev, ino uint64, ok bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, false
	}
	return uint64(st.Dev), uint64(st.Ino), true
}

// pushSymAncestor extends chain with dir's identity, for use as the
// symAncestors of dir's children. Returns the input chain unchanged if
// the identity can't be determined.
func pushSymAncestor(chain *symNode, dir string) *symNode {
	fi, err := os.Lstat(dir)
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
