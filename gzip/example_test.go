package gzip_test

import (
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
