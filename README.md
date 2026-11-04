# fastgzip

fastgzip decodes and encodes gzip and DEFLATE data in Go, without cgo. On an Apple M1 Pro, it decodes a 100 MB log about 3.3 times faster than `klauspost/compress/gzip`, at the speed of the system zlib on macOS.

## Install

```sh
go get github.com/x-mesh/fastgzip
```

fastgzip needs Go 1.24 or later. It has no dependencies.

## Use

`Reader` replaces `compress/gzip.Reader` for reads:

```go
z, err := fastgzip.NewReader(file)
if err != nil {
	return err
}
defer z.Close()
_, err = io.Copy(dst, z)
```

Call `Close` when you do not need the reader. `Close` gives the 1.3 MB of buffers back to a pool, and the next `NewReader` uses them again. `Reset` starts a new stream on the same reader.

If the data is already in memory, use `Gunzip`:

```go
text, err := fastgzip.Gunzip(compressed)
```

`Inflate(dst, src)` decodes one raw DEFLATE stream. It appends the result to `dst` and returns the number of input bytes it used.

The errors are `ErrHeader`, `ErrChecksum` and `ErrCorrupt`. Truncated input gives `io.ErrUnexpectedEOF`. Before `Reader` returns that error, it returns all the bytes that it decoded.

## Compress

`Writer` replaces `compress/gzip.Writer`:

```go
z, err := fastgzip.NewWriterLevel(file, fastgzip.BestSpeed)
if err != nil {
	return err
}
if _, err := io.Copy(z, src); err != nil {
	return err
}
return z.Close()
```

`Close` writes the gzip trailer and gives the buffers back to a pool. `Close` does not close `file`. `Flush` writes all the data that the writer holds, so that a reader can decode it. `Reset` starts a new stream on the same writer.

If the data is already in memory, use `Gzip`:

```go
compressed, err := fastgzip.Gzip(text, fastgzip.DefaultCompression)
```

`Deflate(dst, src, level)` encodes one raw DEFLATE stream and appends it to `dst`.

The levels have the same values as in `compress/gzip`. `BestSpeed` is 1 and `BestCompression` is 9. `DefaultCompression` (-1) is level 6. `NoCompression` (0) writes stored blocks, and `HuffmanOnly` (-2) writes literals only.

If you do not call `Flush`, a `Writer` writes the same bytes as `Gzip` for the same input and level.

### Compress on more than one core

`GzipConcurrent` and `Writer.SetConcurrency` compress on more than one goroutine:

```go
compressed, err := fastgzip.GzipConcurrent(text, fastgzip.DefaultCompression, 0)
```

```go
z := fastgzip.NewWriter(file)
z.SetConcurrency(runtime.GOMAXPROCS(0)) // before the first Write
```

The input goes in chunks of approximately 1 MB. Each chunk uses the 32 KB before it as a dictionary, so the output has approximately the same size as the output of `Gzip`. The output does not change with the number of goroutines. For an input of 1 MB or less, it is the same as the output of `Gzip`. If you do not call `Flush`, a concurrent `Writer` writes the same bytes as `GzipConcurrent`.

A concurrent `Writer` holds up to two chunks of input and output for each goroutine. `GzipConcurrent` with a count of 0 uses `runtime.GOMAXPROCS(0)` goroutines.

## Differences from compress/gzip

- `Reader` does not give the header fields (`Name`, `Comment`, `ModTime`).
- `Reader` always reads concatenated members as one stream. It has no `Multistream` switch.
- `Writer` does not write the header fields (`Name`, `Comment`, `ModTime`). The OS field is 255 (unknown), as in `compress/gzip`.
- The compressed bytes are not the same as the output of `compress/gzip`.

## Performance

### Decompression

The table shows the median time of 11 runs in milliseconds. The machine is an Apple M1 Pro with macOS 27 and Go 1.27.3. Each decoder reads through a 1 MB `bufio.Reader` with 64 KB reads.

| File | Size | compress/gzip | klauspost v1.20.3 | fastgzip |
|---|---|---|---|---|
| Application log, `gzip -1` | 100 MB | 357 | 241 | 70.5 |
| Binaries, level 6 | 16 MB | 64 | 44 | 17.6 |
| Word list, level 1 | 2.5 MB | 14.4 | 9.9 | 3.4 |
| Short repeated patterns, level 6 | 40 MB | 13.8 | 11.7 | 7.2 |
| Byte runs, `Z_RLE` | 15 MB | 8.1 | 7.0 | 3.0 |

For comparison, Apple's libz decodes the 100 MB log in 71.4 ms with one C `inflate` call. `Gunzip` decodes it in 68.7 ms. On Linux arm64 (Docker on the same Mac), the `Reader` time for the log is 70.9 ms. On the same machine, klauspost needs 244 ms, and Debian zlib 1.3.1 needs 193 ms through Python.

We did not measure decompression on amd64.

To measure your own files, run the benchmark tool. It also makes sure that all the decoders give the same output.

```sh
cd bench
go run . -stream -n 9 /path/to/*.gz
```

### Compression

We compared fastgzip with `klauspost/compress/gzip` at all levels on large files. At levels 1-3, fastgzip needs 3-19% less time. At levels 4-9, it needs from 29% less to 10% more time. It needs more time only for the binaries at levels 4 and 6, and for the log at levels 6 and 7. The output size is approximately the same. On 8 goroutines, `GzipConcurrent` needs 3.9-5.6 times less time than `Gzip`, with the same output size to 0.001%. The table shows levels 1, 6 and 9.

The table shows the median time of 5 runs in milliseconds, and the output size in percent of the input. The machine is an Intel Xeon Silver 4112 (4 cores, 8 threads) with Linux and Go 1.27.1. Each compressor gets all the data in memory. The last column is `GzipConcurrent` on 8 goroutines. The other compressors use one goroutine. We did not compare with `klauspost/pgzip`. The fastgzip column is `Gzip`. In this benchmark, a `Writer` that gets all the data in one `Write` needs up to 12% more time than `Gzip`.

| File | Level | compress/gzip | klauspost v1.20.3 | fastgzip | fastgzip, 8 goroutines |
|---|---|---|---|---|---|
| Go source files, 50 MB | 1 | 284 (21.46%) | 238 (21.46%) | 209 (21.19%) | 51 (21.19%) |
| Go binaries, 30 MB | 1 | 298 (44.99%) | 239 (44.99%) | 210 (44.09%) | 54 (44.09%) |
| Generated access log, 100 MB | 1 | 589 (22.83%) | 492 (22.83%) | 418 (22.61%) | 93 (22.61%) |
| Go source files, 50 MB | 6 | 556 (17.67%) | 435 (17.67%) | 409 (17.66%) | 103 (17.66%) |
| Go binaries, 30 MB | 6 | 564 (39.91%) | 457 (39.91%) | 478 (39.65%) | 120 (39.65%) |
| Generated access log, 100 MB | 6 | 1172 (20.08%) | 955 (20.08%) | 987 (20.24%) | 244 (20.24%) |
| Go source files, 50 MB | 9 | 2921 (16.92%) | 2880 (16.92%) | 2492 (16.88%) | 538 (16.88%) |
| Go binaries, 30 MB | 9 | 3870 (38.76%) | 3235 (38.76%) | 2366 (38.71%) | 468 (38.71%) |
| Generated access log, 100 MB | 9 | 6676 (17.90%) | 6367 (17.90%) | 6130 (18.08%) | 1098 (18.08%) |

In Go 1.27, `compress/gzip` gives the same sizes as klauspost.

For small inputs, the table shows the time of one `Gzip` call in microseconds, and of one klauspost `Writer` that is reset for each input. The inputs are parts of the log. The value is the lowest median of 5 runs.

| Input | Level | klauspost v1.20.3 | fastgzip |
|---|---|---|---|
| 1 KB | 1 | 10.3 | 9.7 |
| 8 KB | 1 | 33.0 | 32.6 |
| 64 KB | 1 | 286 | 298 |
| 1 KB | 6 | 12.9 | 13.9 |
| 8 KB | 6 | 63.3 | 77.9 |
| 64 KB | 6 | 578 | 700 |

At level 6, the hash tables of fastgzip have 768 KB, and small inputs use them less efficiently.

We did not measure compression on arm64.

To measure compression, give the uncompressed files and a level to the benchmark tool. It decodes each output with `compress/gzip` and compares it with the input.

```sh
cd bench
go run . -compress 6 -n 5 /path/to/files/*
```

## How it compresses

- Levels 1-3 find matches with one hash table and no chains. The search checks two positions with one 8-byte load. Over literals, the search step increases.
- Levels 4-6 use a table of 4-byte hashes and a table of 8-byte hashes. The second table keeps two positions for each hash. Levels 5 and 6 try the next position when the match is short.
- Levels 7-9 use hash chains and lazy matching, as zlib does. The hash has 17 bits, and the chains are shorter than in zlib. At level 9, a chain of 1024 gave the same sizes as the zlib chain of 4096.
- Each block holds up to 65535 input bytes. The encoder calculates the size of a stored, a fixed and a dynamic block, and writes the smallest.
- The Huffman code lengths come from the Moffat and Katajainen method. If a length is more than 15 bits, the encoder uses the package-merge method.
- Concurrent compression gives each goroutine a chunk of 16 blocks. The encoder first puts the 32 KB before the chunk in the hash tables, and ends the chunk with an empty stored block to align it to a byte. Each goroutine also computes the CRC-32 of its chunk. A port of `crc32_combine` from zlib joins the checksums.
- A new stream does not clear the hash tables. The tables keep each position plus a base, and the encoder moves the base past all the old entries. The tables have up to 768 KB, so for an input of 1 KB, the clear took most of the time.

## Why decompression is fast

- A 64-bit bit buffer. One unaligned 8-byte load refills it, with no branch.
- Each table entry holds the code length plus the extra bits. One shift removes a code and its extra bits.
- Root tables of 10 and 7 bits, so fewer codes need a second table lookup. If the tables do not fit, the decoder uses 9 and 6 bits.
- The table load for the next symbol starts before the refill, so the two delays overlap.
- The decoder reads the next symbol before it copies a match.
- The bit count uses the libdeflate method. The decoder subtracts the full entry and uses only the low 6 bits.
- Match copies move 16 bytes at a time. The first 32 bytes have no loop test, because most matches in log data are 16 bytes or shorter. A short distance repeats as a pattern.
- On arm64, an assembly function computes CRC-32 with `PMULL` carry-less multiplies. It folds eight 128-bit lanes in parallel. This is 45 GB/s, and `hash/crc32` is 8.4 GB/s.
- Above 1 MB, `Gunzip` computes the CRC-32 on a second goroutine. The decoder gives it the output in steps of 1 MB or more, because each step can wake the goroutine.

## Safety

The fast decode loop uses `unsafe` pointers and has no bounds checks. These limits keep each access in its buffer:

- The loop stops when fewer than 25 input bytes or 323 output bytes remain.
- Each table index has a mask with the table size.
- The decoder compares each match distance with the output that it wrote.

The match search of the compressor also loads input through `unsafe` pointers:

- An 8-byte load is at a position that is at least 8 bytes before the end of the block.
- A load at a match candidate occurs only if the candidate is at or after 0 and before the current position.

The token writer stores 8 bytes at a time through an `unsafe` pointer. Before each block, it reserves the bytes for the bits that the symbol counts of the block give, plus 16 bytes. The tests recount the symbols of each block from its tokens, and they stop if the counts are different.

The tests compare the decoder output with `compress/flate` and `compress/gzip` on random and fuzz input. They decode the compressor output with `compress/flate`, `compress/gzip` and fastgzip. The tests run with `-race` and with `-gcflags=all=-d=checkptr`. We ran the decoder tests on darwin/arm64, linux/arm64 and linux/amd64. We ran the compressor tests on linux/amd64 only.

## Tests

```sh
go test ./...
go test -run '^$' -fuzz FuzzReader -fuzztime 60s .
go test -run '^$' -fuzz FuzzWriter -fuzztime 60s .
```

To compare a directory of `.gz` files with `compress/gzip`, set `FASTGZIP_CORPUS`:

```sh
FASTGZIP_CORPUS=/path/to/gz-files go test .
```

To profile one file, set `FASTGZIP_BENCH` and run `BenchmarkReader` or `BenchmarkGunzip`. To profile compression of one uncompressed file, set `FASTGZIP_RAW` and run `BenchmarkGzip`.

## License

MIT. The table builder in `tables.go` is a port of zlib 1.2.12. [NOTICE](NOTICE) has the zlib license.
