package gzip_test

import (
	"archive/zip"
	"bytes"
	stdgzip "compress/gzip"
	"fmt"
	"io"
	"log"

	"github.com/x-mesh/fastcompress/gzip"
)

func compressed(s string) []byte {
	var buf bytes.Buffer
	w := stdgzip.NewWriter(&buf)
	w.Write([]byte(s))
	w.Close()
	return buf.Bytes()
}

func ExampleNewReader() {
	z, err := gzip.NewReader(bytes.NewReader(compressed("hello, gzip\n")))
	if err != nil {
		log.Fatal(err)
	}
	defer z.Close()
	text, err := io.ReadAll(z)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Print(string(text))
	// Output: hello, gzip
}

func ExampleGunzip() {
	text, err := gzip.Gunzip(compressed("whole file in memory\n"))
	if err != nil {
		log.Fatal(err)
	}
	fmt.Print(string(text))
	// Output: whole file in memory
}

func ExampleNewWriterLevel() {
	var buf bytes.Buffer
	z, err := gzip.NewWriterLevel(&buf, gzip.BestSpeed)
	if err != nil {
		log.Fatal(err)
	}
	if _, err := io.WriteString(z, "streamed through a Writer\n"); err != nil {
		log.Fatal(err)
	}
	if err := z.Close(); err != nil {
		log.Fatal(err)
	}
	text, err := gzip.Gunzip(buf.Bytes())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Print(string(text))
	// Output: streamed through a Writer
}

func ExampleGzip() {
	compressed, err := gzip.Gzip([]byte("whole file in memory\n"), gzip.DefaultCompression)
	if err != nil {
		log.Fatal(err)
	}
	r, err := stdgzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		log.Fatal(err)
	}
	text, err := io.ReadAll(r)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Print(string(text))
	// Output: whole file in memory
}

func ExampleGzipConcurrent() {
	text := bytes.Repeat([]byte("one line of a large log\n"), 200000)
	// 0 goroutines means runtime.GOMAXPROCS(0)
	compressed, err := gzip.GzipConcurrent(text, gzip.BestSpeed, 0)
	if err != nil {
		log.Fatal(err)
	}
	got, err := gzip.Gunzip(compressed)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(bytes.Equal(got, text))
	// Output: true
}

func ExampleNewInflateReader() {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("hello.txt")
	if err != nil {
		log.Fatal(err)
	}
	io.WriteString(w, "hello, zip\n")
	if err := zw.Close(); err != nil {
		log.Fatal(err)
	}

	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		log.Fatal(err)
	}
	// archive/zip gives the decompressor a reader that ends with the entry
	zr.RegisterDecompressor(zip.Deflate, func(r io.Reader) io.ReadCloser {
		return gzip.NewInflateReader(r)
	})
	f, err := zr.File[0].Open()
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	text, err := io.ReadAll(f)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Print(string(text))
	// Output: hello, zip
}

func ExampleInflateLimit() {
	raw, err := gzip.Deflate(nil, make([]byte, 8<<20), gzip.BestCompression)
	if err != nil {
		log.Fatal(err)
	}
	out, _, err := gzip.InflateLimit(nil, raw, 1<<20)
	fmt.Println(len(out), err)
	// Output: 1048576 gzip: output exceeds the limit
}
