package zstd

import (
	"encoding/binary"
	"math/bits"
)

// readNCount reads an FSE table description (RFC 8878 4.1.1) from the start
// of src into norm[:maxSym+1]. It returns the accuracy log and the number of
// bytes read.
func readNCount(src []byte, norm *[256]int16, maxSym int, maxLog uint) (tableLog uint, n int, err error) {
	pos := uint(0)
	peek := func(nb uint) uint32 {
		i := int(pos >> 3)
		var w uint32
		if i+4 <= len(src) {
			w = binary.LittleEndian.Uint32(src[i:])
		} else {
			for j := len(src) - 1; j >= i; j-- {
				w = w<<8 | uint32(src[j])
			}
		}
		return (w >> (pos & 7)) & (1<<nb - 1)
	}
	if len(src) == 0 {
		return 0, 0, errEOF
	}
	tableLog = uint(peek(4)) + 5
	pos = 4
	if tableLog > maxLog {
		return 0, 0, ErrCorrupt
	}
	remaining := int32(1)<<tableLog + 1
	threshold := int32(1) << tableLog
	nbits := tableLog + 1
	sym := 0
	prev0 := false
	for remaining > 1 && sym <= maxSym {
		if int(pos>>3) >= len(src) {
			return 0, 0, errEOF
		}
		if prev0 {
			// a zero count is followed by 2-bit repeat counts of more
			// zeros; 3 means another repeat count follows
			z := sym
			for peek(12) == 0xfff {
				z += 18
				pos += 12
				if int(pos>>3) >= len(src) {
					return 0, 0, errEOF
				}
			}
			for peek(2) == 3 {
				z += 3
				pos += 2
			}
			z += int(peek(2))
			pos += 2
			if z > maxSym+1 {
				return 0, 0, ErrCorrupt
			}
			for ; sym < z; sym++ {
				norm[sym] = 0
			}
			prev0 = false
			continue
		}
		maxSmall := 2*threshold - 1 - remaining
		v := int32(peek(nbits))
		var count int32
		if v&(threshold-1) < maxSmall {
			count = v & (threshold - 1)
			pos += nbits - 1
		} else {
			count = v & (2*threshold - 1)
			if count >= threshold {
				count -= maxSmall
			}
			pos += nbits
		}
		count--
		if count >= 0 {
			remaining -= count
		} else {
			remaining--
		}
		norm[sym] = int16(count)
		sym++
		prev0 = count == 0
		if remaining < 1 {
			return 0, 0, ErrCorrupt
		}
		for remaining < threshold {
			nbits--
			threshold >>= 1
		}
	}
	if remaining != 1 {
		return 0, 0, ErrCorrupt
	}
	for ; sym <= maxSym; sym++ {
		norm[sym] = 0
	}
	n = int((pos + 7) >> 3)
	if n > len(src) {
		return 0, 0, errEOF
	}
	return tableLog, n, nil
}

// buildFSE spreads the symbols of norm over the 1<<tableLog states (RFC 8878
// 4.1.1) and stores, for each state, its symbol, the number of bits to read
// for the next state, and the base those bits are added to.
func buildFSE(norm []int16, tableLog uint, syms, nb []uint8, base []uint16) error {
	size := 1 << tableLog
	high := size - 1
	var next [256]uint16
	for s, c := range norm {
		if c == -1 {
			syms[high] = uint8(s)
			high--
			next[s] = 1
		} else {
			next[s] = uint16(c)
		}
	}
	step := size>>1 + size>>3 + 3
	mask := size - 1
	pos := 0
	for s, c := range norm {
		for range max(int(c), 0) {
			syms[pos] = uint8(s)
			pos = (pos + step) & mask
			for pos > high {
				pos = (pos + step) & mask
			}
		}
	}
	if pos != 0 {
		return ErrCorrupt
	}
	for i := range size {
		s := syms[i]
		ns := next[s]
		next[s]++
		if ns == 0 {
			return ErrCorrupt
		}
		n := tableLog - uint(bits.Len16(ns)-1)
		nb[i] = uint8(n)
		base[i] = ns<<n - uint16(size)
	}
	return nil
}

// A sequence table entry packs, for one FSE state, the value base and extra
// bits of its literal length, match length or offset code, and the state
// transition, so decoding a value is one 8-byte load.
//
//	bits 0-31  value base
//	bits 32-39 extra bits of the value
//	bits 40-47 bits to read for the next state
//	bits 48-63 base of the next state
func seqEntry(vbase uint32, extra, nb uint8, next uint16) uint64 {
	return uint64(vbase) | uint64(extra)<<32 | uint64(nb)<<40 | uint64(next)<<48
}

type seqKind uint8

const (
	seqLL seqKind = iota
	seqOF
	seqML
)

const (
	maxLLLog = 9
	maxMLLog = 9
	maxOFLog = 8
	maxLLSym = 35
	maxMLSym = 52
	maxOFSym = 31
)

// RFC 8878 3.1.1.3.2.1.1: literal length codes 16 and up, and match length
// codes 32 and up, have a base and extra bits.
var (
	llCodeBase = [maxLLSym + 1]uint32{
		0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15,
		16, 18, 20, 22, 24, 28, 32, 40, 48, 64, 128, 256, 512, 1024, 2048, 4096,
		8192, 16384, 32768, 65536,
	}
	llCodeBits = [maxLLSym + 1]uint8{
		0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		1, 1, 1, 1, 2, 2, 3, 3, 4, 6, 7, 8, 9, 10, 11, 12,
		13, 14, 15, 16,
	}
	mlCodeBase = [maxMLSym + 1]uint32{
		3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18,
		19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32, 33, 34,
		35, 37, 39, 41, 43, 47, 51, 59, 67, 83, 99, 131, 259, 515, 1027, 2051,
		4099, 8195, 16387, 32771, 65539,
	}
	mlCodeBits = [maxMLSym + 1]uint8{
		0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		1, 1, 1, 1, 2, 2, 3, 3, 4, 4, 5, 7, 8, 9, 10, 11,
		12, 13, 14, 15, 16,
	}
)

// codeValue returns the value base and extra bits of a code. An offset code
// c reads c extra bits onto 1<<c; values 1-3 are repeat offsets and larger
// values are the offset plus 3, so the base of codes 2 and up has 3 taken
// off and the decoder tells repeat offsets by extra bits below 2.
func codeValue(kind seqKind, c uint8) (uint32, uint8) {
	switch kind {
	case seqLL:
		return llCodeBase[c], llCodeBits[c]
	case seqML:
		return mlCodeBase[c], mlCodeBits[c]
	}
	if c < 2 {
		return 1 << c, c
	}
	return 1<<c - 3, c
}

func maxSymOf(kind seqKind) int {
	switch kind {
	case seqLL:
		return maxLLSym
	case seqML:
		return maxMLSym
	}
	return maxOFSym
}

func maxLogOf(kind seqKind) uint {
	switch kind {
	case seqLL:
		return maxLLLog
	case seqML:
		return maxMLLog
	}
	return maxOFLog
}

// buildSeqTable fills t[:1<<tableLog] from normalized counts. Next states
// count from the kind's place in seqTables.tab.
func buildSeqTable(t []uint64, kind seqKind, norm []int16, tableLog uint) error {
	var syms, nb [seqTableSize]uint8
	var base [seqTableSize]uint16
	if err := buildFSE(norm, tableLog, syms[:], nb[:], base[:]); err != nil {
		return err
	}
	off := kindOffset(kind)
	for i := range 1 << tableLog {
		vb, extra := codeValue(kind, syms[i])
		t[i] = seqEntry(vb, extra, nb[i], off+base[i])
	}
	return nil
}

// The three tables of a frame sit in one array, literal lengths, offsets
// and match lengths seqTableSize entries apart, so the decoder needs one
// base pointer and a state is an index into the whole array.
const (
	seqTableSize = 1 << maxLLLog
	seqTabMask   = 4*seqTableSize - 1
)

func kindOffset(k seqKind) uint16 { return uint16(k) * seqTableSize }

// RFC 8878 3.1.1.3.2.2: the predefined distributions.
var (
	predefLLNorm = []int16{
		4, 3, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 1, 1, 1,
		2, 2, 2, 2, 2, 2, 2, 2, 2, 3, 2, 1, 1, 1, 1, 1,
		-1, -1, -1, -1,
	}
	predefMLNorm = []int16{
		1, 4, 3, 2, 2, 2, 2, 2, 2, 1, 1, 1, 1, 1, 1, 1,
		1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1,
		1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, -1, -1,
		-1, -1, -1, -1, -1,
	}
	predefOFNorm = []int16{
		1, 1, 1, 1, 1, 1, 2, 2, 2, 1, 1, 1, 1, 1, 1, 1,
		1, 1, 1, 1, 1, 1, 1, 1, -1, -1, -1, -1, -1,
	}
)

const (
	predefLLLog = 6
	predefMLLog = 6
	predefOFLog = 5
)

var predefLL, predefML, predefOF []uint64

func init() {
	predefLL = make([]uint64, 1<<predefLLLog)
	predefML = make([]uint64, 1<<predefMLLog)
	predefOF = make([]uint64, 1<<predefOFLog)
	if buildSeqTable(predefLL, seqLL, predefLLNorm, predefLLLog) != nil ||
		buildSeqTable(predefML, seqML, predefMLNorm, predefMLLog) != nil ||
		buildSeqTable(predefOF, seqOF, predefOFNorm, predefOFLog) != nil {
		panic("zstd: bad predefined distribution")
	}
}
