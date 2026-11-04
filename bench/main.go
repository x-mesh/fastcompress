// Command bench measures gzip decompression with the Go standard library,
// klauspost/compress, and fastgzip, and zstd decompression with
// klauspost/compress. The magic bytes of each file select the decoders.
// Each file is decoded whole from memory, or with -stream through io.Reader
// from a 1 MB bufio.Reader. It checks that all decoders produce the same
// output and prints the median of -n runs. With -compress it measures gzip
// compression at that level instead, or with -zstd zstd compression.
//
//	go run . -stream -n 9 testdata/*.gz testdata/*.zst
package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"flag"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"slices"
	"time"

	kgzip "github.com/klauspost/compress/gzip"
	"github.com/klauspost/compress/zstd"

	"github.com/x-mesh/fastgzip"
)

type decoder struct {
	name   string
	fn     func([]byte) ([]byte, error)
	reader func(io.Reader) (io.Reader, error)
}

// drain reads r to the end with 64 KB reads, as a line scanner would, and
// hashes the output when h is not nil.
func drain(r io.Reader, h io.Writer) error {
	buf := make([]byte, 64<<10)
	for {
		n, err := r.Read(buf)
		if h != nil {
			h.Write(buf[:n])
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func readAll(r io.Reader, sizeHint int) ([]byte, error) {
	var buf bytes.Buffer
	buf.Grow(sizeHint)
	_, err := buf.ReadFrom(r)
	return buf.Bytes(), err
}

func isize(b []byte) int {
	if len(b) < 4 {
		return 0
	}
	n := len(b)
	return int(b[n-4]) | int(b[n-3])<<8 | int(b[n-2])<<16 | int(b[n-1])<<24
}

var gzipDecoders = []decoder{
	{"std", func(b []byte) ([]byte, error) {
		r, err := gzip.NewReader(bytes.NewReader(b))
		if err != nil {
			return nil, err
		}
		return readAll(r, isize(b))
	}, func(r io.Reader) (io.Reader, error) { return gzip.NewReader(r) }},
	{"klauspost", func(b []byte) ([]byte, error) {
		r, err := kgzip.NewReader(bytes.NewReader(b))
		if err != nil {
			return nil, err
		}
		return readAll(r, isize(b))
	}, func(r io.Reader) (io.Reader, error) { return kgzip.NewReader(r) }},
	{"fastgzip", fastgzip.Gunzip, func(r io.Reader) (io.Reader, error) { return fastgzip.NewReader(r) }},
}

// klauspost asks callers to reuse a Decoder. A new Decoder for each run
// would also measure the allocation of its window buffers.
var (
	kzstd  = newZstd()
	kzstd1 = newZstd(zstd.WithDecoderConcurrency(1))
)

func newZstd(opts ...zstd.DOption) *zstd.Decoder {
	d, err := zstd.NewReader(nil, opts...)
	if err != nil {
		panic(err)
	}
	return d
}

// DecodeAll always decodes on the calling goroutine, so the two decoders
// differ only with -stream. There the default Decoder uses up to four
// goroutines.
var zstdDecoders = []decoder{
	{"klauspost", func(b []byte) ([]byte, error) { return kzstd.DecodeAll(b, nil) },
		func(r io.Reader) (io.Reader, error) { return kzstd, kzstd.Reset(r) }},
	{"klauspost-1", func(b []byte) ([]byte, error) { return kzstd1.DecodeAll(b, nil) },
		func(r io.Reader) (io.Reader, error) { return kzstd1, kzstd1.Reset(r) }},
	{"fastgzip", fastgzip.Unzstd, func(r io.Reader) (io.Reader, error) {
		if fzstd == nil {
			var err error
			fzstd, err = fastgzip.NewZstdReader(r)
			return fzstd, err
		}
		return fzstd, fzstd.Reset(r)
	}},
}

// reused across runs like the klauspost Decoders
var fzstd *fastgzip.ZstdReader

var formats = []struct {
	name     string
	magic    []byte
	decoders []decoder
}{
	{"gzip", []byte{0x1f, 0x8b}, gzipDecoders},
	{"zstd", []byte{0x28, 0xb5, 0x2f, 0xfd}, zstdDecoders},
}

func main() {
	runs := flag.Int("n", 9, "runs per file and decoder")
	only := flag.String("only", "", "run only this decoder")
	stream := flag.Bool("stream", false, "decode through io.Reader from a 1 MB bufio.Reader, as edc grep does")
	level := flag.Int("compress", 0, "compress the files at this level (1-9) instead of decoding them")
	useZstd := flag.Bool("zstd", false, "with -compress, compress with zstd and compare with klauspost/compress/zstd")
	flag.Parse()
	if *runs < 1 {
		fmt.Fprintln(os.Stderr, "-n must be at least 1")
		os.Exit(2)
	}

	var files []string
	for _, a := range flag.Args() {
		m, _ := filepath.Glob(a)
		files = append(files, m...)
	}
	if *level != 0 {
		if *useZstd {
			benchZstdCompress(files, *level, *runs)
		} else {
			benchCompress(files, *level, *runs, *only)
		}
		return
	}

	header := -1
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fi := -1
		for i, ft := range formats {
			if bytes.HasPrefix(src, ft.magic) {
				fi = i
			}
		}
		if fi < 0 {
			fmt.Fprintf(os.Stderr, "%s: not gzip or zstd\n", f)
			os.Exit(1)
		}
		decoders := formats[fi].decoders
		if fi != header {
			header = fi
			fmt.Printf("%-18s", formats[fi].name)
			for _, d := range decoders {
				if *only == "" || *only == d.name {
					fmt.Printf("%12s", d.name)
				}
			}
			fmt.Println()
		}
		fmt.Printf("%-18s", filepath.Base(f))
		var want [32]byte
		for i, d := range decoders {
			if *only != "" && *only != d.name {
				continue
			}
			times := make([]time.Duration, *runs)
			var sum [32]byte
			for r := range times {
				t := time.Now()
				if *stream {
					var h hash.Hash
					if r == 0 {
						h = sha256.New()
					}
					var zr io.Reader
					zr, err = d.reader(bufio.NewReaderSize(bytes.NewReader(src), 1<<20))
					if err == nil {
						if h != nil {
							err = drain(zr, h)
							copy(sum[:], h.Sum(nil))
						} else {
							err = drain(zr, nil)
						}
					}
				} else {
					var out []byte
					out, err = d.fn(src)
					if r == 0 {
						sum = sha256.Sum256(out)
					}
				}
				times[r] = time.Since(t)
				if err != nil {
					fmt.Fprintf(os.Stderr, "\n%s %s: %v\n", d.name, f, err)
					os.Exit(1)
				}
			}
			if i == 0 || want == [32]byte{} {
				want = sum
			} else if sum != want {
				fmt.Fprintf(os.Stderr, "\n%s %s: output differs\n", d.name, f)
				os.Exit(1)
			}
			slices.Sort(times)
			fmt.Printf("%12.2f", float64(times[len(times)/2].Microseconds())/1000)
		}
		fmt.Println()
	}
}

// benchZstdCompress compresses each file from memory with klauspost EncodeAll
// on one goroutine and with fastgzip.Zstd, checks that both decoders
// restore it, and prints the median time of runs and the ratio.
func benchZstdCompress(files []string, level, runs int) {
	kenc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.EncoderLevelFromZstd(level)), zstd.WithEncoderConcurrency(1))
	if err != nil {
		panic(err)
	}
	median := func(f func()) time.Duration {
		times := make([]time.Duration, runs)
		for r := range times {
			t := time.Now()
			f()
			times[r] = time.Since(t)
		}
		slices.Sort(times)
		return times[len(times)/2]
	}
	fmt.Printf("%-18s%22s%22s\n", fmt.Sprintf("zstd -%d", level), "klauspost ms (ratio)", "fastgzip ms (ratio)")
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		var kc, fc []byte
		kt := median(func() { kc = kenc.EncodeAll(src, kc[:0]) })
		ft := median(func() { fc = fastgzip.Zstd(fc[:0], src, level) })
		for _, c := range [][]byte{kc, fc} {
			a, err1 := fastgzip.Unzstd(c)
			b, err2 := kzstd1.DecodeAll(c, nil)
			if err1 != nil || err2 != nil || !bytes.Equal(a, src) || !bytes.Equal(b, src) {
				fmt.Fprintf(os.Stderr, "%s: round trip failed: %v %v\n", f, err1, err2)
				os.Exit(1)
			}
		}
		ratio := func(c []byte) float64 { return float64(len(src)) / float64(len(c)) }
		fmt.Printf("%-18s%13.1f (%5.3f)%13.1f (%5.3f)\n", filepath.Base(f),
			float64(kt.Microseconds())/1000, ratio(kc), float64(ft.Microseconds())/1000, ratio(fc))
	}
}
