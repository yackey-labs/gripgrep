package walk

import "os"

// devIno has no cheap Windows equivalent: os.FileInfo.Sys() exposes
// Win32FileAttributeData, which carries no file identity (the
// VolumeSerialNumber/FileIndex pair needs an extra open handle +
// GetFileInformationByHandle per directory). Report ok=false so callers
// skip symlink-loop detection, per devIno's documented degradation --
// see symlink.go.
func devIno(fi os.FileInfo) (dev, ino uint64, ok bool) {
	return 0, 0, false
}
