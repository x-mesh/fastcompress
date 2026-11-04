package zstd

import (
	"encoding/binary"
	"math/bits"
	"slices"
)

// zbitWriter writes a forward bitstream, least significant bit first, as the
// zstd backward readers expect. add needs n+nb <= 64.
type zbitWriter struct {
	out []byte
	acc uint64
	n   uint
}

func (w *zbitWriter) add(v uint64, nb uint) {
	w.acc |= v << (w.n & 63)
	w.n += nb
}

// flush writes the whole bytes of acc.
func (w *zbitWriter) flush() {
	w.out = binary.LittleEndian.AppendUint64(w.out, w.acc)
	k := w.n >> 3
	w.out = w.out[:len(w.out)-8+int(k)]
	w.acc = w.acc >> (k * 8 & 63)
	if k == 8 {
		w.acc = 0
	}
	w.n &= 7
}

// close ends the stream with the 1 bit the reader starts from.
func (w *zbitWriter) close() {
	w.add(1, 1)
	w.flush()
	if w.n > 0 {
		w.out = append(w.out, byte(w.acc))
	}
	w.acc, w.n = 0, 0
}

// optimalTableLog picks an FSE table log for n symbols up to maxSym, as
// libzstd's FSE_optimalTableLog does.
func optimalTableLog(maxLog uint, n int, maxSym int) uint {
	maxBitsSrc := uint(bits.Len32(uint32(n-1))) - 1 - 2
	minBitsSrc := uint(bits.Len32(uint32(n))) - 1 + 1
	minBitsSyms := uint(bits.Len32(uint32(maxSym))) - 1 + 2
	if maxSym == 0 {
		minBitsSyms = 2
	}
	minBits := min(minBitsSrc, minBitsSyms)
	log := maxLog
	if n > 1 && maxBitsSrc < log {
		log = maxBitsSrc
	}
	if minBits > log {
		log = minBits
	}
	return min(max(log, 5), maxLog)
}

// normalizeCounts scales counts, which sum to total, to sum 1<<log, as
// libzstd's FSE_normalizeCount does: a symbol rarer than 1/(1<<log) gets
// -1 ("less than one") when lowProb is set, else 1, and the rounding of
// small counts follows libzstd's table, which favors rounding up.
func normalizeCounts(norm []int16, counts []uint32, total uint32, log uint, lowProb bool) {
	rtb := [8]uint64{0, 473195, 504333, 520860, 550000, 700000, 750000, 830000}
	low := int16(1)
	if lowProb {
		low = -1
	}
	scale := 62 - log
	step := (uint64(1) << 62) / uint64(total)
	vStep := uint64(1) << (scale - 20)
	left := int32(1) << log
	largest, largestP := 0, int16(0)
	lowThreshold := total >> log
	for s, c := range counts {
		switch {
		case c == 0:
			norm[s] = 0
		case c <= lowThreshold:
			norm[s] = low
			left--
		default:
			pr := int16((uint64(c) * step) >> scale)
			if pr < 8 && uint64(c)*step-uint64(pr)<<scale > vStep*rtb[pr] {
				pr++
			}
			if pr > largestP {
				largest, largestP = s, pr
			}
			norm[s] = pr
			left -= int32(pr)
		}
	}
	if -left >= int32(norm[largest]>>1) {
		normalizeM2(norm, counts, total, log, low)
		return
	}
	norm[largest] += int16(left)
}

// normalizeM2 is libzstd's FSE_normalizeM2, for distributions with many
// rare symbols, where giving the rounding error to the largest fails.
func normalizeM2(norm []int16, counts []uint32, total uint32, log uint, low int16) {
	const unset = -2
	distributed := uint32(0)
	lowThreshold := total >> log
	lowOne := uint32((uint64(total) * 3) >> (log + 1))
	for s, c := range counts {
		switch {
		case c == 0:
			norm[s] = 0
		case c <= lowThreshold:
			norm[s] = low
			distributed++
			total -= c
		case c <= lowOne:
			norm[s] = 1
			distributed++
			total -= c
		default:
			norm[s] = unset
		}
	}
	toDistribute := uint32(1)<<log - distributed
	if toDistribute == 0 {
		return
	}
	if total/toDistribute > lowOne {
		lowOne = uint32((uint64(total) * 3) / (uint64(toDistribute) * 2))
		for s, c := range counts {
			if norm[s] == unset && c <= lowOne {
				norm[s] = 1
				distributed++
				total -= c
			}
		}
		toDistribute = uint32(1)<<log - distributed
	}
	if int(distributed) == len(counts) {
		best := 0
		for s, c := range counts {
			if c > counts[best] {
				best = s
			}
		}
		norm[best] += int16(toDistribute)
		return
	}
	if total == 0 {
		for s := 0; toDistribute > 0; s = (s + 1) % len(counts) {
			if norm[s] > 0 {
				toDistribute--
				norm[s]++
			}
		}
		return
	}
	vStepLog := 62 - log
	mid := uint64(1)<<(vStepLog-1) - 1
	rStep := (uint64(1)<<vStepLog*uint64(toDistribute) + mid) / uint64(total)
	tmp := mid
	for s, c := range counts {
		if norm[s] == unset {
			end := tmp + uint64(c)*rStep
			norm[s] = int16(end>>vStepLog - tmp>>vStepLog)
			tmp = end
		}
	}
}

// writeNCount writes normalized counts (RFC 8878 4.1.1), the inverse of
// readNCount.
func writeNCount(out []byte, norm []int16, log uint) []byte {
	size := int32(1) << log
	var acc uint32 = uint32(log - 5)
	nbits := uint(4)
	remaining := size + 1
	threshold := size
	nb := log + 1
	prev0 := false
	sym := 0
	for sym < len(norm) && remaining > 1 {
		if prev0 {
			start := sym
			for sym < len(norm) && norm[sym] == 0 {
				sym++
			}
			if sym == len(norm) {
				break
			}
			for sym >= start+24 {
				start += 24
				acc |= 0xffff << nbits
				out = append(out, byte(acc), byte(acc>>8))
				acc >>= 16
			}
			for sym >= start+3 {
				start += 3
				acc |= 3 << nbits
				nbits += 2
			}
			acc |= uint32(sym-start) << nbits
			nbits += 2
			if nbits > 16 {
				out = append(out, byte(acc), byte(acc>>8))
				acc >>= 16
				nbits -= 16
			}
		}
		count := int32(norm[sym])
		sym++
		maxSmall := 2*threshold - 1 - remaining
		if count < 0 {
			remaining += count
		} else {
			remaining -= count
		}
		count++
		if count >= threshold {
			count += maxSmall
		}
		acc |= uint32(count) << nbits
		nbits += nb
		if count < maxSmall {
			nbits--
		}
		prev0 = count == 1
		for remaining < threshold {
			nb--
			threshold >>= 1
		}
		if nbits > 16 {
			out = append(out, byte(acc), byte(acc>>8))
			acc >>= 16
			nbits -= 16
		}
	}
	for nbits > 0 {
		out = append(out, byte(acc))
		acc >>= 8
		nbits = uint(max(int(nbits)-8, 0))
	}
	return out
}

// fseEnc is an FSE encoding table (libzstd's FSE_CTable). States run from
// 1<<log to 2<<log.
type fseEnc struct {
	log   uint
	state [1 << maxLLLog]uint16
	// per symbol, libzstd's deltaNbBits | deltaFindState << 32, so one
	// load gets both
	tt [256]uint64
}

func (e *fseEnc) set(s int, nbits uint32, find int32) {
	e.tt[s] = uint64(nbits) | uint64(uint32(find))<<32
}

func (e *fseEnc) build(norm []int16, log uint) {
	size := 1 << log
	high := size - 1
	var cumul [257]int
	var syms [1 << maxLLLog]uint8
	for s, c := range norm {
		if c == -1 {
			cumul[s+1] = cumul[s] + 1
			syms[high] = uint8(s)
			high--
		} else {
			cumul[s+1] = cumul[s] + int(c)
		}
	}
	step := size>>1 + size>>3 + 3
	pos := 0
	for s, c := range norm {
		for range max(int(c), 0) {
			syms[pos] = uint8(s)
			pos = (pos + step) & (size - 1)
			for pos > high {
				pos = (pos + step) & (size - 1)
			}
		}
	}
	for u := range size {
		s := syms[u]
		e.state[cumul[s]] = uint16(size + u)
		cumul[s]++
	}
	// symbols past norm are absent too: encoding one is a bug, but its
	// bits stay within log+1, the bound encodeSeqBits reserves space by
	for s := len(norm); s < len(e.tt); s++ {
		e.set(s, uint32(log+1)<<16-uint32(size), 0)
	}
	total := int32(0)
	for s, c := range norm {
		switch c {
		case 0:
			e.set(s, uint32(log+1)<<16-uint32(size), 0)
		case -1, 1:
			e.set(s, uint32(log)<<16-uint32(size), total-1)
			total++
		default:
			maxOut := log - uint(bits.Len32(uint32(c-1))-1)
			e.set(s, uint32(maxOut)<<16-uint32(c)<<maxOut, total-int32(c))
			total += int32(c)
		}
	}
	e.log = log
}

// init returns the state that encodes sym last.
func (e *fseEnc) init(sym uint8) uint32 {
	t := e.tt[sym]
	nb := (uint32(t) + 1<<15) >> 16
	v := nb<<16 - uint32(t)
	return uint32(e.state[(int32(v>>nb)+int32(t>>32))&(1<<maxLLLog-1)])
}

func (e *fseEnc) encode(w *zbitWriter, state uint32, sym uint8) uint32 {
	t := e.tt[sym]
	nb := (state + uint32(t)) >> 16
	w.add(uint64(state&(1<<nb-1)), uint(nb))
	return uint32(e.state[(int32(state>>nb)+int32(t>>32))&(1<<maxLLLog-1)])
}

func (e *fseEnc) flushState(w *zbitWriter, state uint32) {
	w.add(uint64(state&(1<<e.log-1)), e.log)
}

// hufEnc is a Huffman code for literals: code lengths of at most maxHufLog
// bits, assigned canonically as hufTable.build decodes them.
type hufEnc struct {
	nb     [256]uint8
	code   [256]uint16
	maxSym int
	log    uint
}

// build makes a code for counts, of which counts[maxSym] is the last
// nonzero one. At least two symbols must be present.
func (h *hufEnc) build(counts *[256]uint32, maxSym int) {
	h.maxSym = maxSym
	// two-queue Huffman over the present symbols, sorted by count
	type node struct {
		count  uint32
		parent int16
	}
	// sort the present symbols by count, ties by symbol
	var keys [256]uint64
	n := 0
	for s := 0; s <= maxSym; s++ {
		if counts[s] > 0 {
			keys[n] = uint64(counts[s])<<8 | uint64(s)
			n++
		}
	}
	slices.Sort(keys[:n])
	var syms [256]uint8
	for i, k := range keys[:n] {
		syms[i] = uint8(k)
	}
	sort := syms[:n]
	var nodes [512]node
	for i, s := range sort {
		nodes[i] = node{count: counts[s], parent: -1}
	}
	leaf, inner, next := 0, n, n
	pick := func() int {
		if leaf < n && (inner >= next || nodes[leaf].count <= nodes[inner].count) {
			leaf++
			return leaf - 1
		}
		inner++
		return inner - 1
	}
	for next < 2*n-1 {
		a, b := pick(), pick()
		nodes[next] = node{count: nodes[a].count + nodes[b].count, parent: -1}
		nodes[a].parent = int16(next)
		nodes[b].parent = int16(next)
		next++
	}
	var depth [512]uint8
	for i := 2*n - 3; i >= 0; i-- {
		depth[i] = depth[nodes[i].parent] + 1
	}
	var lens [256]int
	for i, s := range sort {
		lens[s] = int(depth[i])
	}
	limitLengths(&lens, counts, sort, maxHufLog)

	var rank [maxHufLog + 2]uint32
	log := 0
	for _, s := range sort {
		log = max(log, lens[s])
	}
	for s := range h.nb {
		h.nb[s] = 0
	}
	for _, s := range sort {
		h.nb[s] = uint8(lens[s])
		rank[log+1-lens[s]]++
	}
	var start [maxHufLog + 2]uint32
	next32 := uint32(0)
	for w := 1; w <= log; w++ {
		start[w] = next32
		next32 += rank[w] << (w - 1)
	}
	for s := 0; s <= maxSym; s++ {
		if h.nb[s] == 0 {
			continue
		}
		w := log + 1 - int(h.nb[s])
		h.code[s] = uint16(start[w] >> (w - 1))
		start[w] += 1 << (w - 1)
	}
	h.log = uint(log)
}

// limitLengths caps code lengths at limit and keeps the code complete:
// it lengthens the rarest symbols until the Kraft sum fits, then shortens
// the most frequent while it stays within it.
func limitLengths(lens *[256]int, counts *[256]uint32, byCount []uint8, limit int) {
	kraft := 0
	for _, s := range byCount {
		lens[s] = min(lens[s], limit)
		kraft += 1 << (limit - lens[s])
	}
	full := 1 << limit
	for kraft > full {
		for _, s := range byCount {
			if lens[s] < limit {
				lens[s]++
				kraft -= 1 << (limit - lens[s])
				break
			}
		}
	}
	for kraft < full {
		done := true
		for i := len(byCount) - 1; i >= 0; i-- {
			s := byCount[i]
			if gain := 1 << (limit - lens[s]); lens[s] > 1 && kraft+gain <= full {
				lens[s]--
				kraft += gain
				done = false
				break
			}
		}
		if done {
			break
		}
	}
}

// writeTable writes the tree description (RFC 8878 4.2.1): the weights of
// symbols 0 to maxSym-1, compressed with FSE when that is smaller or when
// there are too many for the direct form. ok is false if neither fits.
func (h *hufEnc) writeTable(out []byte) (_ []byte, ok bool) {
	var w [256]uint8
	n := h.maxSym
	for s := range n {
		if h.nb[s] > 0 {
			w[s] = uint8(h.log + 1 - uint(h.nb[s]))
		}
	}
	start := len(out)
	if n > 2 {
		out = append(out, 0)
		out = fseWeights(out, w[:n])
		if c := len(out) - start - 1; c > 0 && c < 128 && (n > 128 || c < (n+1)/2) {
			out[start] = byte(c)
			return out, true
		}
		out = out[:start]
	}
	if n > 128 {
		return out, false
	}
	out = append(out, byte(127+n))
	for i := 0; i < n; i += 2 {
		out = append(out, w[i]<<4|w[i+1])
	}
	return out, true
}

// fseWeights compresses Huffman weights with FSE, two interleaved states,
// as libzstd's HUF_compressWeights does. It returns out unchanged if the
// weights do not compress.
func fseWeights(out []byte, w []uint8) []byte {
	var counts [16]uint32
	maxW := 0
	for _, x := range w {
		counts[x]++
		maxW = max(maxW, int(x))
	}
	for _, c := range counts {
		if int(c) == len(w) {
			return out
		}
	}
	log := optimalTableLog(6, len(w), maxW)
	var norm [16]int16
	normalizeCounts(norm[:maxW+1], counts[:maxW+1], uint32(len(w)), log, false)
	start := len(out)
	out = writeNCount(out, norm[:maxW+1], log)
	var e fseEnc
	e.build(norm[:maxW+1], log)
	bw := zbitWriter{out: out}
	i := len(w)
	var s1, s2 uint32
	if i&1 != 0 {
		s1 = e.init(w[i-1])
		s2 = e.init(w[i-2])
		s1 = e.encode(&bw, s1, w[i-3])
		bw.flush()
		i -= 3
	} else {
		s2 = e.init(w[i-1])
		s1 = e.init(w[i-2])
		i -= 2
	}
	if (len(w)-2)&2 != 0 {
		s2 = e.encode(&bw, s2, w[i-1])
		s1 = e.encode(&bw, s1, w[i-2])
		bw.flush()
		i -= 2
	}
	for i > 0 {
		s2 = e.encode(&bw, s2, w[i-1])
		s1 = e.encode(&bw, s1, w[i-2])
		s2 = e.encode(&bw, s2, w[i-3])
		s1 = e.encode(&bw, s1, w[i-4])
		bw.flush()
		i -= 4
	}
	e.flushState(&bw, s2)
	e.flushState(&bw, s1)
	bw.close()
	if len(bw.out) <= start {
		return out[:start]
	}
	return bw.out
}

// encode1 writes src as one Huffman stream, last symbol first.
func (h *hufEnc) encode1(out []byte, src []byte) []byte {
	w := zbitWriter{out: out}
	i := len(src)
	for ; i&3 != 0; i-- {
		s := src[i-1]
		w.add(uint64(h.code[s]), uint(h.nb[s]))
	}
	w.flush()
	// 4 codes of at most 11 bits fit with the at most 7 bits left over
	for ; i > 0; i -= 4 {
		s := src[i-1]
		w.add(uint64(h.code[s]), uint(h.nb[s]))
		s = src[i-2]
		w.add(uint64(h.code[s]), uint(h.nb[s]))
		s = src[i-3]
		w.add(uint64(h.code[s]), uint(h.nb[s]))
		s = src[i-4]
		w.add(uint64(h.code[s]), uint(h.nb[s]))
		w.flush()
	}
	w.close()
	return w.out
}

// encode4 writes src as four Huffman streams after their jump table. ok is
// false if a stream exceeds the 65535 bytes the table can hold.
func (h *hufEnc) encode4(out []byte, src []byte) (_ []byte, ok bool) {
	seg := (len(src) + 3) / 4
	jump := len(out)
	out = append(out, 0, 0, 0, 0, 0, 0)
	for k := range 4 {
		start := len(out)
		out = h.encode1(out, src[min(k*seg, len(src)):min((k+1)*seg, len(src))])
		n := len(out) - start
		if n > 65535 {
			return out, false
		}
		if k < 3 {
			binary.LittleEndian.PutUint16(out[jump+2*k:], uint16(n))
		}
	}
	return out, true
}
