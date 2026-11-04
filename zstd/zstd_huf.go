package zstd

import (
	"encoding/binary"
	"math/bits"
	"unsafe"
)

// revReader reads a zstd backward bitstream (RFC 8878 4.1): the encoder
// writes bits forward and ends the stream with a 1 bit, and the decoder reads
// them back from the end. c holds the 8 bytes at src[p:], and consumed counts
// the bits of c already read, from the top.
type revReader struct {
	src      []byte
	start, p int
	c        uint64
	consumed uint
}

func (r *revReader) init(src []byte, start, end int) error {
	if end <= start || end > len(src) {
		return ErrCorrupt
	}
	last := src[end-1]
	if last == 0 {
		return ErrCorrupt
	}
	r.src, r.start = src, start
	r.consumed = uint(bits.LeadingZeros8(last)) + 1
	if end-start >= 8 {
		r.p = end - 8
		r.c = binary.LittleEndian.Uint64(src[r.p:])
		return nil
	}
	// a short stream sits in the low bytes, below bytes counted as read
	r.p = start
	r.c = 0
	for i := end - 1; i >= start; i-- {
		r.c = r.c<<8 | uint64(src[i])
	}
	r.consumed += uint(8-(end-start)) * 8
	return nil
}

// reload refills c so that at least 57 bits are unread, unless the stream
// start is near. Past the stream start, reads return zeros and consumed
// grows beyond 64, which done reports.
func (r *revReader) reload() {
	if r.consumed > 64 {
		return
	}
	if r.p >= r.start+8 {
		r.p -= int(r.consumed >> 3)
		r.consumed &= 7
		r.c = binary.LittleEndian.Uint64(r.src[r.p:])
		return
	}
	if r.p == r.start {
		return
	}
	n := int(r.consumed >> 3)
	if r.p-n < r.start {
		n = r.p - r.start
	}
	r.p -= n
	r.consumed -= uint(n) * 8
	r.c = binary.LittleEndian.Uint64(r.src[r.p:])
}

func (r *revReader) read(n uint) uint64 {
	v := (r.c << r.consumed) >> 1 >> (63 - n)
	r.consumed += n
	return v
}

// bitsLeft is the number of unread bits; negative after reading past the
// start.
func (r *revReader) bitsLeft() int {
	return (r.p-r.start)*8 + 64 - int(r.consumed)
}

// done reports whether every bit was read and none beyond.
func (r *revReader) done() bool {
	return r.p == r.start && r.consumed == 64
}

const maxHufLog = 11

// hufTable is a single-symbol Huffman decoding table: entry i, for the next
// log bits i, holds the code length << 8 | the symbol.
type hufTable struct {
	log uint
	t   [1 << maxHufLog]uint16
}

// readHufTable reads a Huffman tree description (RFC 8878 4.2.1) from the
// start of src and returns the number of bytes read.
func (h *hufTable) read(src []byte) (int, error) {
	if len(src) == 0 {
		return 0, errEOF
	}
	hdr := int(src[0])
	var w [256]uint8
	var count int
	n := 1
	if hdr < 128 {
		// weights compressed with FSE, two interleaved states
		if 1+hdr > len(src) {
			return 0, errEOF
		}
		ws := src[1 : 1+hdr]
		var norm [256]int16
		log, nc, err := readNCount(ws, &norm, 255, 6)
		if err != nil {
			return 0, err
		}
		var syms, nb [64]uint8
		var base [64]uint16
		if err := buildFSE(norm[:256], log, syms[:], nb[:], base[:]); err != nil {
			return 0, err
		}
		var br revReader
		if err := br.init(ws, nc, len(ws)); err != nil {
			return 0, err
		}
		s1 := br.read(log)
		s2 := br.read(log)
		for {
			br.reload()
			if count > 254 {
				return 0, ErrCorrupt
			}
			w[count] = syms[s1]
			count++
			if br.bitsLeft() < int(nb[s1]) {
				w[count] = syms[s2]
				count++
				break
			}
			s1 = uint64(base[s1]) + br.read(uint(nb[s1]))
			br.reload()
			if count > 254 {
				return 0, ErrCorrupt
			}
			w[count] = syms[s2]
			count++
			if br.bitsLeft() < int(nb[s2]) {
				w[count] = syms[s1]
				count++
				break
			}
			s2 = uint64(base[s2]) + br.read(uint(nb[s2]))
		}
		n += hdr
	} else {
		count = hdr - 127
		nbytes := (count + 1) / 2
		if 1+nbytes > len(src) {
			return 0, errEOF
		}
		for i := 0; i < count; i += 2 {
			b := src[1+i/2]
			w[i] = b >> 4
			w[i+1] = b & 15
		}
		n += nbytes
	}
	return n, h.build(w[:], count)
}

// build makes the table from count explicit weights; the last weight is
// implied by the total.
func (h *hufTable) build(w []uint8, count int) error {
	var rank [13]uint32
	total := uint32(0)
	for _, x := range w[:count] {
		if x > maxHufLog {
			return ErrCorrupt
		}
		rank[x]++
		if x > 0 {
			total += 1 << (x - 1)
		}
	}
	if total == 0 {
		return ErrCorrupt
	}
	log := uint(bits.Len32(total))
	if log > maxHufLog {
		return ErrCorrupt
	}
	left := uint32(1)<<log - total
	if left == 0 || left&(left-1) != 0 || count >= 256 {
		return ErrCorrupt
	}
	lastW := uint8(bits.Len32(left))
	w[count] = lastW
	count++
	rank[lastW]++
	if rank[1] < 2 || rank[1]&1 != 0 {
		return ErrCorrupt
	}
	var start [13]uint32
	next := uint32(0)
	for x := uint(1); x <= log; x++ {
		start[x] = next
		next += rank[x] << (x - 1)
	}
	for s, x := range w[:count] {
		if x == 0 {
			continue
		}
		e := uint16(log+1-uint(x))<<8 | uint16(s)
		n := uint32(1) << (x - 1)
		for i := start[x]; i < start[x]+n; i++ {
			h.t[i] = e
		}
		start[x] += n
	}
	h.log = log
	return nil
}

// finish decodes out[o:] from br one symbol at a time and checks that the
// stream ends exactly there.
func (h *hufTable) finish(br *revReader, out []byte, o int) error {
	log := h.log
	for ; o < len(out); o++ {
		br.reload()
		e := h.t[(br.c<<br.consumed)>>(64-log)]
		out[o] = byte(e)
		br.consumed += uint(e >> 8)
	}
	br.reload()
	if !br.done() {
		return ErrCorrupt
	}
	return nil
}

func hufAt(t unsafe.Pointer, c uint64, n, shift uint) uint16 {
	return *(*uint16)(unsafe.Add(t, ((c<<(n&63))>>shift)*2))
}

// decode1 decodes one Huffman stream src[start:end] into out, which it fills
// exactly.
//
// The fast loops index through unsafe pointers: table indexes have log bits,
// and they run only while each output has room for the symbols of one round
// and each stream has 8 bytes before its position to reload from.
func (h *hufTable) decode1(out []byte, src []byte, start, end int) error {
	var br revReader
	if err := br.init(src, start, end); err != nil {
		return err
	}
	tp := unsafe.Pointer(&h.t)
	inp := unsafe.Pointer(unsafe.SliceData(src))
	outp := unsafe.Pointer(unsafe.SliceData(out))
	shift := (64 - h.log) & 63
	c, n, p := br.c, br.consumed, br.p
	o := 0
	// 5 symbols of at most 11 bits fit in the 57 bits after a reload
	for o+5 <= len(out) && p >= start+8 {
		p -= int(n >> 3)
		n &= 7
		c = load64(inp, p)
		for k := range 5 {
			e := hufAt(tp, c, n, shift)
			*(*byte)(unsafe.Add(outp, o+k)) = byte(e)
			n += uint(e >> 8)
		}
		o += 5
	}
	br.c, br.consumed, br.p = c, n, p
	return h.finish(&br, out, o)
}

// decode4 decodes the four streams of src (RFC 8878 3.1.1.3.1.6) into out,
// interleaved so their table loads overlap.
func (h *hufTable) decode4(out []byte, src []byte) error {
	if len(src) < 10 || len(out) < 6 {
		return ErrCorrupt
	}
	e1 := 6 + int(binary.LittleEndian.Uint16(src))
	e2 := e1 + int(binary.LittleEndian.Uint16(src[2:]))
	e3 := e2 + int(binary.LittleEndian.Uint16(src[4:]))
	if e3 >= len(src) {
		return ErrCorrupt
	}
	seg := (len(out) + 3) / 4
	if 3*seg > len(out) {
		return ErrCorrupt
	}
	bounds := [5]int{6, e1, e2, e3, len(src)}
	var br [4]revReader
	for k := range br {
		if err := br[k].init(src, bounds[k], bounds[k+1]); err != nil {
			return err
		}
	}
	tp := unsafe.Pointer(&h.t)
	inp := unsafe.Pointer(unsafe.SliceData(src))
	o1 := unsafe.Pointer(unsafe.SliceData(out))
	o2 := unsafe.Add(o1, seg)
	o3 := unsafe.Add(o1, 2*seg)
	o4 := unsafe.Add(o1, 3*seg)
	shift := (64 - h.log) & 63
	i := 0
	if useHufAsm {
		i = h.decode4Asm(&br, bounds, src, out, seg)
	}
	c1, n1, p1 := br[0].c, br[0].consumed, br[0].p
	c2, n2, p2 := br[1].c, br[1].consumed, br[1].p
	c3, n3, p3 := br[2].c, br[2].consumed, br[2].p
	c4, n4, p4 := br[3].c, br[3].consumed, br[3].p
	// the fourth segment is the shortest, so its room bounds all four
	for 3*seg+i+5 <= len(out) && p1 >= bounds[0]+8 && p2 >= bounds[1]+8 &&
		p3 >= bounds[2]+8 && p4 >= bounds[3]+8 {
		p1 -= int(n1 >> 3)
		n1 &= 7
		c1 = load64(inp, p1)
		p2 -= int(n2 >> 3)
		n2 &= 7
		c2 = load64(inp, p2)
		p3 -= int(n3 >> 3)
		n3 &= 7
		c3 = load64(inp, p3)
		p4 -= int(n4 >> 3)
		n4 &= 7
		c4 = load64(inp, p4)
		for k := range 5 {
			e := hufAt(tp, c1, n1, shift)
			*(*byte)(unsafe.Add(o1, i+k)) = byte(e)
			n1 += uint(e >> 8)
			e = hufAt(tp, c2, n2, shift)
			*(*byte)(unsafe.Add(o2, i+k)) = byte(e)
			n2 += uint(e >> 8)
			e = hufAt(tp, c3, n3, shift)
			*(*byte)(unsafe.Add(o3, i+k)) = byte(e)
			n3 += uint(e >> 8)
			e = hufAt(tp, c4, n4, shift)
			*(*byte)(unsafe.Add(o4, i+k)) = byte(e)
			n4 += uint(e >> 8)
		}
		i += 5
	}
	br[0].c, br[0].consumed, br[0].p = c1, n1, p1
	br[1].c, br[1].consumed, br[1].p = c2, n2, p2
	br[2].c, br[2].consumed, br[2].p = c3, n3, p3
	br[3].c, br[3].consumed, br[3].p = c4, n4, p4
	for k := range 4 {
		end := min((k+1)*seg, len(out))
		if err := h.finish(&br[k], out[k*seg:end], i); err != nil {
			return err
		}
	}
	return nil
}
