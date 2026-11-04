package fastgzip

import (
	"encoding/binary"
	"fmt"
	"sync"
)

// Compression levels, with the same values as in compress/gzip.
const (
	NoCompression      = 0
	BestSpeed          = 1
	BestCompression    = 9
	DefaultCompression = -1
	// HuffmanOnly writes literals only, with no matches.
	HuffmanOnly = -2
)

func checkLevel(level int) (int, error) {
	if level == DefaultCompression {
		return 6, nil
	}
	if level < HuffmanOnly || level > BestCompression {
		return 0, fmt.Errorf("fastgzip: invalid compression level: %d", level)
	}
	return level, nil
}

// matcher finds the matches of one block.
type matcher interface {
	// encode adds the tokens for buf[start:end] to e. Matches may refer to
	// buf[start-windowSize:start] and end before end.
	encode(e *encoder, buf []byte, start, end int)
	// shift tells the matcher that the data in its buffer moved delta
	// bytes back.
	shift(delta int)
}

// maxBase bounds posBase.base, so that a position plus base fits in an
// int32 for every buffer: a Writer's window, or a Deflate input that is
// resliced every deflateSlide bytes.
const maxBase = 1 << 30

// posBase is how the matchers store positions in their tables: as pos+base
// in an int32. A candidate below 0 is no candidate.
type posBase struct {
	base int
	// every table entry is below base+hi
	hi int
}

// cover records that the tables may hold positions below end.
func (b *posBase) cover(end int) {
	b.hi = max(b.hi, end)
}

// restart prepares the tables for a new stream. Moving base past every
// entry makes them all invalid without clearing the tables, which took most
// of the time for inputs of a few KB. It reports true when base got too
// large and the tables must be cleared instead. base starts at 1, so that
// the 0 entries of a cleared table are not position 0: the output of a
// stream does not depend on what the tables held before.
func (b *posBase) restart() (clear bool) {
	if b.base > 0 && b.base+b.hi < maxBase {
		b.base += b.hi
		b.hi = 0
		return false
	}
	b.base, b.hi = 1, 0
	return true
}

// slide records that the data moved delta bytes back. It reports true when
// base reached maxBase and the tables must be rebased.
func (b *posBase) slide(delta int) (rebase bool) {
	b.base += delta
	b.hi -= delta
	return b.base >= maxBase
}

// compressor holds the state of one stream. compressorPool keeps them
// between streams: the hash tables are 256 KB or more.
type compressor struct {
	e     encoder
	level int
	m     matcher // nil for NoCompression and HuffmanOnly
	fast  fastMatcher
	dual  dualMatcher
	lazy  lazyMatcher
	// buf is the window of a Writer
	buf []byte
}

var compressorPool sync.Pool

func getCompressor(level int) *compressor {
	c, ok := compressorPool.Get().(*compressor)
	if !ok {
		c = &compressor{}
	}
	c.reset(level)
	return c
}

func (c *compressor) reset(level int) {
	c.level = level
	c.e.bw = bitWriter{}
	c.m = nil
	switch {
	case level >= 7:
		c.lazy.reset(lazyLevels[level])
		c.m = &c.lazy
	case level >= 4:
		c.dual.reset(dualLevels[level])
		c.m = &c.dual
	case level >= BestSpeed:
		c.fast.reset(fastLevels[level])
		c.m = &c.fast
	}
}

// block writes buf[start:end] as one block, or more for NoCompression.
func (c *compressor) block(buf []byte, start, end int, final bool) {
	if c.level == NoCompression {
		c.e.writeStored(buf[start:end], final)
		return
	}
	c.e.startBlock()
	if c.m == nil {
		c.e.literals(buf[start:end])
	} else {
		c.m.encode(&c.e, buf, start, end)
	}
	c.e.writeBlock(buf[start:end], final)
}

// Deflate appends src to dst as one raw DEFLATE stream at the given level
// and returns the extended slice.
func Deflate(dst, src []byte, level int) ([]byte, error) {
	level, err := checkLevel(level)
	if err != nil {
		return dst, err
	}
	c := getCompressor(level)
	defer compressorPool.Put(c)
	return c.deflate(dst, src), nil
}

// deflate appends src to dst as one DEFLATE stream. c must be fresh from
// getCompressor or reset.
func (c *compressor) deflate(dst, src []byte) []byte {
	c.e.bw.out = dst
	// The matchers see src from off on. Moving off forward, as a Writer
	// slides its window, keeps positions small enough for their int32
	// tables in inputs over 2 GB.
	off := 0
	for start := 0; ; start += blockSize {
		end := min(len(src), start+blockSize)
		if c.m != nil && start-off >= deflateSlide {
			d := start - windowSize - off
			off += d
			c.m.shift(d)
		}
		c.block(src[off:], start-off, end-off, end == len(src))
		if end == len(src) {
			return c.e.finish()
		}
	}
}

// deflateSlide is how far Deflate lets positions grow before it moves its
// view of the input forward. A variable, so that a test can lower it.
var deflateSlide = 1 << 29

// appendGzipHeader appends a member header with no name, no modification
// time and operating system 255 (unknown), as compress/gzip writes.
func appendGzipHeader(dst []byte, level int) []byte {
	xfl := byte(0)
	switch level {
	case BestSpeed:
		xfl = 4
	case BestCompression:
		xfl = 2
	}
	return append(dst, 0x1f, 0x8b, 8, 0, 0, 0, 0, 0, xfl, 255)
}

// Gzip returns src compressed as one gzip member at the given level.
func Gzip(src []byte, level int) ([]byte, error) {
	l, err := checkLevel(level)
	if err != nil {
		return nil, err
	}
	dst := appendGzipHeader(make([]byte, 0, len(src)/2+64), l)
	var sum uint32
	if len(src) >= concurrentCRCSize {
		// as in Gunzip, the checksum runs beside the encoder
		done := make(chan uint32)
		go func() { done <- crcUpdate(0, src) }()
		dst, _ = Deflate(dst, src, l)
		sum = <-done
	} else {
		dst, _ = Deflate(dst, src, l)
		sum = crcUpdate(0, src)
	}
	dst = binary.LittleEndian.AppendUint32(dst, sum)
	return binary.LittleEndian.AppendUint32(dst, uint32(len(src))), nil
}
