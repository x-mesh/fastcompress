package fastgzip_test

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"log"

	"github.com/x-mesh/fastgzip"
)

func compressed(s string) []byte {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	w.Write([]byte(s))
	w.Close()
	return buf.Bytes()
}

func ExampleNewReader() {
	z, err := fastgzip.NewReader(bytes.NewReader(compressed("hello, gzip\n")))
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
	text, err := fastgzip.Gunzip(compressed("whole file in memory\n"))
	if err != nil {
		log.Fatal(err)
	}
	fmt.Print(string(text))
	// Output: whole file in memory
}

func ExampleNewWriterLevel() {
	var buf bytes.Buffer
	z, err := fastgzip.NewWriterLevel(&buf, fastgzip.BestSpeed)
	if err != nil {
		log.Fatal(err)
	}
	if _, err := io.WriteString(z, "streamed through a Writer\n"); err != nil {
		log.Fatal(err)
	}
	if err := z.Close(); err != nil {
		log.Fatal(err)
	}
	text, err := fastgzip.Gunzip(buf.Bytes())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Print(string(text))
	// Output: streamed through a Writer
}

func ExampleGzip() {
	compressed, err := fastgzip.Gzip([]byte("whole file in memory\n"), fastgzip.DefaultCompression)
	if err != nil {
		log.Fatal(err)
	}
	r, err := gzip.NewReader(bytes.NewReader(compressed))
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
	compressed, err := fastgzip.GzipConcurrent(text, fastgzip.BestSpeed, 0)
	if err != nil {
		log.Fatal(err)
	}
	got, err := fastgzip.Gunzip(compressed)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(bytes.Equal(got, text))
	// Output: true
}
