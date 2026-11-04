package gzip

import (
	"math/bits"
	"slices"
)

type pmItem struct {
	w   uint64
	sym int16 // -1 for a package
}

// pmScratch keeps the package-merge lists between calls.
type pmScratch struct {
	keys   []uint64
	depth  []int32
	leaves []pmItem
	levels [maxBits][]pmItem
}

// lengths sets lens to optimal code lengths for freq, none longer than limit,
// with the package-merge algorithm. Symbols with zero frequency get length 0.
// A single used symbol gets length 1. With two or more used symbols the code
// is complete: zlib rejects an incomplete literal/length or code length code.
func (s *pmScratch) lengths(freq []uint32, lens []uint8, limit int) {
	clear(lens)
	// packed keys sort faster than pmItems with a comparison function
	s.keys = s.keys[:0]
	for i, f := range freq {
		if f > 0 {
			s.keys = append(s.keys, uint64(f)<<16|uint64(i))
		}
	}
	n := len(s.keys)
	switch n {
	case 0:
		return
	case 1:
		lens[s.keys[0]&0xffff] = 1
		return
	}
	slices.Sort(s.keys)
	if s.huffman(lens, limit) {
		return
	}
	s.leaves = s.leaves[:0]
	for _, k := range s.keys {
		s.leaves = append(s.leaves, pmItem{k >> 16, int16(k & 0xffff)})
	}

	// Only the first 2n-2 items of a list are ever selected.
	keep := 2*n - 2
	var prev []pmItem
	for l := 0; l < limit; l++ {
		cur := s.levels[l][:0]
		np := len(prev) / 2
		i, j := 0, 0
		for len(cur) < keep && (i < n || j < np) {
			if j == np || (i < n && s.leaves[i].w <= prev[2*j].w+prev[2*j+1].w) {
				cur = append(cur, s.leaves[i])
				i++
			} else {
				cur = append(cur, pmItem{prev[2*j].w + prev[2*j+1].w, -1})
				j++
			}
		}
		s.levels[l] = cur
		prev = cur
	}

	take := keep
	for l := limit - 1; l >= 0 && take > 0; l-- {
		packages := 0
		for _, it := range s.levels[l][:take] {
			if it.sym >= 0 {
				lens[it.sym]++
			} else {
				packages++
			}
		}
		take = 2 * packages
	}
}

// huffman sets lens to Huffman code lengths for the sorted s.keys, with the
// in-place algorithm of Moffat and Katajainen. It reports false, and leaves
// lens alone, when a length is over limit. Most codes are within the limit,
// and this is faster than package-merge.
func (s *pmScratch) huffman(lens []uint8, limit int) bool {
	n := len(s.keys)
	a := slices.Grow(s.depth[:0], n)[:n]
	s.depth = a
	for i, k := range s.keys {
		a[i] = int32(k >> 16)
	}
	// phase 1: a[i] becomes the parent of internal node i
	a[0] += a[1]
	root, leaf := 0, 2
	for next := 1; next < n-1; next++ {
		if leaf >= n || a[root] < a[leaf] {
			a[next] = a[root]
			a[root] = int32(next)
			root++
		} else {
			a[next] = a[leaf]
			leaf++
		}
		if leaf >= n || (root < next && a[root] < a[leaf]) {
			a[next] += a[root]
			a[root] = int32(next)
			root++
		} else {
			a[next] += a[leaf]
			leaf++
		}
	}
	// phase 2: a[i] becomes the depth of internal node i
	a[n-2] = 0
	for next := n - 3; next >= 0; next-- {
		a[next] = a[a[next]] + 1
	}
	// phase 3: a[i] becomes the depth of leaf i
	avail, used, depth := 1, 0, int32(0)
	root, next := n-2, n-1
	for avail > 0 {
		for root >= 0 && a[root] == depth {
			used++
			root--
		}
		for avail > used {
			a[next] = depth
			next--
			avail--
		}
		avail, used = 2*used, 0
		depth++
	}
	if int(a[0]) > limit {
		return false
	}
	for i, k := range s.keys {
		lens[k&0xffff] = uint8(a[i])
	}
	return true
}

// canonical assigns DEFLATE canonical codes to lens, bit-reversed so that
// they can be written least significant bit first.
func canonical(lens []uint8, codes []uint16) {
	var count [maxBits + 1]uint16
	for _, l := range lens {
		count[l]++
	}
	count[0] = 0
	var next [maxBits + 1]uint16
	code := uint16(0)
	for b := 1; b <= maxBits; b++ {
		code = (code + count[b-1]) << 1
		next[b] = code
	}
	for i, l := range lens {
		if l != 0 {
			codes[i] = bits.Reverse16(next[l]) >> (16 - l)
			next[l]++
		}
	}
}
