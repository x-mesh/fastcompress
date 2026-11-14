package gzip

import (
	"bufio"
	"bytes"
	"compress/flate"
	"compress/gzip"
	"errors"
	"hash/crc32"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"
	"testing/iotest"
)

func stdGunzip(t testing.TB, b []byte) []byte {
	r, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// FASTCOMPRESS_CORPUS points at a directory of .gz files, such as the corpus
// built for the C comparison.
func TestCorpus(t *testing.T) {
	dir := os.Getenv("FASTCOMPRESS_CORPUS")
	if dir == "" {
		t.Skip("FASTCOMPRESS_CORPUS not set")
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.gz"))
	if len(files) == 0 {
		t.Fatal("no .gz files in", dir)
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		got, err := Gunzip(b)
		if err != nil {
			t.Errorf("%s: %v", filepath.Base(f), err)
			continue
		}
		if want := stdGunzip(t, b); !bytes.Equal(got, want) {
			t.Errorf("%s: output differs (%d vs %d bytes)", filepath.Base(f), len(got), len(want))
		}
	}
}

func sampleData(r *rand.Rand, n int) []byte {
	b := make([]byte, 0, n)
	for len(b) < n {
		switch r.IntN(4) {
		case 0: // random bytes
			for k := r.IntN(64); k > 0; k-- {
				b = append(b, byte(r.Uint32()))
			}
		case 1: // run of one byte
			c := byte(r.Uint32())
			for k := r.IntN(300); k > 0; k-- {
				b = append(b, c)
			}
		case 2: // short period
			p := 1 + r.IntN(20)
			unit := make([]byte, p)
			for i := range unit {
				unit[i] = byte('a' + r.IntN(26))
			}
			for k := r.IntN(30); k > 0; k-- {
				b = append(b, unit...)
			}
		default: // repeat from far back
			if len(b) > 0 {
				s := r.IntN(len(b))
				k := min(r.IntN(400), len(b)-s)
				b = append(b, b[s:s+k]...)
			}
		}
	}
	return b[:n]
}

func TestRandomRoundTrip(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	sizes := []int{0, 1, 2, 7, 25, 100, 323, 1000, 4096, 65536, 1 << 20}
	levels := []int{flate.NoCompression, flate.BestSpeed, 5, flate.BestCompression, flate.HuffmanOnly}
	for _, n := range sizes {
		for _, lv := range levels {
			for rep := 0; rep < 3; rep++ {
				data := sampleData(r, n)
				var buf bytes.Buffer
				w, _ := flate.NewWriter(&buf, lv)
				w.Write(data)
				w.Close()
				got, used, err := Inflate(nil, buf.Bytes())
				if err != nil {
					t.Fatalf("n=%d level=%d: %v", n, lv, err)
				}
				if !bytes.Equal(got, data) {
					t.Fatalf("n=%d level=%d: output differs", n, lv)
				}
				if used != buf.Len() {
					t.Fatalf("n=%d level=%d: consumed %d of %d", n, lv, used, buf.Len())
				}
			}
		}
	}
}

func TestPrefixAndHistory(t *testing.T) {
	// a match may not reach into data that was in dst before the stream
	var buf bytes.Buffer
	w, _ := flate.NewWriter(&buf, 9)
	w.Write(bytes.Repeat([]byte("abcdefgh"), 100))
	w.Close()
	prefix := []byte("unrelated prefix")
	got, _, err := Inflate(append([]byte(nil), prefix...), buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got[:len(prefix)], prefix) || !bytes.Equal(got[len(prefix):], bytes.Repeat([]byte("abcdefgh"), 100)) {
		t.Fatal("output differs")
	}
}

func TestTruncated(t *testing.T) {
	var buf bytes.Buffer
	w, _ := flate.NewWriter(&buf, 6)
	w.Write(sampleData(rand.New(rand.NewPCG(3, 4)), 100000))
	w.Close()
	full := buf.Bytes()
	for _, n := range []int{0, 1, 10, len(full) / 2, len(full) - 1} {
		if _, _, err := Inflate(nil, full[:n]); err == nil {
			t.Errorf("truncated to %d bytes: no error", n)
		}
	}
}

func FuzzInflate(f *testing.F) {
	r := rand.New(rand.NewPCG(5, 6))
	for _, lv := range []int{1, 6, 9, flate.HuffmanOnly} {
		var buf bytes.Buffer
		w, _ := flate.NewWriter(&buf, lv)
		w.Write(sampleData(r, 5000))
		w.Close()
		f.Add(buf.Bytes())
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		got, _, err := Inflate(nil, b)
		want, werr := io.ReadAll(flate.NewReader(bytes.NewReader(b)))
		if err == nil && werr == nil && !bytes.Equal(got, want) {
			t.Fatal("output differs from compress/flate")
		}
		if err == nil && werr != nil {
			t.Fatalf("accepted input compress/flate rejects: %v", werr)
		}
	})
}

// FASTCOMPRESS_BENCH names one .gz file to decode.
func BenchmarkGunzip(b *testing.B) {
	f := os.Getenv("FASTCOMPRESS_BENCH")
	if f == "" {
		b.Skip("FASTCOMPRESS_BENCH not set")
	}
	src, err := os.ReadFile(f)
	if err != nil {
		b.Fatal(err)
	}
	out, err := Gunzip(src)
	if err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(len(out)))
	for b.Loop() {
		if _, err := Gunzip(src); err != nil {
			b.Fatal(err)
		}
	}
}

// readWith reads r to the end with reads of n bytes.
func readWith(r io.Reader, n int) ([]byte, error) {
	var out []byte
	buf := make([]byte, n)
	for {
		k, err := r.Read(buf)
		out = append(out, buf[:k]...)
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return out, err
		}
	}
}

func gzipBytes(t testing.TB, data []byte, level int) []byte {
	var buf bytes.Buffer
	w, _ := gzip.NewWriterLevel(&buf, level)
	w.Write(data)
	w.Close()
	return buf.Bytes()
}

func TestReaderCorpus(t *testing.T) {
	dir := os.Getenv("FASTCOMPRESS_CORPUS")
	if dir == "" {
		t.Skip("FASTCOMPRESS_CORPUS not set")
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.gz"))
	for _, f := range files {
		b, _ := os.ReadFile(f)
		want := stdGunzip(t, b)
		sources := map[string]func() io.Reader{
			"bytes": func() io.Reader { return bytes.NewReader(b) },
			"half":  func() io.Reader { return iotest.HalfReader(bytes.NewReader(b)) },
		}
		if len(b) < 1<<20 {
			sources["onebyte"] = func() io.Reader { return iotest.OneByteReader(bytes.NewReader(b)) }
		}
		for name, src := range sources {
			for _, n := range []int{1, 7, 4096, 1 << 20} {
				if n == 1 && len(want) > 1<<22 {
					continue
				}
				z, err := NewReader(src())
				if err != nil {
					t.Fatalf("%s %s: %v", filepath.Base(f), name, err)
				}
				got, err := readWith(z, n)
				if err != nil {
					t.Fatalf("%s %s read %d: %v", filepath.Base(f), name, n, err)
				}
				if !bytes.Equal(got, want) {
					t.Fatalf("%s %s read %d: output differs (%d vs %d bytes)", filepath.Base(f), name, n, len(got), len(want))
				}
			}
		}
	}
}

func TestReaderMembers(t *testing.T) {
	r := rand.New(rand.NewPCG(7, 8))
	var all, want []byte
	for _, n := range []int{100, 0, 3 << 20, 1, 70000} {
		data := sampleData(r, n)
		want = append(want, data...)
		all = append(all, gzipBytes(t, data, 6)...)
	}
	z, err := NewReader(iotest.HalfReader(bytes.NewReader(all)))
	if err != nil {
		t.Fatal(err)
	}
	got, err := readWith(z, 3000)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("output differs (%d vs %d bytes)", len(got), len(want))
	}
	if g, err := Gunzip(all); err != nil || !bytes.Equal(g, want) {
		t.Fatalf("Gunzip: err=%v equal=%v", err, bytes.Equal(g, want))
	}
}

func TestReaderErrors(t *testing.T) {
	if _, err := NewReader(bytes.NewReader(nil)); err != io.EOF {
		t.Fatalf("empty input: %v, want io.EOF", err)
	}
	if _, err := NewReader(bytes.NewReader([]byte("not gzip data"))); err == nil {
		t.Fatal("bad header: no error")
	}

	full := gzipBytes(t, sampleData(rand.New(rand.NewPCG(9, 10)), 2<<20), 6)
	for _, n := range []int{11, 1000, len(full) / 2, len(full) - 9, len(full) - 1} {
		z, err := NewReader(bytes.NewReader(full[:n]))
		if err != nil {
			continue
		}
		if _, err := readWith(z, 4096); err == nil {
			t.Errorf("truncated to %d bytes: no error", n)
		}
	}

	bad := append([]byte(nil), full...)
	bad[len(bad)-6] ^= 1 // CRC-32 in the trailer
	z, _ := NewReader(bytes.NewReader(bad))
	if _, err := readWith(z, 4096); err != ErrChecksum {
		t.Errorf("bad checksum: %v", err)
	}

	sentinel := errors.New("disk error")
	z, _ = NewReader(io.MultiReader(bytes.NewReader(full[:len(full)/2]), iotest.ErrReader(sentinel)))
	if _, err := readWith(z, 4096); err != sentinel {
		t.Errorf("source error: %v, want %v", err, sentinel)
	}
}

func FuzzReader(f *testing.F) {
	r := rand.New(rand.NewPCG(11, 12))
	for _, lv := range []int{1, 6, 9} {
		f.Add(gzipBytes(f, sampleData(r, 3000), lv))
	}
	// headers with every optional field, so the fuzzer reaches their checks
	var buf bytes.Buffer
	w, _ := gzip.NewWriterLevel(&buf, 6)
	w.Header = gzip.Header{Name: "name.log", Comment: "comment", Extra: []byte("xx")}
	w.Write(sampleData(r, 2000))
	w.Close()
	h := buf.Bytes()
	f.Add(h)
	// the same member with FHCRC: flag bit, then the CRC-16 after the comment
	end := 10 + 2 + 2 + len("name.log") + 1 + len("comment") + 1
	withCRC := append(append([]byte(nil), h[:end]...), 0, 0)
	withCRC[3] |= flagHCRC
	c := crc32.ChecksumIEEE(withCRC[:end])
	withCRC[end], withCRC[end+1] = byte(c), byte(c>>8)
	f.Add(append(withCRC, h[end:]...))
	f.Fuzz(func(t *testing.T, b []byte) {
		var got []byte
		z, err := NewReader(bytes.NewReader(b))
		if err == nil {
			got, err = readWith(z, 777)
		}
		sr, werr := gzip.NewReader(bytes.NewReader(b))
		var want []byte
		if werr == nil {
			want, werr = io.ReadAll(sr)
		}
		if err == nil && werr == nil && !bytes.Equal(got, want) {
			t.Fatal("output differs from compress/gzip")
		}
		if err == nil && werr != nil && werr != io.EOF {
			t.Fatalf("accepted input compress/gzip rejects: %v", werr)
		}
	})
}

// FASTCOMPRESS_BENCH names one .gz file to decode through Reader, as edc grep
// does: from a 1 MB bufio.Reader, with 64 KB reads.
func BenchmarkReader(b *testing.B) {
	f := os.Getenv("FASTCOMPRESS_BENCH")
	if f == "" {
		b.Skip("FASTCOMPRESS_BENCH not set")
	}
	src, err := os.ReadFile(f)
	if err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(len(stdGunzip(b, src))))
	buf := make([]byte, 64<<10)
	for b.Loop() {
		z, err := NewReader(bufio.NewReaderSize(bytes.NewReader(src), 1<<20))
		if err != nil {
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

func TestReaderResetAndClose(t *testing.T) {
	r := rand.New(rand.NewPCG(15, 16))
	var z *Reader
	for i, n := range []int{3 << 20, 10, 0, 500000, 2 << 20} {
		data := sampleData(r, n)
		src := gzipBytes(t, data, 1+i%9)
		var err error
		if z == nil {
			z, err = NewReader(bytes.NewReader(src))
		} else {
			err = z.Reset(bytes.NewReader(src))
		}
		if err != nil {
			t.Fatal(err)
		}
		got, err := readWith(z, 5000)
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("stream %d: err=%v equal=%v", i, err, bytes.Equal(got, data))
		}
		if i%2 == 1 {
			// a closed Reader hands its buffers to the next NewReader
			z.Close()
			if _, err := z.Read(make([]byte, 1)); err == nil {
				t.Fatal("read after Close: no error")
			}
			z = nil
		}
	}
}

// edc shows a truncated HTTP body up to the point it was cut.
func TestReaderTruncatedPrefix(t *testing.T) {
	data := sampleData(rand.New(rand.NewPCG(17, 18)), 3<<20)
	full := gzipBytes(t, data, 6)
	z, err := NewReader(bytes.NewReader(full[:len(full)/2]))
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(z)
	if err != io.ErrUnexpectedEOF {
		t.Fatalf("err = %v, want io.ErrUnexpectedEOF", err)
	}
	if len(got) < len(data)/4 || !bytes.Equal(got, data[:len(got)]) {
		t.Fatalf("got %d bytes; want a correct prefix of %d", len(got), len(data))
	}
}

// Header checks follow compress/gzip: a wrong FHCRC and a name or comment
// longer than 511 bytes are ErrHeader; reserved flag bits are accepted.
func TestHeaderLikeStdlib(t *testing.T) {
	member := func(flg byte, fields []byte) []byte {
		body := gzipBytes(t, []byte("hello"), 6)
		h := append([]byte{0x1f, 0x8b, 8, flg, 0, 0, 0, 0, 0, 255}, fields...)
		return append(h, body[10:]...)
	}
	withCRC := func(flg byte, fields []byte, flip byte) []byte {
		h := append([]byte{0x1f, 0x8b, 8, flg | flagHCRC, 0, 0, 0, 0, 0, 255}, fields...)
		c := crc32.ChecksumIEEE(h)
		return member(flg|flagHCRC, append(append([]byte(nil), fields...), byte(c)^flip, byte(c>>8)))
	}
	name := func(n int) []byte { return append(bytes.Repeat([]byte("n"), n), 0) }
	cases := map[string][]byte{
		"reserved bits":      member(0xe0, nil),
		"FHCRC correct":      withCRC(0, nil, 0),
		"FHCRC wrong":        withCRC(0, nil, 1),
		"FHCRC over name":    withCRC(flagName, name(20), 0),
		"FHCRC wrong, extra": withCRC(flagExtra, []byte{3, 0, 'a', 'b', 'c'}, 1),
		"name 511 bytes":     member(flagName, name(511)),
		"name 512 bytes":     member(flagName, name(512)),
		"comment 512 bytes":  member(flagComment, name(512)),
		"peer report (0x32)": []byte("\x1f\x8b\b2000000\x00000\x12\x00\xed\xffhello ERROR world\n\x030{\xe3\xc9`\x12\x00\x00\x00"),
	}
	for name, b := range cases {
		var want error
		if r, err := gzip.NewReader(bytes.NewReader(b)); err != nil {
			want = err
		} else {
			_, want = io.ReadAll(r)
		}
		_, err := Gunzip(b)
		if (err == nil) != (want == nil) || (want == gzip.ErrHeader && err != ErrHeader) {
			t.Errorf("%s: Gunzip err = %v, compress/gzip %v", name, err, want)
		}
		z, err := NewReader(bytes.NewReader(b))
		if err == nil {
			_, err = io.ReadAll(z)
		}
		if (err == nil) != (want == nil) || (want == gzip.ErrHeader && err != ErrHeader) {
			t.Errorf("%s: Reader err = %v, compress/gzip %v", name, err, want)
		}
	}
}

// A small input buffer ends the input at many places in the block headers,
// so dynamic hands over from lensFast to its byte-at-a-time loop at each of
// them.
func TestReaderSmallInput(t *testing.T) {
	r := rand.New(rand.NewPCG(35, 36))
	data := sampleData(r, 1<<20)
	for _, lv := range []int{1, 6, 9} {
		src := gzipBytes(t, data, lv)
		for _, size := range []int{40, 64, 97} {
			z := &Reader{}
			z.d.inbuf = make([]byte, size)
			z.d.out = make([]byte, 0, window+streamChunk+fastOutMargin)
			if err := z.Reset(bytes.NewReader(src)); err != nil {
				t.Fatal(err)
			}
			got, err := io.ReadAll(z)
			if err != nil || !bytes.Equal(got, data) {
				t.Fatalf("level %d, %d-byte input buffer: %d bytes, %v", lv, size, len(got), err)
			}
		}
	}
}

func flateBytes(t testing.TB, data []byte, level int) []byte {
	var buf bytes.Buffer
	w, _ := flate.NewWriter(&buf, level)
	w.Write(data)
	w.Close()
	return buf.Bytes()
}

func TestInflateReader(t *testing.T) {
	r := rand.New(rand.NewPCG(37, 38))
	z := NewInflateReader(bytes.NewReader(nil))
	for _, n := range []int{0, 1, 5000, 300000, 3 << 20} {
		for _, lv := range []int{flate.NoCompression, 1, 6, 9, flate.HuffmanOnly} {
			data := sampleData(r, n)
			src := flateBytes(t, data, lv)
			for _, rs := range []int{1, 777, 8000, 64 << 10} {
				z.Reset(bytes.NewReader(src))
				got, err := readWith(z, rs)
				if err != nil || !bytes.Equal(got, data) {
					t.Fatalf("n=%d level=%d, %d-byte reads: %d bytes, %v", n, lv, rs, len(got), err)
				}
			}
			// a closed reader takes buffers from the pool at the next Reset
			z.Close()
			if _, err := z.Read(make([]byte, 1)); err == nil {
				t.Fatal("read after Close: no error")
			}
			z.Reset(bytes.NewReader(src[:len(src)/2]))
			if _, err := io.ReadAll(z); err == nil {
				t.Fatalf("n=%d level=%d: truncated stream: no error", n, lv)
			}
		}
	}
}

func TestInflateLimit(t *testing.T) {
	r := rand.New(rand.NewPCG(39, 40))
	prefix := []byte("prefix")
	for _, n := range []int{0, 1, 300, 5000, 300000, 3 << 20} {
		for _, lv := range []int{flate.NoCompression, 1, 9} {
			data := sampleData(r, n)
			src := flateBytes(t, data, lv)
			for _, limit := range []int{n, n + 1, 1 << 40} {
				out, used, err := InflateLimit(prefix, src, limit)
				if err != nil || used != len(src) || !bytes.Equal(out[len(prefix):], data) {
					t.Fatalf("n=%d level=%d limit=%d: %d bytes, %v", n, lv, limit, len(out)-len(prefix), err)
				}
			}
			for _, limit := range []int{0, n / 2, n - 1} {
				if limit < 0 || limit >= n {
					continue
				}
				out, _, err := InflateLimit(prefix, src, limit)
				if err != ErrTooLarge || !bytes.Equal(out[:len(prefix)], prefix) || !bytes.Equal(out[len(prefix):], data[:limit]) {
					t.Fatalf("n=%d level=%d limit=%d: %d bytes, %v", n, lv, limit, len(out)-len(prefix), err)
				}
			}
		}
	}

	// the limit stops 64 MiB of zeros, 65 KB of input, before it allocates
	// the output
	src := flateBytes(t, make([]byte, 64<<20), 9)
	out, _, err := InflateLimit(nil, src, 1<<20)
	if err != ErrTooLarge || len(out) != 1<<20 || cap(out) > 2<<20 {
		t.Fatalf("zeros: %v, len %d, cap %d", err, len(out), cap(out))
	}

	defer func() {
		if recover() == nil {
			t.Fatal("negative limit: no panic")
		}
	}()
	InflateLimit(nil, src, -1)
}

// A read of the start of a long stream decodes about as much as it asks for.
func TestShortRead(t *testing.T) {
	data := make([]byte, 1<<20)
	gz := gzipBytes(t, data, 9)
	raw := flateBytes(t, data, 9)
	for _, rs := range []int{1, 100, 8000, 40000} {
		want := max(rs, minFill) + fastOutMargin
		z, err := NewReader(bytes.NewReader(gz))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.ReadFull(z, make([]byte, rs)); err != nil {
			t.Fatal(err)
		}
		if int(z.size) > want {
			t.Errorf("Reader, %d-byte read: decoded %d bytes", rs, z.size)
		}
		zr := NewInflateReader(bytes.NewReader(raw))
		if _, err := io.ReadFull(zr, make([]byte, rs)); err != nil {
			t.Fatal(err)
		}
		if len(zr.z.d.out) > want {
			t.Errorf("InflateReader, %d-byte read: decoded %d bytes", rs, len(zr.z.d.out))
		}
	}
}

func FuzzInflateReader(f *testing.F) {
	r := rand.New(rand.NewPCG(41, 42))
	for _, lv := range []int{flate.NoCompression, 1, 6, 9, flate.HuffmanOnly} {
		f.Add(flateBytes(f, sampleData(r, 9000), lv))
	}
	f.Add(flateBytes(f, make([]byte, 100000), 9))
	z := NewInflateReader(bytes.NewReader(nil))
	f.Fuzz(func(t *testing.T, b []byte) {
		want, werr := io.ReadAll(flate.NewReader(bytes.NewReader(b)))
		for _, rs := range []int{1, 777, 8000} {
			z.Reset(bytes.NewReader(b))
			got, err := readWith(z, rs)
			if err == nil && werr == nil && !bytes.Equal(got, want) {
				t.Fatalf("%d-byte reads: output differs from compress/flate", rs)
			}
			if err == nil && werr != nil {
				t.Fatalf("%d-byte reads: accepted input compress/flate rejects: %v", rs, werr)
			}
		}
		if werr != nil {
			return
		}
		// as in FuzzInflate, Inflate may reject a stream that compress/flate
		// accepts
		out, _, err := InflateLimit(nil, b, len(want))
		if err == nil && !bytes.Equal(out, want) {
			t.Fatal("limit of the output size: output differs")
		}
		if err == ErrTooLarge {
			t.Fatal("limit of the output size: ErrTooLarge")
		}
		if len(want) > 0 {
			out, _, err = InflateLimit(nil, b, len(want)-1)
			if err == nil {
				t.Fatal("limit one byte short: no error")
			}
			if err == ErrTooLarge && !bytes.Equal(out, want[:len(want)-1]) {
				t.Fatal("limit one byte short: output differs")
			}
		}
	})
}
