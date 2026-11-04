package zstd_test

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"os"

	"github.com/x-mesh/fastcompress/zstd"
)

func ExampleEncode() {
	compressed := zstd.Encode(nil, []byte("hello, hello, hello, zstd"), 1)
	text, err := zstd.Decode(compressed)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(string(text))
	// Output: hello, hello, hello, zstd
}

func ExampleNewWriter() {
	var buf bytes.Buffer
	w := zstd.NewWriter(&buf, 3)
	if _, err := io.WriteString(w, "a stream of text"); err != nil {
		log.Fatal(err)
	}
	if err := w.Close(); err != nil {
		log.Fatal(err)
	}
	z, err := zstd.NewReader(&buf)
	if err != nil {
		log.Fatal(err)
	}
	defer z.Close()
	io.Copy(os.Stdout, z)
	// Output: a stream of text
}
