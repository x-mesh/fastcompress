package zstd

import (
	"bytes"
	"encoding/binary"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
)

var errZstdDict = errors.New("zstd: dictionaries are not supported")

const (
	zstdMagic     = 0xFD2FB528
	skipMagic     = 0x184D2A50
	skipMagicMask = 0xFFFFFFF0
	zstdBlockMax  = 128 << 10
	fcsUnknown    = ^uint64(0)
	maxInt        = uint64(^uint(0) >> 1)
)

var zstdMagicBytes = []byte{0x28, 0xb5, 0x2f, 0xfd}

// zdec is the state of a zstd frame decoder: the parsing state, and x for
// executing blocks on the same goroutine.
type zdec struct {
	lit      []byte
	huf      hufTable
	hufOK    bool
	seq      seqTables
	rep      [3]uint
	blockMax int
	seqs     []seqVal
	free     chan *zbuf
	// copyRaw makes parsed blocks own their raw data, for a reader that
	// reuses its input buffer while blocks wait to execute; splitSeqs
	// leaves the sequences for parseS on another goroutine
	copyRaw   bool
	splitSeqs bool
	x         zexec
}

// zexec is the state of executing a frame's blocks.
type zexec struct {
	window     int
	blockMax   int
	frameStart int    // index in the output where the frame's history starts
	produced   uint64 // bytes the frame has decoded
	fcs        uint64
	checksum   bool
	hash       xxh64
}

var zdecPool = sync.Pool{New: func() any {
	return &zdec{lit: make([]byte, zstdBlockMax+litSlack)}
}}

// Decode decodes every zstd frame in src and returns the concatenated data.
// It skips skippable frames. Frames that need a dictionary are not
// supported.
func Decode(src []byte) ([]byte, error) {
	d := zdecPool.Get().(*zdec)
	defer zdecPool.Put(d)
	d.copyRaw, d.splitSeqs = false, false
	var out []byte
	for len(src) > 0 {
		if len(src) < 4 {
			if bytes.HasPrefix(zstdMagicBytes, src) {
				return nil, errEOF
			}
			return nil, ErrHeader
		}
		magic := binary.LittleEndian.Uint32(src)
		if magic&skipMagicMask == skipMagic {
			if len(src) < 8 {
				return nil, errEOF
			}
			n := uint64(binary.LittleEndian.Uint32(src[4:]))
			if uint64(len(src)-8) < n {
				return nil, errEOF
			}
			src = src[8+n:]
			continue
		}
		if magic != zstdMagic {
			return nil, ErrHeader
		}
		h, err := parseFrameHeader(src)
		if err != nil {
			return nil, err
		}
		if out == nil {
			out = make([]byte, 0, initialSize(h.fcs, len(src)))
		}
		var n int
		out, n, err = d.frame(out, src[h.n:], &h)
		if err != nil {
			return nil, err
		}
		src = src[h.n+n:]
	}
	return out, nil
}

// initialSize is the output capacity for a frame that declares fcs bytes.
// The declaration is trusted only up to 256 times the input, so a short
// corrupt frame cannot make Decode allocate gigabytes; larger frames grow
// the output as they decode.
func initialSize(fcs uint64, srcLen int) int {
	bound := uint64(srcLen)*256 + 1<<20
	if fcs == fcsUnknown {
		return int(min(uint64(srcLen)*4, bound, maxInt/2))
	}
	return int(min(fcs, bound, maxInt/2)) + zOutMargin
}

type frameHeader struct {
	fcs      uint64
	window   uint64
	checksum bool
	n        int
}

// parseFrameHeader reads the frame header (RFC 8878 3.1.1.1) that follows
// the magic number at the start of src.
func parseFrameHeader(src []byte) (frameHeader, error) {
	var h frameHeader
	if len(src) < 5 {
		return h, errEOF
	}
	fhd := src[4]
	if fhd&0x08 != 0 {
		return h, ErrHeader
	}
	single := fhd&0x20 != 0
	h.checksum = fhd&0x04 != 0
	i := 5
	if !single {
		if i >= len(src) {
			return h, errEOF
		}
		wd := src[i]
		i++
		base := uint64(1) << (10 + wd>>3)
		h.window = base + base/8*uint64(wd&7)
	}
	didSize := [4]int{0, 1, 2, 4}[fhd&3]
	fcsSize := [4]int{0, 2, 4, 8}[fhd>>6]
	if fhd>>6 == 0 && single {
		fcsSize = 1
	}
	if i+didSize+fcsSize > len(src) {
		return h, errEOF
	}
	var did uint32
	for j := didSize - 1; j >= 0; j-- {
		did = did<<8 | uint32(src[i+j])
	}
	i += didSize
	if did != 0 {
		return h, errZstdDict
	}
	switch fcsSize {
	case 0:
		h.fcs = fcsUnknown
	case 1:
		h.fcs = uint64(src[i])
	case 2:
		h.fcs = uint64(binary.LittleEndian.Uint16(src[i:])) + 256
	case 4:
		h.fcs = uint64(binary.LittleEndian.Uint32(src[i:]))
	case 8:
		h.fcs = binary.LittleEndian.Uint64(src[i:])
	}
	i += fcsSize
	if single {
		h.window = h.fcs
	}
	h.n = i
	return h, nil
}

// frame decodes the blocks of one frame from src onto out and returns the
// number of bytes of src it used.
func (d *zdec) frame(out, src []byte, h *frameHeader) ([]byte, int, error) {
	d.startFrame(h, len(out))
	var (
		i   int
		err error
	)
	if pipelined(h, len(src)) {
		out, i, err = d.blocksPipelined(out, src, h)
	} else {
		out, i, err = d.blocks(out, src, h)
	}
	if err != nil {
		return out, 0, err
	}
	if err := d.x.end(src[i:]); err != nil {
		return out, 0, err
	}
	if h.checksum {
		i += 4
	}
	return out, i, nil
}

// startFrame resets the parsing state for a frame, and x to execute it onto
// an output where it starts at index start.
func (d *zdec) startFrame(h *frameHeader, start int) {
	d.startParse(h)
	d.x.start(h, start)
}

func (d *zdec) startParse(h *frameHeader) {
	d.startLits(h)
	d.startSeqs()
}

func (d *zdec) startLits(h *frameHeader) {
	d.blockMax = int(min(h.window, zstdBlockMax))
	d.hufOK = false
}

func (d *zdec) startSeqs() {
	d.rep = [3]uint{1, 4, 8}
	d.seq.ok = [3]bool{}
}

func (x *zexec) start(h *frameHeader, start int) {
	x.window = int(min(h.window, maxInt))
	x.blockMax = min(x.window, zstdBlockMax)
	x.frameStart = start
	x.produced = 0
	x.fcs = h.fcs
	x.checksum = h.checksum
	if h.checksum {
		x.hash.reset()
	}
}

// end checks the content size and the checksum, which starts sum.
func (x *zexec) end(sum []byte) error {
	if x.fcs != fcsUnknown && x.produced != x.fcs {
		return ErrCorrupt
	}
	if x.checksum {
		if len(sum) < 4 {
			return errEOF
		}
		if uint32(x.hash.sum()) != binary.LittleEndian.Uint32(sum) {
			return ErrChecksum
		}
	}
	return nil
}

// Frames that decode to at least pipeMinSize bytes (or, without a declared
// size, have that much input / 4) decode on two goroutines. Tests lower it
// to run the pipeline on small inputs.
var pipeMinSize = 1 << 20

const pipeDepth = 4

func pipelined(h *frameHeader, srcLen int) bool {
	if runtime.GOMAXPROCS(0) < 2 {
		return false
	}
	if h.fcs == fcsUnknown {
		return srcLen >= pipeMinSize/4
	}
	return h.fcs >= uint64(pipeMinSize)
}

// zbuf holds the literals and sequences of one parsed block, and its
// sequences section when another goroutine decodes them.
type zbuf struct {
	lit  []byte
	seqs []seqVal
	src  []byte
}

func newZbuf() *zbuf {
	return &zbuf{
		lit:  make([]byte, zstdBlockMax+litSlack),
		seqs: make([]seqVal, zstdBlockMax/3),
		src:  make([]byte, 0, zstdBlockMax),
	}
}

// zitem is one parsed block, ready to execute.
type zitem struct {
	typ    byte // 0 raw, 1 RLE, 2 compressed
	last   bool
	size   int    // Block_Size
	data   []byte // raw content
	lits   []byte
	seqSrc []byte // sequences section
	seqs   []seqVal
	rle    byte
	buf    *zbuf // set when data, lits or seqs live in it
	next   int   // index in src past the block
	err    error

	// for a reader that parses ahead: the header of the frame this block
	// starts, and the frame's checksum after its last block
	frame *frameHeader
	sum   [4]byte
}

// parse reads the block at src[i:] and, for a compressed block, decodes its
// literals and, unless d.splitSeqs, its sequences into zb.
func (d *zdec) parse(src []byte, i int, zb *zbuf) zitem {
	it := d.parseLits(src, i, zb)
	if !d.splitSeqs {
		d.parseSeqs(&it)
	}
	return it
}

// parseLits reads the block at src[i:] and decodes the literals of a
// compressed block (RFC 8878 3.1.1.3.1).
func (d *zdec) parseLits(src []byte, i int, zb *zbuf) zitem {
	if i+3 > len(src) {
		return zitem{err: errEOF}
	}
	bh := uint32(src[i]) | uint32(src[i+1])<<8 | uint32(src[i+2])<<16
	i += 3
	it := zitem{typ: byte(bh >> 1 & 3), last: bh&1 != 0, size: int(bh >> 3)}
	if it.size > d.blockMax {
		it.err = ErrCorrupt
		return it
	}
	switch it.typ {
	case 0:
		if i+it.size > len(src) {
			it.err = errEOF
			return it
		}
		it.data = src[i : i+it.size]
		if d.copyRaw {
			it.data = append(zb.lit[:0], it.data...)
			it.buf = zb
		}
		i += it.size
	case 1:
		if i >= len(src) {
			it.err = errEOF
			return it
		}
		it.rle = src[i]
		i++
	case 2:
		if i+it.size > len(src) {
			it.err = errEOF
			return it
		}
		it.buf = zb
		blk := src[i : i+it.size]
		var n int
		it.lits, n, it.err = d.literals(blk, zb.lit)
		if it.err == nil {
			it.seqSrc = blk[n:]
			if d.splitSeqs {
				it.seqSrc = append(zb.src[:0], it.seqSrc...)
			}
		}
		i += it.size
	default:
		it.err = ErrCorrupt
	}
	it.next = i
	return it
}

// parseSeqs decodes the sequences of a compressed block (RFC 8878
// 3.1.1.3.2) into it.buf.
func (d *zdec) parseSeqs(it *zitem) {
	if it.err != nil || it.typ != 2 {
		return
	}
	src := it.seqSrc
	nseq, m, err := d.seq.readHeader(src, 0)
	if err != nil {
		it.err = err
		return
	}
	if nseq == 0 {
		if m != len(src) {
			it.err = ErrCorrupt
		}
		return
	}
	// every sequence copies a match of at least 3 bytes
	if m >= len(src) || nseq > zstdBlockMax/3 {
		it.err = ErrCorrupt
		return
	}
	seqs := it.buf.seqs[:nseq]
	if err := d.decodeSeqs(seqs, src, m, len(src)); err != nil {
		it.err = err
		return
	}
	it.seqs = seqs
}

// room returns out with space for the next block and the most output the
// block may write: the block maximum, and what is left of a declared content
// size, so a frame that fills its declared size exactly never grows out.
func (x *zexec) room(out []byte) ([]byte, int) {
	room := x.blockMax
	if x.fcs != fcsUnknown {
		room = int(min(uint64(room), x.fcs-x.produced))
	}
	if cap(out)-len(out) < room+zOutMargin {
		out = growOut(out, room+zOutMargin)
	}
	return out, room
}

// finish counts and hashes the output of a block, out[start:].
func (x *zexec) finish(out []byte, start int) {
	x.produced += uint64(len(out) - start)
	if x.checksum {
		// while the block is still in cache
		x.hash.write(out[start:])
	}
}

// run executes a parsed block onto out and hashes its output.
func (x *zexec) run(out []byte, it *zitem) ([]byte, error) {
	out, room := x.room(out)
	start := len(out)
	switch {
	case it.typ == 0:
		if it.size > room {
			return out, ErrCorrupt
		}
		out = append(out, it.data...)
	case it.typ == 1:
		if it.size > room {
			return out, ErrCorrupt
		}
		out = out[:start+it.size]
		for j := start; j < len(out); j++ {
			out[j] = it.rle
		}
	case len(it.seqs) == 0:
		if len(it.lits) > room {
			return out, ErrCorrupt
		}
		out = append(out, it.lits...)
	default:
		var err error
		out, err = execBlock(out, it.seqs, it.lits, start+room, x.frameStart, x.window)
		if err != nil {
			return out, err
		}
	}
	x.finish(out, start)
	return out, nil
}

// blocks decodes the blocks of a frame on this goroutine. It returns the
// index in src past the last block.
func (d *zdec) blocks(out, src []byte, h *frameHeader) ([]byte, int, error) {
	if d.seqs == nil {
		d.seqs = make([]seqVal, zstdBlockMax/3)
	}
	zb := zbuf{lit: d.lit, seqs: d.seqs}
	i := 0
	for {
		var err error
		it := d.parseLits(src, i, &zb)
		if useFusedAsm && it.err == nil && it.typ == 2 {
			out, err = d.runFused(out, &it)
		} else {
			d.parseSeqs(&it)
			if it.err != nil {
				return out, 0, it.err
			}
			out, err = d.x.run(out, &it)
		}
		if err != nil {
			return out, 0, err
		}
		i = it.next
		if it.last {
			return out, i, nil
		}
	}
}

// blocksPipelined decodes the blocks of a frame on two goroutines: another
// goroutine decodes the literals and sequences of the next blocks while
// this one executes the current block and hashes its output. In a
// prototype built on klauspost/compress, this split cut the decode time of
// a 100 MB log by 40% for 15% more CPU.
func (d *zdec) blocksPipelined(out, src []byte, h *frameHeader) ([]byte, int, error) {
	if d.free == nil {
		d.free = make(chan *zbuf, pipeDepth+2)
		for range pipeDepth + 2 {
			d.free <- newZbuf()
		}
	}
	free := d.free
	ch := make(chan zitem, pipeDepth)
	var stop atomic.Bool
	go func() {
		defer close(ch)
		i := 0
		for !stop.Load() {
			zb := <-free
			it := d.parse(src, i, zb)
			if it.buf == nil {
				free <- zb
			}
			ch <- it
			if it.err != nil || it.last {
				return
			}
			i = it.next
		}
	}()
	var err error
	next := 0
	for it := range ch {
		if err == nil {
			err = it.err
			if err == nil {
				out, err = d.x.run(out, &it)
			}
			if err != nil {
				stop.Store(true)
			}
			next = it.next
		}
		if it.buf != nil {
			free <- it.buf
		}
	}
	return out, next, err
}

// literals reads the literals section (RFC 8878 3.1.1.3.1) at the start of
// blk. The literals it returns have litSlack readable bytes past their end.
func (d *zdec) literals(blk, lit []byte) ([]byte, int, error) {
	if len(blk) == 0 {
		return nil, 0, errEOF
	}
	h := int(blk[0])
	typ := h & 3
	sf := h >> 2 & 3
	if typ < 2 {
		var size, hl int
		switch sf {
		case 0, 2:
			size, hl = h>>3, 1
		case 1:
			if len(blk) < 2 {
				return nil, 0, errEOF
			}
			size, hl = h>>4+int(blk[1])<<4, 2
		default:
			if len(blk) < 3 {
				return nil, 0, errEOF
			}
			size, hl = h>>4+int(blk[1])<<4+int(blk[2])<<12, 3
		}
		if size > d.blockMax {
			return nil, 0, ErrCorrupt
		}
		if typ == 1 {
			if hl >= len(blk) {
				return nil, 0, errEOF
			}
			lits := lit[:size]
			v := blk[hl]
			for i := range lits {
				lits[i] = v
			}
			return lits, hl + 1, nil
		}
		if hl+size > len(blk) {
			return nil, 0, errEOF
		}
		if hl+size+litSlack <= cap(blk) && !d.copyRaw {
			return blk[hl : hl+size], hl + size, nil
		}
		return append(lit[:0], blk[hl:hl+size]...), hl + size, nil
	}
	var regen, csize, hl int
	switch sf {
	case 0, 1:
		hl = 3
		if len(blk) < hl {
			return nil, 0, errEOF
		}
		regen = h>>4 | int(blk[1])&0x3f<<4
		csize = int(blk[1])>>6 | int(blk[2])<<2
	case 2:
		hl = 4
		if len(blk) < hl {
			return nil, 0, errEOF
		}
		regen = h>>4 | int(blk[1])<<4 | int(blk[2])&3<<12
		csize = int(blk[2])>>2 | int(blk[3])<<6
	default:
		hl = 5
		if len(blk) < hl {
			return nil, 0, errEOF
		}
		regen = h>>4 | int(blk[1])<<4 | int(blk[2])&0x3f<<12
		csize = int(blk[2])>>6 | int(blk[3])<<2 | int(blk[4])<<10
	}
	if regen > d.blockMax {
		return nil, 0, ErrCorrupt
	}
	if hl+csize > len(blk) {
		return nil, 0, errEOF
	}
	data := blk[hl : hl+csize]
	if typ == 2 {
		d.hufOK = false
		n, err := d.huf.read(data)
		if err != nil {
			return nil, 0, err
		}
		d.hufOK = true
		data = data[n:]
	} else if !d.hufOK {
		return nil, 0, ErrCorrupt
	}
	lits := lit[:regen]
	var err error
	if sf == 0 {
		err = d.huf.decode1(lits, data, 0, len(data))
	} else {
		err = d.huf.decode4(lits, data)
	}
	if err != nil {
		return nil, 0, err
	}
	return lits, hl + csize, nil
}
