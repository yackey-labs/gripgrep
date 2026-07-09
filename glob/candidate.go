package glob

import "bytes"

// basename returns the final path component of path, mirroring
// pathutil::file_name in ../ripgrep/crates/globset/src/pathutil.rs. It
// never allocates. As in upstream, a path ending in ".." (as its final
// component) has no basename — this matters because Set never
// materializes the caller's path as a string and so can't otherwise tell
// "the literal component .." from a real file named "..".
func basename(path []byte) []byte {
	if len(path) == 0 {
		return path
	}
	b := path
	if idx := bytes.LastIndexByte(path, '/'); idx >= 0 {
		b = path[idx+1:]
	}
	if len(b) == 2 && b[0] == '.' && b[1] == '.' {
		return nil
	}
	return b
}

// extOf returns the extension of a basename (as returned by basename),
// mirroring pathutil::file_name_ext. The returned slice includes the
// leading '.', e.g. extOf([]byte("foo.rs")) == ".rs", and
// extOf([]byte(".rs")) == ".rs" too (matching upstream's deliberately
// liberal definition — see the doc comment on file_name_ext).
func extOf(base []byte) []byte {
	if len(base) == 0 {
		return nil
	}
	idx := bytes.LastIndexByte(base, '.')
	if idx < 0 {
		return nil
	}
	return base[idx:]
}
