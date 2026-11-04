package gzip

import (
	"bytes"
	"compress/flate"
	"errors"
	"io"
	"math"
	"math/rand/v2"
	"testing"
)

// concurrentLevels has every kind of matcher and block writer.
var concurrentLevels = []int{HuffmanOnly, NoCompression, BestSpeed, DefaultCompression, 7}

func concurrentInputs(r *rand.Rand) [][]byte {
	var inputs [][]byte
	for _, n := range []int{0, 1, chunkSize - 1, chunkSize, chunkSize + 1, 2*chunkSize + chunkSize/2} {
		inputs = append(inputs, sampleData(r, n))
	}
	return inputs
}

func TestGzipConcurrent(t *testing.T) {
	r := rand.New(rand.NewPCG(41, 42))
	for _, in := range concurrentInputs(r) {
		for _, level := range concurrentLevels {
			want, err := GzipConcurrent(in, level, 1)
			if err != nil {
				t.Fatal(err)
			}
			if got := stdGunzip(t, want); !bytes.Equal(got, in) {
				t.Fatalf("level %d, %d bytes: compress/gzip output differs", level, len(in))
			}
			if got, err := Gunzip(want); err != nil || !bytes.Equal(got, in) {
				t.Fatalf("level %d, %d bytes: Gunzip: %v", level, len(in), err)
			}
			for _, n := range []int{0, 3} {
				got, _ := GzipConcurrent(in, level, n)
				if !bytes.Equal(got, want) {
					t.Fatalf("level %d, %d bytes: output with %d goroutines differs from 1", level, len(in), n)
				}
			}
			// one chunk is the whole stream
			if len(in) <= chunkSize {
				serial, _ := Gzip(in, level)
				if !bytes.Equal(serial, want) {
					t.Fatalf("level %d, %d bytes: one chunk differs from Gzip", level, len(in))
				}
			}
		}
	}
	if _, err := GzipConcurrent(nil, 10, 2); err == nil {
		t.Error("level 10: no error")
	}
}

// TestGzipConcurrentSize checks that the dictionary keeps chunked output
// close to Gzip.
func TestGzipConcurrentSize(t *testing.T) {
	r := rand.New(rand.NewPCG(43, 44))
	data := sampleData(r, 6*chunkSize)
	for _, level := range []int{BestSpeed, DefaultCompression, BestCompression} {
		serial, _ := Gzip(data, level)
		chunked, _ := GzipConcurrent(data, level, 4)
		if got := stdGunzip(t, chunked); !bytes.Equal(got, data) {
			t.Fatalf("level %d: output differs", level)
		}
		if len(chunked) > len(serial)*1005/1000 {
			t.Errorf("level %d: %d bytes, Gzip %d", level, len(chunked), len(serial))
		}
	}
}

func TestWriterConcurrent(t *testing.T) {
	r := rand.New(rand.NewPCG(45, 46))
	for _, in := range concurrentInputs(r) {
		for _, level := range concurrentLevels {
			want, _ := GzipConcurrent(in, level, 1)
			for _, n := range []int{1, 3} {
				var buf bytes.Buffer
				z, _ := NewWriterLevel(&buf, level)
				if err := z.SetConcurrency(n); err != nil {
					t.Fatal(err)
				}
				writeChunks(t, z, r, in, 1+r.IntN(3*chunkSize))
				if err := z.Close(); err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(buf.Bytes(), want) {
					t.Fatalf("level %d, %d bytes, %d goroutines: output differs from GzipConcurrent", level, len(in), n)
				}
			}
		}
	}
}

func TestWriterConcurrentFlush(t *testing.T) {
	r := rand.New(rand.NewPCG(47, 48))
	data := sampleData(r, 3*chunkSize)
	for _, level := range []int{NoCompression, BestSpeed, DefaultCompression, BestCompression} {
		var buf bytes.Buffer
		z, _ := NewWriterLevel(&buf, level)
		z.SetConcurrency(3)
		written := 0
		for written < len(data) {
			k := min(len(data)-written, r.IntN(chunkSize*3/2))
			writeChunks(t, z, r, data[written:written+k], 300000)
			written += k
			if err := z.Flush(); err != nil {
				t.Fatal(err)
			}
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

func TestWriterConcurrentReset(t *testing.T) {
	r := rand.New(rand.NewPCG(49, 50))
	a, b := sampleData(r, 3*chunkSize), sampleData(r, 2*chunkSize+5)
	want, _ := GzipConcurrent(b, BestSpeed, 1)

	var old, buf bytes.Buffer
	z, _ := NewWriterLevel(&old, BestSpeed)
	z.SetConcurrency(4)
	writeChunks(t, z, r, a, chunkSize)
	if err := z.SetConcurrency(2); !errors.Is(err, errConcurrencyAfterWrite) {
		t.Errorf("SetConcurrency after Write: %v", err)
	}
	var empty bytes.Buffer
	zf := NewWriter(&empty)
	zf.Flush()
	if err := zf.SetConcurrency(2); !errors.Is(err, errConcurrencyAfterWrite) {
		t.Errorf("SetConcurrency after Flush: %v", err)
	}
	// Reset with chunks in flight drops them and keeps the concurrency
	n := old.Len()
	z.Reset(&buf)
	if old.Len() != n {
		t.Error("Reset wrote to the old writer")
	}
	writeChunks(t, z, r, b, chunkSize)
	z.Close()
	if !bytes.Equal(buf.Bytes(), want) {
		t.Fatal("output after Reset differs from GzipConcurrent")
	}
	// SetConcurrency(0) goes back to one stream
	buf.Reset()
	z.Reset(&buf)
	z.SetConcurrency(0)
	writeChunks(t, z, r, b, chunkSize)
	z.Close()
	if serial, _ := Gzip(b, BestSpeed); !bytes.Equal(buf.Bytes(), serial) {
		t.Fatal("output after SetConcurrency(0) differs from Gzip")
	}
}

func TestWriterConcurrentErrors(t *testing.T) {
	r := rand.New(rand.NewPCG(51, 52))
	data := sampleData(r, 5*chunkSize)
	z := NewWriter(&failWriter{n: 1})
	z.SetConcurrency(2)
	var err error
	for p := data; len(p) > 0 && err == nil; p = p[min(len(p), 100000):] {
		_, err = z.Write(p[:min(len(p), 100000)])
	}
	if err == nil {
		err = z.Close()
	}
	if !errors.Is(err, errFail) {
		t.Fatalf("got %v", err)
	}
	if _, err := z.Write([]byte("x")); !errors.Is(err, errFail) {
		t.Errorf("Write after error: %v", err)
	}
	if err := z.Close(); !errors.Is(err, errFail) {
		t.Errorf("Close after error: %v", err)
	}
	// Reset waits for the chunks in flight and starts clean
	var buf bytes.Buffer
	z.Reset(&buf)
	writeChunks(t, z, r, data, chunkSize)
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	if got := stdGunzip(t, buf.Bytes()); !bytes.Equal(got, data) {
		t.Fatal("output after Reset differs")
	}
}

// TestWriterConcurrentMax checks that the largest n does not overflow the
// limit of chunks in flight.
func TestWriterConcurrentMax(t *testing.T) {
	r := rand.New(rand.NewPCG(53, 54))
	data := sampleData(r, 2*chunkSize+7)
	want, _ := GzipConcurrent(data, BestSpeed, 1)
	var buf bytes.Buffer
	z, _ := NewWriterLevel(&buf, BestSpeed)
	if err := z.SetConcurrency(math.MaxInt); err != nil {
		t.Fatal(err)
	}
	writeChunks(t, z, r, data, chunkSize)
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buf.Bytes(), want) {
		t.Fatal("output differs from GzipConcurrent")
	}
}
