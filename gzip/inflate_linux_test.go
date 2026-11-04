package gzip

import (
	"bytes"
	"compress/flate"
	"math/rand/v2"
	"os"
	"runtime/debug"
	"syscall"
	"testing"
)

// guarded maps n bytes, rounded up to whole pages, between two ranges of
// pages that fault on any access.
func guarded(t *testing.T, n int) []byte {
	t.Helper()
	// a multiple of every page size, and more than the largest distance
	const guard = 64 << 10
	page := os.Getpagesize()
	size := (n + page - 1) / page * page
	mem, err := syscall.Mmap(-1, 0, guard+size+guard,
		syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_ANON|syscall.MAP_PRIVATE)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { syscall.Munmap(mem) })
	for _, g := range [][]byte{mem[:guard], mem[guard+size:]} {
		if err := syscall.Mprotect(g, syscall.PROT_NONE); err != nil {
			t.Fatal(err)
		}
	}
	return mem[guard : guard+size : guard+size]
}

// The fast loop accesses memory through unsafe pointers, or assembly on
// amd64, and checkptr does not check assembly. Here the input ends and the
// output capacity ends where faulting pages start, so an access past either
// end stops the test. The output has room for about half of the data, so
// the fast loop meets the end of the room in the middle of the stream.
func TestInflateGuardPages(t *testing.T) {
	defer debug.SetPanicOnFault(debug.SetPanicOnFault(true))
	r := rand.New(rand.NewPCG(25, 26))
	for _, n := range []int{0, 1, 400, 5000, 70000, 1 << 20} {
		data := sampleData(r, n)
		out := guarded(t, n/2)
		for _, lv := range []int{flate.HuffmanOnly, 1, 6, 9} {
			var buf bytes.Buffer
			w, _ := flate.NewWriter(&buf, lv)
			w.Write(data)
			w.Close()
			src := buf.Bytes()
			in := guarded(t, len(src))
			// the first pass decodes src intact, the others damaged copies
			for k := range 20 {
				m := len(src)
				if k > 0 {
					m -= r.IntN(min(m, 64))
				}
				b := in[len(in)-m:]
				copy(b, src)
				for j := min(k, 1+r.IntN(3)); j > 0; j-- {
					b[r.IntN(m)] ^= byte(1 + r.IntN(255))
				}
				got, _, err := Inflate(out[:0], b)
				if k == 0 && (err != nil || !bytes.Equal(got, data)) {
					t.Fatalf("%d bytes at level %d: got %d bytes, %v", n, lv, len(got), err)
				}
			}
		}
	}
}
