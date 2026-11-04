package fastgzip

import (
	"encoding/binary"
	"math/bits"
	"unsafe"
)

const (
	windowSize = 1 << 15
	minMatch   = 4
	maxMatch   = 258
	// blockSize is the input encoded into one block: few enough tokens to
	// keep the token buffer in cache, many enough to spread the dynamic
	// header over them. It fits one stored block when the data does not
	// compress.
	blockSize = maxStored
	// The search loop reads 8 bytes at s, so it stops this far before the
	// end of the block.
	fastTail = 8
)

type fastParams struct {
	// step is the least distance between two search steps, which check
	// two positions each: 2 checks every position.
	step int
	// fill inserts every fill-th position inside a match into the table;
	// 0 inserts none.
	fill int
}

// Levels 1-3 share the hash of 5 bytes and the table of 16 bits: as
// constants, the hash shifts made level 1 3-5% faster than as fields, and
// 16 bits was faster than 15 and compressed better.
const (
	fastHashBytes = 5
	fastTableBits = 16
)

var fastLevels = [4]fastParams{
	1: {step: 3},
	2: {step: 2},
	3: {step: 2, fill: 3},
}

// fastMatcher finds matches with one hash table and no chains (levels 1-3).
type fastMatcher struct {
	p     fastParams
	table *[1 << fastTableBits]int32
	posBase
}

// shift moves every table entry delta bytes back, after the window slid by
// delta bytes.
func (m *fastMatcher) shift(delta int) {
	if !m.slide(delta) {
		return
	}
	// keep base and the entries far from int32 overflow
	for i, v := range m.table {
		pos := int(v) - m.base
		if pos < 0 {
			pos = -1
		}
		m.table[i] = int32(pos)
	}
	m.base = 0
}

func (m *fastMatcher) reset(p fastParams) {
	m.p = p
	if m.table == nil {
		m.table = new([1 << fastTableBits]int32)
	}
	if m.restart() {
		clear(m.table[:])
	}
}

func load32s(b []byte, i int) uint32 { return binary.LittleEndian.Uint32(b[i:]) }

// matchLenAt counts the equal bytes at p+a and p+b, up to max. The caller
// guarantees that p+a+max and p+b+max are in the same buffer. Unlike a
// version on slices, it has no bounds checks in the loop.
func matchLenAt(p unsafe.Pointer, a, b, max int) int {
	n := 0
	for ; n+8 <= max; n += 8 {
		if x := load64(p, a+n) ^ load64(p, b+n); x != 0 {
			return n + bits.TrailingZeros64(x)>>3
		}
	}
	for ; n < max && *(*byte)(unsafe.Add(p, a+n)) == *(*byte)(unsafe.Add(p, b+n)); n++ {
	}
	return n
}

// encode adds the tokens for buf[start:end] to e. Matches may refer to
// buf[start-windowSize:start] and end before end.
//
// The search loop loads through unsafe pointers. Each load of 8 bytes is at
// a position below limit, which is fastTail bytes before end, and each load
// of 4 bytes is at a candidate that is at least 0 and before s.
func (m *fastMatcher) encode(e *encoder, buf []byte, start, end int) {
	m.cover(end)
	emit := start
	tokens := e.tokens
	freq := &e.litFreq
	if end-start >= 2*fastTail {
		const prime = 0xcf1bbcdcb7a56463
		hash := func(v uint64) uint32 {
			return uint32((v << (64 - 8*fastHashBytes)) * prime >> (64 - fastTableBits))
		}
		tbl := m.table
		base := m.base
		fill := m.p.fill
		step := m.p.step
		p := unsafe.Pointer(unsafe.SliceData(buf))
		// table entries are at or after 0 and before s, unless the window
		// slid after they were written
		found := func(cand, s int, v uint64) bool {
			return uint(s-cand-1) < windowSize && cand >= 0 &&
				uint32(v) == *(*uint32)(unsafe.Add(p, cand))
		}
		limit := end - fastTail
		s := start
		cv := load64(p, s)
		for {
			// Each step checks s and s+1 from one load, so that their table
			// and candidate loads overlap. The step grows with the literals
			// since the last match: incompressible data is skipped faster.
			var cand int
			for {
				h0, h1 := hash(cv), hash(cv>>8)
				c0, c1 := int(tbl[h0])-base, int(tbl[h1])-base
				tbl[h0], tbl[h1] = int32(s+base), int32(s+1+base)
				if found(c0, s, cv) {
					cand = c0
					break
				}
				if found(c1, s+1, cv>>8) {
					cand = c1
					s++
					break
				}
				s += step + (s-emit)>>5
				if s >= limit {
					goto tail
				}
				cv = load64(p, s)
			}

			for {
				for cand > 0 && s > emit && buf[cand-1] == buf[s-1] {
					cand--
					s--
				}
				if s > emit {
					tokens = append(tokens, uint32(s-emit))
					for _, c := range buf[emit:s] {
						freq[c]++
					}
				}
				l := minMatch + matchLenAt(p, s+minMatch, cand+minMatch, end-s-minMatch)
				d := s - cand
				dsym := distSymbol(uint32(d - 1))
				// a match longer than maxMatch continues at the same distance
				for rest := l; rest > 0; {
					n := min(rest, maxMatch)
					if r := rest - n; r > 0 && r < 3 {
						n -= 3 - r
					}
					tokens = append(tokens, matchToken(n, d, dsym))
					freq[257+int(lengthSym[n-3])]++
					e.distFreq[dsym]++
					rest -= n
				}
				if fill > 0 {
					for i := s + 1; i < s+l && i < limit; i += fill {
						tbl[hash(load64(p, i))] = int32(i + base)
					}
				}
				s += l
				emit = s
				if s >= limit {
					goto tail
				}
				// index s-2, and take a match at s right away
				tbl[hash(load64(p, s-2))] = int32(s - 2 + base)
				cv = load64(p, s)
				h := hash(cv)
				cand = int(tbl[h]) - base
				tbl[h] = int32(s + base)
				if !found(cand, s, cv) {
					break
				}
			}
		}
	}
tail:
	if end > emit {
		tokens = append(tokens, uint32(end-emit))
		for _, c := range buf[emit:end] {
			freq[c]++
		}
	}
	e.tokens = tokens
}
