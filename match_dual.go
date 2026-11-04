package fastgzip

import "unsafe"

const (
	dualShortBits = 16
	dualLongBits  = 16
)

type dualParams struct {
	// lazy tries a long match at the next position when the match found is
	// shorter than lazy.
	lazy int
	// fill inserts every fill-th position inside a match into the tables.
	fill int
	// skipLog sets how fast the search speeds up over literals: the step
	// grows by one per 1<<skipLog literals.
	skipLog uint
}

var dualLevels = [7]dualParams{
	4: {lazy: 0, fill: 4, skipLog: 6},
	5: {lazy: 16, fill: 2, skipLog: 7},
	6: {lazy: 32, fill: 1, skipLog: 8},
}

type dualEntry struct{ cur, prev int32 }

// maxFill is the most positions at the start of a match that go into the
// tables.
const maxFill = 32

// dualMatcher finds matches with a table of 4-byte hashes and a table of
// 8-byte hashes that keeps two positions per hash (levels 4-6). Every search
// tries at most three candidates, so it does not slow down on data with
// long hash chains.
type dualMatcher struct {
	p     dualParams
	short *[1 << dualShortBits]int32
	long  *[1 << dualLongBits]dualEntry
	posBase
}

func (m *dualMatcher) reset(p dualParams) {
	m.p = p
	if m.short == nil {
		m.short = new([1 << dualShortBits]int32)
		m.long = new([1 << dualLongBits]dualEntry)
	}
	if m.restart() {
		clear(m.short[:])
		clear(m.long[:])
	}
}

func (m *dualMatcher) shift(delta int) {
	if !m.slide(delta) {
		return
	}
	rebase := func(v int32) int32 {
		if pos := int(v) - m.base; pos >= 0 {
			return int32(pos)
		}
		return -1
	}
	for i, v := range m.short {
		m.short[i] = rebase(v)
	}
	for i, v := range m.long {
		m.long[i] = dualEntry{rebase(v.cur), rebase(v.prev)}
	}
	m.base = 0
}

func hashShort(v uint64) uint32 {
	return uint32(v) * 0x9e3779b1 >> (32 - dualShortBits)
}

func hashLong(v uint64) uint32 {
	return uint32(v * 0xcf1bbcdcb7a56463 >> (64 - dualLongBits))
}

// encode adds the tokens for buf[start:end] to e.
//
// Loads go through unsafe pointers. Each 8-byte load is at a position below
// limit, which is fastTail bytes before end, or at a candidate that is at
// least 0 and before that position.
func (m *dualMatcher) encode(e *encoder, buf []byte, start, end int) {
	m.cover(end)
	emit := start
	tokens := e.tokens
	freq := &e.litFreq
	if end-start >= 2*fastTail {
		p := unsafe.Pointer(unsafe.SliceData(buf))
		base := m.base
		short, long := m.short, m.long
		lazy, fill, skipLog := m.p.lazy, m.p.fill, m.p.skipLog
		limit := end - fastTail
		valid := func(cand, s int) bool { return uint(s-cand-1) < windowSize && cand >= 0 }
		// extend returns the match length at s and cand, which share n
		// bytes.
		extend := func(s, cand, n int) int {
			return n + matchLenAt(p, s+n, cand+n, min(end-s, maxMatch)-n)
		}
		// insert puts position i into both tables.
		insert := func(i int) {
			v := load64(p, i)
			hl := hashLong(v)
			long[hl] = dualEntry{int32(i + base), long[hl].cur}
			short[hashShort(v)] = int32(i + base)
		}

		// find inserts s into the tables and returns the longest match at s
		// that is longer than best, among the two long candidates and the
		// short one, or 0.
		find := func(s, best int) (int, int) {
			cv := load64(p, s)
			hl, hs := hashLong(cv), hashShort(cv)
			le := long[hl]
			sc := int(short[hs]) - base
			long[hl] = dualEntry{int32(s + base), le.cur}
			short[hs] = int32(s + base)
			if best >= min(end-s, maxMatch) {
				return 0, 0
			}
			// A candidate is longer than l only if it also matches at
			// offset l: one byte compare skips most extends. s+l < end.
			l, cand := best, 0
			beats := func(c int) bool {
				return *(*byte)(unsafe.Add(p, c+l)) == *(*byte)(unsafe.Add(p, s+l))
			}
			if c := int(le.cur) - base; valid(c, s) && load64(p, c) == cv && beats(c) {
				if n := extend(s, c, 8); n > l {
					l, cand = n, c
				}
			}
			if c := int(le.prev) - base; valid(c, s) && load64(p, c) == cv && beats(c) {
				if n := extend(s, c, 8); n > l {
					l, cand = n, c
				}
			}
			if l == best && valid(sc, s) && uint32(load64(p, sc)) == uint32(cv) && beats(sc) {
				if n := extend(s, sc, 4); n > l {
					l, cand = n, sc
				}
			}
			if l == best {
				return 0, 0
			}
			return l, cand
		}

		s := start
		for s < limit {
			// find(s, 0), written out: the compiler does not inline find
			// into this large function, and this is the hottest call.
			cv := load64(p, s)
			hl, hs := hashLong(cv), hashShort(cv)
			le := long[hl]
			sc := int(short[hs]) - base
			long[hl] = dualEntry{int32(s + base), le.cur}
			short[hs] = int32(s + base)
			l, cand := 0, 0
			if c := int(le.cur) - base; uint(s-c-1) < windowSize && c >= 0 && load64(p, c) == cv {
				l, cand = extend(s, c, 8), c
			}
			// s+l < end before the byte compare at offset l, as in find
			if c := int(le.prev) - base; l < min(end-s, maxMatch) &&
				uint(s-c-1) < windowSize && c >= 0 && load64(p, c) == cv &&
				*(*byte)(unsafe.Add(p, c+l)) == *(*byte)(unsafe.Add(p, s+l)) {
				if n := extend(s, c, 8); n > l {
					l, cand = n, c
				}
			}
			if l == 0 {
				if uint(s-sc-1) < windowSize && sc >= 0 && uint32(load64(p, sc)) == uint32(cv) {
					l, cand = extend(s, sc, 4), sc
				} else {
					s += 1 + (s-emit)>>skipLog
					continue
				}
			}
			// positions before ins are in the tables
			ins := s + 1
			// a longer match at s+1 is worth a literal
			for l < lazy && s+1 < limit {
				l2, c2 := find(s+1, l)
				ins = s + 2
				if l2 == 0 {
					break
				}
				s++
				l, cand = l2, c2
			}

			for cand > 0 && s > emit && buf[cand-1] == buf[s-1] {
				cand--
				s--
				l++
			}
			if s > emit {
				tokens = append(tokens, uint32(s-emit))
				for _, c := range buf[emit:s] {
					freq[c]++
				}
			}
			d := s - cand
			dsym := distSymbol(uint32(d - 1))
			// a match longer than maxMatch, after the backward extension,
			// continues at the same distance
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
			// Inside a long match, only the first positions and the last two
			// go in: the positions in between seldom start a better match.
			stop := min(s+l, limit)
			for i := ins; i < min(stop, s+maxFill); i += fill {
				insert(i)
			}
			for i := max(ins, s+maxFill, stop-2); i < stop; i++ {
				insert(i)
			}
			s += l
			emit = s
		}
	}
	if end > emit {
		tokens = append(tokens, uint32(end-emit))
		for _, c := range buf[emit:end] {
			freq[c]++
		}
	}
	e.tokens = tokens
}
