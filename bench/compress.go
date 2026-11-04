package main

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"time"

	kgzip "github.com/klauspost/compress/gzip"

	"github.com/x-mesh/fastgzip"
)

type compressor struct {
	name string
	fn   func(src []byte, level int) ([]byte, error)
}

func writerCompress(newWriter func(io.Writer, int) (io.WriteCloser, error)) func([]byte, int) ([]byte, error) {
	return func(src []byte, level int) ([]byte, error) {
		var buf bytes.Buffer
		buf.Grow(len(src) / 2)
		w, err := newWriter(&buf, level)
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(src); err != nil {
			return nil, err
		}
		err = w.Close()
		return buf.Bytes(), err
	}
}

var compressors = []compressor{
	{"std", writerCompress(func(w io.Writer, l int) (io.WriteCloser, error) { return gzip.NewWriterLevel(w, l) })},
	{"klauspost", writerCompress(func(w io.Writer, l int) (io.WriteCloser, error) { return kgzip.NewWriterLevel(w, l) })},
	{"fastgzip", fastgzip.Gzip},
	{"fastgzip-w", writerCompress(func(w io.Writer, l int) (io.WriteCloser, error) { return fastgzip.NewWriterLevel(w, l) })},
	{"fastgzip-p", func(src []byte, l int) ([]byte, error) { return fastgzip.GzipConcurrent(src, l, 0) }},
}

// benchCompress prints the median time in ms and the compressed size in
// percent of the input for each file and compressor. fastgzip-w writes all
// the input to a fastgzip.Writer at once. fastgzip-p is GzipConcurrent on
// GOMAXPROCS goroutines. It checks every output with
// compress/gzip.
func benchCompress(files []string, level, runs int, only string) {
	fmt.Printf("level %d: ms / %% of input\n%-18s", level, "file")
	for _, c := range compressors {
		if only == "" || only == c.name {
			fmt.Printf("%20s", c.name)
		}
	}
	fmt.Println()
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Printf("%-18s", filepath.Base(f))
		for _, c := range compressors {
			if only != "" && only != c.name {
				continue
			}
			times := make([]time.Duration, runs)
			var out []byte
			for r := range times {
				t := time.Now()
				out, err = c.fn(src, level)
				times[r] = time.Since(t)
				if err != nil {
					fmt.Fprintf(os.Stderr, "\n%s %s: %v\n", c.name, f, err)
					os.Exit(1)
				}
			}
			zr, err := gzip.NewReader(bytes.NewReader(out))
			if err == nil {
				var got []byte
				got, err = io.ReadAll(zr)
				if err == nil && !bytes.Equal(got, src) {
					err = fmt.Errorf("output differs")
				}
			}
			if err != nil {
				fmt.Fprintf(os.Stderr, "\n%s %s: %v\n", c.name, f, err)
				os.Exit(1)
			}
			slices.Sort(times)
			fmt.Printf("%11.2f / %5.2f%%", float64(times[len(times)/2].Microseconds())/1000, 100*float64(len(out))/float64(len(src)))
		}
		fmt.Println()
	}
}
