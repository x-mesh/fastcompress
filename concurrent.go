package fastgzip

import (
	"encoding/binary"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
)

// chunkSize is the input of one chunk in concurrent compression: 16 whole
// blocks, about 1 MB. A chunk costs its 32 KB dictionary once more and a sync
// marker of up to 5 bytes, about 3% of the time and 0.0005% of the size.
const chunkSize = 16 * blockSize

var errConcurrencyAfterWrite = errors.New("fastgzip: SetConcurrency after Write")

// compressChunk appends buf[dict:] to dst as DEFLATE blocks at level. Its
// matches may refer to the dictionary buf[:dict], the data before the chunk.
// Unless final, the chunk ends with an empty stored block, so that the
// next chunk starts on a byte boundary and the chunks can be concatenated.
// The output depends only on buf, dict, level and final.
func compressChunk(dst, buf []byte, dict, level int, final bool) []byte {
	c := getCompressor(level)
	c.e.bw.out = dst
	if c.m != nil && dict > 0 {
		// Encoding the dictionary fills the match tables; its tokens are
		// dropped by the first block.
		c.e.startBlock()
		c.m.encode(&c.e, buf, 0, dict)
	}
	if len(buf) > dict || final {
		for start := dict; ; start += blockSize {
			end := min(len(buf), start+blockSize)
			c.block(buf, start, end, final && end == len(buf))
			if end == len(buf) {
				break
			}
		}
	}
	if !final {
		c.e.writeStored(nil, false)
	}
	dst = c.e.finish()
	// the pool must not keep the caller's output alive
	c.e.bw.out = nil
	compressorPool.Put(c)
	return dst
}

// GzipConcurrent compresses src to one gzip member, like Gzip, on up to n
// goroutines. n below 1 means runtime.GOMAXPROCS(0).
//
// The input goes in chunks of about 1 MB. Each chunk can refer to the 32 KB
// before it, so the output is at most a few tenths of a percent larger
// than the output of Gzip. The output is the same for every n, and the same
// as the output of Gzip for an input of one chunk or less.
func GzipConcurrent(src []byte, level, n int) ([]byte, error) {
	l, err := checkLevel(level)
	if err != nil {
		return nil, err
	}
	if n < 1 {
		n = runtime.GOMAXPROCS(0)
	}
	chunks := max(1, (len(src)+chunkSize-1)/chunkSize)
	outs := make([][]byte, chunks)
	sums := make([]uint32, chunks)
	compress := func(i int) {
		start := i * chunkSize
		from := max(0, start-windowSize)
		end := min(len(src), start+chunkSize)
		outs[i] = compressChunk(make([]byte, 0, (end-start)/2+64), src[from:end], start-from, l, i == chunks-1)
		sums[i] = crcUpdate(0, src[start:end])
	}

	if n = min(n, chunks); n == 1 {
		for i := range chunks {
			compress(i)
		}
	} else {
		var next atomic.Int64
		var wg sync.WaitGroup
		for range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := int(next.Add(1) - 1); i < chunks; i = int(next.Add(1) - 1) {
					compress(i)
				}
			}()
		}
		wg.Wait()
	}

	size := 10 + 8
	for _, o := range outs {
		size += len(o)
	}
	dst := appendGzipHeader(make([]byte, 0, size), l)
	sum := uint32(0)
	for i, o := range outs {
		dst = append(dst, o...)
		sum = crcCombine(sum, sums[i], min(chunkSize, len(src)-i*chunkSize))
	}
	dst = binary.LittleEndian.AppendUint32(dst, sum)
	return binary.LittleEndian.AppendUint32(dst, uint32(len(src))), nil
}

// chunkJob is one chunk of a concurrent Writer, compressed on its own
// goroutine.
type chunkJob struct {
	buf  []byte // dictionary and chunk; the job owns it until done
	out  []byte
	sum  uint32 // CRC-32 of the chunk without its dictionary
	n    int    // chunk length
	done chan struct{}
}

var chunkPool = sync.Pool{New: func() any {
	b := make([]byte, 0, windowSize+chunkSize)
	return &b
}}

// outPool keeps the output buffers of a concurrent Writer's chunks. Growing
// a new one for every chunk took about a tenth of the time.
var outPool = sync.Pool{New: func() any {
	b := make([]byte, 0, chunkSize/2)
	return &b
}}

// SetConcurrency makes z compress chunks of about 1 MB on up to n
// goroutines, as GzipConcurrent does. Without Flush, z then writes the same
// bytes as GzipConcurrent for the same input and level. n below 1 sets
// the default: one stream on the caller's goroutine.
//
// It holds up to 2n chunks of input and output at a time. Call it before the
// first Write or Flush after NewWriter or Reset; Reset keeps the setting.
func (z *Writer) SetConcurrency(n int) error {
	if z.started {
		return errConcurrencyAfterWrite
	}
	z.conc = max(n, 0)
	return nil
}

func (z *Writer) writeConcurrent(p []byte) error {
	for len(p) > 0 {
		if z.chunk == nil {
			z.chunk = (*chunkPool.Get().(*[]byte))[:0]
			z.dict = 0
		}
		// a full chunk waits for more input or Close, which marks it final
		if len(z.chunk)-z.dict == chunkSize {
			if err := z.dispatch(false); err != nil {
				return err
			}
		}
		k := copy(z.chunk[len(z.chunk):z.dict+chunkSize], p)
		z.chunk = z.chunk[:len(z.chunk)+k]
		p = p[k:]
	}
	return nil
}

// dispatch starts compressing the current chunk and, unless final, starts
// the next one with the last 32 KB as its dictionary. It then writes
// finished chunks in order while z.conc or more are in flight.
func (z *Writer) dispatch(final bool) error {
	if z.chunk == nil {
		z.chunk = (*chunkPool.Get().(*[]byte))[:0]
		z.dict = 0
	}
	j := &chunkJob{buf: z.chunk, n: len(z.chunk) - z.dict, done: make(chan struct{})}
	dict, level := z.dict, z.level
	if z.sem == nil {
		z.sem = make(chan struct{}, z.conc)
	}
	sem := z.sem
	go func() {
		sem <- struct{}{}
		j.out = compressChunk((*outPool.Get().(*[]byte))[:0], j.buf, dict, level, final)
		j.sum = crcUpdate(0, j.buf[dict:])
		<-sem
		close(j.done)
	}()
	z.queue = append(z.queue, j)
	z.chunk = nil
	if !final {
		next := (*chunkPool.Get().(*[]byte))[:0]
		d := min(windowSize, len(j.buf))
		z.chunk = append(next, j.buf[len(j.buf)-d:]...)
		z.dict = d
	}
	// Twice as many chunks in flight as goroutines keep the goroutines busy
	// while the oldest chunk is awaited.
	// z.conc can be up to MaxInt, where 2*z.conc overflows
	for len(z.queue)/2 >= z.conc {
		if err := z.writeHead(); err != nil {
			return err
		}
	}
	return nil
}

// writeHead waits for the oldest chunk and writes it, after the header for
// the first one.
func (z *Writer) writeHead() error {
	j := z.queue[0]
	z.queue[0] = nil
	z.queue = z.queue[1:]
	<-j.done
	b := j.buf[:0]
	chunkPool.Put(&b)
	z.crc = crcCombine(z.crc, j.sum, j.n)
	if z.err != nil {
		return z.err
	}
	if err := z.output(); err != nil {
		return err
	}
	err := writeFull(z.w, j.out)
	o := j.out[:0]
	outPool.Put(&o)
	if err != nil {
		z.err = err
	}
	return z.err
}

// discard waits for every chunk in flight, which still owns its buffer,
// and drops its output.
func (z *Writer) discard() {
	for _, j := range z.queue {
		<-j.done
		b, o := j.buf[:0], j.out[:0]
		chunkPool.Put(&b)
		outPool.Put(&o)
	}
	z.queue = nil
}

// drain waits for every chunk in flight. It writes them while z has no
// error.
func (z *Writer) drain() error {
	for len(z.queue) > 0 {
		z.writeHead()
	}
	return z.err
}
