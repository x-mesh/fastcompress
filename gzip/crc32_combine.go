// crcCombine is a port of crc32_combine from zlib 1.2.12, altered to Go.
// zlib is Copyright (C) 1995-2022 Mark Adler; see NOTICE for its license.

package gzip

const crcPoly = 0xedb88320

// crcX2n[k] is x^(2^k) modulo the CRC-32 polynomial.
var crcX2n = func() (t [32]uint32) {
	p := uint32(1) << 30 // x^1
	t[0] = p
	for k := 1; k < 32; k++ {
		p = crcMultModP(p, p)
		t[k] = p
	}
	return t
}()

// crcMultModP returns a times b modulo the CRC-32 polynomial, in the
// reflected bit order of the CRC.
func crcMultModP(a, b uint32) uint32 {
	m := uint32(1) << 31
	p := uint32(0)
	for {
		if a&m != 0 {
			p ^= b
			if a&(m-1) == 0 {
				break
			}
		}
		m >>= 1
		if b&1 != 0 {
			b = b>>1 ^ crcPoly
		} else {
			b >>= 1
		}
	}
	return p
}

// crcCombine returns the CRC-32 of A followed by B, from the CRC-32 of A, the
// CRC-32 of B and the length of B. Concurrent compression computes the
// checksum of each chunk on the chunk's goroutine.
func crcCombine(crcA, crcB uint32, lenB int) uint32 {
	// x^(8*lenB): one factor x^(2^k) for each bit k of 8*lenB
	p := uint32(1) << 31 // x^0
	for n, k := uint64(lenB), 3; n != 0; n, k = n>>1, k+1 {
		if n&1 != 0 {
			p = crcMultModP(crcX2n[k&31], p)
		}
	}
	return crcMultModP(p, crcA) ^ crcB
}
