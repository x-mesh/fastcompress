package zstd

import (
	"encoding/binary"
	"errors"
	"io"
	"math"
)

// A Reader keeps at most this much history, libzstd's default window
// limit; a frame with a larger window fails with errZstdWindow.
const zstdMaxStreamWindow = 1 << 27

var errZstdWindow = errors.New("zstd: window larger than 128 MB")

// zstdStreamIn holds the largest block with its header and a frame header.
const zstdStreamIn = 256 << 10

// Reader decompresses a zstd stream, including concatenated frames. It
// skips skippable frames. Frames that need a dictionary are not supported.
//
// For a frame of at least 1 MB, or of unknown size, with more than one CPU,
// a Reader reads ahead from its source: one goroutine reads and decodes
// the literals of the next blocks, another their sequences, while Read
// executes the current block.
type Reader struct {
	in *zinput

	x    zexec
	hist []byte // output; the last window bytes are the frame's history
	r    int    // next unread byte in hist
	err  error

	// read-ahead
	ch       chan zitem
	done     chan struct{}
	prodDone bool // the read-ahead goroutines sent their last item
}

// zinput is the input side of a Reader: it reads frame headers and
// parses blocks. A read-ahead goroutine owns it until it stops.
type zinput struct {
	src      io.Reader
	buf      []byte // buf[ip:ie] is unread
	ip, ie   int
	srcErr   error
	empty    int // reads in a row that returned nothing
	d        *zdec
	zb       zbuf
	h        frameHeader
	inFrame  bool
	newFrame bool
}

// NewReader reads the first zstd frame header from src. It returns
// io.EOF when src is empty.
func NewReader(src io.Reader) (*Reader, error) {
	z := &Reader{}
	if err := z.Reset(src); err != nil {
		return nil, err
	}
	return z, nil
}

// Reset discards the state of z and reads the first frame header from src,
// as NewReader does, keeping z's buffers.
func (z *Reader) Reset(src io.Reader) error {
	z.stop()
	in, hist := z.in, z.hist
	if in == nil {
		in = &zinput{
			// litSlack past the end lets raw literals be read in place
			buf: make([]byte, zstdStreamIn, zstdStreamIn+litSlack),
			d:   zdecPool.Get().(*zdec),
		}
	}
	d := in.d
	if d.seqs == nil {
		d.seqs = make([]seqVal, zstdBlockMax/3)
	}
	d.copyRaw, d.splitSeqs = false, false
	*in = zinput{src: src, buf: in.buf, d: d, zb: zbuf{lit: d.lit, seqs: d.seqs}}
	*z = Reader{in: in, hist: hist[:0]}
	if err := in.frame(); err != nil {
		return err
	}
	if pipelined(&in.h, math.MaxInt) {
		if d.free == nil {
			d.free = make(chan *zbuf, pipeDepth+2)
			for range pipeDepth + 2 {
				d.free <- newZbuf()
			}
		}
		// blocks wait in the channels while the input buffer is reused
		d.copyRaw, d.splitSeqs = true, true
		lits := make(chan zitem, pipeDepth)
		seqsDone := make(chan struct{})
		z.ch = make(chan zitem, pipeDepth)
		z.done = make(chan struct{})
		go in.produce(lits, d.free, z.done, seqsDone)
		go d.seqStage(lits, z.ch, d.free, z.done, seqsDone)
	}
	return nil
}

// stop ends the read-ahead goroutines. If they may still be running, one
// blocked in a read from the source, z leaves them the input side.
func (z *Reader) stop() {
	if z.ch == nil {
		return
	}
	if !z.prodDone {
		close(z.done)
		z.in = nil
	}
	z.ch, z.done = nil, nil
}

func (z *Reader) Read(p []byte) (int, error) {
	for z.r == len(z.hist) {
		if z.err != nil {
			return 0, z.err
		}
		z.fill()
	}
	n := copy(p, z.hist[z.r:])
	z.r += n
	return n, nil
}

// Close returns z's decoder state for reuse. It does not close the
// underlying reader. Reset makes z usable again.
func (z *Reader) Close() error {
	z.stop()
	if z.in != nil {
		z.in.d.copyRaw, z.in.d.splitSeqs = false, false
		zdecPool.Put(z.in.d)
	}
	z.in, z.hist, z.r = nil, nil, 0
	z.err = errClosed
	return nil
}

// need makes n bytes of input available at buf[ip:]. It returns errEOF if
// the input ends first.
func (in *zinput) need(n int) error {
	for in.ie-in.ip < n {
		if in.srcErr != nil {
			if in.srcErr == io.EOF {
				return errEOF
			}
			return in.srcErr
		}
		if in.ip > 0 {
			in.ie = copy(in.buf, in.buf[in.ip:in.ie])
			in.ip = 0
		}
		m, err := in.src.Read(in.buf[in.ie:])
		in.ie += m
		switch {
		case err != nil:
			in.srcErr = err
		case m > 0:
			in.empty = 0
		default:
			// like bufio.Reader, give up on a reader that never
			// returns data
			in.empty++
			if in.empty == 100 {
				in.srcErr = io.ErrNoProgress
			}
		}
	}
	return nil
}

// skip discards n bytes of input.
func (in *zinput) skip(n int64) error {
	for n > 0 {
		if in.ip == in.ie {
			if err := in.need(1); err != nil {
				return err
			}
		}
		k := int(min(n, int64(in.ie-in.ip)))
		in.ip += k
		n -= int64(k)
	}
	return nil
}

func frameHeaderSize(fhd byte) int {
	single := fhd&0x20 != 0
	n := 5 + [4]int{0, 1, 2, 4}[fhd&3] + [4]int{0, 2, 4, 8}[fhd>>6]
	if !single {
		n++
	} else if fhd>>6 == 0 {
		n++
	}
	return n
}

// frame reads the next frame header, skipping skippable frames. It returns
// io.EOF at the end of the input.
func (in *zinput) frame() error {
	for {
		if in.ip == in.ie {
			if err := in.need(1); err == errEOF {
				return io.EOF
			} else if err != nil {
				return err
			}
		}
		if err := in.need(4); err != nil {
			return err
		}
		magic := binary.LittleEndian.Uint32(in.buf[in.ip:])
		if magic&skipMagicMask == skipMagic {
			if err := in.need(8); err != nil {
				return err
			}
			n := int64(binary.LittleEndian.Uint32(in.buf[in.ip+4:]))
			in.ip += 8
			if err := in.skip(n); err != nil {
				return err
			}
			continue
		}
		if magic != zstdMagic {
			return ErrHeader
		}
		if err := in.need(5); err != nil {
			return err
		}
		if err := in.need(frameHeaderSize(in.buf[in.ip+4])); err != nil {
			return err
		}
		h, err := parseFrameHeader(in.buf[in.ip:in.ie])
		if err != nil {
			return err
		}
		if h.window > zstdMaxStreamWindow {
			return errZstdWindow
		}
		in.ip += h.n
		in.h = h
		if in.d.splitSeqs {
			// the sequences goroutine resets its state at the
			// frame's first block
			in.d.startLits(&in.h)
		} else {
			in.d.startParse(&in.h)
		}
		in.inFrame, in.newFrame = true, true
		return nil
	}
}

// next reads and parses the next block, into zb if it needs a buffer. The
// first block of a frame carries the frame header.
func (in *zinput) next(zb *zbuf) zitem {
	if !in.inFrame {
		if err := in.frame(); err != nil {
			return zitem{err: err}
		}
	}
	var fh *frameHeader
	if in.newFrame {
		h := in.h
		fh = &h
		in.newFrame = false
	}
	if err := in.need(3); err != nil {
		return zitem{err: err}
	}
	bh := uint32(in.buf[in.ip]) | uint32(in.buf[in.ip+1])<<8 | uint32(in.buf[in.ip+2])<<16
	size := int(bh >> 3)
	if size > in.d.blockMax {
		return zitem{err: ErrCorrupt}
	}
	if bh>>1&3 == 1 {
		size = 1
	}
	// read the checksum with the last block: reading more after parsing
	// could move the input that the block's raw data points at
	sum := 0
	if bh&1 != 0 && in.h.checksum {
		sum = 4
	}
	if err := in.need(3 + size + sum); err != nil {
		return zitem{err: err}
	}
	it := in.d.parse(in.buf[:in.ie], in.ip, zb)
	it.frame = fh
	if it.err != nil {
		return it
	}
	in.ip = it.next
	if it.last {
		copy(it.sum[:], in.buf[in.ip:in.ip+sum])
		in.ip += sum
		in.inFrame = false
	}
	return it
}

// produce runs next on another goroutine until the input ends, done closes,
// or the sequences goroutine fails and closes seqsDone. It does not touch
// in after it sends the item that ends the input.
func (in *zinput) produce(ch chan<- zitem, free chan *zbuf, done, seqsDone <-chan struct{}) {
	defer close(ch)
	for {
		var zb *zbuf
		select {
		case zb = <-free:
		case <-done:
			return
		case <-seqsDone:
			return
		}
		it := in.next(zb)
		if it.buf == nil {
			free <- zb
		}
		select {
		case ch <- it:
		case <-done:
			return
		case <-seqsDone:
			return
		}
		if it.err != nil {
			return
		}
	}
}

// seqStage decodes the sequences of the blocks from lits and passes them on.
// When it fails, it stops produce and waits for lits to close before it
// sends the error, so the error means neither goroutine touches the input
// side again.
func (d *zdec) seqStage(lits <-chan zitem, out chan<- zitem, free chan *zbuf, done <-chan struct{}, seqsDone chan<- struct{}) {
	defer close(out)
	for it := range lits {
		if it.frame != nil {
			d.startSeqs()
		}
		d.parseSeqs(&it)
		if it.err != nil {
			close(seqsDone)
			for rest := range lits {
				if rest.buf != nil {
					free <- rest.buf
				}
			}
		}
		select {
		case out <- it:
		case <-done:
			return
		}
		if it.err != nil {
			return
		}
	}
}

// fill decodes the next block once every decoded byte has been read.
func (z *Reader) fill() {
	if z.ch == nil {
		it := z.in.next(&z.in.zb)
		z.exec(&it)
		return
	}
	it := <-z.ch
	if it.err != nil {
		z.prodDone = true
	}
	z.exec(&it)
	if it.buf != nil {
		z.in.d.free <- it.buf
	}
}

// exec executes a parsed block onto the history.
func (z *Reader) exec(it *zitem) {
	if it.err != nil {
		z.err = it.err
		return
	}
	x := &z.x
	if it.frame != nil {
		z.hist, z.r = z.hist[:0], 0
		x.start(it.frame, 0)
		// sliding the history back copies a window each time the space
		// past it fills: two windows of space copy one byte per two
		// decoded, and one window, for windows past the 8 MB that zstd
		// levels up to 19 use, one per byte
		w := x.window
		extra := max(2*w, 1<<20)
		if w > 8<<20 {
			extra = w
		}
		if need := w + extra + zstdBlockMax + zOutMargin; cap(z.hist) < need {
			z.hist = make([]byte, 0, need)
		}
	}
	if cap(z.hist)-len(z.hist) < x.blockMax+zOutMargin {
		keep := min(len(z.hist), x.window)
		shift := len(z.hist) - keep
		copy(z.hist, z.hist[shift:])
		z.hist = z.hist[:keep]
		z.r -= shift
		x.frameStart = max(x.frameStart-shift, 0)
	}
	if z.hist, z.err = x.run(z.hist, it); z.err != nil {
		return
	}
	if it.last {
		z.err = x.end(it.sum[:])
	}
}
