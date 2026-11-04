package main

import (
	"bytes"
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"

	"github.com/klauspost/compress/zstd"

	"github.com/x-mesh/fastgzip"
)

// sample makes data with literal runs, repeats at short and long distances,
// and byte runs, so encoders emit every block and sequence shape.
func sample(r *rand.Rand, n int) []byte {
	b := make([]byte, 0, n)
	for len(b) < n {
		switch r.IntN(4) {
		case 0:
			for range r.IntN(200) {
				b = append(b, byte('a'+r.IntN(26)))
			}
		case 1:
			if len(b) > 0 {
				d := 1 + r.IntN(min(len(b), 1<<r.IntN(20)))
				for range 3 + r.IntN(300) {
					b = append(b, b[len(b)-d])
				}
			}
		case 2:
			c := byte(r.Uint32())
			for range r.IntN(1000) {
				b = append(b, c)
			}
		default:
			for range r.IntN(100) {
				b = append(b, byte(r.Uint32()))
			}
		}
	}
	return b[:n]
}

func encoderOptions(flags byte) []zstd.EOption {
	levels := []zstd.EncoderLevel{zstd.SpeedFastest, zstd.SpeedDefault, zstd.SpeedBetterCompression, zstd.SpeedBestCompression}
	opts := []zstd.EOption{
		zstd.WithEncoderLevel(levels[flags&3]),
		zstd.WithEncoderCRC(flags&4 != 0),
		zstd.WithSingleSegment(flags&8 != 0),
		zstd.WithZeroFrames(true),
	}
	if flags&16 != 0 {
		opts = append(opts, zstd.WithWindowSize(1<<10))
	}
	if flags&32 != 0 {
		opts = append(opts, zstd.WithNoEntropyCompression(true))
	}
	return opts
}

func roundTrip(t *testing.T, data []byte, flags byte) {
	enc, err := zstd.NewWriter(nil, encoderOptions(flags)...)
	if err != nil {
		t.Fatal(err)
	}
	comp := enc.EncodeAll(data, nil)
	got, err := fastgzip.Unzstd(comp)
	if err != nil {
		t.Fatalf("flags %#x, %d bytes: %v", flags, len(data), err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("flags %#x, %d bytes: output differs", flags, len(data))
	}
}

// fastgzip decodes what klauspost/compress encodes, at every level and
// frame option.
func TestUnzstdKlauspostFrames(t *testing.T) {
	r := rand.New(rand.NewPCG(5, 6))
	for _, n := range []int{0, 1, 100, 5000, 200 << 10, 3 << 20} {
		data := sample(r, n)
		for flags := range byte(64) {
			roundTrip(t, data, flags)
		}
	}
}

func FuzzUnzstdKlauspostFrames(f *testing.F) {
	f.Add([]byte("hello, hello, hello"), byte(0))
	f.Fuzz(func(t *testing.T, data []byte, flags byte) {
		roundTrip(t, data, flags)
	})
}

// On any input, fastgzip must not accept what klauspost rejects, and when
// both accept they must agree. fastgzip may reject more.
func FuzzUnzstdKlauspostRaw(f *testing.F) {
	files, _ := filepath.Glob("../testdata/zstd/*.zst")
	for _, name := range files {
		b, err := os.ReadFile(name)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(b)
	}
	dec, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1))
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		got, err := fastgzip.Unzstd(b)
		if err != nil {
			return
		}
		want, kerr := dec.DecodeAll(b, nil)
		if kerr != nil {
			t.Fatalf("fastgzip accepted input klauspost rejects: %v", kerr)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("output differs: %d vs %d bytes", len(got), len(want))
		}
	})
}

// klauspost decodes what fastgzip encodes.
func checkKlauspostDecodes(t *testing.T, data []byte, level int) {
	dec, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1))
	if err != nil {
		t.Fatal(err)
	}
	comp := fastgzip.Zstd(nil, data, level)
	got, err := dec.DecodeAll(comp, nil)
	if err != nil {
		t.Fatalf("level %d, %d bytes: klauspost: %v", level, len(data), err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("level %d, %d bytes: klauspost decodes different data", level, len(data))
	}
}

func TestKlauspostDecodesZstd(t *testing.T) {
	r := rand.New(rand.NewPCG(9, 10))
	for _, n := range []int{0, 1, 100, 5000, 200 << 10, 3 << 20} {
		checkKlauspostDecodes(t, sample(r, n), 1)
	}
}

func FuzzKlauspostDecodesZstd(f *testing.F) {
	f.Add([]byte("hello, hello, hello"), byte(0))
	f.Fuzz(func(t *testing.T, data []byte, level byte) {
		checkKlauspostDecodes(t, data, int(level%3)+1)
	})
}

// klauspost decodes what a ZstdWriter writes.
func TestKlauspostDecodesZstdWriter(t *testing.T) {
	dec, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1))
	if err != nil {
		t.Fatal(err)
	}
	r := rand.New(rand.NewPCG(19, 20))
	data := sample(r, 20<<20)
	for _, level := range []int{1, 3} {
		var buf bytes.Buffer
		w := fastgzip.NewZstdWriter(&buf, level)
		for p := data; len(p) > 0; {
			n := min(len(p), 77777)
			w.Write(p[:n])
			if len(p)%3 == 0 {
				w.Flush()
			}
			p = p[n:]
		}
		w.Close()
		got, err := dec.DecodeAll(buf.Bytes(), nil)
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("level %d: klauspost: %v", level, err)
		}
	}
}
