package match

// byteFrequency is a static table ranking how commonly each byte value
// occurs in real-world text, ported byte-for-byte from ripgrep's
// regex-syntax crate (src/rank.rs, BYTE_FREQUENCIES). Higher values mean
// more common; a good prefilter byte is the one with the lowest rank in
// a literal, since it produces the fewest false-positive candidate hits
// for bytes.IndexByte.
var byteFrequency = [256]byte{
	55, 52, 51, 50, 49, 48, 47, 46, 45, 103, 242, 66, 67, 229, 44, 43,
	42, 41, 40, 39, 38, 37, 36, 35, 34, 33, 56, 32, 31, 30, 29, 28,
	255, 148, 164, 149, 136, 160, 155, 173, 221, 222, 134, 122, 232, 202, 215, 224,
	208, 220, 204, 187, 183, 179, 177, 168, 178, 200, 226, 195, 154, 184, 174, 126,
	120, 191, 157, 194, 170, 189, 162, 161, 150, 193, 142, 137, 171, 176, 185, 167,
	186, 112, 175, 192, 188, 156, 140, 143, 123, 133, 128, 147, 138, 146, 114, 223,
	151, 249, 216, 238, 236, 253, 227, 218, 230, 247, 135, 180, 241, 233, 246, 244,
	231, 139, 245, 243, 251, 235, 201, 196, 240, 214, 152, 182, 205, 181, 127, 27,
	212, 211, 210, 213, 228, 197, 169, 159, 131, 172, 105, 80, 98, 96, 97, 81,
	207, 145, 116, 115, 144, 130, 153, 121, 107, 132, 109, 110, 124, 111, 82, 108,
	118, 141, 113, 129, 119, 125, 165, 117, 92, 106, 83, 72, 99, 93, 65, 79,
	166, 237, 163, 199, 190, 225, 209, 203, 198, 217, 219, 206, 234, 248, 158, 239,
	255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255,
	255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255,
	255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255,
	255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255,
}

// rank returns the frequency rank of byte b: higher means more common.
func rank(b byte) int { return int(byteFrequency[b]) }

// poisonousRank is the threshold at or above which a single byte is
// considered too common to be a useful prefilter (rg's is_poisonous).
const poisonousRank = 250

// isPoisonousByte reports whether b is too common to be a good sole
// prefilter byte.
func isPoisonousByte(b byte) bool { return rank(b) >= poisonousRank }

// rarestByte returns the index within lit of the byte with the lowest
// frequency rank (ties broken by earliest position, mirroring a stable
// left-to-right scan). lit must be non-empty.
func rarestByte(lit []byte) int {
	best := 0
	bestRank := rank(lit[0])
	for i := 1; i < len(lit); i++ {
		r := rank(lit[i])
		if r < bestRank {
			bestRank = r
			best = i
		}
	}
	return best
}
