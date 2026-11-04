package fastgzip

import "unsafe"

const (
	chainHashBits = 17
	windowMask    = windowSize - 1
)

// lazyParams are the fields of zlib's configuration_table for deflate_slow.
type lazyParams struct {
	// good shortens the search to a quarter of chain when the previous
	// match is at least this long.
	good int
	// lazy is the match length from which the next position is not tried.
	lazy int
	// nice stops the search at a match this long.
	nice int
	// chain is the most candidates the search tries.
	chain int
}

// Measured on source, binaries and logs: zlib's level 9 chain of 4096 gave
// the same sizes as 1024 (to 0.01%) in up to 1.5 times the time, and zlib's
// levels 7 and 8 took 2-2.6 times as long as klauspost/compress. Level 8 uses klauspost's values;
// level 7 uses longer chains than klauspost, which make it compress better
// than level 6.
var lazyLevels = [10]lazyParams{
	7: {8, 30, 32, 16},
	8: {16, 30, 40, 64},
	9: {32, 258, 258, 1024},
}

// lazyMatcher finds matches with hash chains and lazy evaluation, as zlib
// does (levels 7-9).
type lazyMatcher struct {
	p    lazyParams
	head *[1 << chainHashBits]int32
	// prev[(pos+base)&windowMask] is the position before pos with the
	// same hash. Indexing with pos+base keeps the slots when the window
	// slides.
	prev *[windowSize]int32
	posBase
}

func (m *lazyMatcher) reset(p lazyParams) {
	m.p = p
	if m.head == nil {
		m.head = new([1 << chainHashBits]int32)
		m.prev = new([windowSize]int32)
	}
	if m.restart() {
		clear(m.head[:])
		clear(m.prev[:])
	}
}

func (m *lazyMatcher) shift(delta int) {
	if !m.slide(delta) {
		return
	}
	// subtract a multiple of the window size, so that prev keeps its slots
	sub := m.base &^ windowMask
	rebase := func(t []int32) {
		for i, v := range t {
			if int(v) < sub {
				t[i] = -1
			} else {
				t[i] = v - int32(sub)
			}
		}
	}
	rebase(m.head[:])
	rebase(m.prev[:])
	m.base -= sub
}

func hash4(v uint32) uint32 {
	return v * 0x9e3779b1 >> (32 - chainHashBits)
}

// longest returns the longest match for buf[s:end] that is longer than
// best, or 0. It tries at most chain candidates.
//
// It loads through unsafe pointers: s+4 <= end <= len(buf), and every
// candidate is at least 0 and before s, so cand+best < s+best < end.
func (m *lazyMatcher) longest(buf []byte, s, end, best, chain int) (length, dist int) {
	limit := min(end, s+maxMatch)
	if best >= limit-s {
		return 0, 0
	}
	p := unsafe.Pointer(unsafe.SliceData(buf))
	first := *(*uint32)(unsafe.Add(p, s))
	base := m.base
	prev := m.prev
	cand := int(m.head[hash4(first)]) - base
	nice := m.p.nice
	for ; chain > 0; chain-- {
		// candidates come in decreasing order; one that is not was
		// overwritten by a newer position and ends the chain
		if uint(s-cand-1) >= windowSize || cand < 0 {
			break
		}
		if *(*byte)(unsafe.Add(p, cand+best)) == *(*byte)(unsafe.Add(p, s+best)) &&
			*(*uint32)(unsafe.Add(p, cand)) == first {
			l := minMatch + matchLenAt(p, s+minMatch, cand+minMatch, limit-s-minMatch)
			if l > best {
				best, length, dist = l, l, s-cand
				if l >= nice || l == limit-s {
					break
				}
			}
		}
		next := int(prev[(cand+base)&windowMask]) - base
		if next >= cand {
			break
		}
		cand = next
	}
	return length, dist
}

// insert adds pos, which is at most len(buf)-4, to the chains.
func (m *lazyMatcher) insert(buf []byte, pos int) {
	h := hash4(load32s(buf, pos))
	m.prev[(pos+m.base)&windowMask] = m.head[h]
	m.head[h] = int32(pos + m.base)
}

func (m *lazyMatcher) encode(e *encoder, buf []byte, start, end int) {
	m.cover(end)
	// a position needs 4 bytes to hash; the 3 positions after last get
	// literals unless a match covers them
	last := end - minMatch
	emit := start
	ins := start // positions before ins are in the chains
	insertTo := func(p int) {
		for ; ins <= p && ins <= last; ins++ {
			m.insert(buf, ins)
		}
	}
	s := start
	for s <= last {
		l, d := m.longest(buf, s, end, minMatch-1, m.p.chain)
		insertTo(s)
		if l == 0 {
			s++
			continue
		}
		// try the next position while the match is short
		for l < m.p.lazy && s+1 <= last {
			chain := m.p.chain
			if l >= m.p.good {
				chain >>= 2
			}
			l2, d2 := m.longest(buf, s+1, end, l, chain)
			insertTo(s + 1)
			if l2 == 0 {
				break
			}
			s++
			l, d = l2, d2
		}
		e.literals(buf[emit:s])
		e.match(l, d)
		s += l
		emit = s
		insertTo(s - 1)
	}
	e.literals(buf[emit:end])
}
