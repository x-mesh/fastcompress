package fastgzip

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"
)

type zstdVector struct {
	name string
	src  []byte
	sum  string
}

// testdata/zstd holds frames made by the zstd 1.5.5 CLI, with the SHA-256 of
// what `zstd -dc` decodes them to.
func zstdVectors(t testing.TB) []zstdVector {
	f, err := os.Open("testdata/zstd/SHA256SUMS")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var vs []zstdVector
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		sum, name, ok := strings.Cut(sc.Text(), "  ")
		if !ok {
			t.Fatalf("bad SHA256SUMS line %q", sc.Text())
		}
		src, err := os.ReadFile(filepath.Join("testdata/zstd", name))
		if err != nil {
			t.Fatal(err)
		}
		vs = append(vs, zstdVector{name, src, sum})
	}
	if len(vs) == 0 {
		t.Fatal("no zstd test vectors")
	}
	return vs
}

func sha256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func forEachPipeline(t *testing.T, f func(t *testing.T)) {
	t.Run("serial", f)
	t.Run("pipelined", func(t *testing.T) {
		old := pipeMinSize
		pipeMinSize = 0
		defer func() { pipeMinSize = old }()
		f(t)
	})
}

func TestUnzstdVectors(t *testing.T) {
	forEachPipeline(t, func(t *testing.T) {
		for _, v := range zstdVectors(t) {
			got, err := Unzstd(v.src)
			if err != nil {
				t.Errorf("%s: %v", v.name, err)
				continue
			}
			if sha256Hex(got) != v.sum {
				t.Errorf("%s: output differs (%d bytes)", v.name, len(got))
			}
		}
	})
}

// FASTGZIP_ZSTD_CORPUS points at a directory of .zst files. A file with the
// same name without .zst holds the expected output; otherwise the frame
// checksums are the check.
func TestUnzstdCorpus(t *testing.T) {
	dir := os.Getenv("FASTGZIP_ZSTD_CORPUS")
	if dir == "" {
		t.Skip("FASTGZIP_ZSTD_CORPUS not set")
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.zst"))
	if len(files) == 0 {
		t.Fatal("no .zst files in", dir)
	}
	forEachPipeline(t, func(t *testing.T) {
		for _, f := range files {
			src, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			got, err := Unzstd(src)
			if err != nil {
				t.Errorf("%s: %v", filepath.Base(f), err)
				continue
			}
			base := filepath.Base(f)
			want, err := os.ReadFile(filepath.Join(dir, base[:strings.IndexByte(base, '.')]))
			if err == nil && !bytes.Equal(got, want) {
				t.Errorf("%s: output differs (%d vs %d bytes)", base, len(got), len(want))
			}
		}
	})
}

func TestUnzstdTruncated(t *testing.T) {
	for _, v := range zstdVectors(t) {
		if v.name == "multi.zst" || v.name == "skippable.zst" {
			// end at frame boundaries, where a prefix is valid
			continue
		}
		// an empty input is no frames, like Gunzip
		for n := 1; n < len(v.src); n++ {
			if n > 64 && n < len(v.src)-64 && n%97 != 0 {
				continue
			}
			if _, err := Unzstd(v.src[:n]); err == nil {
				t.Errorf("%s: %d of %d bytes accepted", v.name, n, len(v.src))
			}
		}
	}
}

func TestUnzstdErrors(t *testing.T) {
	tiny := zstdVectors(t)[0].src
	for _, tc := range []struct {
		name string
		src  []byte
		want error
	}{
		{"magic prefix", []byte{0x28, 0xb5}, errEOF},
		{"not zstd", []byte("hello"), ErrHeader},
		{"reserved bit", append([]byte{0x28, 0xb5, 0x2f, 0xfd, 0x08}, tiny[5:]...), ErrHeader},
		{"trailing garbage", append(append([]byte(nil), tiny...), 1, 2, 3, 4), ErrHeader},
		{"dictionary", []byte{0x28, 0xb5, 0x2f, 0xfd, 0x01, 0x00, 0x07}, errZstdDict},
	} {
		if _, err := Unzstd(tc.src); err != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, err, tc.want)
		}
	}
}

func TestUnzstdChecksum(t *testing.T) {
	for _, v := range zstdVectors(t) {
		if v.name != "text.l3.zst" {
			continue
		}
		b := append([]byte(nil), v.src...)
		b[len(b)-1] ^= 1
		if _, err := Unzstd(b); err != ErrChecksum {
			t.Errorf("got %v, want ErrChecksum", err)
		}
		return
	}
	t.Fatal("text.l3.zst missing")
}

// Corrupt frames must fail or decode, never panic, in both decoders.
func TestUnzstdCorrupt(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	vs := zstdVectors(t)
	forEachPipeline(t, func(t *testing.T) {
		for range 3000 {
			v := vs[r.IntN(len(vs))]
			b := append([]byte(nil), v.src...)
			if len(b) == 0 {
				continue
			}
			for range 1 + r.IntN(4) {
				b[r.IntN(len(b))] ^= byte(1 + r.IntN(255))
			}
			Unzstd(b)
		}
	})
}

func TestXXH64(t *testing.T) {
	// vectors from Go's internal/zstd
	for _, tc := range []struct {
		data string
		sum  uint64
	}{
		{"hello, world", 0xb33a384e6d1b1242},
		{"abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789$", 0x1032d841e824f998},
	} {
		if got := xxh64Sum([]byte(tc.data)); got != tc.sum {
			t.Errorf("%q: got %#x, want %#x", tc.data, got, tc.sum)
		}
	}
	r := rand.New(rand.NewPCG(3, 4))
	data := make([]byte, 5000)
	for i := range data {
		data[i] = byte(r.Uint32())
	}
	want := xxh64Sum(data)
	for range 100 {
		var h xxh64
		h.reset()
		for p := data; len(p) > 0; {
			n := min(len(p), r.IntN(100))
			h.write(p[:n])
			p = p[n:]
		}
		if got := h.sum(); got != want {
			t.Fatalf("chunked: got %#x, want %#x", got, want)
		}
	}
}

func FuzzUnzstd(f *testing.F) {
	for _, v := range zstdVectors(f) {
		f.Add(v.src)
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		got, err := Unzstd(b)
		old := pipeMinSize
		pipeMinSize = 0
		got2, err2 := Unzstd(b)
		pipeMinSize = old
		if (err == nil) != (err2 == nil) || !bytes.Equal(got, got2) {
			t.Fatalf("serial and pipelined decoders disagree: %v / %v", err, err2)
		}
	})
}

// FASTGZIP_ZSTD_BENCH names one .zst file to decode.
func BenchmarkUnzstd(b *testing.B) {
	f := os.Getenv("FASTGZIP_ZSTD_BENCH")
	if f == "" {
		b.Skip("FASTGZIP_ZSTD_BENCH not set")
	}
	src, err := os.ReadFile(f)
	if err != nil {
		b.Fatal(err)
	}
	out, err := Unzstd(src)
	if err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(len(out)))
	for b.Loop() {
		if _, err := Unzstd(src); err != nil {
			b.Fatal(err)
		}
	}
}

func readAllZstd(r io.Reader, n int) ([]byte, error) {
	z, err := NewZstdReader(r)
	if err == io.EOF {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer z.Close()
	return readWith(z, n)
}

func TestZstdReaderVectors(t *testing.T) {
	forEachPipeline(t, func(t *testing.T) {
		for _, v := range zstdVectors(t) {
			for _, n := range []int{1, 7, 4096, 1 << 20} {
				got, err := readAllZstd(bytes.NewReader(v.src), n)
				if err != nil {
					t.Errorf("%s, %d-byte reads: %v", v.name, n, err)
					continue
				}
				if sha256Hex(got) != v.sum {
					t.Errorf("%s, %d-byte reads: output differs (%d bytes)", v.name, n, len(got))
				}
			}
			got, err := readAllZstd(iotest.OneByteReader(bytes.NewReader(v.src)), 4096)
			if err != nil || sha256Hex(got) != v.sum {
				t.Errorf("%s, one-byte source: %v", v.name, err)
			}
		}
	})
}

func TestZstdReaderCorpus(t *testing.T) {
	dir := os.Getenv("FASTGZIP_ZSTD_CORPUS")
	if dir == "" {
		t.Skip("FASTGZIP_ZSTD_CORPUS not set")
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.zst"))
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		want, err := Unzstd(src)
		if err != nil {
			t.Fatal(err)
		}
		got, err := readAllZstd(bufio.NewReaderSize(bytes.NewReader(src), 1<<20), 64<<10)
		if err != nil {
			t.Errorf("%s: %v", filepath.Base(f), err)
			continue
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s: output differs (%d vs %d bytes)", filepath.Base(f), len(got), len(want))
		}
	}
}

// A truncated stream returns everything it decoded, then
// io.ErrUnexpectedEOF.
func TestZstdReaderTruncated(t *testing.T) {
	forEachPipeline(t, func(t *testing.T) {
		for _, v := range zstdVectors(t) {
			if v.name != "text.l3.zst" {
				continue
			}
			full, _ := Unzstd(v.src)
			for _, n := range []int{5, 20, len(v.src) / 2, len(v.src) - 1} {
				got, err := readAllZstd(bytes.NewReader(v.src[:n]), 4096)
				if err != io.ErrUnexpectedEOF {
					t.Errorf("%d bytes: got %v, want io.ErrUnexpectedEOF", n, err)
				}
				if !bytes.HasPrefix(full, got) {
					t.Errorf("%d bytes: output is not a prefix", n)
				}
			}
			return
		}
		t.Fatal("text.l3.zst missing")
	})
}

func TestZstdReaderErrors(t *testing.T) {
	if _, err := NewZstdReader(bytes.NewReader(nil)); err != io.EOF {
		t.Errorf("empty: got %v, want io.EOF", err)
	}
	if _, err := NewZstdReader(strings.NewReader("hello")); err != ErrHeader {
		t.Errorf("not zstd: got %v, want ErrHeader", err)
	}
	tiny := zstdVectors(t)[0].src
	_, err := readAllZstd(bytes.NewReader(append(append([]byte(nil), tiny...), 1, 2, 3, 4)), 4096)
	if err != ErrHeader {
		t.Errorf("trailing garbage: got %v, want ErrHeader", err)
	}
}

func TestZstdReaderResetAndClose(t *testing.T) {
	forEachPipeline(t, func(t *testing.T) {
		vs := zstdVectors(t)
		z, err := NewZstdReader(bytes.NewReader(vs[0].src))
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range vs {
			if v.name == "empty.zst" {
				continue
			}
			if err := z.Reset(bytes.NewReader(v.src)); err != nil {
				t.Fatalf("%s: %v", v.name, err)
			}
			got, err := io.ReadAll(z)
			if err != nil || sha256Hex(got) != v.sum {
				t.Errorf("%s after Reset: %v", v.name, err)
			}
		}
		z.Close()
		if _, err := z.Read(make([]byte, 1)); err != errClosed {
			t.Errorf("Read after Close: got %v", err)
		}
		if err := z.Reset(bytes.NewReader(vs[1].src)); err != nil {
			t.Fatal(err)
		}
		if got, err := io.ReadAll(z); err != nil || sha256Hex(got) != vs[1].sum {
			t.Errorf("after Close and Reset: %v", err)
		}
	})
}

func FuzzZstdReader(f *testing.F) {
	for _, v := range zstdVectors(f) {
		f.Add(v.src)
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		want, werr := Unzstd(b)
		old := pipeMinSize
		pipeMinSize = 0
		got, err := readAllZstd(bytes.NewReader(b), 1000)
		pipeMinSize = old
		if werr == nil && err == nil {
			if !bytes.Equal(got, want) {
				t.Fatalf("Reader and Unzstd disagree: %d vs %d bytes", len(got), len(want))
			}
			return
		}
		if werr == nil && err != errZstdWindow {
			t.Fatalf("Reader failed where Unzstd succeeded: %v", err)
		}
		if err == nil {
			t.Fatalf("Reader succeeded where Unzstd failed: %v", werr)
		}
	})
}

// FASTGZIP_ZSTD_BENCH names one .zst file to read with 64 KB reads.
func BenchmarkZstdReader(b *testing.B) {
	f := os.Getenv("FASTGZIP_ZSTD_BENCH")
	if f == "" {
		b.Skip("FASTGZIP_ZSTD_BENCH not set")
	}
	src, err := os.ReadFile(f)
	if err != nil {
		b.Fatal(err)
	}
	out, err := Unzstd(src)
	if err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(len(out)))
	z, err := NewZstdReader(bytes.NewReader(src))
	if err != nil {
		b.Fatal(err)
	}
	buf := make([]byte, 64<<10)
	for b.Loop() {
		if err := z.Reset(bytes.NewReader(src)); err != nil {
			b.Fatal(err)
		}
		for {
			_, err := z.Read(buf)
			if err == io.EOF {
				break
			}
			if err != nil {
				b.Fatal(err)
			}
		}
	}
}

// Reset in the middle of a stream must stop the read-ahead goroutine from
// touching the new stream.
func TestZstdReaderResetMidStream(t *testing.T) {
	old := pipeMinSize
	pipeMinSize = 0
	defer func() { pipeMinSize = old }()
	vs := zstdVectors(t)
	var big, small zstdVector
	for _, v := range vs {
		switch v.name {
		case "text.l1.zst":
			big = v
		case "binary.l3.zst":
			small = v
		}
	}
	z, err := NewZstdReader(bytes.NewReader(big.src))
	if err != nil {
		t.Fatal(err)
	}
	for range 50 {
		if _, err := io.ReadFull(z, make([]byte, 100)); err != nil {
			t.Fatal(err)
		}
		if err := z.Reset(bytes.NewReader(small.src)); err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(z)
		if err != nil || sha256Hex(got) != small.sum {
			t.Fatalf("after Reset mid-stream: %v", err)
		}
		if err := z.Reset(bytes.NewReader(big.src)); err != nil {
			t.Fatal(err)
		}
	}
}
