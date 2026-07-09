package printer

// ANSI escape sequences matching rg's default color scheme exactly
// (verified byte-for-byte against `rg --color=always`): every colored
// segment is wrapped reset-color-text-reset, never just color-text.
const (
	ansiReset = "\x1b[0m"
	ansiPath  = "\x1b[35m"        // magenta
	ansiLine  = "\x1b[32m"        // green
	ansiMatch = "\x1b[1m\x1b[31m" // bold red
)

// appendColoredBytes appends s wrapped in the given color escape and a
// reset, operating on []byte to avoid a string conversion on the hot
// color path.
func appendColoredBytes(buf []byte, color string, s []byte) []byte {
	buf = append(buf, ansiReset...)
	buf = append(buf, color...)
	buf = append(buf, s...)
	buf = append(buf, ansiReset...)
	return buf
}
