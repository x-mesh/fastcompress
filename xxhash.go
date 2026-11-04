package fastgzip

import (
	"encoding/binary"
	"math/bits"
)

// XXH64 primes, from the xxHash specification.
const (
	xxPrime1 uint64 = 0x9E3779B185EBCA87
	xxPrime2 uint64 = 0xC2B2AE3D27D4EB4F
	xxPrime3 uint64 = 0x165667B19E3779F9
	xxPrime4 uint64 = 0x85EBCA77C2B2AE63
	xxPrime5 uint64 = 0x27D4EB2F165667C5
)

// xxh64 is the streaming XXH64 with seed 0, as zstd uses for the content
// checksum.
type xxh64 struct {
	v     [4]uint64
	total uint64
	buf   [32]byte
	n     int
}

func (h *xxh64) reset() {
	// variables, not constants: the seed arithmetic wraps
	p1, p2 := xxPrime1, xxPrime2
	h.v = [4]uint64{p1 + p2, p2, 0, -p1}
	h.total = 0
	h.n = 0
}

func xxRound(acc, in uint64) uint64 {
	acc += in * xxPrime2
	acc = bits.RotateLeft64(acc, 31)
	return acc * xxPrime1
}

func xxMerge(acc, v uint64) uint64 {
	acc ^= xxRound(0, v)
	return acc*xxPrime1 + xxPrime4
}

func (h *xxh64) write(p []byte) {
	h.total += uint64(len(p))
	if h.n > 0 {
		c := copy(h.buf[h.n:], p)
		h.n += c
		p = p[c:]
		if h.n < 32 {
			return
		}
		h.stripes(h.buf[:])
		h.n = 0
	}
	if len(p) >= 32 {
		n := len(p) &^ 31
		h.stripes(p[:n])
		p = p[n:]
	}
	h.n = copy(h.buf[:], p)
}

func (h *xxh64) stripes(p []byte) {
	v0, v1, v2, v3 := h.v[0], h.v[1], h.v[2], h.v[3]
	for ; len(p) >= 32; p = p[32:] {
		v0 = xxRound(v0, binary.LittleEndian.Uint64(p))
		v1 = xxRound(v1, binary.LittleEndian.Uint64(p[8:]))
		v2 = xxRound(v2, binary.LittleEndian.Uint64(p[16:]))
		v3 = xxRound(v3, binary.LittleEndian.Uint64(p[24:]))
	}
	h.v = [4]uint64{v0, v1, v2, v3}
}

func (h *xxh64) sum() uint64 {
	var acc uint64
	if h.total >= 32 {
		v := h.v
		acc = bits.RotateLeft64(v[0], 1) + bits.RotateLeft64(v[1], 7) +
			bits.RotateLeft64(v[2], 12) + bits.RotateLeft64(v[3], 18)
		for _, x := range v {
			acc = xxMerge(acc, x)
		}
	} else {
		acc = h.v[2] + xxPrime5
	}
	acc += h.total
	p := h.buf[:h.n]
	for ; len(p) >= 8; p = p[8:] {
		acc ^= xxRound(0, binary.LittleEndian.Uint64(p))
		acc = bits.RotateLeft64(acc, 27)*xxPrime1 + xxPrime4
	}
	if len(p) >= 4 {
		acc ^= uint64(binary.LittleEndian.Uint32(p)) * xxPrime1
		acc = bits.RotateLeft64(acc, 23)*xxPrime2 + xxPrime3
		p = p[4:]
	}
	for _, b := range p {
		acc ^= uint64(b) * xxPrime5
		acc = bits.RotateLeft64(acc, 11) * xxPrime1
	}
	acc ^= acc >> 33
	acc *= xxPrime2
	acc ^= acc >> 29
	acc *= xxPrime3
	acc ^= acc >> 32
	return acc
}

func xxh64Sum(p []byte) uint64 {
	var h xxh64
	h.reset()
	h.write(p)
	return h.sum()
}
