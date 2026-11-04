package fastgzip

import (
	"encoding/binary"
	"errors"
	"io"
)

var errWriteAfterClose = errors.New("fastgzip: write after Close")

// writerBufSize is the window a Writer keeps: the 32 KB of match history and
// up to 8 blocks of input. Sliding copies 32 KB once per 8 blocks.
const writerBufSize = windowSize + 8*blockSize

// Writer compresses to a gzip stream, like compress/gzip.Writer. It writes
// one member with no name, comment or modification time.
//
// Without Flush, a Writer writes the same bytes as Gzip for the same input
// and level. SetConcurrency makes it compress on more goroutines.
type Writer struct {
	w       io.Writer
	level   int
	c       *compressor
	pend    int // start of the input in c.buf that is not encoded yet
	crc     uint32
	size    uint32
	started bool // a Write or Flush since Reset
	closed  bool
	err     error

	// concurrent compression: the goroutines, the chunk being filled with
	// its dictionary of chunk[:dict], and the chunks in flight in order
	conc  int
	sem   chan struct{} // limits the goroutines that compress to conc
	chunk []byte
	dict  int
	queue []*chunkJob
}

// NewWriter returns a Writer at DefaultCompression.
func NewWriter(w io.Writer) *Writer {
	z, _ := NewWriterLevel(w, DefaultCompression)
	return z
}

// NewWriterLevel returns a Writer at the given level, from HuffmanOnly to
// BestCompression.
func NewWriterLevel(w io.Writer, level int) (*Writer, error) {
	l, err := checkLevel(level)
	if err != nil {
		return nil, err
	}
	z := &Writer{level: l}
	z.Reset(w)
	return z, nil
}

// Reset discards the state of z and starts a new stream to w at the same
// level, keeping z's buffers.
func (z *Writer) Reset(w io.Writer) {
	z.discard()
	if z.chunk != nil {
		b := z.chunk[:0]
		chunkPool.Put(&b)
	}
	c := z.c
	if c == nil {
		c = getCompressor(z.level)
	} else {
		c.reset(z.level)
	}
	*z = Writer{w: w, level: z.level, c: c, conc: z.conc}
	if cap(z.c.buf) < writerBufSize {
		z.c.buf = make([]byte, 0, writerBufSize)
	}
	z.c.buf = z.c.buf[:0]
	z.c.e.bw.out = appendGzipHeader(z.c.e.bw.out[:0], z.level)
}

// Write compresses p. It writes to the underlying writer when whole blocks
// are ready, so it keeps up to 64 KB of input.
func (z *Writer) Write(p []byte) (int, error) {
	if z.err != nil {
		return 0, z.err
	}
	if z.closed {
		return 0, errWriteAfterClose
	}
	n := len(p)
	z.started = true
	z.size += uint32(n)
	if z.conc > 0 {
		// each chunk's goroutine computes its CRC-32
		if err := z.writeConcurrent(p); err != nil {
			return 0, err
		}
		return n, nil
	}
	z.crc = crcUpdate(z.crc, p)
	c := z.c
	for len(p) > 0 {
		if len(c.buf) == cap(c.buf) {
			z.slide()
		}
		k := copy(c.buf[len(c.buf):cap(c.buf)], p)
		c.buf = c.buf[:len(c.buf)+k]
		p = p[k:]
		// the last block waits for more input or Close, which marks it final
		for len(c.buf)-z.pend > blockSize {
			c.block(c.buf, z.pend, z.pend+blockSize, false)
			z.pend += blockSize
		}
		if err := z.output(); err != nil {
			return 0, err
		}
	}
	return n, nil
}

// slide keeps the input that is not encoded yet and the window before it.
func (z *Writer) slide() {
	c := z.c
	d := z.pend - windowSize
	if d <= 0 {
		return
	}
	copy(c.buf, c.buf[d:])
	c.buf = c.buf[:len(c.buf)-d]
	z.pend -= d
	if c.m != nil {
		c.m.shift(d)
	}
}

// output writes the finished bytes to w.
func (z *Writer) output() error {
	out := z.c.e.bw.out
	if len(out) == 0 {
		return nil
	}
	if _, err := z.w.Write(out); err != nil {
		z.err = err
		return err
	}
	z.c.e.bw.out = out[:0]
	return nil
}

// Flush compresses the pending input and writes it to the underlying writer,
// ending with an empty stored block, as compress/gzip does. A reader can then
// decode all the data written so far.
func (z *Writer) Flush() error {
	if z.err != nil {
		return z.err
	}
	if z.closed {
		return nil
	}
	z.started = true
	if z.conc > 0 {
		// the chunk ends with the sync marker
		if err := z.dispatch(false); err != nil {
			return err
		}
		return z.drain()
	}
	c := z.c
	if z.pend < len(c.buf) {
		c.block(c.buf, z.pend, len(c.buf), false)
		z.pend = len(c.buf)
	}
	c.e.writeStored(nil, false)
	return z.output()
}

// Close writes the rest of the stream and the gzip trailer. It does not close
// the underlying writer. Close gives z's buffers back to a pool; Reset makes
// z usable again.
func (z *Writer) Close() error {
	if z.err != nil {
		return z.err
	}
	if z.closed {
		return nil
	}
	z.closed = true
	c := z.c
	if z.conc > 0 {
		if err := z.dispatch(true); err != nil {
			return err
		}
		if err := z.drain(); err != nil {
			return err
		}
	} else {
		c.block(c.buf, z.pend, len(c.buf), true)
		z.pend = len(c.buf)
	}
	c.e.bw.align()
	c.e.bw.out = binary.LittleEndian.AppendUint32(c.e.bw.out, z.crc)
	c.e.bw.out = binary.LittleEndian.AppendUint32(c.e.bw.out, z.size)
	err := z.output()
	if err == nil {
		c.buf = c.buf[:0]
		compressorPool.Put(c)
		z.c = nil
	}
	return err
}
