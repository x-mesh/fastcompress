// buildTable follows build_decode_table from libdeflate, altered to write
// entries in the layout described below. libdeflate is Copyright 2016, 2018
// Eric Biggers; see NOTICE for its license.

package gzip

import "math/bits"

// A table entry packs one decoding step into a uint32:
//
//	bits 0-7   number of bits to consume: code length plus extra bits
//	bits 8-15  op: 0 literal, 16|n length/distance base with n extra bits,
//	           32 end of block, 64 invalid, 1-15 link to a sub-table of 2^op
//	bits 16-31 literal, base value, or sub-table offset
//
// Counting the extra bits in bits 0-7 lets the decoder consume a code and its
// extra bits with one shift.
const (
	opLiteral = 0
	opBase    = 16
	opEnd     = 32
	opInvalid = 64
)

func entryBits(e uint32) uint  { return uint(e & 0xff) }
func entryOp(e uint32) uint32  { return (e >> 8) & 0xff }
func entryVal(e uint32) uint32 { return e >> 16 }

func makeEntry(bits, op, val uint32) uint32 { return bits | op<<8 | val<<16 }

type codeType int

const (
	codeLens codeType = iota
	codeLitLen
	codeDist
)

const maxBits = 15

var (
	lenBase   = [31]uint16{3, 4, 5, 6, 7, 8, 9, 10, 11, 13, 15, 17, 19, 23, 27, 31, 35, 43, 51, 59, 67, 83, 99, 115, 131, 163, 195, 227, 258, 0, 0}
	lenExtra  = [31]uint16{16, 16, 16, 16, 16, 16, 16, 16, 17, 17, 17, 17, 18, 18, 18, 18, 19, 19, 19, 19, 20, 20, 20, 20, 21, 21, 21, 21, 16, 64, 64}
	distBase  = [32]uint16{1, 2, 3, 4, 5, 7, 9, 13, 17, 25, 33, 49, 65, 97, 129, 193, 257, 385, 513, 769, 1025, 1537, 2049, 3073, 4097, 6145, 8193, 12289, 16385, 24577, 0, 0}
	distExtra = [32]uint16{16, 16, 16, 16, 17, 17, 18, 18, 19, 19, 20, 20, 21, 21, 22, 22, 23, 23, 24, 24, 25, 25, 26, 26, 27, 27, 28, 28, 29, 29, 64, 64}
)

// symEntry[typ][sym] is the table entry of symbol sym without its code
// length.
var symEntry = func() (s [3][]uint32) {
	baseEntry := func(op, base uint16) uint32 {
		var n uint32
		if op&opBase != 0 {
			n = uint32(op & 15)
		}
		return makeEntry(n, uint32(op), uint32(base))
	}
	s[codeLens] = make([]uint32, 19)
	for i := range s[codeLens] {
		s[codeLens][i] = makeEntry(0, opLiteral, uint32(i))
	}
	s[codeLitLen] = make([]uint32, 288)
	for i := range 256 {
		s[codeLitLen][i] = makeEntry(0, opLiteral, uint32(i))
	}
	s[codeLitLen][256] = makeEntry(0, opEnd|opInvalid, 0)
	for i := 257; i < 288; i++ {
		s[codeLitLen][i] = baseEntry(lenExtra[i-257], lenBase[i-257])
	}
	s[codeDist] = make([]uint32, 32)
	for i := range s[codeDist] {
		s[codeDist][i] = baseEntry(distExtra[i], distBase[i])
	}
	return s
}()

// buildTable writes the table for lens into table and returns the entries
// used and the root bits chosen. errNoRoom means table is too small for this
// root size; the caller retries with a smaller root.
//
// It writes each code of root bits or fewer once, in the first 2^len entries,
// and copies the filled part of the table to double it at each next length.
// Each entry then repeats for every value of the bits after its code.
func buildTable(typ codeType, lens []uint16, table []uint32, root uint, work []uint16) (used int, rootOut uint, err error) {
	// Unused symbols come in long runs, and counting them one at a time
	// makes each increment of count[0] wait for the one before it. A group
	// of 4 unused symbols costs one increment, and the sort below skips it.
	var count, offs [maxBits + 1]uint16
	n4 := len(lens) &^ 3
	for i := 0; i < n4; i += 4 {
		q := lens[i : i+4 : i+4]
		if q[0]|q[1]|q[2]|q[3] == 0 {
			count[0] += 4
			continue
		}
		count[q[0]]++
		count[q[1]]++
		count[q[2]]++
		count[q[3]]++
	}
	for _, l := range lens[n4:] {
		count[l]++
	}

	max := uint(maxBits)
	for max >= 1 && count[max] == 0 {
		max--
	}
	if root > max {
		root = max
	}
	if max == 0 {
		// no codes: two invalid entries so decoding fails cleanly
		table[0] = makeEntry(1, opInvalid, 0)
		table[1] = makeEntry(1, opInvalid, 0)
		return 2, 1, nil
	}
	min := uint(1)
	for min < max && count[min] == 0 {
		min++
	}
	if root < min {
		root = min
	}

	left := 1
	for l := 1; l <= maxBits; l++ {
		left <<= 1
		left -= int(count[l])
		if left < 0 {
			return 0, 0, ErrCorrupt
		}
	}
	if left > 0 && (typ == codeLens || max != 1) {
		return 0, 0, ErrCorrupt
	}

	// Sort the symbols by code length, the unused ones first. Testing each
	// symbol for length 0 mispredicts when used and unused symbols mix, so
	// only whole groups of 4 unused symbols are skipped.
	for l := 0; l < maxBits; l++ {
		offs[l+1] = offs[l] + count[l]
	}
	for i := 0; i < n4; i += 4 {
		q := lens[i : i+4 : i+4]
		if q[0]|q[1]|q[2]|q[3] == 0 {
			continue
		}
		for j, l := range q {
			work[offs[l]] = uint16(i + j)
			offs[l]++
		}
	}
	for sym := n4; sym < len(lens); sym++ {
		l := lens[sym]
		work[offs[l]] = uint16(sym)
		offs[l]++
	}

	end := 1 << root
	if end > len(table) {
		return 0, 0, errNoRoom
	}
	ent := symEntry[typ]
	if left > 0 {
		// one code of one bit; the other bit value is invalid
		table[0] = ent[work[count[0]]] + 1
		table[1] = makeEntry(1, opInvalid, 0)
		return 2, 1, nil
	}

	// code is the next codeword with its bits reversed, as the decoder reads
	// them; appending zeros for a longer code leaves it unchanged
	code, s := 0, int(count[0])
	l := min
	end = 1 << l
	for ; l <= root; l++ {
		if l > min {
			copy(table[end:2*end], table[:end])
			end <<= 1
		}
		for n := count[l]; n > 0; n-- {
			table[code] = ent[work[s]] + uint32(l)
			s++
			if code == end-1 {
				// all ones: the last code
				for ; l < root; l++ {
					copy(table[end:2*end], table[:end])
					end <<= 1
				}
				return end, root, nil
			}
			// increment the reversed code: set its highest 0 bit and clear
			// the 1 bits above it
			bit := 1 << (bits.Len(uint(code^(end-1))) - 1)
			code = code&(bit-1) | bit
		}
	}

	// longer codes go to sub-tables after the root table
	for count[l] == 0 {
		l++
	}
	n := count[l]
	prefix, start := -1, 0
	mask := 1<<root - 1
	for {
		if code&mask != prefix {
			// as in zlib, the sub-table gets the fewest bits that the codes
			// left, from this length on, fill
			prefix, start = code&mask, end
			sub := l - root
			space := int(n)
			for space < 1<<sub {
				sub++
				space = space<<1 + int(count[root+sub])
			}
			end = start + 1<<sub
			if end > len(table) {
				return 0, 0, errNoRoom
			}
			table[prefix] = makeEntry(uint32(root), uint32(sub), uint32(start))
		}
		e := ent[work[s]] + uint32(l-root)
		s++
		for i := start + code>>root; i < end; i += 1 << (l - root) {
			table[i] = e
		}
		last := 1<<l - 1
		if code == last {
			return end, root, nil
		}
		bit := 1 << (bits.Len(uint(code^last)) - 1)
		code = code&(bit-1) | bit
		for n--; n == 0; n = count[l] {
			l++
		}
	}
}
