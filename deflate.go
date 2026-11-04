package fastgzip

import (
	"encoding/binary"
	"slices"
	"unsafe"
)

const (
	numLitLen  = 286
	numDist    = 30
	numCL      = 19
	endOfBlock = 256

	maxStored = 65535

	maxCLBits = 7
)

// A token is the length of a run of literals, which the block's input
// holds, or tokenMatch with the match length minus 3 in bits 0-7, the
// distance minus 1 in bits 8-22 and the distance code in bits 23-27. One
// token for a run, instead of one per literal, halves the token traffic of
// literal-heavy data. The matchers count the distance code, so the token
// keeps it for writeTokens.
const tokenMatch = 1 << 31

func matchToken(length, dist int, dsym uint32) uint32 {
	return tokenMatch | dsym<<23 | uint32(dist-1)<<8 | uint32(length-3)
}

var (
	// lengthSym maps a match length minus 3 to its length code minus 257.
	lengthSym [256]uint8
	// distSym maps a distance minus 1 to its distance code, as zlib's
	// _dist_code: distances below 257 directly, the rest by distance >> 7.
	distSym [512]uint8

	fixedLitLen   [288]uint8
	fixedLitCode  [288]uint16
	fixedDistLen  [numDist]uint8
	fixedDistCode [numDist]uint16
)

func init() {
	for c := 0; c < 28; c++ {
		base := int(lenBase[c]) - 3
		for l := base; l < base+1<<(lenExtra[c]-16); l++ {
			lengthSym[l] = uint8(c)
		}
	}
	// 258 has its own code; code 27 would also reach it with 5 extra bits
	lengthSym[255] = 28

	for c := 0; c < numDist; c++ {
		base := int(distBase[c]) - 1
		for d := base; d < base+1<<(distExtra[c]-16); d++ {
			if d < 256 {
				distSym[d] = uint8(c)
			} else {
				distSym[256+d>>7] = uint8(c)
			}
		}
	}

	for i := range fixedLitLen {
		switch {
		case i < 144:
			fixedLitLen[i] = 8
		case i < 256:
			fixedLitLen[i] = 9
		case i < 280:
			fixedLitLen[i] = 7
		default:
			fixedLitLen[i] = 8
		}
	}
	canonical(fixedLitLen[:], fixedLitCode[:])
	for i := range fixedDistLen {
		fixedDistLen[i] = 5
	}
	canonical(fixedDistLen[:], fixedDistCode[:])
}

func distSymbol(d uint32) uint32 {
	if d < 256 {
		return uint32(distSym[d])
	}
	return uint32(distSym[256+d>>7])
}

// bitWriter appends bits to out, least significant bit first. Bits of bits
// at and above n are always zero.
type bitWriter struct {
	out  []byte
	bits uint64
	n    uint
}

// write appends the low n bits of v; v must have no higher bits set, and n
// is at most 32.
func (w *bitWriter) write(v uint32, n uint) {
	w.bits |= uint64(v) << w.n
	w.n += n
	if w.n >= 32 {
		w.out = binary.LittleEndian.AppendUint32(w.out, uint32(w.bits))
		w.bits >>= 32
		w.n -= 32
	}
}

// align pads with zero bits to a byte boundary and moves every bit to out.
func (w *bitWriter) align() {
	w.n = (w.n + 7) &^ 7
	for ; w.n > 0; w.n -= 8 {
		w.out = append(w.out, byte(w.bits))
		w.bits >>= 8
	}
}

type encoder struct {
	bw bitWriter

	// tokens of the current block, counted in litFreq and distFreq
	tokens []uint32

	litFreq  [numLitLen]uint32
	distFreq [numDist]uint32
	litLen   [numLitLen]uint8
	litCode  [numLitLen]uint16
	distLen  [numDist]uint8
	distCode [numDist]uint16

	// dynamic block header
	nlit, ndist, ncl int
	clFreq           [numCL]uint32
	clLen            [numCL]uint8
	clCode           [numCL]uint16
	// rle holds code length codes: symbol in bits 0-4, extra bits value above
	rle     []uint16
	lensBuf [numLitLen + numDist]uint8

	pm pmScratch
}

var clExtra = [numCL]uint8{16: 2, 17: 3, 18: 7}

// startBlock empties the token buffer and the frequencies.
func (e *encoder) startBlock() {
	e.tokens = e.tokens[:0]
	clear(e.litFreq[:])
	clear(e.distFreq[:])
}

func (e *encoder) literals(b []byte) {
	if len(b) == 0 {
		return
	}
	e.tokens = append(e.tokens, uint32(len(b)))
	for _, c := range b {
		e.litFreq[c]++
	}
}

func (e *encoder) match(length, dist int) {
	dsym := distSymbol(uint32(dist - 1))
	e.tokens = append(e.tokens, matchToken(length, dist, dsym))
	e.litFreq[257+int(lengthSym[length-3])]++
	e.distFreq[dsym]++
}

// extraBits returns the length and distance extra bits of the counted tokens.
func (e *encoder) extraBits() int {
	n := 0
	for c := 0; c < 29; c++ {
		n += int(e.litFreq[257+c]) * int(lenExtra[c]-16)
	}
	for c := 0; c < numDist; c++ {
		n += int(e.distFreq[c]) * int(distExtra[c]-16)
	}
	return n
}

// codeBits returns the bits of the counted codes with the fixed codes and
// with e's dynamic codes.
func (e *encoder) codeBits() (fixed, dynamic int) {
	for i, f := range e.litFreq {
		fixed += int(f) * int(fixedLitLen[i])
		dynamic += int(f) * int(e.litLen[i])
	}
	for i, f := range e.distFreq {
		fixed += int(f) * int(fixedDistLen[i])
		dynamic += int(f) * int(e.distLen[i])
	}
	return fixed, dynamic
}

// dynamicCodes builds the block's codes and header and returns the header
// size in bits.
func (e *encoder) dynamicCodes() int {
	e.pm.lengths(e.litFreq[:], e.litLen[:], maxBits)
	e.pm.lengths(e.distFreq[:], e.distLen[:], maxBits)
	if e.distFreq == [numDist]uint32{} {
		// HDIST cannot express zero codes; one unused code of length 1 is
		// what compress/flate and zlib write
		e.distLen[0] = 1
	}
	canonical(e.litLen[:], e.litCode[:])
	canonical(e.distLen[:], e.distCode[:])

	e.nlit = numLitLen
	for e.nlit > 257 && e.litLen[e.nlit-1] == 0 {
		e.nlit--
	}
	e.ndist = numDist
	for e.ndist > 1 && e.distLen[e.ndist-1] == 0 {
		e.ndist--
	}
	lens := append(append(e.lensBuf[:0], e.litLen[:e.nlit]...), e.distLen[:e.ndist]...)

	e.rle = e.rle[:0]
	emit := func(sym, extra int) { e.rle = append(e.rle, uint16(sym|extra<<5)) }
	for i := 0; i < len(lens); {
		v := int(lens[i])
		run := 1
		for i+run < len(lens) && int(lens[i+run]) == v {
			run++
		}
		i += run
		if v == 0 {
			for run >= 11 {
				r := min(run, 138)
				emit(18, r-11)
				run -= r
			}
			if run >= 3 {
				emit(17, run-3)
				run = 0
			}
		} else {
			emit(v, 0)
			run--
			for run >= 3 {
				r := min(run, 6)
				emit(16, r-3)
				run -= r
			}
		}
		for ; run > 0; run-- {
			emit(v, 0)
		}
	}

	clear(e.clFreq[:])
	used := 0
	for _, r := range e.rle {
		if e.clFreq[r&31] == 0 {
			used++
		}
		e.clFreq[r&31]++
	}
	if used == 1 {
		// zlib rejects a code length code with one code, so add a second
		sym := 0
		if e.clFreq[0] != 0 {
			sym = 1
		}
		e.clFreq[sym] = 1
	}
	e.pm.lengths(e.clFreq[:], e.clLen[:], maxCLBits)
	canonical(e.clLen[:], e.clCode[:])
	e.ncl = numCL
	for e.ncl > 4 && e.clLen[clOrder[e.ncl-1]] == 0 {
		e.ncl--
	}

	n := 5 + 5 + 4 + 3*e.ncl
	for _, r := range e.rle {
		sym := r & 31
		n += int(e.clLen[sym]) + int(clExtra[sym])
	}
	return n
}

func (e *encoder) writeDynamicHeader() {
	w := &e.bw
	w.write(uint32(e.nlit-257), 5)
	w.write(uint32(e.ndist-1), 5)
	w.write(uint32(e.ncl-4), 4)
	for _, sym := range clOrder[:e.ncl] {
		w.write(uint32(e.clLen[sym]), 3)
	}
	for _, r := range e.rle {
		sym := r & 31
		w.write(uint32(e.clCode[sym])|uint32(r>>5)<<e.clLen[sym], uint(e.clLen[sym]+clExtra[sym]))
	}
}

// writeTokens writes e.tokens, which encode raw, and the end of block code
// with the given codes.
// size is the bits of the block, which writeBlock counted.
func (e *encoder) writeTokens(raw []byte, size int, litLen []uint8, litCode []uint16, distLen []uint8, distCode []uint16) {
	// Each entry is a code with its length in bits 24-31; a length entry
	// includes its extra bits, a distance entry does not.
	var lit [256]uint32
	var length [256]uint32
	// 32 entries, so that the 5-bit code from a token needs no bounds check
	var dist [32]uint32
	for i := range lit {
		lit[i] = uint32(litCode[i]) | uint32(litLen[i])<<24
	}
	// one code at a time: for inputs of a few KB, a lengthSym lookup per
	// entry took 8% of the time. Code 28 (258) comes last and replaces
	// the entry that code 27 would also reach.
	for ls := range 29 {
		sym := 257 + ls
		eb := uint32(lenExtra[ls] - 16)
		e0 := uint32(litCode[sym]) | (uint32(litLen[sym])+eb)<<24
		base := int(lenBase[ls]) - 3
		for k := range min(1<<eb, 256-base) {
			length[base+k] = e0 | uint32(k)<<litLen[sym]
		}
	}
	for i := range numDist {
		dist[i] = uint32(distCode[i]) | uint32(distLen[i])<<24
	}

	// o never passes the bytes of the bits pending and the block, and the
	// last 8-byte store needs 8 more. A bound from the token count instead
	// was so loose that small inputs reallocated out for every block.
	out := slices.Grow(e.bw.out, (int(e.bw.n)+size)/8+16)
	o := len(out)
	out = out[:cap(out)]
	op := unsafe.Pointer(unsafe.SliceData(out))
	bits, n := e.bw.bits, e.bw.n
	// flush stores bits and keeps fewer than 8 in it. After a flush, three
	// literals (45 bits) or one match (48 bits) fit in bits. It stores
	// through an unsafe pointer: o advances by at most the bytes of the
	// codes written, which the capacity above covers with 8 to spare.
	flush := func() {
		store64(op, o, bits)
		k := n >> 3
		o += int(k)
		bits >>= k << 3
		n &= 7
	}
	pos := 0
	for _, t := range e.tokens {
		if t&tokenMatch == 0 {
			run := raw[pos : pos+int(t)]
			pos += int(t)
			for ; len(run) >= 3; run = run[3:] {
				flush()
				c0, c1, c2 := lit[run[0]], lit[run[1]], lit[run[2]]
				bits |= uint64(c0&0xffffff) << n
				n += uint(c0 >> 24)
				bits |= uint64(c1&0xffffff) << n
				n += uint(c1 >> 24)
				bits |= uint64(c2&0xffffff) << n
				n += uint(c2 >> 24)
			}
			for _, b := range run {
				flush()
				c := lit[b]
				bits |= uint64(c&0xffffff) << n
				n += uint(c >> 24)
			}
			continue
		}
		flush()
		c := length[t&0xff]
		bits |= uint64(c&0xffffff) << n
		n += uint(c >> 24)
		d := t >> 8 & 0x7fff
		ds := t >> 23 & 31
		c = dist[ds]
		extra := d - (uint32(distBase[ds]) - 1)
		bits |= uint64(c&0xffffff|extra<<(c>>24)) << n
		n += uint(c>>24) + uint(distExtra[ds]-16)
		pos += int(t&0xff) + 3
	}
	flush()
	e.bw.out = out[:o]
	e.bw.bits, e.bw.n = bits, n
	e.bw.write(uint32(litCode[endOfBlock]), uint(litLen[endOfBlock]))
}

func (e *encoder) storedBits(n int) int {
	chunks := max(1, (n+maxStored-1)/maxStored)
	pad := (8 - (e.bw.n+3)%8) % 8
	// after the first chunk the output is byte aligned: 3 header bits and 5
	// padding bits
	return chunks*(3+32) + int(pad) + (chunks-1)*5 + 8*n
}

func (e *encoder) writeStored(raw []byte, final bool) {
	for first := true; first || len(raw) > 0; first = false {
		n := min(len(raw), maxStored)
		last := uint32(0)
		if final && n == len(raw) {
			last = 1
		}
		e.bw.write(last, 3)
		e.bw.align()
		e.bw.out = binary.LittleEndian.AppendUint16(e.bw.out, uint16(n))
		e.bw.out = binary.LittleEndian.AppendUint16(e.bw.out, ^uint16(n))
		e.bw.out = append(e.bw.out, raw[:n]...)
		raw = raw[n:]
	}
}

// writeBlock writes e.tokens, which encode raw, as the smallest of a stored,
// fixed or dynamic block.
func (e *encoder) writeBlock(raw []byte, final bool) {
	if checkCounts {
		e.verifyCounts(raw)
	}
	e.litFreq[endOfBlock] = 1
	extra := e.extraBits()
	header := e.dynamicCodes()
	fixedBits, dynamicBits := e.codeBits()
	fixed := 3 + extra + fixedBits
	dynamic := 3 + extra + header + dynamicBits
	stored := e.storedBits(len(raw))

	last := uint32(0)
	if final {
		last = 1
	}
	switch {
	case stored < fixed && stored < dynamic:
		e.writeStored(raw, final)
	case fixed <= dynamic:
		e.bw.write(last|1<<1, 3)
		e.writeTokens(raw, fixed, fixedLitLen[:], fixedLitCode[:], fixedDistLen[:], fixedDistCode[:])
	default:
		e.bw.write(last|2<<1, 3)
		e.writeDynamicHeader()
		e.writeTokens(raw, dynamic, e.litLen[:], e.litCode[:], e.distLen[:], e.distCode[:])
	}
}

// checkCounts makes writeBlock recount the frequencies from the tokens. The
// tests set it: writeTokens reserves output by the bits that the counts give
// and stores without bounds checks, so the counts must match the tokens.
var checkCounts = false

func (e *encoder) verifyCounts(raw []byte) {
	var lit [numLitLen]uint32
	var dist [numDist]uint32
	pos := 0
	for _, t := range e.tokens {
		if t&tokenMatch == 0 {
			for _, c := range raw[pos : pos+int(t)] {
				lit[c]++
			}
			pos += int(t)
			continue
		}
		d := t >> 8 & 0x7fff
		if ds := t >> 23 & 31; ds != distSymbol(d) {
			panic("fastgzip: token has the wrong distance code")
		}
		lit[257+int(lengthSym[t&0xff])]++
		dist[distSymbol(d)]++
		pos += int(t&0xff) + 3
	}
	lit[endOfBlock] = e.litFreq[endOfBlock]
	if pos != len(raw) || lit != e.litFreq || dist != e.distFreq {
		panic("fastgzip: block frequencies do not match its tokens")
	}
}

// finish moves the remaining bits to out and returns it. The encoder keeps
// no reference to out.
func (e *encoder) finish() []byte {
	e.bw.align()
	out := e.bw.out
	e.bw = bitWriter{}
	return out
}
