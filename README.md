# fastcompress

fastcompress decodes and encodes gzip, DEFLATE and zstd data in Go, without cgo. On an Apple M1 Pro, it decodes a 100 MB log about 3.3 times faster than `klauspost/compress/gzip`, at the speed of the system zlib on macOS. On an Intel Xeon, its zstd decoder and encoder are faster than `klauspost/compress/zstd` on each file that we measured.

| Package | Formats |
|---|---|
| `github.com/x-mesh/fastcompress/gzip` | gzip and raw DEFLATE |
| `github.com/x-mesh/fastcompress/zstd` | zstd |

## Install

```sh
go get github.com/x-mesh/fastcompress
```

fastcompress needs Go 1.24 or later. It has no dependencies.

### Move from fastgzip

Until v0.1.2, this module was `github.com/x-mesh/fastgzip`, with all the functions in one package. Change the imports as follows:

| fastgzip | fastcompress |
|---|---|
| `fastgzip.Gunzip`, `fastgzip.NewReader`, `fastgzip.Gzip` and the other gzip and DEFLATE names | the same names in the `gzip` package |
| `fastgzip.Unzstd` | `zstd.Decode` |
| `fastgzip.NewZstdReader`, `fastgzip.ZstdReader` | `zstd.NewReader`, `zstd.Reader` |
| `fastgzip.Zstd` | `zstd.Encode` |
| `fastgzip.NewZstdWriter`, `fastgzip.ZstdWriter` | `zstd.NewWriter`, `zstd.Writer` |

The zstd package has its own `ErrHeader`, `ErrChecksum` and `ErrCorrupt`. The error texts start with `gzip:` or `zstd:`.

## Use

The package names are `gzip` and `zstd`. If a file also imports `compress/gzip`, give one of the two imports another name:

```go
import (
	stdgzip "compress/gzip"

	"github.com/x-mesh/fastcompress/gzip"
)
```

`Reader` replaces `compress/gzip.Reader` for reads:

```go
z, err := gzip.NewReader(file)
if err != nil {
	return err
}
defer z.Close()
_, err = io.Copy(dst, z)
```

Call `Close` when you do not need the reader. `Close` gives the 1.3 MB of buffers back to a pool, and the next `NewReader` uses them again. `Reset` starts a new stream on the same reader.

If the data is already in memory, use `Gunzip`:

```go
text, err := gzip.Gunzip(compressed)
```

`Inflate(dst, src)` decodes one raw DEFLATE stream. It appends the result to `dst` and returns the number of input bytes it used.

The errors are `ErrHeader`, `ErrChecksum` and `ErrCorrupt`. Truncated input gives `io.ErrUnexpectedEOF`. Before `Reader` returns that error, it returns all the bytes that it decoded.

## Compress

`Writer` replaces `compress/gzip.Writer`:

```go
z, err := gzip.NewWriterLevel(file, gzip.BestSpeed)
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
compressed, err := gzip.Gzip(text, gzip.DefaultCompression)
```

`Deflate(dst, src, level)` encodes one raw DEFLATE stream and appends it to `dst`.

The levels have the same values as in `compress/gzip`. `BestSpeed` is 1 and `BestCompression` is 9. `DefaultCompression` (-1) is level 6. `NoCompression` (0) writes stored blocks, and `HuffmanOnly` (-2) writes literals only.

If you do not call `Flush`, a `Writer` writes the same bytes as `Gzip` for the same input and level.

### Compress on more than one core

`GzipConcurrent` and `Writer.SetConcurrency` compress on more than one goroutine:

```go
compressed, err := gzip.GzipConcurrent(text, gzip.DefaultCompression, 0)
```

```go
z := gzip.NewWriter(file)
z.SetConcurrency(runtime.GOMAXPROCS(0)) // before the first Write
```

The input goes in chunks of approximately 1 MB. Each chunk uses the 32 KB before it as a dictionary, so the output has approximately the same size as the output of `Gzip`. The output does not change with the number of goroutines. For an input of 1 MB or less, it is the same as the output of `Gzip`. If you do not call `Flush`, a concurrent `Writer` writes the same bytes as `GzipConcurrent`.

A concurrent `Writer` holds up to two chunks of input and output for each goroutine. `GzipConcurrent` with a count of 0 uses `runtime.GOMAXPROCS(0)` goroutines.

## zstd

`zstd.Decode` decodes zstd data that is in memory:

```go
text, err := zstd.Decode(compressed)
```

`zstd.Reader` reads a zstd stream. `NewReader`, `Read`, `Reset` and `Close` work like the methods of `gzip.Reader`:

```go
z, err := zstd.NewReader(file)
if err != nil {
	return err
}
defer z.Close()
_, err = io.Copy(dst, z)
```

`zstd.Encode` appends one zstd frame to `dst`. Level 1 is the fastest. Level 3 gives a better ratio, as `zstd -3` does. Levels below 3 use the method of level 1, and levels above 3 use the method of level 3.

```go
compressed := zstd.Encode(nil, data, 1)
```

`zstd.Writer` compresses a stream into one frame. Call `Close` to end the frame:

```go
w := zstd.NewWriter(file, 1)
if _, err := io.Copy(w, src); err != nil {
	return err
}
return w.Close()
```

The decoders skip skippable frames. They do not support dictionaries. A `zstd.Reader` refuses a window larger than 128 MB, as the zstd library does by default. A bad magic number gives `zstd.ErrHeader`, a bad content checksum gives `zstd.ErrChecksum`, and other bad data gives `zstd.ErrCorrupt`.

With more than one CPU, the zstd code uses a second goroutine for 1 MB of data or more. `zstd.Reader` uses a third goroutine, and `zstd.Writer` always uses a second goroutine. This makes one call faster, but it uses more CPU time. Set `GOMAXPROCS=1` to use one goroutine. A write error of the writer under a `zstd.Writer` shows at the next `Flush` or `Close`.

## Differences from compress/gzip

These items apply to the `gzip` package.


- `Reader` does not give the header fields (`Name`, `Comment`, `ModTime`).
- `Reader` always reads concatenated members as one stream. It has no `Multistream` switch.
- `Writer` does not write the header fields (`Name`, `Comment`, `ModTime`). The OS field is 255 (unknown), as in `compress/gzip`.
- The compressed bytes are not the same as the output of `compress/gzip`.

## Performance

### gzip decompression

The table shows the median time of 11 runs in milliseconds. The machine is an Apple M1 Pro with macOS 27 and Go 1.27.3. Each decoder reads through a 1 MB `bufio.Reader` with 64 KB reads. The files are a 100 MB synthetic application log, a 45 MB tar of Go binaries and a 61 MB tar of Go source, at gzip levels 1 and 6. The zstd section uses the same files.

| File | Size | compress/gzip | klauspost v1.20.3 | fastcompress |
|---|---|---|---|---|
| Log, level 1 | 100 MB | 310 | 223 | 67.5 |
| Log, level 6 | 100 MB | 265 | 193 | 70.2 |
| Binaries, level 1 | 45 MB | 252 | 178 | 62.7 |
| Binaries, level 6 | 45 MB | 218 | 152 | 56.7 |
| Source, level 1 | 61 MB | 222 | 156 | 51.1 |
| Source, level 6 | 61 MB | 187 | 132 | 46.0 |

For comparison, Apple's libz 1.2.12 decodes the log at level 1 in 66.1 ms with one C `inflate` call. `Gunzip` decodes it in 62.7 ms. On Linux arm64 (Docker on the same Mac), the `Reader` time for this file is 66.7 ms. On the same machine, klauspost needs 222 ms, and Debian zlib 1.3.1 needs 178 ms through Python.

The next table is for amd64. The machine is an Intel Xeon Silver 4112 (4 cores, 8 threads) with Linux and Go 1.27.1. The table shows the median time of 11 runs in milliseconds, with the same `Reader` setup. The files are the files of the table above. The "Go loop" column is fastcompress with `-tags purego`, which turns off the assembly.

| File | Size | compress/gzip | klauspost v1.20.3 | fastcompress, Go loop | fastcompress |
|---|---|---|---|---|---|
| Log, level 1 | 100 MB | 532 | 389 | 161.0 | 106.5 |
| Log, level 6 | 100 MB | 480 | 335 | 148.0 | 115.3 |
| Binaries, level 1 | 45 MB | 421 | 311 | 133.8 | 98.8 |
| Binaries, level 6 | 45 MB | 361 | 270 | 114.9 | 86.9 |
| Source, level 1 | 61 MB | 371 | 274 | 116.8 | 78.6 |
| Source, level 6 | 61 MB | 304 | 227 | 99.4 | 72.8 |

For comparison, libdeflate 1.19 decodes the log at level 1 in 96.0 ms with one C call. `Gunzip` decodes it in 107.5 ms, and in 156.8 ms without the assembly.

To measure your own files, run the benchmark tool. It also makes sure that all the decoders give the same output.

```sh
cd bench
go run . -stream -n 9 /path/to/*.gz
```

### gzip compression

We compared fastcompress with `klauspost/compress/gzip` at all levels on large files. At levels 1-3, fastcompress needs 3-19% less time. At levels 4-9, it needs from 29% less to 10% more time. It needs more time only for the binaries at levels 4 and 6, and for the log at levels 6 and 7. The output size is approximately the same. On 8 goroutines, `GzipConcurrent` needs 3.9-5.6 times less time than `Gzip`, with the same output size to 0.001%. The table shows levels 1, 6 and 9.

The table shows the median time of 5 runs in milliseconds, and the output size in percent of the input. The machine is an Intel Xeon Silver 4112 (4 cores, 8 threads) with Linux and Go 1.27.1. Each compressor gets all the data in memory. The last column is `GzipConcurrent` on 8 goroutines. The other compressors use one goroutine. We did not compare with `klauspost/pgzip`. The fastcompress column is `Gzip`. In this benchmark, a `Writer` that gets all the data in one `Write` needs up to 12% more time than `Gzip`.

| File | Level | compress/gzip | klauspost v1.20.3 | fastcompress | fastcompress, 8 goroutines |
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

| Input | Level | klauspost v1.20.3 | fastcompress |
|---|---|---|---|
| 1 KB | 1 | 10.3 | 9.7 |
| 8 KB | 1 | 33.0 | 32.6 |
| 64 KB | 1 | 286 | 298 |
| 1 KB | 6 | 12.9 | 13.9 |
| 8 KB | 6 | 63.3 | 77.9 |
| 64 KB | 6 | 578 | 700 |

At level 6, the hash tables of fastcompress have 768 KB, and small inputs use them less efficiently.

We did not measure gzip compression on arm64.

To measure compression, give the uncompressed files and a level to the benchmark tool. It decodes each output with `compress/gzip` and compares it with the input.

```sh
cd bench
go run . -compress 6 -n 5 /path/to/files/*
```

### zstd

The tables show the median time in milliseconds. The machine is an Intel Xeon Silver 4112 (4 cores, 8 threads) with Linux and Go 1.27.1. The other library is `klauspost/compress` v1.20.3. Other users also loaded the machine, so a time can vary by about 5%.

The files are a 100 MB synthetic application log, a 45 MB tar of Go binaries and a 61 MB tar of Go source. The zstd 1.5.5 command compressed them at levels 1, 3 and 19.

Decoding, 21 runs. The readers read through a 1 MB `bufio.Reader` with 64 KB reads. `DecodeAll` uses one goroutine with any setting, so the table shows the faster of two runs with one and four goroutines. The klauspost `Decoder` has its default concurrency.

| File | Level | klauspost `DecodeAll` | `zstd.Decode` | klauspost `Decoder` | `zstd.Reader` |
|---|---|---|---|---|---|
| Log | 1 | 121.8 | 89.6 | 100.3 | 80.9 |
| Log | 3 | 128.2 | 89.8 | 93.6 | 88.0 |
| Log | 19 | 109.4 | 63.6 | 78.0 | 72.9 |
| Binaries | 1 | 72.2 | 60.5 | 54.6 | 45.9 |
| Binaries | 3 | 80.2 | 57.5 | 61.5 | 54.2 |
| Binaries | 19 | 99.1 | 88.0 | 99.2 | 83.6 |
| Source | 1 | 76.0 | 55.8 | 62.9 | 49.0 |
| Source | 3 | 84.5 | 58.0 | 68.7 | 60.2 |
| Source | 19 | 92.5 | 83.4 | 93.3 | 80.7 |

With `GOMAXPROCS=1`, `zstd.Decode` takes 72 to 126 ms on these files. That is between 3% slower and 14% faster than `DecodeAll`.

Encoding from memory, 9 runs, with the ratio in parentheses. `klauspost/compress` uses `zstd.EncoderLevelFromZstd`: `SpeedFastest` for level 1 and `SpeedDefault` for level 3. `EncodeAll` uses one goroutine.

| File | Level | klauspost `EncodeAll` | `zstd.Encode` | `zstd.Encode`, one CPU |
|---|---|---|---|---|
| Log | 1 | 426.0 (6.110) | 237.6 (6.153) | 346.4 |
| Binaries | 1 | 265.8 (2.704) | 147.9 (2.745) | 232.2 |
| Source | 1 | 275.1 (4.129) | 168.8 (4.341) | 244.4 |
| Log | 3 | 569.1 (5.952) | 421.0 (6.075) | 535.9 |
| Binaries | 3 | 368.2 (2.829) | 277.4 (2.827) | 363.8 |
| Source | 3 | 360.9 (4.655) | 260.6 (4.717) | 337.4 |

Encoding a stream with 64 KB writes, 7 runs. The ratios are the same as above. The `klauspost/compress` `Encoder` has its default concurrency.

| File | Level | klauspost `Encoder` | `zstd.Writer` |
|---|---|---|---|
| Log | 1 | 339.3 | 284.2 |
| Binaries | 1 | 217.0 | 168.1 |
| Source | 1 | 288.7 | 190.1 |
| Log | 3 | 503.1 | 467.6 |
| Binaries | 3 | 307.8 | 290.5 |
| Source | 3 | 329.4 | 296.7 |

The next tables are for arm64. The machine is an Apple M1 Pro with macOS 27 and Go 1.27.3. The files and the setup are the same as above. Other programs also ran on the machine, so a time can vary by about 5%.

Decoding, 21 runs:

| File | Level | klauspost `DecodeAll` | `zstd.Decode` | klauspost `Decoder` | `zstd.Reader` |
|---|---|---|---|---|---|
| Log | 1 | 60.1 | 50.7 | 35.2 | 34.8 |
| Log | 3 | 58.9 | 48.9 | 36.2 | 38.7 |
| Log | 19 | 46.1 | 31.8 | 30.0 | 27.7 |
| Binaries | 1 | 39.0 | 34.6 | 21.6 | 22.3 |
| Binaries | 3 | 39.9 | 36.2 | 25.0 | 25.6 |
| Binaries | 19 | 46.6 | 41.0 | 36.3 | 33.7 |
| Source | 1 | 39.9 | 34.4 | 24.3 | 24.3 |
| Source | 3 | 40.7 | 34.7 | 27.4 | 28.4 |
| Source | 19 | 43.2 | 36.7 | 36.0 | 34.1 |

With `GOMAXPROCS=1`, `zstd.Decode` takes 40 to 62 ms on these files. That is between 0% and 13% slower than `DecodeAll`. klauspost/compress also decodes the sequences in arm64 assembly.

Encoding from memory, 9 runs, with the ratio in parentheses. The "one CPU" column has 15 runs.

| File | Level | klauspost `EncodeAll` | `zstd.Encode` | `zstd.Encode`, one CPU |
|---|---|---|---|---|
| Log | 1 | 193.6 (6.110) | 121.3 (6.153) | 210.6 |
| Binaries | 1 | 126.4 (2.704) | 80.8 (2.745) | 140.8 |
| Source | 1 | 133.8 (4.129) | 90.5 (4.341) | 146.3 |
| Log | 3 | 235.5 (5.952) | 169.5 (6.075) | 262.7 |
| Binaries | 3 | 153.6 (2.829) | 106.5 (2.827) | 173.0 |
| Source | 3 | 153.9 (4.655) | 111.0 (4.717) | 167.7 |

On one CPU, `zstd.Encode` takes 9% to 13% more time than `EncodeAll`.

To measure your own `.zst` files, run the benchmark tool. It compares the zstd decoders and makes sure that they give the same output. With `-compress` and `-zstd`, it compresses the files at that level and makes sure that both decoders restore them.

```sh
cd bench
go run . -stream -n 9 /path/to/*.zst
go run . -compress 3 -zstd -n 9 /path/to/files/*
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
- The decoder builds each table with the libdeflate method. It writes each code once, and one copy doubles the filled part of the table at each next code length. zlib writes each table entry separately. On the Xeon, the literal/length and distance tables of a block of the log at level 6 take 1.2 µs instead of 3.4 µs.
- The code lengths in the header of a dynamic block come from 8-byte refills of the bit buffer, not from one byte at a time.
- The table load for the next symbol starts before the refill, so the two delays overlap.
- The decoder reads the next symbol before it copies a match.
- The bit count uses the libdeflate method. The decoder subtracts the full entry and uses only the low 6 bits.
- Match copies move 16 bytes at a time. The first 32 bytes have no loop test, because most matches in log data are 16 bytes or shorter. A short distance repeats as a pattern.
- On amd64 with BMI2, an assembly function does the steps of the fast loop. It keeps all its state in registers. In the compiled Go loop, each shift count must go into CX, and some values come from the stack again in each step. With `GOAMD64=v3` alone, the Go loop was 8-11% faster. The runtime cannot preempt assembly, so one call stops after about 64 KB of output. At the slowest rate that we measured, 2.9 ns for each byte of output, one call takes about 0.2 ms.
- On arm64, an assembly function computes CRC-32 with `PMULL` carry-less multiplies. It folds eight 128-bit lanes in parallel. This is 45 GB/s, and `hash/crc32` is 8.4 GB/s.
- Above 1 MB, `Gunzip` computes the CRC-32 on a second goroutine. The decoder gives it the output in steps of 1 MB or more, because each step can wake the goroutine.

## Why zstd is fast

- On amd64 with BMI2, assembly decodes the sequences and the four Huffman streams of the literals, executes the sequences, and encodes the sequences. BMI2 shifts take their count from any register. With `GOAMD64=v3` alone, the Go decoder was 15% faster, because the Go loops spent instructions to move shift counts.
- On arm64, assembly decodes, executes and encodes the sequences. On one goroutine, one loop decodes each sequence and then copies it, so that the copies use the time while the decoder waits for its loads. With a loop for each step, a 100 MB log at level 1 took 18% more time on an M1 Pro.
- The decoder decodes the sequences of a block into an array, then executes them. Each of the two loops keeps its state in registers.
- Execution copies 16 bytes at a time. A match closer than 16 bytes starts with one `PSHUFB` that makes the pattern. The loop prefetches the source of the match eight sequences ahead.
- A large frame decodes on two goroutines: one decodes the literals and the sequences, and the other executes them.
- Each hash table entry of the encoder keeps the first 4 bytes at its position. A candidate that does not match costs no load from the input.
- The fast level checks the last offset two bytes ahead. Both levels encode a match at one of the last three offsets as a repeat offset.
- The encoder chooses the predefined, the previous or a new FSE table by its estimated size.
- A large input compresses on two goroutines: one finds the matches, and the other encodes them.

## Safety

The fast decode loop uses `unsafe` pointers, or assembly on amd64, and has no bounds checks. These limits keep each access in its buffer:

- The loop stops when fewer than 25 input bytes or 323 output bytes remain.
- Each table index has a mask with the table size.
- The decoder compares each match distance with the output that it wrote.

The match search of the compressor also loads input through `unsafe` pointers:

- An 8-byte load is at a position that is at least 8 bytes before the end of the block.
- A load at a match candidate occurs only if the candidate is at or after 0 and before the current position.

The token writer stores 8 bytes at a time through an `unsafe` pointer. Before each block, it reserves the bytes for the bits that the symbol counts of the block give, plus 16 bytes. The tests recount the symbols of each block from its tokens, and they stop if the counts are different.

The tests compare the decoder output with `compress/flate` and `compress/gzip` on random and fuzz input. They decode the compressor output with `compress/flate`, `compress/gzip` and fastcompress. The tests run with `-race` and with `-gcflags=all=-d=checkptr`. These checks do not see into assembly. Other tests and a fuzz test make sure that the assembly loop and the Go loop decode the same data and refuse the same data. On Linux, a test puts the input and the output of the decoder next to pages that the process cannot access. An access past the end of a buffer then stops the test. The CI also runs the Go loop with `-tags purego`. We ran the decoder tests on darwin/arm64, linux/arm64 and linux/amd64. We ran the compressor tests on linux/amd64 only.

The zstd decoder also uses `unsafe` pointers and assembly without bounds checks. These limits keep each access in its buffer:

- Each table index has a mask with the table size.
- Before the decoder copies a sequence, it compares the sequence with the literals that remain and with the end of the block. It compares each offset with the output of the frame and with the window.
- The output has 32 spare bytes after each block, and the literals have 32 spare bytes after their end.
- The assembly loops stop 24 bytes before the start of a bitstream. The Go code decodes the rest.

The zstd encoder writes through `unsafe` pointers into space that it reserves first. Each table index has a mask, and each position stays 8 bytes before the end of the input.

The zstd tests decode files from the zstd 1.5.5 command and compare them with the output of that command. In the `bench` module, the tests compare the decoder with `klauspost/compress` on fuzz input, and make sure that fastcompress does not accept input that `klauspost/compress` refuses. The fastcompress and `klauspost/compress` decoders must restore the encoder output. We also decoded the encoder output with the zstd command. Other tests make sure that the assembly and the Go code give the same output and refuse the same data. The input of these tests is files, encoder output, damaged copies, fuzz input, random sequences, and random bitstreams from random tables. The tests of the executor also make sure that it writes nothing past the space of the output. The CI also runs the Go code with `-tags purego`.

## Tests

```sh
go test ./...
go test -run '^$' -fuzz FuzzReader -fuzztime 60s ./gzip
go test -run '^$' -fuzz FuzzWriter -fuzztime 60s ./gzip
```

To compare a directory of `.gz` files with `compress/gzip`, set `FASTCOMPRESS_CORPUS`:

```sh
FASTCOMPRESS_CORPUS=/path/to/gz-files go test ./gzip
```

To profile one file, set `FASTCOMPRESS_BENCH` and run `BenchmarkReader` or `BenchmarkGunzip` in `./gzip`. To profile compression of one uncompressed file, set `FASTCOMPRESS_RAW` and run `BenchmarkGzip`.

For zstd:

```sh
go test -run '^$' -fuzz FuzzDecode -fuzztime 60s ./zstd
go test -run '^$' -fuzz FuzzReader -fuzztime 60s ./zstd
go test -run '^$' -fuzz FuzzRoundTrip -fuzztime 60s ./zstd
go test -run '^$' -fuzz FuzzWriter -fuzztime 60s ./zstd
cd bench && go test .
```

`FASTCOMPRESS_ZSTD_CORPUS` names a directory of `.zst` files to decode. If a file with the same name without `.zst` exists, the test compares the output with it. `FASTCOMPRESS_ZSTD_BENCH` names one `.zst` file for `BenchmarkDecode` and `BenchmarkReader` in `./zstd`. `FASTCOMPRESS_ZSTD_ENC_BENCH` names one file for `BenchmarkEncode`, at the level in `FASTCOMPRESS_ZSTD_LEVEL`.

## License

MIT. The table builder in `gzip/tables.go` follows libdeflate. The tests compare it with a port of the zlib 1.2.12 table builder. The CRC-32 combination in `gzip/crc32_combine.go` is a port of zlib. Parts of the zstd code follow Go's `internal/zstd` and the zstd library. [NOTICE](NOTICE) has their licenses.
