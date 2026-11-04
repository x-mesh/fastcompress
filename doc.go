// Package fastgzip decodes and encodes gzip (RFC 1952) and DEFLATE (RFC 1951)
// data in Go, without cgo.
//
// Reader replaces compress/gzip.Reader for reading: NewReader, Read, Reset and
// Close behave the same, and concatenated members are read as one stream.
// Reader does not expose the gzip header fields (Name, Comment, ModTime).
// Gunzip and Inflate decode data that is already in memory.
//
// Writer replaces compress/gzip.Writer for writing, with the same levels and
// the methods NewWriter, NewWriterLevel, Write, Flush, Close and Reset.
// Writer does not write the header fields. Gzip and Deflate encode data that
// is already in memory; without Flush, a Writer writes the same bytes as Gzip.
// GzipConcurrent and Writer.SetConcurrency compress chunks of about 1 MB on
// several goroutines; the output does not depend on their number.
//
// The decoder uses a 64-bit bit buffer refilled with one 8-byte load, table
// entries that count their extra bits so one shift consumes a code and its
// extra bits, table loads overlapped with refills, decoding the next symbol
// before a match copy, and wide match copies. On arm64 the CRC-32 folds eight
// 128-bit lanes with PMULL in assembly; elsewhere it uses hash/crc32.
//
// The encoder finds matches with one hash table at levels 1-3, with a 4-byte
// and an 8-byte hash table at levels 4-6, and with zlib-style hash chains and
// lazy matching at levels 7-9. It writes each block as the smallest of a
// stored, fixed or dynamic block.
package fastgzip
