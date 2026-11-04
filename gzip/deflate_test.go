package gzip

import (
	"bytes"
	"compress/flate"
	"container/heap"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"testing"
)

func init() {
	// every test and fuzz input checks the counts that writeTokens trusts
	checkCounts = true
}

// noCheckCounts turns checkCounts off for a benchmark.
func noCheckCounts(b *testing.B) {
	checkCounts = false
	b.Cleanup(func() { checkCounts = true })
}

func testMatchToken(length, dist int) uint32 {
	return matchToken(length, dist, distSymbol(uint32(dist-1)))
}

func checkInflate(t testing.TB, comp, want []byte) {
	t.Helper()
	got, used, err := Inflate(nil, comp)
	if err != nil {
		t.Fatalf("Inflate: %v", err)
	}
	if used != len(comp) {
		t.Fatalf("Inflate consumed %d of %d bytes", used, len(comp))
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("Inflate output differs (%d vs %d bytes)", len(got), len(want))
	}
	std, err := io.ReadAll(flate.NewReader(bytes.NewReader(comp)))
	if err != nil {
		t.Fatalf("compress/flate: %v", err)
	}
	if !bytes.Equal(std, want) {
		t.Fatalf("compress/flate output differs (%d vs %d bytes)", len(std), len(want))
	}
}

type node struct {
	w     uint64
	depth int
}

type nodeHeap []node

func (h nodeHeap) Len() int           { return len(h) }
func (h nodeHeap) Less(i, j int) bool { return h[i].w < h[j].w }
func (h nodeHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *nodeHeap) Push(x any)        { *h = append(*h, x.(node)) }
func (h *nodeHeap) Pop() any {
	old := *h
	x := old[len(old)-1]
	*h = old[:len(old)-1]
	return x
}

// huffman returns the cost and the longest code of an unlimited Huffman code
// for freq, which has at least two used symbols.
func huffman(freq []uint32) (cost uint64, depth int) {
	h := nodeHeap{}
	for _, f := range freq {
		if f > 0 {
			h = append(h, node{w: uint64(f)})
		}
	}
	heap.Init(&h)
	for h.Len() > 1 {
		a, b := heap.Pop(&h).(node), heap.Pop(&h).(node)
		cost += a.w + b.w
		heap.Push(&h, node{a.w + b.w, max(a.depth, b.depth) + 1})
	}
	return cost, h[0].depth
}

func TestPackageMerge(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4))
	var pm pmScratch
	for iter := 0; iter < 2000; iter++ {
		n := 1 + r.IntN(numLitLen)
		limit := maxBits
		if iter%3 == 0 {
			n = 1 + r.IntN(numCL)
			limit = maxCLBits
		}
		freq := make([]uint32, n)
		switch iter % 4 {
		case 0: // Fibonacci weights force the length limit
			a, b := uint32(1), uint32(1)
			for i := range freq {
				if r.IntN(4) > 0 {
					freq[i] = a
					a, b = b, min(a+b, 1<<30)
				}
			}
		case 1:
			for i := range freq {
				if r.IntN(3) == 0 {
					freq[i] = 1 + r.Uint32N(1000)
				}
			}
		default:
			for i := range freq {
				freq[i] = r.Uint32N(1 << r.IntN(20))
			}
		}
		lens := make([]uint8, n)
		pm.lengths(freq, lens, limit)

		used, kraft, maxLen := 0, 0, 0
		cost := uint64(0)
		for i, l := range lens {
			if (freq[i] == 0) != (l == 0) {
				t.Fatalf("iter %d: symbol %d freq %d has length %d", iter, i, freq[i], l)
			}
			if l == 0 {
				continue
			}
			used++
			kraft += 1 << (maxBits - l)
			maxLen = max(maxLen, int(l))
			cost += uint64(freq[i]) * uint64(l)
		}
		if maxLen > limit {
			t.Fatalf("iter %d: length %d over limit %d", iter, maxLen, limit)
		}
		if used >= 2 && kraft != 1<<maxBits {
			t.Fatalf("iter %d: code not complete (kraft %d)", iter, kraft)
		}
		if used >= 2 {
			best, depth := huffman(freq)
			if cost < best || (depth <= limit && cost != best) {
				t.Fatalf("iter %d: cost %d, Huffman %d with depth %d", iter, cost, best, depth)
			}
		}
	}
}

// randomTokens returns tokens with every length and distance code, and the
// data they encode.
func randomTokens(r *rand.Rand, n int) ([]uint32, []byte) {
	var tokens []uint32
	var data []byte
	for len(tokens) < n {
		if len(data) == 0 || r.IntN(3) == 0 {
			b := byte(r.IntN(256))
			if r.IntN(2) == 0 {
				b = 'a' + byte(r.IntN(4))
			}
			tokens = append(tokens, uint32(b))
			data = append(data, b)
			continue
		}
		length := 3 + r.IntN(256)
		if r.IntN(2) == 0 {
			length = 3 + r.IntN(8)
		}
		maxDist := min(len(data), 32768)
		dist := 1 + r.IntN(maxDist)
		if r.IntN(2) == 0 {
			dist = 1 + r.IntN(min(maxDist, 16))
		}
		for i := 0; i < length; i++ {
			data = append(data, data[len(data)-dist])
		}
		tokens = append(tokens, testMatchToken(length, dist))
	}
	return tokens, data
}

// skewedTokens returns tokens whose literals, length codes and distance
// codes have Fibonacci frequencies, and the data they encode. In one block the
// rarest symbols get 15-bit codes, so the rarest match takes 48 bits.
func skewedTokens(r *rand.Rand) ([]uint32, []byte) {
	fib := func(n int, f func(i, k int)) {
		a, b := 1, 1
		for i := 0; i < n; i++ {
			f(i, a)
			a, b = b, a+b
		}
	}
	var lits []uint32
	fib(25, func(i, k int) {
		for ; k > 0; k-- {
			lits = append(lits, uint32(i))
		}
	})
	var lcodes, dcodes []int
	fib(21, func(i, k int) {
		for ; k > 0; k-- {
			lcodes = append(lcodes, 28-i)
			dcodes = append(dcodes, 29-i)
		}
	})
	// lcodes[i] and dcodes[i] are equally rare: the rarest length goes with
	// the rarest distance
	events := lits[40000:]
	for i, lc := range lcodes {
		length := int(lenBase[lc]) + r.IntN(1<<(lenExtra[lc]-16))
		dc := dcodes[i]
		dist := int(distBase[dc]) + r.IntN(1<<(distExtra[dc]-16))
		events = append(events, testMatchToken(min(length, maxMatch), dist))
	}
	r.Shuffle(len(events), func(i, j int) { events[i], events[j] = events[j], events[i] })
	tokens := append(lits[:40000:40000], events...)

	var data []byte
	for _, t := range tokens {
		if t&tokenMatch == 0 {
			data = append(data, byte(t))
			continue
		}
		length, dist := int(t&0xff)+3, int(t>>8&0x7fff)+1
		for k := 0; k < length; k++ {
			data = append(data, data[len(data)-dist])
		}
	}
	return tokens, data
}

func TestWriteBlockTokens(t *testing.T) {
	r := rand.New(rand.NewPCG(7, 8))
	for _, n := range []int{1, 2, 10, 1000, 50000, 200000, 0} {
		tokens, data := randomTokens(r, n)
		if n == 0 {
			tokens, data = skewedTokens(r)
		}
		var e encoder
		pos := 0
		for len(tokens) > 0 {
			k := min(len(tokens), 1+r.IntN(blockSize))
			if n == 0 {
				k = len(tokens)
			}
			size := 0
			for _, tk := range tokens[:k] {
				if tk&tokenMatch == 0 {
					size++
				} else {
					size += int(tk&0xff) + 3
				}
			}
			e.startBlock()
			for _, tk := range tokens[:k] {
				if tk&tokenMatch == 0 {
					e.literals([]byte{byte(tk)})
				} else {
					e.match(int(tk&0xff)+3, int(tk>>8&0x7fff)+1)
				}
			}
			e.writeBlock(data[pos:pos+size], k == len(tokens))
			tokens = tokens[k:]
			pos += size
		}
		checkInflate(t, e.finish(), data)
	}
}

func TestDeflateRoundTrip(t *testing.T) {
	r := rand.New(rand.NewPCG(9, 10))
	inputs := [][]byte{nil, {0}, bytes.Repeat([]byte{'x'}, 100000), make([]byte, 300000)}
	for _, n := range []int{1, 2, 7, 100, 1000, 65535, 65536, 65537, 200000, 1 << 20} {
		inputs = append(inputs, sampleData(r, n))
		random := make([]byte, n)
		for i := range random {
			random[i] = byte(r.Uint32())
		}
		inputs = append(inputs, random)
	}
	skew := make([]byte, 0, 1<<20)
	for c, k := 0, 1; len(skew) < cap(skew); c, k = c+1, k*2 {
		skew = append(skew, bytes.Repeat([]byte{byte(c)}, min(k, cap(skew)-len(skew)))...)
	}
	inputs = append(inputs, skew)

	for _, in := range inputs {
		stored, _ := Deflate(nil, in, NoCompression)
		checkInflate(t, stored, in)
		comp, _ := Deflate(nil, in, HuffmanOnly)
		checkInflate(t, comp, in)
		// a block falls back to stored when literals cost more
		if limit := len(in) + 5*(len(in)/blockSize+1); len(comp) > limit {
			t.Errorf("%d bytes: Huffman output %d bytes, more than stored %d", len(in), len(comp), limit)
		}
	}
}

func TestDeflateAppends(t *testing.T) {
	prefix := []byte("prefix")
	data := []byte("hello, hello, hello")
	for _, level := range []int{NoCompression, HuffmanOnly} {
		out, _ := Deflate(append([]byte(nil), prefix...), data, level)
		if !bytes.Equal(out[:len(prefix)], prefix) {
			t.Fatal("prefix changed")
		}
		checkInflate(t, out[len(prefix):], data)
	}
}

func TestHuffmanSmallerThanStored(t *testing.T) {
	r := rand.New(rand.NewPCG(11, 12))
	data := sampleData(r, 100000)
	h, _ := Deflate(nil, data, HuffmanOnly)
	st, _ := Deflate(nil, data, NoCompression)
	if h, s := len(h), len(st); h >= s {
		t.Fatalf("Huffman %d bytes, stored %d bytes", h, s)
	}
}

func FuzzDeflate(f *testing.F) {
	r := rand.New(rand.NewPCG(13, 14))
	f.Add([]byte{})
	f.Add(sampleData(r, 5000))
	f.Fuzz(func(t *testing.T, b []byte) {
		for level := HuffmanOnly; level <= BestCompression; level++ {
			comp, err := Deflate(nil, b, level)
			if err != nil {
				t.Fatal(err)
			}
			checkInflate(t, comp, b)
		}
	})
}

func TestDeflateLevels(t *testing.T) {
	r := rand.New(rand.NewPCG(15, 16))
	var inputs [][]byte
	for _, n := range []int{0, 1, 15, 16, 17, 100, 1000, blockSize - 1, blockSize, blockSize + 1, 300000, 600000} {
		inputs = append(inputs, sampleData(r, n))
	}
	inputs = append(inputs, make([]byte, 500000), bytes.Repeat([]byte("abcdefghijklmnopqrstuvwxyz0123456789"), 50000))
	for level := HuffmanOnly; level <= BestCompression; level++ {
		for _, in := range inputs {
			comp, err := Deflate([]byte("x"), in, level)
			if err != nil {
				t.Fatal(err)
			}
			if comp[0] != 'x' {
				t.Fatal("prefix changed")
			}
			checkInflate(t, comp[1:], in)

			gz, err := Gzip(in, level)
			if err != nil {
				t.Fatal(err)
			}
			if got := stdGunzip(t, gz); !bytes.Equal(got, in) {
				t.Fatalf("level %d, %d bytes: compress/gzip output differs", level, len(in))
			}
			if got, err := Gunzip(gz); err != nil || !bytes.Equal(got, in) {
				t.Fatalf("level %d, %d bytes: Gunzip: %v", level, len(in), err)
			}
		}
	}
	for _, level := range []int{-3, 10} {
		if _, err := Deflate(nil, nil, level); err == nil {
			t.Errorf("level %d: no error", level)
		}
		if _, err := Gzip(nil, level); err == nil {
			t.Errorf("level %d: no error", level)
		}
	}
}

func TestDeflateCompresses(t *testing.T) {
	r := rand.New(rand.NewPCG(17, 18))
	data := sampleData(r, 1<<20)
	var buf bytes.Buffer
	w, _ := flate.NewWriter(&buf, flate.BestSpeed)
	w.Write(data)
	w.Close()
	comp, _ := Deflate(nil, data, BestSpeed)
	// sampleData repeats a lot; level 1 must find most of it
	if len(comp) > buf.Len()*3/2 {
		t.Fatalf("level 1: %d bytes, compress/flate level 1: %d bytes", len(comp), buf.Len())
	}
}

// FASTCOMPRESS_RAW names one uncompressed file to compress.
func BenchmarkGzip(b *testing.B) {
	f := os.Getenv("FASTCOMPRESS_RAW")
	if f == "" {
		b.Skip("FASTCOMPRESS_RAW not set")
	}
	src, err := os.ReadFile(f)
	if err != nil {
		b.Fatal(err)
	}
	noCheckCounts(b)
	for _, level := range []int{1, 2, 3, 6, 7, 9} {
		b.Run(fmt.Sprintf("level%d", level), func(b *testing.B) {
			b.SetBytes(int64(len(src)))
			for b.Loop() {
				if _, err := Gzip(src, level); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// FASTCOMPRESS_RAW names one uncompressed file to compress with a Writer, in
// 64 KB writes as io.Copy from a file makes them.
func BenchmarkWriter(b *testing.B) {
	f := os.Getenv("FASTCOMPRESS_RAW")
	if f == "" {
		b.Skip("FASTCOMPRESS_RAW not set")
	}
	src, err := os.ReadFile(f)
	if err != nil {
		b.Fatal(err)
	}
	noCheckCounts(b)
	var buf bytes.Buffer
	for _, level := range []int{1, 6} {
		b.Run(fmt.Sprintf("level%d", level), func(b *testing.B) {
			b.SetBytes(int64(len(src)))
			z, _ := NewWriterLevel(&buf, level)
			for b.Loop() {
				buf.Reset()
				z.Reset(&buf)
				for p := src; len(p) > 0; {
					k := min(len(p), 64<<10)
					z.Write(p[:k])
					p = p[k:]
				}
				if err := z.Close(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// TestGzipCapacity checks that Gzip returns no more than twice the capacity
// its output needs, however well the input compresses.
func TestGzipCapacity(t *testing.T) {
	r := rand.New(rand.NewPCG(57, 58))
	for _, in := range [][]byte{make([]byte, 4<<20), sampleData(r, 4<<20)} {
		for _, level := range []int{HuffmanOnly, NoCompression, BestSpeed, DefaultCompression, BestCompression} {
			out, _ := Gzip(in, level)
			if cap(out) > 2*len(out) {
				t.Errorf("level %d: %d bytes in a capacity of %d", level, len(out), cap(out))
			}
		}
	}
}
