package fastgzip

import (
	"bytes"
	"compress/flate"
	"errors"
	"io"
	"math/rand/v2"
	"sync"
	"testing"
)

// writeChunks writes data to z in chunks of random sizes up to max.
func writeChunks(t testing.TB, z *Writer, r *rand.Rand, data []byte, max int) {
	t.Helper()
	for len(data) > 0 {
		k := min(len(data), 1+r.IntN(max))
		n, err := z.Write(data[:k])
		if err != nil || n != k {
			t.Fatalf("Write: %d, %v", n, err)
		}
		data = data[k:]
	}
}

func TestWriterMatchesGzip(t *testing.T) {
	r := rand.New(rand.NewPCG(21, 22))
	var inputs [][]byte
	for _, n := range []int{0, 1, 100, blockSize, blockSize + 1, 3*blockSize - 7, writerBufSize + 12345, 2*writerBufSize + 100} {
		inputs = append(inputs, sampleData(r, n))
	}
	for level := HuffmanOnly; level <= BestCompression; level++ {
		for _, in := range inputs {
			want, err := Gzip(in, level)
			if err != nil {
				t.Fatal(err)
			}
			var buf bytes.Buffer
			z, err := NewWriterLevel(&buf, level)
			if err != nil {
				t.Fatal(err)
			}
			writeChunks(t, z, r, in, 1+r.IntN(200000))
			if err := z.Close(); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(buf.Bytes(), want) {
				t.Fatalf("level %d, %d bytes: output differs from Gzip", level, len(in))
			}
			if got := stdGunzip(t, buf.Bytes()); !bytes.Equal(got, in) {
				t.Fatalf("level %d, %d bytes: compress/gzip output differs", level, len(in))
			}
		}
	}
}

func TestWriterFlush(t *testing.T) {
	r := rand.New(rand.NewPCG(23, 24))
	data := sampleData(r, 2*writerBufSize)
	for _, level := range []int{NoCompression, BestSpeed, 3, DefaultCompression, HuffmanOnly} {
		var buf bytes.Buffer
		z, _ := NewWriterLevel(&buf, level)
		written := 0
		for written < len(data) {
			k := min(len(data)-written, r.IntN(300000))
			writeChunks(t, z, r, data[written:written+k], 70000)
			written += k
			if err := z.Flush(); err != nil {
				t.Fatal(err)
			}
			// everything written so far decodes from what z has output
			got := make([]byte, written)
			fr := flate.NewReader(bytes.NewReader(buf.Bytes()[10:]))
			if _, err := io.ReadFull(fr, got); err != nil {
				t.Fatalf("level %d after %d bytes: %v", level, written, err)
			}
			if !bytes.Equal(got, data[:written]) {
				t.Fatalf("level %d after %d bytes: output differs", level, written)
			}
		}
		if err := z.Close(); err != nil {
			t.Fatal(err)
		}
		if got := stdGunzip(t, buf.Bytes()); !bytes.Equal(got, data) {
			t.Fatalf("level %d: output differs", level)
		}
	}
}

func TestWriterReset(t *testing.T) {
	r := rand.New(rand.NewPCG(25, 26))
	a, b := sampleData(r, 1<<20), sampleData(r, 700000)
	want, _ := Gzip(b, BestSpeed)

	var buf bytes.Buffer
	z, _ := NewWriterLevel(io.Discard, BestSpeed)
	writeChunks(t, z, r, a, 100000)
	// Reset in the middle of a stream
	z.Reset(&buf)
	writeChunks(t, z, r, b, 100000)
	z.Close()
	if !bytes.Equal(buf.Bytes(), want) {
		t.Fatal("output after Reset differs from Gzip")
	}
	// Reset after Close
	buf.Reset()
	z.Reset(&buf)
	writeChunks(t, z, r, b, 100000)
	z.Close()
	if !bytes.Equal(buf.Bytes(), want) {
		t.Fatal("output after Close and Reset differs from Gzip")
	}
}

type failWriter struct{ n int }

var errFail = errors.New("write failed")

func (w *failWriter) Write(p []byte) (int, error) {
	if w.n <= 0 {
		return 0, errFail
	}
	w.n--
	return len(p), nil
}

// shortWriter writes one byte less than asked, without an error.
type shortWriter struct{}

func (shortWriter) Write(p []byte) (int, error) { return max(len(p)-1, 0), nil }

func TestWriterShortWrite(t *testing.T) {
	r := rand.New(rand.NewPCG(55, 56))
	data := sampleData(r, chunkSize+100)
	for _, conc := range []int{0, 2} {
		z := NewWriter(shortWriter{})
		z.SetConcurrency(conc)
		_, err := z.Write(data)
		if err == nil {
			err = z.Close()
		}
		if !errors.Is(err, io.ErrShortWrite) {
			t.Errorf("concurrency %d: got %v", conc, err)
		}
	}
}

func TestWriterErrors(t *testing.T) {
	if _, err := NewWriterLevel(io.Discard, 10); err == nil {
		t.Error("level 10: no error")
	}
	if _, err := NewWriterLevel(io.Discard, -3); err == nil {
		t.Error("level -3: no error")
	}

	z := NewWriter(io.Discard)
	z.Write([]byte("hello"))
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
	if _, err := z.Write([]byte("x")); err == nil {
		t.Error("Write after Close: no error")
	}

	r := rand.New(rand.NewPCG(27, 28))
	z = NewWriter(&failWriter{n: 1})
	_, err := z.Write(sampleData(r, 1<<20))
	if !errors.Is(err, errFail) {
		t.Fatalf("Write: %v", err)
	}
	if _, err := z.Write([]byte("x")); !errors.Is(err, errFail) {
		t.Errorf("Write after error: %v", err)
	}
	if err := z.Close(); !errors.Is(err, errFail) {
		t.Errorf("Close after error: %v", err)
	}
}

// TestWriterRebase starts the table base near its limit, so that the window
// slides past it and shift rewrites the table.
func TestWriterRebase(t *testing.T) {
	r := rand.New(rand.NewPCG(29, 30))
	data := sampleData(r, 4<<20)
	for _, level := range []int{BestSpeed, 6, BestCompression} {
		want, _ := Gzip(data, level)
		var buf bytes.Buffer
		z, _ := NewWriterLevel(&buf, level)
		var base *int
		switch m := z.c.m.(type) {
		case *fastMatcher:
			base = &m.base
		case *dualMatcher:
			base = &m.base
		case *lazyMatcher:
			base = &m.base
		}
		*base = 1<<30 - 3*writerBufSize/2
		writeChunks(t, z, r, data, 100000)
		if *base >= 1<<30 || *base < 0 {
			t.Fatalf("level %d: base %d", level, *base)
		}
		z.Close()
		// the rebase keeps every entry inside the window
		if !bytes.Equal(buf.Bytes(), want) {
			t.Fatalf("level %d: output differs from Gzip", level)
		}
	}
}

func TestDeflateConcurrent(t *testing.T) {
	r := rand.New(rand.NewPCG(31, 32))
	inputs := make([][]byte, 8)
	for i := range inputs {
		inputs[i] = sampleData(r, 200000+i*1000)
	}
	var wg sync.WaitGroup
	for i, in := range inputs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for k := 0; k < 5; k++ {
				gz, err := Gzip(in, 1+(i+k)%9)
				if err != nil {
					t.Error(err)
					return
				}
				if got, err := Gunzip(gz); err != nil || !bytes.Equal(got, in) {
					t.Errorf("input %d: %v", i, err)
					return
				}
			}
		}()
	}
	wg.Wait()
}

func FuzzWriter(f *testing.F) {
	r := rand.New(rand.NewPCG(33, 34))
	f.Add(sampleData(r, 5000), uint64(1), uint8(1))
	f.Add([]byte{}, uint64(2), uint8(0))
	f.Fuzz(func(t *testing.T, data []byte, seed uint64, level uint8) {
		lv := int(level)%12 - 2
		r := rand.New(rand.NewPCG(seed, 0))
		var buf bytes.Buffer
		z, err := NewWriterLevel(&buf, lv)
		if err != nil {
			t.Fatal(err)
		}
		// 0 is one stream; 1 and 2 compress chunks on goroutines
		if err := z.SetConcurrency(int(seed % 3)); err != nil {
			t.Fatal(err)
		}
		for rest := data; len(rest) > 0; {
			k := min(len(rest), r.IntN(len(data)+1))
			z.Write(rest[:k])
			rest = rest[k:]
			if r.IntN(3) == 0 {
				z.Flush()
			}
		}
		if err := z.Close(); err != nil {
			t.Fatal(err)
		}
		if got := stdGunzip(t, buf.Bytes()); !bytes.Equal(got, data) {
			t.Fatal("compress/gzip output differs")
		}
		if got, err := Gunzip(buf.Bytes()); err != nil || !bytes.Equal(got, data) {
			t.Fatalf("Gunzip: %v", err)
		}
	})
}

// TestTableReuse checks that a compressor gives the same output for an
// input whatever it compressed before: restart invalidates old entries
// without clearing the tables, or clears them when base is too large.
func TestTableReuse(t *testing.T) {
	r := rand.New(rand.NewPCG(35, 36))
	// b repeats a's start at other positions, so that a's table entries
	// point at bytes of b that match
	a := sampleData(r, 300000)
	b := append(append(sampleData(r, 3000), a[:20000]...), a[:20000]...)
	for level := BestSpeed; level <= BestCompression; level++ {
		var c compressor
		c.reset(level)
		// the 0 entries of new tables are not position 0
		if pos, ok := validEntry(c.m); ok {
			t.Fatalf("level %d: entry for position %d in new tables", level, pos)
		}
		want := c.deflate(nil, b)
		c.reset(level)
		c.deflate(nil, a)
		c.reset(level)
		if pos, ok := validEntry(c.m); ok {
			t.Fatalf("level %d: entry for position %d after reset", level, pos)
		}
		if got := c.deflate(nil, b); !bytes.Equal(got, want) {
			t.Fatalf("level %d: output after another stream differs", level)
		}
		// base near maxBase: restart clears the tables
		c.reset(level)
		c.deflate(nil, a)
		switch m := c.m.(type) {
		case *fastMatcher:
			m.base = maxBase - 1000
		case *dualMatcher:
			m.base = maxBase - 1000
		case *lazyMatcher:
			m.base = maxBase - 1000
		}
		c.reset(level)
		if got := c.deflate(nil, b); !bytes.Equal(got, want) {
			t.Fatalf("level %d: output after clear differs", level)
		}
	}
}

// validEntry returns a table entry of m that is a position, if one exists.
func validEntry(m matcher) (int, bool) {
	var tables [][]int32
	var base int
	switch m := m.(type) {
	case *fastMatcher:
		tables, base = [][]int32{m.table[:]}, m.base
	case *dualMatcher:
		long := make([]int32, 0, 2*len(m.long))
		for _, e := range m.long {
			long = append(long, e.cur, e.prev)
		}
		tables, base = [][]int32{m.short[:], long}, m.base
	case *lazyMatcher:
		tables, base = [][]int32{m.head[:]}, m.base
	}
	for _, t := range tables {
		for _, v := range t {
			if pos := int(v) - base; pos >= 0 {
				return pos, true
			}
		}
	}
	return 0, false
}

// TestDeflateSlide lowers deflateSlide, so that Deflate moves its view of
// the input as it does every 512 MB.
func TestDeflateSlide(t *testing.T) {
	r := rand.New(rand.NewPCG(37, 38))
	data := sampleData(r, 2<<20)
	var want [BestCompression + 1][]byte
	for level := BestSpeed; level <= BestCompression; level++ {
		want[level], _ = Deflate(nil, data, level)
	}
	defer func(v int) { deflateSlide = v }(deflateSlide)
	deflateSlide = 300000
	for level := BestSpeed; level <= BestCompression; level++ {
		got, _ := Deflate(nil, data, level)
		if !bytes.Equal(got, want[level]) {
			t.Fatalf("level %d: output differs", level)
		}
	}
}
