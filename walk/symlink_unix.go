//go:build unix

package walk

import (
	"os"
	"syscall"
)

// devIno extracts the (device, inode) pair from a FileInfo via the
// platform Stat_t. The explicit uint64 conversions absorb the
// per-platform field-type differences (e.g. Dev is int32 on darwin,
// uint64 on linux).
func devIno(fi os.FileInfo) (dev, ino uint64, ok bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, false
	}
	return uint64(st.Dev), uint64(st.Ino), true
}
