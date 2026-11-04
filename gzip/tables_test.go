// buildTableZlib is the port of inftrees.c from zlib 1.2.12 that buildTable
// replaced. zlib is Copyright (C) 1995-2022 Mark Adler; see NOTICE for its
// license.

package gzip

import (
	"math"
	"math/rand/v2"
	"slices"
	"testing"
)

// buildTableZlib writes the same tables as buildTable, one entry at a time.
// The tests use it as the reference.
func buildTableZlib(typ codeType, lens []uint16, table []uint32, root uint, work []uint16) (used int, rootOut uint, err error) {
	var count, offs [maxBits + 1]uint16
	for _, l := range lens {
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

	for l := 1; l < maxBits; l++ {
		offs[l+1] = offs[l] + count[l]
	}
	for sym, l := range lens {
		if l != 0 {
			work[offs[l]] = uint16(sym)
			offs[l]++
		}
	}

	var base, extra []uint16
	var match uint32
	switch typ {
	case codeLens:
		match = 20
	case codeLitLen:
		base, extra, match = lenBase[:], lenExtra[:], 257
	default:
		base, extra, match = distBase[:], distExtra[:], 0
	}

	var (
		huff uint32
		sym  int
		l    = min
		next = 0
		curr = root
		drop uint
		low  = ^uint32(0)
	)
	used = 1 << root
	mask := uint32(used - 1)
	if used > len(table) {
		return 0, 0, errNoRoom
	}

	for {
		var here uint32
		s := uint32(work[sym])
		switch {
		case s+1 < match:
			here = makeEntry(uint32(l-drop), opLiteral, s)
		case s >= match:
			op := uint32(extra[s-match])
			bits := uint32(l - drop)
			if op&opBase != 0 {
				bits += op & 15
			}
			here = makeEntry(bits, op, uint32(base[s-match]))
		default:
			here = makeEntry(uint32(l-drop), opEnd|opInvalid, 0)
		}

		incr := uint32(1) << (l - drop)
		fill := uint32(1) << curr
		minFill := fill
		for {
			fill -= incr
			table[next+int(huff>>drop+fill)] = here
			if fill == 0 {
				break
			}
		}

		incr = 1 << (l - 1)
		for huff&incr != 0 {
			incr >>= 1
		}
		if incr != 0 {
			huff &= incr - 1
			huff += incr
		} else {
			huff = 0
		}

		sym++
		count[l]--
		if count[l] == 0 {
			if l == max {
				break
			}
			l = uint(lens[work[sym]])
		}

		if l > root && huff&mask != low {
			if drop == 0 {
				drop = root
			}
			next += int(minFill)
			curr = l - drop
			left := 1 << curr
			for curr+drop < max {
				left -= int(count[curr+drop])
				if left <= 0 {
					break
				}
				curr++
				left <<= 1
			}
			used += 1 << curr
			if used > len(table) {
				return 0, 0, errNoRoom
			}
			low = huff & mask
			table[low] = makeEntry(uint32(root), uint32(curr), uint32(next))
		}
	}

	if huff != 0 {
		table[next+int(huff)] = makeEntry(uint32(l-drop), opInvalid, 0)
	}
	return used, root, nil
}

// textLengths returns code lengths for n symbols. used of them get
// frequencies that fall off like those of text, so long codes need
// sub-tables.
func textLengths(r *rand.Rand, pm *pmScratch, n, used, limit int) []uint16 {
	freq := make([]uint32, n)
	s := 0.8 + r.Float64()
	for rank, sym := range r.Perm(n)[:used] {
		freq[sym] = uint32(1e6/math.Pow(float64(rank+1), s)) + 1
	}
	l8 := make([]uint8, n)
	pm.lengths(freq, l8, limit)
	lens := make([]uint16, n)
	for i, l := range l8 {
		lens[i] = uint16(l)
	}
	return lens
}

func sameTable(t testing.TB, typ codeType, lens []uint16, size int, root uint) {
	t.Helper()
	got := make([]uint32, size)
	want := make([]uint32, size)
	var work [288]uint16
	gu, gr, gerr := buildTable(typ, lens, got, root, work[:])
	wu, wr, werr := buildTableZlib(typ, lens, want, root, work[:])
	if gerr != werr || gerr == nil && (gu != wu || gr != wr || !slices.Equal(got[:gu], want[:wu])) {
		t.Fatalf("type %d, root %d, %d entries, lens %v: got %d entries, root %d, %v; zlib %d, %d, %v",
			typ, root, size, lens, gu, gr, gerr, wu, wr, werr)
	}
}

var tableCases = []struct {
	typ   codeType
	n     int
	limit int
	roots []uint
}{
	{codeLitLen, 286, 15, []uint{rootLenBits, fallbackLenBits}},
	{codeDist, 30, 15, []uint{rootDistBits, fallbackDistBits}},
	{codeLens, 19, 7, []uint{7}},
}

func TestBuildTableMatchesZlib(t *testing.T) {
	r := rand.New(rand.NewPCG(29, 30))
	var pm pmScratch
	for range 1000 {
		for _, c := range tableCases {
			lens := textLengths(r, &pm, c.n, 1+r.IntN(c.n), c.limit)
			for _, root := range c.roots {
				sameTable(t, c.typ, lens, lenTableSize+distTableSize, root)
				// tables too small for the root or for the sub-tables
				sameTable(t, c.typ, lens, r.IntN(2<<root), root)
			}
			// damaged lengths: mostly over-subscribed or incomplete codes
			for k := 1 + r.IntN(3); k > 0; k-- {
				lens[r.IntN(c.n)] = uint16(r.IntN(maxBits + 1))
			}
			sameTable(t, c.typ, lens, lenTableSize+distTableSize, c.roots[0])
		}
	}
	for root := uint(1); root <= maxBits; root++ {
		for _, c := range tableCases {
			sameTable(t, c.typ, make([]uint16, c.n), 1<<maxBits, root)
			sameTable(t, c.typ, textLengths(r, &pm, c.n, c.n, c.limit), 1<<maxBits, root)
		}
	}
	var fixed [288]uint16
	for i := range fixed {
		switch {
		case i < 144:
			fixed[i] = 8
		case i < 256:
			fixed[i] = 9
		case i < 280:
			fixed[i] = 7
		default:
			fixed[i] = 8
		}
	}
	sameTable(t, codeLitLen, fixed[:], 512, 9)
	for i := range 32 {
		fixed[i] = 5
	}
	sameTable(t, codeDist, fixed[:32], 32, 5)
}

func FuzzBuildTable(f *testing.F) {
	r := rand.New(rand.NewPCG(31, 32))
	var pm pmScratch
	for i, c := range tableCases {
		b := []byte{byte(i), byte(c.roots[0] - 1)}
		for _, l := range textLengths(r, &pm, c.n, c.n/2, c.limit) {
			b = append(b, byte(l))
		}
		f.Add(b)
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) < 2 {
			return
		}
		c := tableCases[int(b[0])%len(tableCases)]
		lens := make([]uint16, min(len(b)-2, c.n))
		for i := range lens {
			lens[i] = uint16(b[2+i] & 15)
		}
		sameTable(t, c.typ, lens, lenTableSize+distTableSize, uint(1+b[1]%maxBits))
	})
}

// BenchmarkBuildTable builds the three tables of a dynamic block.
func BenchmarkBuildTable(b *testing.B) {
	r := rand.New(rand.NewPCG(33, 34))
	var pm pmScratch
	type block struct{ lit, dist, cl []uint16 }
	blocks := make([]block, 64)
	for i := range blocks {
		blocks[i] = block{
			textLengths(r, &pm, 286, 150+r.IntN(137), 15),
			textLengths(r, &pm, 30, 20+r.IntN(11), 15),
			textLengths(r, &pm, 19, 10+r.IntN(10), 7),
		}
	}
	for _, impl := range []struct {
		name  string
		build func(codeType, []uint16, []uint32, uint, []uint16) (int, uint, error)
	}{{"zlib", buildTableZlib}, {"buildTable", buildTable}} {
		b.Run(impl.name, func(b *testing.B) {
			var codes [lenTableSize + distTableSize]uint32
			var cl [128]uint32
			var work [288]uint16
			for i := range b.N {
				k := &blocks[i%len(blocks)]
				impl.build(codeLens, k.cl, cl[:], 7, work[:])
				used, _, _ := impl.build(codeLitLen, k.lit, codes[:], rootLenBits, work[:])
				impl.build(codeDist, k.dist, codes[used:], rootDistBits, work[:])
			}
		})
	}
}
