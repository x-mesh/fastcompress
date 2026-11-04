package fastgzip

import (
	"encoding/binary"
	"errors"
	"io"
	"runtime"
)

var errWriterClosed = errors.New("fastgzip: write after Close")

// ZstdWriter compresses what is written to it into one zstd frame, which
// Close ends. Levels are as for Zstd. The frame does not declare its
// content size. It keeps two windows of history: 4 MB at levels below 3,
// 16 MB from 3.
//
// With more than one CPU, a ZstdWriter emits blocks on another goroutine
// while Write finds the sequences of the next ones, and a write error from
// the underlying writer shows at the next Flush or Close. Close ends that
// goroutine.
type ZstdWriter struct {
	w     io.Writer
	level int
	e     *zenc
	buf   []byte // history, then the input not yet compressed from start
	start int
	err   error

	// the emitting side, on this goroutine or another
	out    []byte
	header bool // the frame header is written
	werr   error

	ch   chan *encBlk // to the emitting goroutine; nil if there is none
	free chan *encBlk
	ack  chan struct{}
}

// NewZstdWriter returns a ZstdWriter that writes to w at level.
func NewZstdWriter(w io.Writer, level int) *ZstdWriter {
	z := &ZstdWriter{}
	z.Reset(w, level)
	return z
}

// Reset discards the state of z and starts a new frame to w, keeping z's
// buffers.
func (z *ZstdWriter) Reset(w io.Writer, level int) {
	z.stop()
	e := z.e
	if e == nil {
		e = &zenc{}
	}
	e.reset(level)
	// two windows and a block: sliding the history back copies a window
	// for each window compressed
	n := 2*e.window + zstdBlockMax
	buf := z.buf
	if cap(buf) < n {
		buf = make([]byte, 0, n)
	}
	free := z.free
	*z = ZstdWriter{w: w, level: level, e: e, buf: buf[:0], out: z.out[:0], free: free}
}

// stop ends the emitting goroutine once it has emitted every block sent.
func (z *ZstdWriter) stop() {
	if z.ch != nil {
		close(z.ch)
		<-z.ack
		z.ch = nil
	}
}

func (z *ZstdWriter) Write(p []byte) (int, error) {
	if z.err != nil {
		return 0, z.err
	}
	n := len(p)
	for len(p) > 0 {
		if len(z.buf) == cap(z.buf) {
			z.slide()
		}
		k := copy(z.buf[len(z.buf):cap(z.buf)], p)
		z.buf = z.buf[:len(z.buf)+k]
		p = p[k:]
		for len(z.buf)-z.start >= zstdBlockMax {
			z.block(z.start+zstdBlockMax, false)
		}
		if z.err != nil {
			return n - len(p), z.err
		}
	}
	return n, nil
}

// Flush compresses the input written so far into a block and writes the
// output to the underlying writer.
func (z *ZstdWriter) Flush() error {
	if z.err != nil {
		return z.err
	}
	if len(z.buf) > z.start {
		z.block(len(z.buf), false)
	}
	z.sync()
	return z.err
}

// sync waits until every block sent is written, and takes up a write
// error.
func (z *ZstdWriter) sync() {
	if z.ch != nil {
		z.ch <- nil
		<-z.ack
	}
	if z.err == nil {
		z.err = z.werr
	}
}

// Close compresses the rest of the input, ends the frame with its checksum,
// and writes the output. It does not close the underlying writer.
func (z *ZstdWriter) Close() error {
	if z.err != nil {
		if z.err == errWriterClosed {
			return nil
		}
		return z.err
	}
	z.block(len(z.buf), true)
	z.stop()
	if z.err == nil {
		z.err = z.werr
	}
	if z.err != nil {
		return z.err
	}
	z.out = binary.LittleEndian.AppendUint32(z.out[:0], uint32(z.e.hash.sum()))
	z.write()
	if z.werr != nil {
		z.err = z.werr
		return z.err
	}
	z.err = errWriterClosed
	return nil
}

// slide keeps the last window of history and moves it to the start of buf.
func (z *ZstdWriter) slide() {
	shift := z.start - z.e.window
	if shift <= 0 {
		return
	}
	copy(z.buf, z.buf[shift:])
	z.buf = z.buf[:len(z.buf)-shift]
	z.start -= shift
	z.e.shiftTables(shift)
}

// block finds the sequences of buf[start:end] and emits the block, here or
// on the emitting goroutine.
func (z *ZstdWriter) block(end int, last bool) {
	e := z.e
	blk := z.buf[z.start:end]
	run := len(blk) > 0 && isRun(blk)
	if z.ch == nil && runtime.GOMAXPROCS(0) > 1 {
		z.ch = make(chan *encBlk, pipeDepth)
		z.ack = make(chan struct{})
		if z.free == nil {
			z.free = make(chan *encBlk, pipeDepth+2)
			for range pipeDepth + 2 {
				z.free <- &encBlk{}
			}
		}
		go z.emitter()
	}
	if z.ch == nil {
		if !run {
			e.find(z.buf, z.start, end)
		}
		z.emit(blk, e.lits, e.seqs, run, last)
	} else {
		b := <-z.free
		if !run {
			e.lits, e.seqs = b.lits, b.seqs
			e.find(z.buf, z.start, end)
			b.lits, b.seqs = e.lits, e.seqs
		}
		// buf slides, so the block takes a copy
		b.blk = append(b.blk[:0], blk...)
		b.run, b.last = run, last
		z.ch <- b
	}
	z.start = end
}

// emitter emits the blocks from z.ch; a nil block asks it to report that
// it has written the ones before.
func (z *ZstdWriter) emitter() {
	for b := range z.ch {
		if b == nil {
			z.ack <- struct{}{}
			continue
		}
		z.emit(b.blk, b.lits, b.seqs, b.run, b.last)
		z.free <- b
	}
	z.ack <- struct{}{}
}

// emit writes one block, with the frame header first.
func (z *ZstdWriter) emit(blk, lits []byte, seqs []encSeq, run, last bool) {
	e := z.e
	z.out = z.out[:0]
	if !z.header {
		z.out = binary.LittleEndian.AppendUint32(z.out, zstdMagic)
		z.out = append(z.out, 1<<2, byte((e.windowLog-10)<<3)) // checksum, no content size
		z.header = true
	}
	z.out = e.emit(z.out, blk, lits, seqs, run, last)
	e.hash.write(blk)
	z.write()
}

func (z *ZstdWriter) write() {
	if z.werr != nil {
		return
	}
	if _, err := z.w.Write(z.out); err != nil {
		z.werr = err
	}
}
