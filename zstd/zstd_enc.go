package zstd

import (
	"encoding/binary"
	"math"
	"math/bits"
	"runtime"
	"sync"
	"unsafe"
)

// encSeq is one sequence to encode: literal length, match length, and the
// offset. sequences turns the offset into its offset value: 1-3 for a
// repeat offset, else the offset plus 3.
type encSeq struct {
	ll, ml, off uint32
}

// llCodeOf and mlCodeOf map small literal lengths and match lengths minus 3
// to their codes; larger ones take the code from their highest bit.
var llCodeOf [64]uint8
var mlCodeOf [128]uint8

func init() {
	for c := range llCodeBase {
		for v := llCodeBase[c]; v < llCodeBase[c]+1<<llCodeBits[c] && v < 64; v++ {
			llCodeOf[v] = uint8(c)
		}
	}
	for c := range mlCodeBase {
		for v := mlCodeBase[c] - 3; v < mlCodeBase[c]-3+1<<mlCodeBits[c] && v < 128; v++ {
			mlCodeOf[v] = uint8(c)
		}
	}
}

func llCode(ll uint32) uint8 {
	if ll < 64 {
		return llCodeOf[ll]
	}
	return uint8(bits.Len32(ll)-1) + 19
}

func mlCode(ml uint32) uint8 {
	if ml-3 < 128 {
		return mlCodeOf[ml-3]
	}
	return uint8(bits.Len32(ml-3)-1) + 36
}

// The predefined tables, for encoding.
var predefEnc [3]fseEnc

func init() {
	predefEnc[seqLL].build(predefLLNorm, predefLLLog)
	predefEnc[seqOF].build(predefOFNorm, predefOFLog)
	predefEnc[seqML].build(predefMLNorm, predefMLLog)
}

// zenc is the state of a zstd encoder.
type zenc struct {
	level     int
	windowLog uint
	window    int
	hashLog   uint
	table     []uint64 // per hash: the last position with it, and its first 4 bytes << 32
	longLog   uint
	long      []uint64 // the same for 8-byte hashes, at level 3 and up
	base      int      // table positions count from here
	lits      []byte
	seqs      []encSeq
	rep       [3]uint32 // repeat offsets the matchers try first
	erep      [3]int    // the decoder's repeat offsets, after the blocks sent
	huf       hufEnc
	codes     []uint32 // per sequence: literal length | match length << 8 | offset code << 16
	hdr       []byte
	hash      xxh64
	// tabs holds the table the decoder has for each kind of code, for
	// repeat mode; a block builds new ones in spare and swaps them in when
	// it is sent compressed
	tabs, spare [3]*seqTab
	modes       [3]byte
}

// seqTab is a sequence code table the decoder may hold.
type seqTab struct {
	ok   bool
	log  uint
	norm [maxMLSym + 1]int16
	enc  *fseEnc // own, or a predefined table
	own  fseEnc
}

var zencPool sync.Pool

// Encode appends a zstd frame holding src to dst and returns it. Levels below
// 3 use zstd's fast strategy; levels 3 and up use its double fast strategy,
// which compresses better and slower, as zstd -3 does. Higher levels do not
// compress more.
func Encode(dst, src []byte, level int) []byte {
	e, _ := zencPool.Get().(*zenc)
	if e == nil {
		e = &zenc{}
	}
	defer zencPool.Put(e)
	e.reset(level)
	return e.frame(dst, src)
}

func (e *zenc) reset(level int) {
	e.level = level
	e.windowLog = 21
	e.hashLog, e.longLog = 15, 0
	if level >= 3 {
		// 256 KB and 1 MB of entries, within the L2 cache with room;
		// a 512 KB short table was 3% slower on a binary tar
		e.hashLog, e.longLog = 15, 17
		// an 8 MB window raised the ratio of a source tar from 4.61
		// to 4.71 at the same speed
		e.windowLog = 23
	}
	e.window = 1 << e.windowLog
	e.table = resetTable(e.table, e.hashLog)
	if e.longLog > 0 {
		e.long = resetTable(e.long, e.longLog)
	}
	e.rep = [3]uint32{1, 4, 8}
	e.erep = [3]int{1, 4, 8}
	e.base = 0
	e.hash.reset()
	for k := range e.tabs {
		if e.tabs[k] == nil {
			e.tabs[k], e.spare[k] = new(seqTab), new(seqTab)
		}
		e.tabs[k].ok = false
	}
}

// Once a block starts this far past e.base, the tables move e.base up to
// a window before it; tests lower it.
var rebaseAt = 1 << 30

// rebase moves e.base shift bytes on, keeping a window of history.
func (e *zenc) rebase(shift int) {
	e.shiftTables(shift)
	e.base += shift
}

// shiftTables moves the positions in the tables shift bytes back; those
// before the start become out of every window.
func (e *zenc) shiftTables(shift int) {
	for _, t := range [2][]uint64{e.table, e.long} {
		for i, x := range t {
			pos := max(int64(int32(x))-int64(shift), -1<<30)
			t[i] = x&^0xffffffff | uint64(uint32(int32(pos)))
		}
	}
}

func resetTable(t []uint64, log uint) []uint64 {
	if cap(t) < 1<<log {
		t = make([]uint64, 1<<log)
	}
	t = t[:1<<log]
	// an entry at position -1<<30 is out of every window
	for i := range t {
		t[i] = 0xc0000000 // int32(-1 << 30)
	}
	return t
}

// frame writes the frame header, the blocks and the checksum.
func (e *zenc) frame(out, src []byte) []byte {
	out = binary.LittleEndian.AppendUint32(out, zstdMagic)
	n := uint64(len(src))
	single := n <= uint64(e.window)
	var fhd byte = 1 << 2 // checksum
	var fcs []byte
	switch {
	case single && n < 256:
		fcs = []byte{byte(n)}
	case n < 256:
		fcs = binary.LittleEndian.AppendUint32(nil, uint32(n))
		fhd |= 2 << 6
	case n < 65536+256:
		fcs = binary.LittleEndian.AppendUint16(nil, uint16(n-256))
		fhd |= 1 << 6
	case n < 1<<32:
		fcs = binary.LittleEndian.AppendUint32(nil, uint32(n))
		fhd |= 2 << 6
	default:
		fcs = binary.LittleEndian.AppendUint64(nil, n)
		fhd |= 3 << 6
	}
	if single {
		fhd |= 1 << 5
		out = append(out, fhd)
	} else {
		out = append(out, fhd, byte((e.windowLog-10)<<3))
	}
	out = append(out, fcs...)

	if len(src) >= pipeMinSize && runtime.GOMAXPROCS(0) > 1 {
		out = e.blocksPipelined(out, src)
	} else {
		for i := 0; ; i += zstdBlockMax {
			end := min(i+zstdBlockMax, len(src))
			e.rebaseFor(i)
			out = e.block(out, src, i, end, end == len(src))
			if end == len(src) {
				break
			}
		}
		e.hash.write(src)
	}
	return binary.LittleEndian.AppendUint32(out, uint32(e.hash.sum()))
}

// rebaseFor moves e.base up before the block at start if needed.
func (e *zenc) rebaseFor(start int) {
	if shift := start - e.window - e.base; start-e.base > rebaseAt && shift > 0 {
		e.rebase(shift)
	}
}

// encBlk is one block between find and emit.
type encBlk struct {
	lits []byte
	seqs []encSeq
	blk  []byte
	run  bool
	last bool
}

// blocksPipelined writes the blocks of src on two goroutines: this one
// finds the sequences of the next blocks while another emits the current
// one and hashes it.
func (e *zenc) blocksPipelined(out, src []byte) []byte {
	free := make(chan *encBlk, pipeDepth+2)
	for range pipeDepth + 2 {
		free <- &encBlk{}
	}
	ch := make(chan *encBlk, pipeDepth)
	done := make(chan []byte)
	go func() {
		for b := range ch {
			out = e.emit(out, b.blk, b.lits, b.seqs, b.run, b.last)
			e.hash.write(b.blk)
			free <- b
		}
		done <- out
	}()
	for i := 0; ; i += zstdBlockMax {
		end := min(i+zstdBlockMax, len(src))
		b := <-free
		b.blk, b.last = src[i:end], end == len(src)
		b.run = len(b.blk) > 0 && isRun(b.blk)
		if !b.run {
			e.rebaseFor(i)
			// find appends to e.lits and e.seqs: lend it the block's
			e.lits, e.seqs = b.lits, b.seqs
			e.find(src, i, end)
			b.lits, b.seqs = e.lits, e.seqs
		}
		ch <- b
		if b.last {
			break
		}
	}
	close(ch)
	return <-done
}

// block writes the block src[start:end]; src[:start] is its history.
func (e *zenc) block(out, src []byte, start, end int, last bool) []byte {
	blk := src[start:end]
	run := len(blk) > 0 && isRun(blk)
	if !run {
		e.find(src, start, end)
	}
	return e.emit(out, blk, e.lits, e.seqs, run, last)
}

// find fills e.lits and e.seqs with the literals and sequences of
// src[start:end].
func (e *zenc) find(src []byte, start, end int) {
	e.lits = e.lits[:0]
	e.seqs = e.seqs[:0]
	// the matchers see positions from e.base, which keeps them in the
	// int32 the tables hold
	b := e.base
	switch {
	case start == end:
	case e.level >= 3:
		e.matchDouble(src[b:], start-b, end-b)
	default:
		e.matchFast(src[b:], start-b, end-b)
	}
}

// emit writes the block blk from its literals and sequences: compressed,
// or raw when that is not smaller, or RLE for a run. It keeps the
// entropy state, apart from what find uses, so it can run on another
// goroutine.
func (e *zenc) emit(out, blk, lits []byte, seqs []encSeq, run, last bool) []byte {
	var lastBit uint32
	if last {
		lastBit = 1
	}
	if run {
		out = appendBlockHeader(out, lastBit|1<<1|uint32(len(blk))<<3)
		return append(out, blk[0])
	}
	reps := e.erep
	hdr := len(out)
	out = append(out, 0, 0, 0)
	out = e.literals(out, lits)
	out = e.sequences(out, seqs)
	if size := len(out) - hdr - 3; size < len(blk) {
		h := lastBit | 2<<1 | uint32(size)<<3
		out[hdr], out[hdr+1], out[hdr+2] = byte(h), byte(h>>8), byte(h>>16)
		if len(seqs) > 0 {
			for k, m := range e.modes {
				if m != 3 {
					e.tabs[k], e.spare[k] = e.spare[k], e.tabs[k]
				}
			}
		}
		return out
	}
	// not smaller: store it, and forget the offsets the sequences set
	e.erep = reps
	out = appendBlockHeader(out[:hdr], lastBit|uint32(len(blk))<<3)
	return append(out, blk...)
}

func appendBlockHeader(out []byte, h uint32) []byte {
	return append(out, byte(h), byte(h>>8), byte(h>>16))
}

func isRun(b []byte) bool {
	c := b[0]
	for _, x := range b {
		if x != c {
			return false
		}
	}
	return true
}

func appendLitHeader(out []byte, typ, size int) []byte {
	switch {
	case size < 32:
		return append(out, byte(typ|size<<3))
	case size < 4096:
		h := typ | 1<<2 | size<<4
		return append(out, byte(h), byte(h>>8))
	default:
		h := typ | 3<<2 | size<<4
		return append(out, byte(h), byte(h>>8), byte(h>>16))
	}
}

// literals writes the literals section (RFC 8878 3.1.1.3.1): Huffman
// coded when that is smaller, else raw, or RLE for a run.
func (e *zenc) literals(out, lits []byte) []byte {
	n := len(lits)
	if n == 0 {
		return append(out, 0)
	}
	var counts [256]uint32
	for _, c := range lits {
		counts[c]++
	}
	maxSym := 255
	for counts[maxSym] == 0 {
		maxSym--
	}
	if int(counts[maxSym]) == n {
		return append(appendLitHeader(out, 1, n), lits[0])
	}
	start := len(out)
	if n >= 64 {
		e.huf.build(&counts, maxSym)
		four := n >= 256
		hl := 3
		if four && n >= 1024 {
			hl = 4
			if n >= 16384 {
				hl = 5
			}
		}
		out = append(out, make([]byte, hl)...)
		var ok bool
		out, ok = e.huf.writeTable(out)
		if ok {
			if four {
				out, ok = e.huf.encode4(out, lits)
			} else {
				out = e.huf.encode1(out, lits)
			}
		}
		csize := len(out) - start - hl
		if ok && hl == 3 && csize >= 1024 {
			ok = false
		}
		if ok && csize+hl < n+3 {
			sf := 0
			if four {
				sf = hl - 2
			}
			sizeBits := uint(4*hl - 2) // 10, 14 or 18 bits per size
			v := uint64(2) | uint64(sf)<<2 | uint64(n)<<4 | uint64(csize)<<(4+sizeBits)
			for i := range hl {
				out[start+i] = byte(v >> (8 * i))
			}
			return out
		}
		out = out[:start]
	}
	return append(appendLitHeader(out, 0, n), lits...)
}

// sequences writes the sequences section (RFC 8878 3.1.1.3.2).
func (e *zenc) sequences(out []byte, seqs []encSeq) []byte {
	n := len(seqs)
	switch {
	case n < 128:
		out = append(out, byte(n))
	case n < 0x7f00:
		out = append(out, byte(n>>8+128), byte(n))
	default:
		out = append(out, 255, byte(n-0x7f00), byte((n-0x7f00)>>8))
	}
	if n == 0 {
		return out
	}
	if cap(e.codes) < n {
		e.codes = make([]uint32, n, max(n, 1<<12))
	}
	codes := e.codes[:n]
	var counts [3][64]uint32
	// offsets become offset values as offCode does, with the decoder's
	// repeat offsets in locals: a call per sequence cost 5% at level 3
	r0, r1, r2 := uint32(e.erep[0]), uint32(e.erep[1]), uint32(e.erep[2])
	for i := range seqs {
		s := &seqs[i]
		off := s.off
		switch {
		case s.ll > 0 && off == r0:
			s.off = 1
		case s.ll > 0 && off == r1:
			s.off, r1, r0 = 2, r0, r1
		case s.ll > 0 && off == r2:
			s.off, r2, r1, r0 = 3, r1, r0, r2
		case s.ll == 0 && off == r1:
			s.off, r1, r0 = 1, r0, r1
		case s.ll == 0 && off == r2:
			s.off, r2, r1, r0 = 2, r1, r0, r2
		case s.ll == 0 && off == r0-1:
			s.off, r2, r1, r0 = 3, r1, r0, r0-1
		default:
			s.off, r2, r1, r0 = off+3, r1, r0, off
		}
		lc, mc, oc := llCode(s.ll), mlCode(s.ml), uint8(bits.Len32(s.off)-1)
		codes[i] = uint32(lc) | uint32(mc)<<8 | uint32(oc)<<16
		counts[seqLL][lc]++
		counts[seqML][mc&63]++
		counts[seqOF][oc&63]++
	}
	e.erep = [3]int{int(r0), int(r1), int(r2)}
	modes := len(out)
	out = append(out, 0)
	var tabs [3]*fseEnc
	for _, k := range [3]seqKind{seqLL, seqOF, seqML} {
		var mode byte
		mode, tabs[k], out = e.seqTable(out, k, counts[k][:maxSymOf(k)+1], n)
		out[modes] |= mode << (6 - 2*k)
		e.modes[k] = mode
	}

	return encodeSeqBits(out, seqs, codes, tabs[seqLL], tabs[seqML], tabs[seqOF])
}

// encodeSeqBits writes the sequences bitstream, last sequence first, as
// libzstd's ZSTD_encodeSequences does: the decoder reads the initial
// states, then for each sequence the offset, match length and literal
// length extra bits, then the state updates.
//
// It writes through an unsafe pointer into reserved capacity: a sequence
// takes at most 26 state bits and 63 extra bits, 12 bytes, and each flush
// stores 8 bytes.
func encodeSeqBits(out []byte, seqs []encSeq, codes []uint32, llT, mlT, ofT *fseEnc) []byte {
	n := len(seqs)
	if need := len(out) + 12*n + 32; cap(out) < need {
		out = append(out[:cap(out)], make([]byte, need-cap(out))...)[:len(out)]
	}
	_ = codes[n-1]
	p := unsafe.Pointer(unsafe.SliceData(out))
	o := len(out)
	var acc uint64
	var nb uint
	// a flush stores 8 bytes and keeps the at most 7 bits of a partial
	// byte; acc >>= 64 clears it
	c := codes[n-1]
	lc, mc, oc := uint8(c), uint8(c>>8), uint8(c>>16)
	llS := llT.init(lc)
	mlS := mlT.init(mc)
	ofS := ofT.init(oc)
	last := seqs[n-1]
	li, mi := llInfo[lc], mlInfo[mc]
	acc = uint64(last.ll - uint32(li))
	nb = uint(li >> 32)
	acc |= uint64(last.ml-uint32(mi)) << nb
	nb += uint(mi >> 32)
	store64(p, o, acc)
	o += int(nb >> 3)
	acc >>= nb &^ 7
	nb &= 7
	acc |= uint64(last.off-1<<oc) << nb
	nb += uint(oc)
	store64(p, o, acc)
	o += int(nb >> 3)
	acc >>= nb &^ 7
	nb &= 7
	if n >= 2 && useEncAsm {
		ctx := seqEncCtx{
			out: p, o: o, seqs: unsafe.Pointer(unsafe.SliceData(seqs)),
			codes: unsafe.Pointer(unsafe.SliceData(codes)), i: n - 2,
			tabs:   [3]unsafe.Pointer{unsafe.Pointer(llT), unsafe.Pointer(mlT), unsafe.Pointer(ofT)},
			states: [3]uint64{uint64(llS), uint64(mlS), uint64(ofS)},
			acc:    acc, nb: uint64(nb),
			llInfo: unsafe.Pointer(&llInfo), mlInfo: unsafe.Pointer(&mlInfo),
		}
		encodeSeqsAsm(&ctx)
		o, acc, nb = ctx.o, ctx.acc, uint(ctx.nb)
		llS, mlS, ofS = uint32(ctx.states[0]), uint32(ctx.states[1]), uint32(ctx.states[2])
	} else {
		sp := unsafe.Pointer(unsafe.SliceData(seqs))
		cp := unsafe.Pointer(unsafe.SliceData(codes))
		// shift counts are masked so the compiler drops its checks for
		// counts of 64 and up: state bits are at most 10, and nb stays
		// below 64 between flushes
		for i := n - 2; i >= 0; i-- {
			s := *(*encSeq)(unsafe.Add(sp, uintptr(i)*unsafe.Sizeof(encSeq{})))
			c := *(*uint32)(unsafe.Add(cp, uintptr(i)*4))
			lc, mc, oc := uint8(c), uint8(c>>8), uint8(c>>16)
			// state transitions: offset, match length, literal length
			t := ofT.tt[oc]
			k := (ofS + uint32(t)) >> 16 & 31
			acc |= uint64(ofS&(1<<k-1)) << (nb & 63)
			nb += uint(k)
			ofS = uint32(ofT.state[(int32(ofS>>k)+int32(t>>32))&(1<<maxLLLog-1)])
			t = mlT.tt[mc]
			k = (mlS + uint32(t)) >> 16 & 31
			acc |= uint64(mlS&(1<<k-1)) << (nb & 63)
			nb += uint(k)
			mlS = uint32(mlT.state[(int32(mlS>>k)+int32(t>>32))&(1<<maxLLLog-1)])
			t = llT.tt[lc]
			k = (llS + uint32(t)) >> 16 & 31
			acc |= uint64(llS&(1<<k-1)) << (nb & 63)
			nb += uint(k)
			llS = uint32(llT.state[(int32(llS>>k)+int32(t>>32))&(1<<maxLLLog-1)])
			store64(p, o, acc)
			o += int(nb >> 3)
			acc >>= nb &^ 7 & 63
			nb &= 7
			li, mi := llInfo[lc], mlInfo[mc]
			lb, mb := uint(li>>32), uint(mi>>32)
			acc |= uint64(s.ll-uint32(li)) << (nb & 63)
			nb += lb
			acc |= uint64(s.ml-uint32(mi)) << (nb & 63)
			nb += mb
			if nb+uint(oc) >= 64 {
				store64(p, o, acc)
				o += int(nb >> 3)
				acc >>= nb &^ 7 & 63
				nb &= 7
			}
			acc |= uint64(s.off-1<<(oc&31)) << (nb & 63)
			nb += uint(oc)
			store64(p, o, acc)
			o += int(nb >> 3)
			acc >>= nb &^ 7 & 63
			nb &= 7
		}
	}
	acc |= uint64(mlS&(1<<mlT.log-1)) << nb
	nb += mlT.log
	acc |= uint64(ofS&(1<<ofT.log-1)) << nb
	nb += ofT.log
	store64(p, o, acc)
	o += int(nb >> 3)
	acc >>= nb &^ 7
	nb &= 7
	acc |= uint64(llS&(1<<llT.log-1)) << nb
	nb += llT.log
	acc |= 1 << nb // end mark
	nb++
	store64(p, o, acc)
	o += int(nb >> 3)
	acc >>= nb &^ 7
	nb &= 7
	if nb > 0 {
		store64(p, o, acc)
		o++
	}
	return out[:o]
}

// llInfo and mlInfo hold the value base | extra bits << 32 of each code,
// for every byte so a code indexes them without a bounds check.
var llInfo, mlInfo [256]uint64

func init() {
	for c := range llCodeBase {
		llInfo[c] = uint64(llCodeBase[c]) | uint64(llCodeBits[c])<<32
	}
	for c := range mlCodeBase {
		mlInfo[c] = uint64(mlCodeBase[c]) | uint64(mlCodeBits[c])<<32
	}
}

// seqTable picks the table mode for one kind of code, writes the table
// description, and returns the table to encode with: RLE for a single code,
// else the cheapest of the predefined table, the decoder's current table
// (repeat mode), and a new FSE table with its description, by estimated
// bits. A new table goes in e.spare[k].
func (e *zenc) seqTable(out []byte, k seqKind, counts []uint32, n int) (byte, *fseEnc, []byte) {
	maxSym := len(counts) - 1
	for maxSym > 0 && counts[maxSym] == 0 {
		maxSym--
	}
	counts = counts[:maxSym+1]
	nt := e.spare[k]
	nt.ok = true
	if int(counts[maxSym]) == n {
		clear(nt.norm[:])
		norm := nt.norm[:maxSym+1]
		norm[maxSym] = 1
		nt.log = 0
		nt.own.build(norm, 0)
		nt.enc = &nt.own
		return 1, nt.enc, append(out, byte(maxSym))
	}

	log := optimalTableLog(maxLogOf(k), n, maxSym)
	norm := nt.norm[:maxSym+1]
	// as libzstd: "less than one" counts pay off with many sequences
	normalizeCounts(norm, counts, uint32(n), log, n >= 2048)
	e.hdr = writeNCount(e.hdr[:0], norm, log)
	best, mode := tableCost(counts, norm, log)+float64(8*len(e.hdr)), byte(2)

	predef := [3][]int16{predefLLNorm, predefOFNorm, predefMLNorm}[k]
	predefLog := [3]uint{predefLLLog, predefOFLog, predefMLLog}[k]
	if c := tableCost(counts, predef, predefLog); c <= best {
		best, mode = c, 0
	}
	if cur := e.tabs[k]; cur.ok {
		if c := tableCost(counts, cur.norm[:], cur.log); c <= best {
			best, mode = c, 3
		}
	}
	switch mode {
	case 0:
		nt.log = predefLog
		clear(nt.norm[:])
		copy(nt.norm[:], predef)
		nt.enc = &predefEnc[k]
	case 2:
		clear(nt.norm[maxSym+1:])
		nt.log = log
		nt.own.build(norm, log)
		nt.enc = &nt.own
		out = append(out, e.hdr...)
	case 3:
		return 3, e.tabs[k].enc, out
	}
	return mode, nt.enc, out
}

// tableCost estimates the bits that counts take with a table of
// normalized counts norm, or +Inf if a symbol is missing from it.
func tableCost(counts []uint32, norm []int16, log uint) float64 {
	c := 0.0
	for s, x := range counts {
		if x == 0 {
			continue
		}
		if s >= len(norm) || norm[s] == 0 {
			return math.Inf(1)
		}
		p := max(norm[s], 1)
		c += float64(x) * (float64(log) - math.Log2(float64(p)))
	}
	return c
}

func load32(p unsafe.Pointer, off int) uint32 {
	return binary.LittleEndian.Uint32((*[4]byte)(unsafe.Add(p, off))[:])
}

// matchLen counts the bytes from a and b that match, up to n.
func matchLen(p unsafe.Pointer, a, b, n int) int {
	m := 0
	for m+8 <= n {
		x := load64(p, a+m) ^ load64(p, b+m)
		if x != 0 {
			return m + bits.TrailingZeros64(x)>>3
		}
		m += 8
	}
	for m < n && *(*byte)(unsafe.Add(p, a+m)) == *(*byte)(unsafe.Add(p, b+m)) {
		m++
	}
	return m
}

const prime6bytes = 227718039650203

func hash6(v uint64, log uint) uint32 {
	return uint32(((v << 16) * prime6bytes) >> (64 - log))
}

// matchFast finds the sequences of src[start:end] with one hash table of
// 6-byte prefixes, like zstd's fast strategy. Each step hashes two
// positions from one 8-byte load and checks the last offset two bytes on,
// then the table candidates. Table entries keep their first 4 bytes, so a
// candidate that does not match costs no load from src. A match at the last
// offset extends back while literals remain, and data that does not match
// is skipped faster.
//
// Loads go through unsafe pointers: positions stay 8 bytes before the end
// of src, candidates are checked to be before the position, and table
// indexes are masked to the table size.
func (e *zenc) matchFast(src []byte, start, end int) {
	p := unsafe.Pointer(unsafe.SliceData(src))
	tp := unsafe.Pointer(unsafe.SliceData(e.table))
	mask := uint32(len(e.table) - 1)
	hlog := e.hashLog
	window := e.window
	reps := [3]int{int(e.rep[0]), int(e.rep[1]), int(e.rep[2])}
	ip, anchor := start, start
	limit := end - 8
	at := func(h uint32) *uint64 { return (*uint64)(unsafe.Add(tp, uintptr(h&mask)*8)) }
	entry := func(pos int, v uint64) uint64 { return uint64(uint32(pos)) | v<<32 }
	for ip < limit {
		cv := load64(p, ip)
		t0 := at(hash6(cv, hlog))
		t1 := at(hash6(cv>>8, hlog))
		e0, e1 := *t0, *t1
		*t0, *t1 = entry(ip, cv), entry(ip+1, cv>>8)

		var cand, ml int
		switch {
		case reps[0] <= ip+2 && load32(p, ip+2-reps[0]) == uint32(cv>>16):
			// the last offset two bytes on, extended back while at
			// least one literal remains: none would change its meaning.
			// Two bytes on, not one, raised the ratio of a log from
			// 5.94 to 6.14.
			r := reps[0]
			ip += 2
			ml = 4 + matchLen(p, ip+4, ip+4-r, end-ip-4)
			for ip > anchor+1 && ip > r && *(*byte)(unsafe.Add(p, ip-1)) == *(*byte)(unsafe.Add(p, ip-1-r)) {
				ip--
				ml++
			}
			e.lits = append(e.lits, src[anchor:ip]...)
			e.seqs = append(e.seqs, encSeq{uint32(ip - anchor), uint32(ml), uint32(r)})
			ip += ml
			anchor = ip
			continue
		case uint32(e0>>32) == uint32(cv) && ip-int(int32(e0)) > 0 && ip-int(int32(e0)) <= window:
			cand = int(int32(e0))
		case uint32(e1>>32) == uint32(cv>>8) && ip+1-int(int32(e1)) > 0 && ip+1-int(int32(e1)) <= window:
			ip++
			cand = int(int32(e1))
		default:
			ip += 2 + (ip-anchor)>>8
			continue
		}
		ml = 4 + matchLen(p, ip+4, cand+4, end-ip-4)
		for ip > anchor && cand > 0 && *(*byte)(unsafe.Add(p, ip-1)) == *(*byte)(unsafe.Add(p, cand-1)) {
			ip--
			cand--
			ml++
		}
		e.lits = append(e.lits, src[anchor:ip]...)
		e.seqs = append(e.seqs, encSeq{uint32(ip - anchor), uint32(ml), uint32(ip - cand)})
		if off := ip - cand; off != reps[0] {
			reps[2], reps[1], reps[0] = reps[1], reps[0], off
		}
		ip += ml
		anchor = ip
		if ip < limit {
			*at(hash6(load64(p, ip-2), hlog)) = entry(ip-2, load64(p, ip-2))
		}
		// the previous offset again right away, with no literals, is
		// repeat offset 1, which swaps the two
		for ip < limit && reps[1] <= ip && load32(p, ip) == load32(p, ip-reps[1]) {
			ml := 4 + matchLen(p, ip+4, ip-reps[1]+4, end-ip-4)
			e.seqs = append(e.seqs, encSeq{0, uint32(ml), uint32(reps[1])})
			reps[1], reps[0] = reps[0], reps[1]
			v := load64(p, ip)
			*at(hash6(v, hlog)) = entry(ip, v)
			ip += ml
			anchor = ip
		}
	}
	e.lits = append(e.lits, src[anchor:end]...)
	e.rep = [3]uint32{uint32(reps[0]), uint32(reps[1]), uint32(reps[2])}
}

const (
	prime5bytes = 889523592379
	prime8bytes = 0xCF1BBCDCB7A56463
)

func hash5(v uint64, log uint) uint32 {
	return uint32(((v << 24) * prime5bytes) >> (64 - log))
}

func hash8(v uint64, log uint) uint32 {
	return uint32((v * prime8bytes) >> (64 - log))
}

// matchDouble finds the sequences of src[start:end] with two hash tables,
// of 8-byte and 5-byte prefixes, like zstd's double fast strategy: it
// checks the last offset one and two bytes on, then a long match, then a
// short one, and takes a long match one byte on instead when it is longer.
//
// Loads go through unsafe pointers, as in matchFast.
func (e *zenc) matchDouble(src []byte, start, end int) {
	p := unsafe.Pointer(unsafe.SliceData(src))
	sp := unsafe.Pointer(unsafe.SliceData(e.table))
	lp := unsafe.Pointer(unsafe.SliceData(e.long))
	smask, lmask := uint32(len(e.table)-1), uint32(len(e.long)-1)
	slog, llog := e.hashLog, e.longLog
	window := e.window
	reps := [3]int{int(e.rep[0]), int(e.rep[1]), int(e.rep[2])}
	ip, anchor := start, start
	limit := end - 8
	for ip < limit {
		cv := load64(p, ip)
		tl := tabAt(lp, hash8(cv, llog), lmask)
		ts := tabAt(sp, hash5(cv, slog), smask)
		el, es := *tl, *ts
		*tl, *ts = tabEntry(ip, cv), tabEntry(ip, cv)

		r := reps[0]
		rp := 0
		// one byte on suits binaries, two bytes on logs (2.849 and
		// 5.949 against 2.818 and 6.075): try both
		if r <= ip+1 && load32(p, ip+1-r) == uint32(cv>>8) {
			rp = 1
		} else if r <= ip+2 && load32(p, ip+2-r) == uint32(cv>>16) {
			rp = 2
		}
		if rp > 0 {
			// the last offset one or two bytes on, extended back while
			// at least one literal remains
			ip += rp
			ml := 4 + matchLen(p, ip+4, ip+4-r, end-ip-4)
			for ip > anchor+1 && ip > r && *(*byte)(unsafe.Add(p, ip-1)) == *(*byte)(unsafe.Add(p, ip-1-r)) {
				ip--
				ml++
			}
			e.lits = append(e.lits, src[anchor:ip]...)
			e.seqs = append(e.seqs, encSeq{uint32(ip - anchor), uint32(ml), uint32(r)})
			ip += ml
			anchor = ip
			continue
		}
		cand := int(int32(el))
		var ml int
		if uint32(el>>32) == uint32(cv) && ip-cand > 0 && ip-cand <= window {
			ml = 4 + matchLen(p, ip+4, cand+4, end-ip-4)
		} else if cand = int(int32(es)); uint32(es>>32) == uint32(cv) && ip-cand > 0 && ip-cand <= window {
			ml = 4 + matchLen(p, ip+4, cand+4, end-ip-4)
			// a long match one byte on may be longer
			cv1 := load64(p, ip+1)
			t1 := tabAt(lp, hash8(cv1, llog), lmask)
			e1 := *t1
			*t1 = tabEntry(ip+1, cv1)
			if c1 := int(int32(e1)); uint32(e1>>32) == uint32(cv1) && ip+1-c1 > 0 && ip+1-c1 <= window {
				if ml1 := 4 + matchLen(p, ip+5, c1+4, end-ip-5); ml1 > ml {
					ip, cand, ml = ip+1, c1, ml1
				}
			}
		} else {
			ip += 1 + (ip-anchor)>>8
			continue
		}
		for ip > anchor && cand > 0 && *(*byte)(unsafe.Add(p, ip-1)) == *(*byte)(unsafe.Add(p, cand-1)) {
			ip--
			cand--
			ml++
		}
		mstart := ip
		e.lits = append(e.lits, src[anchor:ip]...)
		e.seqs = append(e.seqs, encSeq{uint32(ip - anchor), uint32(ml), uint32(ip - cand)})
		if off := ip - cand; off != reps[0] {
			reps[2], reps[1], reps[0] = reps[1], reps[0], off
		}
		ip += ml
		anchor = ip
		if ip < limit {
			for _, pos := range [2]int{mstart + 2, ip - 2} {
				v := load64(p, pos)
				*tabAt(lp, hash8(v, llog), lmask) = tabEntry(pos, v)
				*tabAt(sp, hash5(v, slog), smask) = tabEntry(pos, v)
			}
		}
		for ip < limit && reps[1] <= ip && load32(p, ip) == load32(p, ip-reps[1]) {
			ml := 4 + matchLen(p, ip+4, ip-reps[1]+4, end-ip-4)
			e.seqs = append(e.seqs, encSeq{0, uint32(ml), uint32(reps[1])})
			reps[1], reps[0] = reps[0], reps[1]
			v := load64(p, ip)
			*tabAt(lp, hash8(v, llog), lmask) = tabEntry(ip, v)
			*tabAt(sp, hash5(v, slog), smask) = tabEntry(ip, v)
			ip += ml
			anchor = ip
		}
	}
	e.lits = append(e.lits, src[anchor:end]...)
	e.rep = [3]uint32{uint32(reps[0]), uint32(reps[1]), uint32(reps[2])}
}

// tabAt is the entry for hash h in a table of mask+1 entries at t.
func tabAt(t unsafe.Pointer, h, mask uint32) *uint64 {
	return (*uint64)(unsafe.Add(t, uintptr(h&mask)*8))
}

// tabEntry is a table entry: a position and the first 4 bytes there.
func tabEntry(pos int, v uint64) uint64 { return uint64(uint32(pos)) | v<<32 }
