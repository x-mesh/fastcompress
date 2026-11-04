package fastgzip

import (
	"bytes"
	"io"
	"math"
	"math/rand/v2"
	"os"
	"strconv"
	"testing"
)

func TestZstdRoundTrip(t *testing.T) {
	r := rand.New(rand.NewPCG(7, 8))
	inputs := [][]byte{nil, {1}, []byte("hello, hello, hello, zstd"), bytes.Repeat([]byte{7}, 300000)}
	for _, n := range []int{100, 1000, 5000, 70000, 200 << 10, 1 << 20, 3 << 20} {
		inputs = append(inputs, sampleData(r, n))
	}
	rnd := make([]byte, 100000)
	for i := range rnd {
		rnd[i] = byte(r.Uint32())
	}
	inputs = append(inputs, rnd)
	for _, level := range []int{1, 3} {
		for i, in := range inputs {
			comp := Zstd(nil, in, level)
			got, err := Unzstd(comp)
			if err != nil {
				t.Fatalf("level %d, input %d (%d bytes): %v", level, i, len(in), err)
			}
			if !bytes.Equal(got, in) {
				t.Fatalf("level %d, input %d (%d bytes): round trip differs", level, i, len(in))
			}
		}
	}
}

// FASTGZIP_ZSTD_ENC_BENCH names one file to compress, at the level in
// FASTGZIP_ZSTD_LEVEL (default 1).
func BenchmarkZstd(b *testing.B) {
	level, _ := strconv.Atoi(os.Getenv("FASTGZIP_ZSTD_LEVEL"))
	level = max(level, 1)
	f := os.Getenv("FASTGZIP_ZSTD_ENC_BENCH")
	if f == "" {
		b.Skip("FASTGZIP_ZSTD_ENC_BENCH not set")
	}
	src, err := os.ReadFile(f)
	if err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(len(src)))
	var out []byte
	for b.Loop() {
		out = Zstd(out[:0], src, level)
	}
}

func FuzzZstdRoundTrip(f *testing.F) {
	f.Add([]byte("hello, hello, hello, zstd"), byte(1))
	f.Add(bytes.Repeat([]byte("abcd"), 1000), byte(1))
	f.Fuzz(func(t *testing.T, data []byte, level byte) {
		comp := Zstd(nil, data, int(level%3)+1)
		got, err := Unzstd(comp)
		if err != nil {
			t.Fatalf("%d bytes: %v", len(data), err)
		}
		if !bytes.Equal(got, data) {
			t.Fatalf("%d bytes: round trip differs", len(data))
		}
	})
}

// Zstd appends to dst and leaves what it held.
func TestZstdAppends(t *testing.T) {
	prefix := []byte("prefix")
	out := Zstd(append([]byte(nil), prefix...), []byte("hello, zstd"), 1)
	if !bytes.HasPrefix(out, prefix) {
		t.Fatal("prefix overwritten")
	}
	got, err := Unzstd(out[len(prefix):])
	if err != nil || string(got) != "hello, zstd" {
		t.Fatalf("got %q, %v", got, err)
	}
}

// Moving the table base, which inputs past 1 GB do, keeps the output valid
// and the ratio close.
func TestZstdRebase(t *testing.T) {
	r := rand.New(rand.NewPCG(13, 14))
	// past the 8 MB window of level 3, so both levels move the base
	data := sampleData(r, 12<<20)
	for _, level := range []int{1, 3} {
		plain := Zstd(nil, data, level)
		old := rebaseAt
		rebaseAt = 1 << 18
		moved := Zstd(nil, data, level)
		rebaseAt = old
		got, err := Unzstd(moved)
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("level %d: round trip after rebase: %v", level, err)
		}
		if float64(len(moved)) > 1.01*float64(len(plain)) {
			t.Errorf("level %d: %d bytes with rebasing, %d without", level, len(moved), len(plain))
		}
	}
}

func writeZstd(t *testing.T, data []byte, level, chunk int) []byte {
	var buf bytes.Buffer
	w := NewZstdWriter(&buf, level)
	for p := data; len(p) > 0; {
		n := min(chunk, len(p))
		if _, err := w.Write(p[:n]); err != nil {
			t.Fatal(err)
		}
		p = p[n:]
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestZstdWriter(t *testing.T) {
	r := rand.New(rand.NewPCG(15, 16))
	// 20 MB slides the history of both levels
	big := sampleData(r, 20<<20)
	for _, tc := range []struct {
		data  []byte
		chunk int
	}{
		{nil, 1}, {[]byte("x"), 1}, {sampleData(r, 300<<10), 1}, {sampleData(r, 300<<10), 1000},
		{big, 1 << 20}, {big, 100003},
	} {
		for _, level := range []int{1, 3} {
			comp := writeZstd(t, tc.data, level, tc.chunk)
			got, err := Unzstd(comp)
			if err != nil || !bytes.Equal(got, tc.data) {
				t.Fatalf("level %d, %d bytes in %d-byte writes: %v", level, len(tc.data), tc.chunk, err)
			}
			if len(tc.data) > 1<<20 {
				got, err = readAllZstd(bytes.NewReader(comp), 64<<10)
				if err != nil || !bytes.Equal(got, tc.data) {
					t.Fatalf("level %d: ZstdReader: %v", level, err)
				}
				// the stream costs at most 1% more than one Zstd call
				if whole := Zstd(nil, tc.data, level); float64(len(comp)) > 1.01*float64(len(whole)) {
					t.Errorf("level %d: %d bytes streamed, %d whole", level, len(comp), len(whole))
				}
			}
		}
	}
}

// After Flush, the output so far decodes to everything written.
func TestZstdWriterFlush(t *testing.T) {
	r := rand.New(rand.NewPCG(17, 18))
	data := sampleData(r, 1<<20)
	var buf bytes.Buffer
	w := NewZstdWriter(&buf, 1)
	written := 0
	for _, cut := range []int{1000, 5000, 400 << 10, 1 << 20} {
		if _, err := w.Write(data[written:cut]); err != nil {
			t.Fatal(err)
		}
		written = cut
		if err := w.Flush(); err != nil {
			t.Fatal(err)
		}
		got, err := readAllZstd(bytes.NewReader(buf.Bytes()), 4096)
		if err != io.ErrUnexpectedEOF || !bytes.Equal(got, data[:cut]) {
			t.Fatalf("after Flush at %d: %d bytes, %v", cut, len(got), err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if got, err := Unzstd(buf.Bytes()); err != nil || !bytes.Equal(got, data) {
		t.Fatalf("after Close: %v", err)
	}
	if _, err := w.Write([]byte("x")); err != errWriterClosed {
		t.Errorf("Write after Close: got %v", err)
	}
	if err := w.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
	var buf2 bytes.Buffer
	w.Reset(&buf2, 3)
	w.Write(data[:1000])
	w.Close()
	if got, err := Unzstd(buf2.Bytes()); err != nil || !bytes.Equal(got, data[:1000]) {
		t.Fatalf("after Reset: %v", err)
	}
}

func FuzzZstdWriter(f *testing.F) {
	f.Add([]byte("hello, hello, hello, zstd"), byte(1), uint16(3), uint16(7))
	f.Fuzz(func(t *testing.T, data []byte, level byte, chunk, flushAt uint16) {
		var buf bytes.Buffer
		w := NewZstdWriter(&buf, int(level%3)+1)
		c := int(chunk%4096) + 1
		for i := 0; i < len(data); i += c {
			w.Write(data[i:min(i+c, len(data))])
			if flushAt > 0 && (i/c)%int(flushAt) == 0 {
				w.Flush()
			}
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		got, err := Unzstd(buf.Bytes())
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("%d bytes: %v", len(data), err)
		}
	})
}

// The pipelined encoder writes the same bytes as the serial one.
func TestZstdPipelinedMatchesSerial(t *testing.T) {
	r := rand.New(rand.NewPCG(21, 22))
	data := sampleData(r, 5<<20)
	for _, level := range []int{1, 3} {
		piped := Zstd(nil, data, level)
		old := pipeMinSize
		pipeMinSize = math.MaxInt
		serial := Zstd(nil, data, level)
		pipeMinSize = old
		if !bytes.Equal(piped, serial) {
			t.Errorf("level %d: pipelined and serial output differ", level)
		}
	}
}

type budgetWriter struct{ n int }

func (w *budgetWriter) Write(p []byte) (int, error) {
	if w.n < len(p) {
		return 0, io.ErrShortWrite
	}
	w.n -= len(p)
	return len(p), nil
}

// A failing underlying writer shows at Flush or Close, and z does not hang.
func TestZstdWriterError(t *testing.T) {
	r := rand.New(rand.NewPCG(23, 24))
	data := sampleData(r, 2<<20)
	w := NewZstdWriter(&budgetWriter{n: 50000}, 1)
	w.Write(data)
	if err := w.Flush(); err != io.ErrShortWrite {
		t.Errorf("Flush: got %v", err)
	}
	if _, err := w.Write(data[:10]); err != io.ErrShortWrite {
		t.Errorf("Write after the error: got %v", err)
	}
	if err := w.Close(); err != io.ErrShortWrite {
		t.Errorf("Close: got %v", err)
	}
	w = NewZstdWriter(&budgetWriter{n: 50000}, 3)
	w.Write(data)
	if err := w.Close(); err != io.ErrShortWrite {
		t.Errorf("Close without Flush: got %v", err)
	}
}
